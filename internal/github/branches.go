package github

import (
	"context"
	"fmt"
)

// ScanBranches tries non-default branches when the default branch shows ahead==0.
// Returns the best CompareResult and the branch name that produced it.
// Skips if headroom < 20%.
func (c *Client) ScanBranches(
	ctx context.Context,
	parentOwner, parentRepo, parentBranch string,
	fork ForkInfo,
	branches []BranchInfo,
) (*CompareResult, string, error) {
	// Rate limit guard
	if c.Headroom() < 0.20 {
		return nil, "", nil
	}

	var bestResult *CompareResult
	var bestBranch string

	for _, branch := range branches {
		if branch.Name == fork.DefaultBranch {
			continue
		}

		select {
		case <-ctx.Done():
			return bestResult, bestBranch, ctx.Err()
		default:
		}

		compare, err := c.FetchCompare(ctx, parentOwner, parentRepo, parentBranch, fork.Owner.Login, branch.Name)
		if err != nil {
			continue // skip branches that fail
		}

		if compare.AheadBy > 0 {
			if bestResult == nil || compare.AheadBy > bestResult.AheadBy {
				cpy := compare
				bestResult = &cpy
				bestBranch = branch.Name
			}
		}
	}

	return bestResult, bestBranch, nil
}

// FetchCompareWithBranchScan first tries the default branch, then scans side branches
// if ahead==0 and branch info is available.
func (c *Client) FetchCompareWithBranchScan(
	ctx context.Context,
	parentOwner, parentRepo, parentBranch string,
	fork ForkInfo,
	branches []BranchInfo,
) (CompareResult, string, error) {
	// Try default branch first
	result, err := c.FetchCompare(ctx, parentOwner, parentRepo, parentBranch, fork.Owner.Login, fork.DefaultBranch)
	if err != nil {
		return result, fork.DefaultBranch, err
	}

	// If default shows work, use it
	if result.AheadBy > 0 {
		return result, fork.DefaultBranch, nil
	}

	// Scan side branches if we have branch info
	if len(branches) == 0 {
		return result, fork.DefaultBranch, nil
	}

	branchResult, branchName, scanErr := c.ScanBranches(ctx, parentOwner, parentRepo, parentBranch, fork, branches)
	if scanErr != nil {
		return result, fork.DefaultBranch, nil // return default result on scan error
	}

	if branchResult != nil && branchResult.AheadBy > result.AheadBy {
		return *branchResult, branchName, nil
	}

	return result, fork.DefaultBranch, nil
}

// FormatBranchCloneCmd generates a clone + checkout command for a side branch.
func FormatBranchCloneCmd(htmlURL, repoName, branch, defaultBranch string) string {
	if branch == "" || branch == defaultBranch {
		return fmt.Sprintf("git clone %s", htmlURL)
	}
	return fmt.Sprintf("git clone %s && cd %s && git checkout %s", htmlURL, repoName, branch)
}
