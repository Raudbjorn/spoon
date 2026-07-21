package github

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// BranchScan is the outcome of deciding which fork branch to attribute work to.
// It carries the divergence for the selected branch plus the already-upstreamed
// verdict for that branch's tip, so callers don't re-probe.
type BranchScan struct {
	Compare      CompareResult // divergence for the selected branch
	Branch       string        // selected branch name
	Upstreamed   bool          // selected branch tip heads a merged upstream PR
	UpstreamedPR int           // that PR's number when Upstreamed is true
}

// minHeadroom is the rate-limit reserve below which speculative branch scans
// and upstreamed probes are skipped — they are enrichment, not core data.
const minHeadroom = 0.20

// tipUpstreamed reports whether the head commit of cmp (the newest ahead commit)
// heads a merged PR into upstreamFullName. Returns (false, 0, nil) when there is
// nothing to probe, headroom is exhausted, the commit list is truncated, or the
// probe fails for a non-context reason — the divergence is then treated as
// genuine rather than dropped. A context cancellation is returned as an error so
// callers can propagate it instead of recording a fabricated "genuine work"
// verdict produced by an aborted probe.
func (c *Client) tipUpstreamed(ctx context.Context, upstreamFullName, forkOwner, forkRepo string, cmp CompareResult) (bool, int, error) {
	if len(cmp.Commits) == 0 || c.Headroom() < minHeadroom {
		return false, 0, nil
	}
	// GitHub's compare endpoint caps the embedded commit list (250 entries) and
	// reports the true size in TotalCommits. When the list is truncated the last
	// element is a middle commit, not the branch tip: probing it could match an
	// older squash-merged PR and wrongly zero a branch that carries genuine work
	// above it. No tip SHA is available from this response, so skip the probe.
	if cmp.TotalCommits > len(cmp.Commits) {
		return false, 0, nil
	}
	// GitHub compare returns ahead commits oldest-first, so the tip is last.
	tip := cmp.Commits[len(cmp.Commits)-1].SHA
	res, err := c.CheckUpstreamed(ctx, forkOwner, forkRepo, tip, upstreamFullName)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, 0, ctxErr
		}
		return false, 0, nil
	}
	return res.Upstreamed, res.PRNumber, nil
}

// ScanBranches selects a non-default branch to attribute work to when the
// default branch shows no divergence. It prefers the MOST RECENT branch that
// carries genuine, not-yet-upstreamed work — not the branch with the most
// commits, which on maintainer forks is typically an abandoned scratch/demo
// branch whose PR was squash-merged long ago.
//
// Returns nil when no side branch is divergent. When every divergent branch is
// already upstreamed, it returns the most-recent such branch with Upstreamed set
// so the caller can zero the fork as "merged" rather than mislabel it "no work".
// Skips entirely below the rate-limit reserve.
func (c *Client) ScanBranches(
	ctx context.Context,
	parentOwner, parentRepo, parentBranch string,
	fork ForkInfo,
	branches []BranchInfo,
) (*BranchScan, error) {
	if c.Headroom() < minHeadroom {
		return nil, nil
	}
	upstream := parentOwner + "/" + parentRepo

	// Branches arrive newest-first from the provider, but sort defensively so
	// recency-first selection holds regardless of caller ordering. Parse each
	// timestamp once up front rather than inside the comparator (which would
	// re-parse O(N log N) times).
	type datedBranch struct {
		info BranchInfo
		at   time.Time
	}
	ordered := make([]datedBranch, len(branches))
	for i, b := range branches {
		at, _ := time.Parse(time.RFC3339, b.LastCommitAt)
		ordered[i] = datedBranch{info: b, at: at}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].at.After(ordered[j].at)
	})

	var fallback *BranchScan // most-recent divergent-but-upstreamed branch
	for _, db := range ordered {
		branch := db.info
		if branch.Name == fork.DefaultBranch {
			continue
		}

		select {
		case <-ctx.Done():
			return fallback, ctx.Err()
		default:
		}

		cmp, err := c.FetchCompare(ctx, parentOwner, parentRepo, parentBranch, fork.Owner.Login, branch.Name)
		if err != nil {
			// A cancelled context makes every remaining fetch fail; returning
			// here keeps the last branch from exiting the loop with a nil error
			// that reads as "scan completed".
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fallback, ctxErr
			}
			continue // skip branches that fail
		}
		if cmp.AheadBy <= 0 {
			continue
		}

		up, pr, upErr := c.tipUpstreamed(ctx, upstream, fork.Owner.Login, fork.Name, cmp)
		if upErr != nil {
			return fallback, upErr
		}
		if up {
			// Remember the most-recent upstreamed branch, but keep looking for
			// genuine work on an older branch.
			if fallback == nil {
				cp := cmp
				fallback = &BranchScan{Compare: cp, Branch: branch.Name, Upstreamed: true, UpstreamedPR: pr}
			}
			continue
		}

		// First (most-recent) branch with genuine, not-yet-upstreamed work wins.
		return &BranchScan{Compare: cmp, Branch: branch.Name}, nil
	}

	return fallback, nil
}

// FetchCompareWithBranchScan returns the divergence to attribute to a fork. It
// tries the default branch first; if the default has genuine (not-upstreamed)
// work it wins. Otherwise it scans side branches for the most-recent branch
// with real work, falling back to the default-branch result.
//
// When the default branch is ahead but already upstreamed and every side branch
// is upstreamed too, the default-branch result is returned in preference to
// ScanBranches' upstreamed fallback. Both carry Upstreamed=true and score the
// fork identically; attributing the merged work to the fork's primary branch is
// the more meaningful of the two. ScanBranches' upstreamed fallback is only
// used when the default branch itself showed no divergence at all.
func (c *Client) FetchCompareWithBranchScan(
	ctx context.Context,
	parentOwner, parentRepo, parentBranch string,
	fork ForkInfo,
	branches []BranchInfo,
) (BranchScan, error) {
	upstream := parentOwner + "/" + parentRepo

	// Default branch first.
	result, err := c.FetchCompare(ctx, parentOwner, parentRepo, parentBranch, fork.Owner.Login, fork.DefaultBranch)
	if err != nil {
		return BranchScan{Compare: result, Branch: fork.DefaultBranch}, err
	}

	if result.AheadBy > 0 {
		up, pr, upErr := c.tipUpstreamed(ctx, upstream, fork.Owner.Login, fork.Name, result)
		if upErr != nil {
			return BranchScan{}, upErr
		}
		if !up {
			// Default branch carries genuine work — use it, no scan needed.
			return BranchScan{Compare: result, Branch: fork.DefaultBranch}, nil
		}
		// Default's own work is already merged; prefer a side branch with
		// genuine work if one exists before falling back to the merged default.
		if len(branches) > 0 {
			scan, scanErr := c.ScanBranches(ctx, parentOwner, parentRepo, parentBranch, fork, branches)
			if scanErr != nil {
				// ScanBranches only errors on ctx cancellation; propagate it
				// rather than masking it as a successful (merged) result.
				return BranchScan{}, scanErr
			}
			if scan != nil && !scan.Upstreamed {
				return *scan, nil
			}
		}
		return BranchScan{Compare: result, Branch: fork.DefaultBranch, Upstreamed: up, UpstreamedPR: pr}, nil
	}

	// Default shows no work: scan side branches.
	if len(branches) == 0 {
		return BranchScan{Compare: result, Branch: fork.DefaultBranch}, nil
	}
	scan, scanErr := c.ScanBranches(ctx, parentOwner, parentRepo, parentBranch, fork, branches)
	if scanErr != nil {
		// ctx cancellation: propagate rather than returning a successful
		// default-branch result the caller would treat as complete.
		return BranchScan{}, scanErr
	}
	if scan == nil {
		return BranchScan{Compare: result, Branch: fork.DefaultBranch}, nil
	}
	return *scan, nil
}

// FormatBranchCloneCmd generates a clone + checkout command for a side branch.
func FormatBranchCloneCmd(htmlURL, repoName, branch, defaultBranch string) string {
	if branch == "" || branch == defaultBranch {
		return fmt.Sprintf("git clone %s", htmlURL)
	}
	return fmt.Sprintf("git clone %s && cd %s && git checkout %s", htmlURL, repoName, branch)
}
