package github

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/github/treecommitinfo"
	"github.com/svnbjrn/spoon/internal/heat"
)

// Compile-time check that GHProvider implements forge.Forge.
var _ forge.Forge = (*GHProvider)(nil)

// GHProvider wraps the existing GitHub client to implement forge.Forge.
type GHProvider struct {
	client *Client
	status AuthStatus

	// Cached after Parent() call; used by Compare().
	mu                  sync.RWMutex
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
		APIVersion:  p.status.APIVersion,
		AuthMode:    p.status.AuthMode,
		AuthScopeID: p.status.AuthScopeID,
	}, nil
}

// Headroom implements forge.Forge.
func (p *GHProvider) Headroom() float64 {
	return p.client.Headroom()
}

// The TUI's cached-fork-list path restores the compare baseline through this
// interface via a type assertion. A failed assertion would silently no-op and
// reinstate the all-zeros bug, so pin it at compile time.
var _ forge.CompareBaselineSetter = (*GHProvider)(nil)

// DivergentBranchCounts implements forge.BranchDivergenceProvider. The whole
// batch costs two GraphQL queries regardless of how many forks or branches are
// involved, so unlike the REST branch scan it needs no per-branch budget gate.
//
// Compares against the network root (sourceOwner/sourceRepo), not a fork's
// direct parent — planning/spoon-plan.md:159.
func (p *GHProvider) DivergentBranchCounts(ctx context.Context, forks []forge.T1Data) (map[string]int, map[string]string, []string, error) {
	targets := make([]ForkTarget, 0, len(forks))
	for _, f := range forks {
		targets = append(targets, ForkTarget{ID: f.ID, Owner: f.Owner, Name: f.Name})
	}
	p.mu.RLock()
	sourceOwner := p.sourceOwner
	sourceRepo := p.sourceRepo
	sourceDefaultBranch := p.sourceDefaultBranch
	p.mu.RUnlock()
	counts, err := p.client.FetchDivergentBranchCounts(ctx, sourceOwner, sourceRepo, sourceDefaultBranch, targets)
	if err != nil {
		return nil, nil, nil, err
	}
	return counts.Divergent, counts.Fingerprint, counts.Truncated, nil
}

var _ forge.BranchDivergenceProvider = (*GHProvider)(nil)

// BatchCompare implements forge.BatchCompareProvider: it resolves ahead/
// behind (and, for ahead branches, tip + upstreamed-PR status) for every
// fork's default and side branches against the network root in two GraphQL
// passes, replacing a per-fork REST branch scan.
//
// Returns forge.ErrBatchCompareUnavailable when the client has no working
// GraphQL backend, so callers fall back to the REST path silently rather
// than treating it as a hard failure.
func (p *GHProvider) BatchCompare(ctx context.Context, forks []forge.T1Data) (map[string]forge.ForkDivergence, forge.BatchStats, error) {
	if !p.client.HasGraphQL() {
		return nil, forge.BatchStats{}, forge.ErrBatchCompareUnavailable
	}
	p.mu.RLock()
	sourceOwner := p.sourceOwner
	sourceRepo := p.sourceRepo
	sourceDefaultBranch := p.sourceDefaultBranch
	p.mu.RUnlock()

	targets := make([]BatchTarget, 0, len(forks))
	for _, f := range forks {
		// Inherited upstream branches are not fork work; pairing them would
		// report upstream's own unmerged commits as this fork's divergence.
		branches := forge.PostForkBranches(f)
		sides := make([]BatchBranch, 0, len(branches))
		for _, b := range branches {
			sides = append(sides, BatchBranch{
				Name:        b.Name,
				TipSHA:      b.TipSHA,
				CommittedAt: b.CommittedDate,
			})
		}
		targets = append(targets, BatchTarget{
			ID:                 f.ID,
			Owner:              f.Owner,
			Name:               f.Name,
			DefaultBranch:      f.DefaultBranch,
			DefaultTipSHA:      f.DefaultTipSHA,
			DefaultCommittedAt: f.PushedAt,
			Sides:              sides,
		})
	}
	return p.client.FetchBatchDivergence(ctx, sourceOwner, sourceRepo, sourceDefaultBranch, targets)
}

var _ forge.BatchCompareProvider = (*GHProvider)(nil)

// MergeCommitHistory implements forge.LinearHistoryProvider. The whole batch
// costs one GraphQL compare query per linearHistoryBatchSize forks; the
// returned vector is the raw parents.totalCount per commit, capped at
// linearHistoryCommitLimit per fork. Forks with deeper histories land in
// Truncated and the consumer renders the lower-bound signal.
func (p *GHProvider) MergeCommitHistory(ctx context.Context, forks []forge.T1Data) (map[string][]int, []string, error) {
	targets := make([]forge.T1Data, 0, len(forks))
	for _, f := range forks {
		if f.Owner == "" || f.Name == "" {
			continue
		}
		targets = append(targets, f)
	}
	hist, err := p.client.FetchMergeCommitHistory(ctx, p.sourceOwner, p.sourceRepo, p.sourceDefaultBranch, targets)
	if err != nil {
		return nil, nil, err
	}
	return hist.Histories, hist.Truncated, nil
}

var _ forge.LinearHistoryProvider = (*GHProvider)(nil)

// PathLastTouch implements forge.LastTouchProvider. It resolves the upstream
// side of the last-touch comparison: for each path, the most recent commit
// on the network root's default branch that touched it, and how many
// commits landed there since. See DivergentBranchCounts for why the baseline
// is read under p.mu rather than taken from a field directly.
func (p *GHProvider) PathLastTouch(ctx context.Context, paths []string) (map[string]forge.PathLastTouch, error) {
	p.mu.RLock()
	sourceOwner := p.sourceOwner
	sourceRepo := p.sourceRepo
	sourceDefaultBranch := p.sourceDefaultBranch
	p.mu.RUnlock()
	return p.client.FetchPathLastTouch(ctx, sourceOwner, sourceRepo, sourceDefaultBranch, paths)
}

// ForkLastTouch implements forge.LastTouchProvider. It is the fork side of
// the last-touch comparison: a thin wrapper over the opt-in, best-effort
// tree-commit-info client. Reports LastTouchDisabled (and a nil map) when
// EnableTreeCommitInfo was never called, the same "capability not turned on"
// signal a disabled client would produce live.
func (p *GHProvider) ForkLastTouch(ctx context.Context, fork forge.T1Data, ref, dir string) (map[string]string, forge.LastTouchOutcome) {
	tci := p.client.TreeCommitInfo()
	if tci == nil {
		return nil, forge.LastTouchDisabled
	}
	entries, outcome := tci.LastTouch(ctx, fork.Owner, fork.Name, ref, dir)
	return entries, forgeLastTouchOutcome(outcome)
}

// forgeLastTouchOutcome translates treecommitinfo's package-local Outcome to
// forge.LastTouchOutcome, the shared vocabulary Task 7 consumes.
func forgeLastTouchOutcome(o treecommitinfo.Outcome) forge.LastTouchOutcome {
	switch o {
	case treecommitinfo.OK:
		return forge.LastTouchOK
	case treecommitinfo.NotFound:
		return forge.LastTouchNotFound
	case treecommitinfo.Disabled:
		return forge.LastTouchDisabled
	default:
		return forge.LastTouchError
	}
}

var _ forge.LastTouchProvider = (*GHProvider)(nil)

// SetCompareBaseline implements forge.CompareBaselineSetter. Parent() calls it
// on the live path; a caller that serves the fork list from a local cache must
// call it explicitly, or every Compare that follows has no upstream to compare
// against.
func (p *GHProvider) SetCompareBaseline(owner, repo, defaultBranch string) {
	p.mu.Lock()
	p.sourceOwner = owner
	p.sourceRepo = repo
	p.sourceDefaultBranch = defaultBranch
	p.mu.Unlock()
}

// Parent implements forge.Forge.
func (p *GHProvider) Parent(ctx context.Context, owner, repo string) (forge.ParentData, error) {
	info, err := p.client.FetchParent(ctx, owner, repo)
	if err != nil {
		return forge.ParentData{}, err
	}

	baseOwner, baseRepo, baseBranch := repoCompareBaseline(info, owner, repo)
	p.SetCompareBaseline(baseOwner, baseRepo, baseBranch)

	pushed, _ := time.Parse(time.RFC3339, info.PushedAt)

	headSHA, _ := p.client.defaultBranchTipSHA(ctx, baseOwner, baseRepo)

	parent := forge.ParentData{
		FullName:             info.FullName,
		Description:          info.Description,
		DefaultBranch:        info.DefaultBranch,
		HeadSHA:              headSHA,
		Stars:                info.Stars,
		Forks:                info.Forks,
		Size:                 info.Size,
		PushedAt:             pushed,
		URL:                  info.HTMLURL,
		Language:             info.Language,
		Topics:               info.Topics,
		DirectParentFullPath: directParentFullPath(info),
	}
	if baseOwner != owner || baseRepo != repo {
		parent.SourceFullPath = baseOwner + "/" + baseRepo
		parent.SourceDefaultBranch = baseBranch
	}
	return parent, nil
}

// ListForks implements forge.Forge.
func (p *GHProvider) ListForks(ctx context.Context, owner, repo string) (<-chan forge.ForkMsg, error) {
	out := make(chan forge.ForkMsg, 64)

	go func() {
		defer close(out)

		forks, extrasMap, report, err := p.client.FetchForksAuto(ctx, owner, repo, nil)
		if err != nil {
			// On fatal acquisition failure, send the terminal report (when
			// the provider managed to build one) before any error ForkMsg so
			// observers can see what we attempted before the failure.
			if report != nil {
				select {
				case out <- forge.ForkMsg{Report: report}:
				case <-ctx.Done():
					return
				}
			}
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
			sourcePath := owner + "/" + repo
			p.mu.RLock()
			if p.sourceOwner != "" && p.sourceRepo != "" {
				sourcePath = p.sourceOwner + "/" + p.sourceRepo
			}
			p.mu.RUnlock()
			t1 := forkInfoToT1(f, extra, owner+"/"+repo, sourcePath)
			select {
			case out <- forge.ForkMsg{Fork: t1}:
			case <-ctx.Done():
				return
			}
		}

		// Terminal acquisition report: always last on the channel.
		if report != nil {
			select {
			case out <- forge.ForkMsg{Report: report}:
			case <-ctx.Done():
			}
		}
	}()

	return out, nil
}

// ListForksBounded implements forge.ListForksBoundedProvider.
func (p *GHProvider) ListForksBounded(ctx context.Context, owner, repo string, opts forge.BoundedOptions) (<-chan forge.ForkMsg, error) {
	out := make(chan forge.ForkMsg, 64)
	go func() {
		defer close(out)
		boundedOpts := BoundedOptions{
			MaxNodes:   opts.MaxNodes,
			MaxDepth:   opts.MaxDepth,
			MaxPages:   opts.MaxPages,
			MaxElapsed: opts.MaxElapsed,
		}
		forks, extrasMap, report, err := p.client.FetchForksBounded(ctx, owner, repo, nil, boundedOpts)
		// Emit partial forks first even on error. FetchForksBounded returns
		// whatever it had already accepted when an in-flight GraphQL call
		// fails; discarding that inventory here would mean forksops.Stream
		// sees an empty result set and exits with success while the upstream
		// failure goes unreported. Stream filters ForkMsg.Err after consuming
		// Fork and Report, so the right order is forks -> report -> err.
		for _, f := range forks {
			var extra *T1Extra
			if extrasMap != nil {
				if e, ok := extrasMap[f.ID]; ok {
					extra = &e
				}
			}
			sourcePath := owner + "/" + repo
			p.mu.RLock()
			if p.sourceOwner != "" && p.sourceRepo != "" {
				sourcePath = p.sourceOwner + "/" + p.sourceRepo
			}
			p.mu.RUnlock()
			// parentFullPath is left unknown here: the bounded walk can surface
			// forks nested at any depth, so the requested owner/repo is not
			// necessarily this node's direct parent (only extra's GraphQL
			// lineage, when present, can say that accurately).
			t1 := forkInfoToT1(f, extra, "", sourcePath)
			select {
			case out <- forge.ForkMsg{Fork: t1}:
			case <-ctx.Done():
				return
			}
		}
		if report != nil {
			select {
			case out <- forge.ForkMsg{Report: report}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			select {
			case out <- forge.ForkMsg{Err: err}:
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
	// Without a baseline the compare path degrades to "repos///compare/HEAD...",
	// which 404s for every fork and — because a 404 is not a run-ending error —
	// used to surface as a fork list where everything is 0 ahead / 0 behind.
	// Fail loudly instead; the gitea provider already guards this the same way.
	p.mu.RLock()
	sourceOwner := p.sourceOwner
	sourceRepo := p.sourceRepo
	sourceDefaultBranch := p.sourceDefaultBranch
	p.mu.RUnlock()
	if sourceOwner == "" || sourceRepo == "" {
		return forge.T2Data{}, fmt.Errorf("compare %s@%s: upstream not resolved (Parent not called)", fork.ID, branch)
	}

	parentBranch := sourceDefaultBranch
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
		ctx, sourceOwner, sourceRepo, parentBranch,
		ghFork, ghBranches,
	)
	if err != nil {
		return forge.T2Data{}, fmt.Errorf("compare %s@%s: %w", fork.ID, branch, err)
	}

	return p.finishT2(ctx, sourceOwner, sourceRepo, scan, fork), nil
}

// CompareResolved implements forge.ResolvedCompareProvider. The branch to
// attribute the fork's work to, and its upstreamed verdict, were already
// decided by forge.SelectDivergentBranch from a batch-resolved
// forge.ForkDivergence -- so unlike Compare, this issues exactly one REST
// compare against sel.Branch and never scans side branches or probes
// commits/{sha}/pulls.
func (p *GHProvider) CompareResolved(ctx context.Context, fork forge.T1Data, sel forge.BranchSelection) (forge.T2Data, error) {
	// Same baseline guard as Compare: without it the compare path degrades to
	// "repos///compare/HEAD...", which 404s and would otherwise launder into
	// a false "0 ahead, 0 behind" result.
	p.mu.RLock()
	sourceOwner := p.sourceOwner
	sourceRepo := p.sourceRepo
	sourceDefaultBranch := p.sourceDefaultBranch
	p.mu.RUnlock()
	if sourceOwner == "" || sourceRepo == "" {
		return forge.T2Data{}, fmt.Errorf("compare resolved %s@%s: upstream not resolved (Parent not called)", fork.ID, sel.Branch)
	}

	parentBranch := sourceDefaultBranch
	if parentBranch == "" {
		parentBranch = "HEAD"
	}

	result, err := p.client.FetchCompare(ctx, sourceOwner, sourceRepo, parentBranch, fork.Owner, sel.Branch)
	if err != nil {
		return forge.T2Data{}, fmt.Errorf("compare resolved %s@%s: %w", fork.ID, sel.Branch, err)
	}

	scan := BranchScan{
		Compare:      result,
		Branch:       sel.Branch,
		Upstreamed:   sel.Upstreamed,
		UpstreamedPR: sel.UpstreamedPR,
	}

	return p.finishT2(ctx, sourceOwner, sourceRepo, scan, fork), nil
}

var _ forge.ResolvedCompareProvider = (*GHProvider)(nil)

// finishT2 completes a compare into forge.T2Data: converts the REST compare
// result, fills in patches the compare response omitted via the cookie
// web-diff client when one is configured, and applies the branch-work and
// upstreamed flags carried by scan. Compare (full branch scan) and
// CompareResolved (single pre-selected branch) differ only in how scan is
// produced; from here on they finish identically.
func (p *GHProvider) finishT2(ctx context.Context, sourceOwner, sourceRepo string, scan BranchScan, fork forge.T1Data) forge.T2Data {
	t2 := compareToT2(scan.Compare)

	if t2.FilesTruncated {
		p.fillTruncatedFiles(ctx, sourceOwner, sourceRepo, scan, fork, &t2)
	}

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
			} else if patches, truncated, webErr := p.client.webDiff.Fetch(ctx, sourceOwner, sourceRepo, base, head); webErr != nil {
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

	return t2
}

// fillTruncatedFiles attempts to recover the files a JSON compare lost past
// forge.CompareFilesCap by re-fetching the same compare as an unbounded
// unified diff (FetchCompareDiff). It mutates t2 in place and never returns
// an error: a Performed=true T2Data must stay Performed=true no matter how
// this fallback goes, so every non-success path just records why in
// t2.FilesTruncatedReason and leaves t2.Diffs (and the truncated flag) as
// compareToT2 left them.
//
// Called before the web-diff patch-fill step in finishT2 so that step still
// runs afterward and can top up any file — from the original 300 or from the
// newly recovered ones — that still lacks a patch (binary files, oversize
// patches).
func (p *GHProvider) fillTruncatedFiles(ctx context.Context, sourceOwner, sourceRepo string, scan BranchScan, fork forge.T1Data, t2 *forge.T2Data) {
	forkBranch := scan.Branch
	if forkBranch == "" {
		forkBranch = fork.DefaultBranch
	}
	// The same baseline compareToT2/Compare/CompareResolved already used —
	// re-read here rather than threaded through BranchScan because both
	// callers derive it identically from p.sourceDefaultBranch and this is
	// the only place downstream of them that needs it a second time.
	p.mu.RLock()
	parentBranch := p.sourceDefaultBranch
	p.mu.RUnlock()
	if parentBranch == "" {
		parentBranch = "HEAD"
	}

	files, complete, err := p.client.FetchCompareDiff(ctx, sourceOwner, sourceRepo, parentBranch, fork.Owner, forkBranch)
	switch {
	case err == nil && complete && len(files) >= len(t2.Diffs):
		// REST's patch is authoritative for files it covered (the diff's hunk
		// text should be identical, but there is no reason to discard a
		// value already known good); files beyond the original cap have no
		// REST patch to carry over and keep whatever unidiff.Parse gave them.
		restPatches := make(map[string]forge.FileDiff, len(t2.Diffs))
		for _, d := range t2.Diffs {
			if d.Patch != "" {
				restPatches[d.Path] = d
			}
		}
		var totalAdd, totalDel int
		for i := range files {
			if rd, ok := restPatches[files[i].Path]; ok {
				files[i].Patch = rd.Patch
				files[i].PatchSource = rd.PatchSource
			}
			totalAdd += files[i].Additions
			totalDel += files[i].Deletions
		}
		t2.Diffs = files
		t2.TotalAdditions = totalAdd
		t2.TotalDeletions = totalDel
		t2.MNA = computeMNAFromDiffs(files)
		t2.FilesTruncated = false
		t2.FilesComplete = true
	case err != nil:
		t2.FilesTruncatedReason = err.Error()
	case !complete:
		t2.FilesTruncatedReason = "diff fallback unavailable (compare not renderable as a diff, or exceeded the size cap)"
	default:
		t2.FilesTruncatedReason = fmt.Sprintf("diff fallback returned %d files, fewer than the %d-file compare JSON", len(files), len(t2.Diffs))
	}
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

// sourceFullPath returns the network root for a repository.
// For a non-fork, this is the repo's own FullName.
// For a fork, this is its source's FullName (or own FullName if source unavailable).
func sourceFullPath(info RepoInfo) string {
	if info.Source != nil && info.Source.FullName != "" {
		return info.Source.FullName
	}
	return info.FullName
}

// directParentFullPath returns the immediate parent's full name.
// For a non-fork, this is empty.
func directParentFullPath(info RepoInfo) string {
	if info.Parent != nil && info.Parent.FullName != "" {
		return info.Parent.FullName
	}
	return ""
}

func forkInfoToT1(f ForkInfo, extra *T1Extra, parentFullPath, sourceFullPath string) forge.T1Data {
	if sourceFullPath == "" {
		sourceFullPath = parentFullPath
	}
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
		SourceFullPath: sourceFullPath,
	}
	if parentFullPath != "" {
		t1.ParentFullPath = parentFullPath
		t1.IsForkOfFork = parentFullPath != sourceFullPath
	}

	if extra != nil {
		t1.OpenPRCount = extra.OpenPRCount
		t1.ReleaseCount = extra.ReleaseCount
		// Lineage fields are populated only on the GraphQL path (the
		// REST fallback leaves the extras nil, so all of these stay at
		// zero/empty/false — see brief: "REST list-forks has no parent
		// fields: keep direct parent and depth unknown instead of
		// fabricating the requested root").
		t1.ParentFullPath = extra.ParentFullPath
		t1.DepthFromRoot = extra.DepthFromRoot
		t1.IsForkOfFork = extra.DepthFromRoot > 1
		t1.DirectTotalCount = extra.DirectTotalCount
		t1.WholeNetworkForkCount = extra.WholeNetworkForkCount
		t1.DefaultTipSHA = extra.DefaultTipSHA

		branches := make([]forge.BranchRef, 0, len(extra.TopBranches))
		for _, br := range extra.TopBranches {
			cd, _ := time.Parse(time.RFC3339, br.LastCommitAt)
			branches = append(branches, forge.BranchRef{
				Name:          br.Name,
				CommittedDate: cd,
				TipSHA:        br.TipSHA,
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
	// GitHub's compare endpoint caps the returned commit list (250 at time of
	// writing) and reports the true count separately as TotalCommits. Above
	// that cap, Commits[len-1] is just the deepest commit the API happened to
	// return, not the fork's actual tip — trusting it would let two forks that
	// share a base and their first N ahead-commits, then diverge, collide on
	// a HeadSHA neither of them actually has at that position. Only trust it
	// when the list is known-complete, mirroring the Gitea provider's !capped
	// guard for the same reason.
	headSHA := ""
	if len(r.Commits) > 0 && r.TotalCommits == len(r.Commits) {
		headSHA = r.Commits[len(r.Commits)-1].SHA
	}

	return forge.T2Data{
		Performed:          r.Performed,
		AheadCount:         r.AheadBy,
		BehindCount:        r.BehindBy,
		MNA:                mna,
		TotalAdditions:     totalAdd,
		TotalDeletions:     totalDel,
		FeatureCommitRatio: fcr,
		BaseSHA:            baseSHA,
		HeadSHA:            headSHA,
		Diffs:              diffs,
		FilesTruncated:     len(r.Files) >= forge.CompareFilesCap,
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

// repoCompareBaseline returns the owner/repo/branch Compare and
// DivergentBranchCounts must use. Named seed wins unless GET /repos says
// this is a fork and source.full_name is a well-formed owner/repo.
func repoCompareBaseline(info RepoInfo, namedOwner, namedRepo string) (owner, repo, branch string) {
	owner, repo, branch = namedOwner, namedRepo, info.DefaultBranch
	if !info.Fork || info.Source == nil {
		return owner, repo, branch
	}
	so, sr, ok := splitFullName(info.Source.FullName)
	if !ok {
		return owner, repo, branch
	}
	owner, repo = so, sr
	if info.Source.DefaultBranch != "" {
		branch = info.Source.DefaultBranch
	}
	return owner, repo, branch
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
