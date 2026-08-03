package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// Compile-time check that GHProvider implements forge.Forge.
var _ forge.Forge = (*GHProvider)(nil)

// GHProvider wraps the existing GitHub client to implement forge.Forge.
type GHProvider struct {
	client *Client
	status AuthStatus

	// Cached after Parent() call; used by Compare().
	sourceOwner         string
	sourceRepo          string
	sourceDefaultBranch string
}

// NewGHProvider returns a forge.Forge backed by the existing GitHub client.
func NewGHProvider(client *Client, status AuthStatus) *GHProvider {
	return &GHProvider{client: client, status: status}
}

// Client returns the underlying *Client. Exposed so callers (e.g., the dump
// cluster pipeline) can build adapters against the same HTTP client without
// re-authenticating.
func (p *GHProvider) Client() *Client {
	return p.client
}

// Auth implements forge.Forge.
func (p *GHProvider) Auth(_ context.Context) (forge.AuthInfo, error) {
	tier := forge.AuthNone
	conc := 2
	rl := 60
	if p.status.Authenticated {
		tier = forge.AuthCLI
		conc = 10
		rl = 5000
	}
	return forge.AuthInfo{
		Provider:    forge.ProviderGitHub,
		Tier:        tier,
		Host:        p.status.Host,
		Username:    "",
		Concurrency: conc,
		RateLimit:   rl,
		RateUnit:    "hour",
	}, nil
}

// Headroom implements forge.Forge.
func (p *GHProvider) Headroom() float64 {
	return p.client.Headroom()
}

// Parent implements forge.Forge.
func (p *GHProvider) Parent(ctx context.Context, owner, repo string) (forge.ParentData, error) {
	info, err := p.client.FetchParent(ctx, owner, repo)
	if err != nil {
		return forge.ParentData{}, err
	}

	// Cache for Compare() calls.
	p.sourceOwner = owner
	p.sourceRepo = repo
	p.sourceDefaultBranch = info.DefaultBranch

	pushed, _ := time.Parse(time.RFC3339, info.PushedAt)

	// Resolve the default-branch tip SHA so the MDG cache (cluster
	// pipeline's CentralityHeadSHA) can pin entries. Soft-fail: a missing
	// SHA disables cache persistence (and the 24h fast path) but does not
	// fail the parent fetch — change-impact still computes via the
	// directory proxy.
	headSHA, _ := p.client.defaultBranchTipSHA(ctx, owner, repo)

	return forge.ParentData{
		FullName:      info.FullName,
		Description:   info.Description,
		DefaultBranch: info.DefaultBranch,
		HeadSHA:       headSHA,
		Stars:         info.Stars,
		Forks:         info.Forks,
		Size:          info.Size,
		PushedAt:      pushed,
		URL:           info.HTMLURL,
		Language:      info.Language,
		Topics:        info.Topics,
	}, nil
}

// ListForks implements forge.Forge.
func (p *GHProvider) ListForks(ctx context.Context, owner, repo string) (<-chan forge.ForkMsg, error) {
	out := make(chan forge.ForkMsg, 64)

	go func() {
		defer close(out)

		forks, extrasMap, err := p.client.FetchForksAuto(ctx, owner, repo, nil)
		if err != nil {
			select {
			case out <- forge.ForkMsg{Err: err}:
			case <-ctx.Done():
			}
			return
		}

		for _, f := range forks {
			var extra *T1Extra
			if extrasMap != nil {
				if e, ok := extrasMap[f.ID]; ok {
					extra = &e
				}
			}
			t1 := forkInfoToT1(f, extra, owner+"/"+repo)
			select {
			case out <- forge.ForkMsg{Fork: t1}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}

// Branches implements forge.Forge.
func (p *GHProvider) Branches(_ context.Context, fork forge.T1Data, n int) ([]forge.BranchRef, error) {
	if len(fork.Branches) > 0 {
		limit := n
		if limit > len(fork.Branches) {
			limit = len(fork.Branches)
		}
		return fork.Branches[:limit], nil
	}
	return nil, nil
}

// Compare implements forge.Forge.
func (p *GHProvider) Compare(ctx context.Context, fork forge.T1Data, branch string) (forge.T2Data, error) {
	parentBranch := p.sourceDefaultBranch
	if parentBranch == "" {
		parentBranch = "HEAD"
	}

	// Build a ForkInfo for FetchCompareWithBranchScan.
	ghFork := ForkInfo{
		Owner:         OwnerInfo{Login: fork.Owner},
		DefaultBranch: fork.DefaultBranch,
		Name:          fork.Name,
	}

	// Convert forge.BranchRef to gh.BranchInfo for branch scanning.
	var ghBranches []BranchInfo
	for _, br := range fork.Branches {
		ghBranches = append(ghBranches, BranchInfo{
			Name:         br.Name,
			LastCommitAt: br.CommittedDate.Format(time.RFC3339),
		})
	}

	scan, err := p.client.FetchCompareWithBranchScan(
		ctx, p.sourceOwner, p.sourceRepo, parentBranch,
		ghFork, ghBranches,
	)
	if err != nil {
		return forge.T2Data{}, fmt.Errorf("compare %s@%s: %w", fork.ID, branch, err)
	}

	t2 := compareToT2(scan.Compare)

	if p.client.webDiff != nil {
		missing := false
		for _, diff := range t2.Diffs {
			if diff.Patch == "" {
				missing = true
				break
			}
		}
		if missing {
			base := scan.Compare.MergeBaseCommit.SHA
			if base == "" {
				base = scan.Compare.BaseCommit.SHA
			}
			head := ""
			if n := len(scan.Compare.Commits); n > 0 {
				head = scan.Compare.Commits[n-1].SHA
			}
			if base == "" || head == "" {
				t2.PatchSkipReason = "web diff unavailable: compare response omitted base/head SHA"
			} else if patches, truncated, webErr := p.client.webDiff.Fetch(ctx, p.sourceOwner, p.sourceRepo, base, head); webErr != nil {
				t2.PatchSkipReason = webErr.Error()
			} else if truncated {
				// A later page failed to parse: the collected patches are
				// incomplete and we cannot tell which files are whole. Persisting
				// a leading fragment as a complete diff would silently corrupt the
				// store and the semantic index, so discard and record the skip.
				t2.PatchSkipReason = "web diff truncated: pagination incomplete"
			} else {
				for i := range t2.Diffs {
					if t2.Diffs[i].Patch == "" && patches[t2.Diffs[i].Path] != "" {
						t2.Diffs[i].Patch = patches[t2.Diffs[i].Path]
						t2.Diffs[i].PatchSource = "github_web"
					}
				}
			}
		}
	}

	// Track branch work
	if scan.Branch != "" && scan.Branch != fork.DefaultBranch {
		t2.IsBranchWork = true
		t2.ActiveBranch = scan.Branch
	}

	// Branch selection already probed the chosen branch tip: a maintainer fork's
	// "ahead" branch is often a PR that was squash-merged upstream — real to a
	// commit-graph compare but worthless to integrate. Propagate the verdict so
	// scoring can zero it.
	if scan.Upstreamed {
		t2.Upstreamed = true
		t2.UpstreamedPR = scan.UpstreamedPR
	}

	return t2, nil
}

// Contributors implements forge.Forge.
func (p *GHProvider) Contributors(ctx context.Context, fork forge.T1Data) (forge.T3Data, error) {
	parts := strings.SplitN(fork.ID, "/", 2)
	if len(parts) != 2 {
		return forge.T3Data{}, fmt.Errorf("invalid fork ID %q: expected owner/repo", fork.ID)
	}

	raw, err := p.client.FetchContributors(ctx, parts[0], parts[1])
	if err != nil {
		return forge.T3Data{}, fmt.Errorf("contributors %s: %w", fork.ID, err)
	}

	contributors := make([]forge.Contributor, 0, len(raw))
	for _, c := range raw {
		contributors = append(contributors, forge.Contributor{
			Login:       c.Author.Login,
			CommitCount: c.Total,
		})
	}

	return forge.T3Data{
		Contributors:   contributors,
		CommitSpanDays: 0, // derived from T2.Commits in forksops.rescore()
	}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func forkInfoToT1(f ForkInfo, extra *T1Extra, parentFullPath string) forge.T1Data {
	pushed, _ := time.Parse(time.RFC3339, f.PushedAt)
	created, _ := time.Parse(time.RFC3339, f.CreatedAt)

	t1 := forge.T1Data{
		ID:             f.FullName,
		Owner:          f.Owner.Login,
		Name:           f.Name,
		URL:            f.HTMLURL,
		DefaultBranch:  f.DefaultBranch,
		Stars:          f.Stars,
		PushedAt:       pushed,
		IsArchived:     f.Archived || f.Disabled,
		SubForkCount:   f.Forks,
		Description:    f.Description,
		Size:           f.Size,
		Language:       f.Language,
		OpenIssues:     f.OpenIssues,
		CreatedAt:      created,
		Topics:         f.Topics,
		SourceFullPath: parentFullPath,
		ParentFullPath: parentFullPath,
	}

	if extra != nil {
		t1.OpenPRCount = extra.OpenPRCount
		t1.ReleaseCount = extra.ReleaseCount

		branches := make([]forge.BranchRef, 0, len(extra.TopBranches))
		for _, br := range extra.TopBranches {
			cd, _ := time.Parse(time.RFC3339, br.LastCommitAt)
			branches = append(branches, forge.BranchRef{
				Name:          br.Name,
				CommittedDate: cd,
			})
		}
		t1.Branches = branches
	}

	return t1
}

func compareToT2(r CompareResult) forge.T2Data {
	diffs := make([]forge.FileDiff, 0, len(r.Files))
	var totalAdd, totalDel int
	for _, f := range r.Files {
		totalAdd += f.Additions
		totalDel += f.Deletions
		diffs = append(diffs, forge.FileDiff{
			Path:         f.Filename,
			PreviousPath: f.PreviousFilename,
			Status:       f.Status,
			Additions:    f.Additions,
			Deletions:    f.Deletions,
			Patch:        f.Patch,
			PatchSource:  "compare_rest",
		})
	}

	ahead := make([]forge.AheadCommit, 0, len(r.Commits))
	for _, c := range r.Commits {
		ahead = append(ahead, commitToAhead(c))
	}

	mna := computeMNAFromDiffs(diffs)
	fcr := featureCommitRatio(ahead)

	// Identity of the compared work: the merge base it diverged from and the
	// tip it diverged to. Both are needed to tell two forks carrying the same
	// commits apart from two that merely have similar diff statistics.
	baseSHA := r.MergeBaseCommit.SHA
	if baseSHA == "" {
		baseSHA = r.BaseCommit.SHA
	}
	headSHA := ""
	if len(r.Commits) > 0 {
		headSHA = r.Commits[len(r.Commits)-1].SHA
	}

	return forge.T2Data{
		AheadCount:         r.AheadBy,
		BehindCount:        r.BehindBy,
		MNA:                mna,
		TotalAdditions:     totalAdd,
		TotalDeletions:     totalDel,
		FeatureCommitRatio: fcr,
		BaseSHA:            baseSHA,
		HeadSHA:            headSHA,
		Diffs:              diffs,
		Commits:            ahead,
	}
}

func commitToAhead(c Commit) forge.AheadCommit {
	ac := forge.AheadCommit{
		SHA:         c.SHA,
		Message:     c.CommitDet.Message,
		AuthorEmail: c.CommitDet.Author.Email,
	}
	if c.Author != nil {
		ac.AuthorLogin = c.Author.Login
	}
	ts, _ := time.Parse(time.RFC3339, c.CommitDet.Author.Date)
	ac.Timestamp = ts
	return ac
}

func computeMNAFromDiffs(diffs []forge.FileDiff) int {
	total := 0
	for _, d := range diffs {
		w := heat.FileWeight(d.Path)
		net := d.Additions - d.Deletions
		if net < 0 {
			net = 0
		}
		total += int(float64(net) * w)
	}
	return total
}

func featureCommitRatio(commits []forge.AheadCommit) float64 {
	if len(commits) == 0 {
		return 0
	}
	feature := 0
	for _, c := range commits {
		if !isMergeOrSyncMsg(c.Message) {
			feature++
		}
	}
	return float64(feature) / float64(len(commits))
}

func isMergeOrSyncMsg(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	for _, pfx := range []string{
		"merge branch",
		"merge pull request",
		"merge remote-tracking",
		"sync with upstream",
		"sync upstream",
		`revert "merge`,
	} {
		if strings.HasPrefix(lower, pfx) {
			return true
		}
	}
	return false
}

// SearchTopicRepos implements the optional topics.TopicSearcher capability:
// it returns repositories carrying the GitHub topic, mapped to forge types.
func (p *GHProvider) SearchTopicRepos(ctx context.Context, topic string, limit int) ([]forge.TopicRepo, error) {
	return p.searchTopicReposSorted(ctx, topic, "stars", limit)
}

func (p *GHProvider) SearchTopicReposByLane(ctx context.Context, topic string, lane forge.TopicLane, limit int) ([]forge.TopicRepo, error) {
	switch lane {
	case forge.TopicLaneDefault, forge.TopicLaneStars:
		return p.searchTopicReposSorted(ctx, topic, "stars", limit)
	case forge.TopicLaneUpdated:
		return p.searchTopicReposSorted(ctx, topic, "updated", limit)
	case forge.TopicLaneForks:
		return p.searchTopicReposSorted(ctx, topic, "forks", limit)
	default:
		return nil, fmt.Errorf("unsupported topic lane %q", lane)
	}
}

func (p *GHProvider) searchTopicReposSorted(ctx context.Context, topic, sort string, limit int) ([]forge.TopicRepo, error) {
	repos, err := p.client.SearchTopicReposSorted(ctx, topic, sort, limit)
	if err != nil {
		return nil, err
	}
	out := make([]forge.TopicRepo, 0, len(repos))
	for _, r := range repos {
		if r.IsFork {
			continue // belt and braces; the query already excludes forks
		}
		pushed, err := time.Parse(time.RFC3339, r.PushedAt)
		if err != nil {
			// Recency drives topic selection; a zero PushedAt from an
			// unparseable timestamp would silently skew scoring, so skip
			// the repo rather than rank it as ancient.
			continue
		}
		out = append(out, forge.TopicRepo{
			FullName:    r.FullName,
			Description: r.Description,
			Language:    r.Language,
			Stars:       r.Stars,
			ForkCount:   r.Forks,
			PushedAt:    pushed,
			Archived:    r.Archived,
		})
	}
	return out, nil
}
