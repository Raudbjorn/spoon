package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
)

// ForkRecord is one fork-with-intent's payload in features.json. Stable JSON
// shape — the Python side depends on the field names. "Fork" terminology is
// kept from the original spec; in this experiment a record's source is a PR's
// head repo (which is a fork or a same-repo branch), and the diff is the
// PR's diff against its base.
type ForkRecord struct {
	ID       string             `json:"id"`
	Owner    string             `json:"owner"`
	Name     string             `json:"name"`
	URL      string             `json:"url"`
	Stars    int                `json:"stars"`
	Features embed.ForkFeatures `json:"features"`
}

// prInfo is the subset of GitHub's REST PR-list response we need.
type prInfo struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref  string `json:"ref"`
		Repo *struct {
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
			Name string `json:"name"`
		} `json:"repo"`
	} `json:"head"`
}

// prFile is one entry from the GitHub PR files endpoint.
type prFile struct {
	Filename  string `json:"filename"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// prCommit is one entry from the GitHub PR commits endpoint.
type prCommit struct {
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

// dumpFeatures gathers up to topN PRs from owner/repo, fetches each PR's
// changed-files list and commits, builds embed.ForkFeatures via the same
// path spoon uses for production fork clustering, and returns one
// ForkRecord per PR with a non-empty DiffChunk.
//
// We use PRs (instead of fork list + Compare) because the vast majority of
// GitHub forks are pristine clones — only PR-bearing forks have a documented
// intent. Each PR maps to one record; the same fork can produce multiple
// records if it has multiple PRs. topN<=0 means no cap.
//
// State filter defaults to "all" (open + closed + merged). Forks-only filter
// is applied at consumer level — PRs from branches on the same repo are
// kept (they carry valid "intent" signal even if technically not from a
// fork).
func dumpFeatures(ctx context.Context, owner, repo string, topN int) ([]ForkRecord, error) {
	client, err := defaultClient()
	if err != nil {
		return nil, fmt.Errorf("REST client init: %w", err)
	}
	prs, err := listPRs(client, owner, repo, topN)
	if err != nil {
		return nil, fmt.Errorf("list PRs: %w", err)
	}

	out := make([]ForkRecord, 0, len(prs))
	for i, pr := range prs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		files, ferr := getPRFiles(client, owner, repo, pr.Number)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "files PR#%d: %v\n", pr.Number, ferr)
			continue
		}
		commits, cerr := getPRCommits(client, owner, repo, pr.Number)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "commits PR#%d: %v\n", pr.Number, cerr)
			continue
		}
		t2 := prToT2Data(pr, files, commits)
		features := embed.BuildFeatures(t2, "", 0)
		if features.DiffChunk == "" {
			continue
		}
		out = append(out, ForkRecord{
			ID:       fmt.Sprintf("pr-%d", pr.Number),
			Owner:    prHeadOwner(pr, owner),
			Name:     prHeadName(pr, repo),
			URL:      pr.HTMLURL,
			Features: features,
		})
		if (i+1)%25 == 0 {
			fmt.Fprintf(os.Stderr, "processed %d/%d PRs\n", i+1, len(prs))
		}
	}
	return out, nil
}

// prHeadOwner returns the PR's head-repo owner, falling back to the base
// repo's owner when head.repo is null (which can happen for PRs whose source
// fork has been deleted).
func prHeadOwner(pr prInfo, baseOwner string) string {
	if pr.Head.Repo != nil && pr.Head.Repo.Owner.Login != "" {
		return pr.Head.Repo.Owner.Login
	}
	return baseOwner
}

func prHeadName(pr prInfo, baseRepo string) string {
	if pr.Head.Repo != nil && pr.Head.Repo.Name != "" {
		return pr.Head.Repo.Name
	}
	return baseRepo
}

// prToT2Data converts the GitHub PR diff payloads into the forge.T2Data
// shape that embed.BuildFeatures expects. The PR title is prepended to the
// commits list — it's usually the cleanest single statement of intent,
// and BuildFeatures' commit-deduping will fold it away if it duplicates an
// existing commit message.
func prToT2Data(pr prInfo, files []prFile, commits []prCommit) forge.T2Data {
	diffs := make([]forge.FileDiff, 0, len(files))
	for _, f := range files {
		diffs = append(diffs, forge.FileDiff{
			Path:      f.Filename,
			Additions: f.Additions,
			Deletions: f.Deletions,
		})
	}
	ac := make([]forge.AheadCommit, 0, len(commits)+1)
	if strings.TrimSpace(pr.Title) != "" {
		ac = append(ac, forge.AheadCommit{Message: pr.Title})
	}
	for _, c := range commits {
		ac = append(ac, forge.AheadCommit{Message: c.Commit.Message})
	}
	return forge.T2Data{Diffs: diffs, Commits: ac}
}

// listPRs paginates the PR list endpoint and returns up to topN PRs sorted
// by updated_at desc.
func listPRs(client restClient, owner, repo string, topN int) ([]prInfo, error) {
	var out []prInfo
	for page := 1; ; page++ {
		path := fmt.Sprintf(
			"repos/%s/%s/pulls?state=all&sort=updated&direction=desc&per_page=100&page=%d",
			owner, repo, page,
		)
		var pagePRs []prInfo
		if err := client.Get(path, &pagePRs); err != nil {
			return out, fmt.Errorf("page %d: %w", page, err)
		}
		if len(pagePRs) == 0 {
			break
		}
		for _, p := range pagePRs {
			out = append(out, p)
			if topN > 0 && len(out) >= topN {
				return out, nil
			}
		}
		if len(pagePRs) < 100 {
			break
		}
	}
	return out, nil
}

func getPRFiles(client restClient, owner, repo string, n int) ([]prFile, error) {
	var out []prFile
	for page := 1; ; page++ {
		path := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100&page=%d", owner, repo, n, page)
		var pageFiles []prFile
		if err := client.Get(path, &pageFiles); err != nil {
			return out, fmt.Errorf("page %d: %w", page, err)
		}
		if len(pageFiles) == 0 {
			break
		}
		out = append(out, pageFiles...)
		if len(pageFiles) < 100 {
			break
		}
	}
	return out, nil
}

func getPRCommits(client restClient, owner, repo string, n int) ([]prCommit, error) {
	var out []prCommit
	for page := 1; ; page++ {
		path := fmt.Sprintf("repos/%s/%s/pulls/%d/commits?per_page=100&page=%d", owner, repo, n, page)
		var pageCommits []prCommit
		if err := client.Get(path, &pageCommits); err != nil {
			return out, fmt.Errorf("page %d: %w", page, err)
		}
		if len(pageCommits) == 0 {
			break
		}
		out = append(out, pageCommits...)
		if len(pageCommits) < 100 {
			break
		}
	}
	return out, nil
}

// restClient is the minimal interface dumpFeatures needs from the REST
// client. The production type ghAPI.RESTClient satisfies it; tests inject
// a fake to avoid hitting GitHub.
type restClient interface {
	Get(path string, response any) error
}

// Static check: ghAPI.RESTClient satisfies restClient.
var _ restClient = (*ghAPI.RESTClient)(nil)

// defaultClient is the per-process REST client constructor. Production uses
// ghAPI.DefaultRESTClient; tests override with a fake.
var defaultClient = func() (restClient, error) {
	return ghAPI.DefaultRESTClient()
}
