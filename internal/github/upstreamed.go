package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// associatedPR is the subset of the "list pull requests associated with a
// commit" response we need to decide whether a fork's work has already been
// merged upstream. A PR is "merged" when MergedAt is non-nil; State alone is
// insufficient because a closed-unmerged PR also reports state "closed".
type associatedPR struct {
	Number   int     `json:"number"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
	Base     struct {
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

// UpstreamedResult reports whether a fork commit's work has already landed in
// the upstream repository through a merged pull request.
type UpstreamedResult struct {
	// Upstreamed is true when the probed commit heads a merged PR whose base is
	// the upstream repo — i.e. the work is already integrated and there is
	// nothing left to pull from the fork.
	Upstreamed bool
	// PRNumber is the merged PR's number (0 when Upstreamed is false).
	PRNumber int
}

// PullsForCommit calls
//
//	GET /repos/{owner}/{repo}/commits/{sha}/pulls
//
// returning the pull requests that have {sha} as their head commit. Squash- and
// rebase-merges rewrite the SHA on the upstream default branch, so a
// "is this commit reachable from upstream/master" check (GET .../commits/{sha})
// returns 404 even for fully-merged work. GitHub nonetheless preserves the
// *PR association* on the original head commit, which is what makes this the
// reliable "already upstreamed" probe.
//
// A 404/403 (deleted or private fork) returns (nil, nil) — an empty association
// list, not an error. There is deliberately no distinct "unknown" state: callers
// treat an empty list as "no merged upstream PR found", which leaves the fork's
// divergence counted as genuine work. That is the conservative direction — an
// unreadable fork keeps its work rather than being silently zeroed as merged.
func (c *Client) PullsForCommit(ctx context.Context, owner, repo, sha string) ([]associatedPR, error) {
	path := fmt.Sprintf("repos/%s/%s/commits/%s/pulls", owner, repo, sha)

	resp, err := c.GetRaw(ctx, path)
	if err != nil {
		if isNotFound(err) || isForbidden(err) {
			return nil, nil
		}
		return nil, err
	}
	defer resp.Body.Close()

	var prs []associatedPR
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return nil, fmt.Errorf("parsing pulls-for-commit response: %w", err)
	}
	return prs, nil
}

// CheckUpstreamed reports whether the work at forkOwner/forkRepo@sha has been
// merged into upstreamFullName. A commit counts as upstreamed when it heads a
// pull request that (a) targets the upstream repo and (b) has been merged.
//
// Probing the branch *tip* is deliberately conservative: a merged tip means the
// entire branch up to it was merged (nothing left to integrate), while a branch
// carrying unmerged commits on top of a merged base has a non-merged tip and is
// correctly left unflagged. The empty-sha and empty-upstream cases short-circuit
// to "not upstreamed" so callers can probe unconditionally.
func (c *Client) CheckUpstreamed(ctx context.Context, forkOwner, forkRepo, sha, upstreamFullName string) (UpstreamedResult, error) {
	if sha == "" || upstreamFullName == "" {
		return UpstreamedResult{}, nil
	}

	prs, err := c.PullsForCommit(ctx, forkOwner, forkRepo, sha)
	if err != nil {
		return UpstreamedResult{}, err
	}

	for _, pr := range prs {
		if pr.MergedAt == nil || *pr.MergedAt == "" {
			continue
		}
		// Only a PR whose base is the upstream repo proves the work reached
		// upstream. PRs merged into the fork's own branches (or some unrelated
		// repo in the network) don't count. GitHub repo full names are
		// case-insensitive, so compare with EqualFold to avoid false negatives
		// on a casing mismatch (e.g. OpenVINOtoolkit vs openvinotoolkit).
		if !strings.EqualFold(pr.Base.Repo.FullName, upstreamFullName) {
			continue
		}
		return UpstreamedResult{Upstreamed: true, PRNumber: pr.Number}, nil
	}
	return UpstreamedResult{}, nil
}
