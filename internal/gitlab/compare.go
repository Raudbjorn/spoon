package gitlab

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// Compare implements forge.Forge.
func (p *Provider) Compare(ctx context.Context, fork forge.T1Data, branch string) (forge.T2Data, error) {
	forkEnc := encodeProjectPath(fork.ID)
	sourceEnc := encodeProjectPath(fork.SourceFullPath)

	// 1. Resolve upstream default branch name and tip SHA.
	upstreamBranch, upstreamSHA, err := p.resolveBranchTip(ctx, fork.SourceFullPath, "")
	if err != nil {
		return forge.T2Data{}, fmt.Errorf("resolve upstream tip for %s: %w", fork.SourceFullPath, err)
	}

	// 2. Commits and diffs: fork@branch vs upstreamSHA.
	cmp, err := p.rawCompare(ctx, forkEnc, upstreamSHA, branch)
	if err != nil {
		return forge.T2Data{}, fmt.Errorf("compare %s@%s vs upstream: %w", fork.ID, branch, err)
	}

	// 3. Behind count: upstream vs fork tip.
	var behindCount int
	forkTipSHA, tipErr := p.branchTipSHA(ctx, fork.ID, branch)
	if tipErr != nil {
		slog.Warn("could not resolve fork tip SHA; behind count will be 0",
			"fork", fork.ID, "branch", branch, "error", tipErr)
	} else {
		behind, behindErr := p.rawCompare(ctx, sourceEnc, forkTipSHA, upstreamBranch)
		if behindErr != nil {
			slog.Warn("behind-count compare failed; proceeding with 0",
				"fork", fork.ID, "error", behindErr)
		} else {
			behindCount = len(behind.Commits)
		}
	}

	// 4. Convert glDiff -> forge.FileDiff with parsed line counts.
	diffs := make([]forge.FileDiff, 0, len(cmp.Diffs))
	var totalAdd, totalDel int
	for _, d := range cmp.Diffs {
		add, del := d.parseDiffStats()
		totalAdd += add
		totalDel += del
		status := "modified"
		if d.OldPath != d.NewPath {
			status = "renamed"
		}
		diffs = append(diffs, forge.FileDiff{
			Path:         d.NewPath,
			PreviousPath: d.OldPath,
			Status:       status,
			Additions:    add,
			Deletions:    del,
			Patch:        d.Diff,
			PatchSource:  "compare_rest",
		})
	}

	// 5. Convert glCommit -> forge.AheadCommit.
	aheadCommits := make([]forge.AheadCommit, 0, len(cmp.Commits))
	for _, c := range cmp.Commits {
		aheadCommits = append(aheadCommits, forge.AheadCommit{
			SHA:         c.ID,
			Message:     c.FullMessage,
			AuthorEmail: c.AuthorEmail,
			Timestamp:   c.CommittedDate,
		})
	}

	// 6. Compute MNA.
	mna := computeMNA(diffs)

	// 7. Feature commit ratio.
	fcr := featureCommitRatio(aheadCommits)

	t2 := forge.T2Data{
		AheadCount:         len(aheadCommits),
		BehindCount:        behindCount,
		MNA:                mna,
		TotalAdditions:     totalAdd,
		TotalDeletions:     totalDel,
		FeatureCommitRatio: fcr,
		Diffs:              diffs,
		Commits:            aheadCommits,
	}

	return t2, nil
}

// rawCompare calls /projects/:enc/repository/compare.
func (p *Provider) rawCompare(ctx context.Context, projectEnc, from, to string) (glCompare, error) {
	var result glCompare
	_, err := p.client.Get(ctx,
		fmt.Sprintf("/projects/%s/repository/compare", projectEnc),
		url.Values{
			"from":     []string{from},
			"to":       []string{to},
			"straight": []string{"false"},
		},
		&result,
	)
	if err != nil {
		return glCompare{}, err
	}
	return result, nil
}

// resolveBranchTip returns the branch name and its tip commit SHA.
func (p *Provider) resolveBranchTip(ctx context.Context, fullPath, branch string) (resolvedBranch, sha string, err error) {
	enc := encodeProjectPath(fullPath)

	if branch == "" {
		var proj glProject
		if _, err := p.client.Get(ctx, "/projects/"+enc, nil, &proj); err != nil {
			return "", "", fmt.Errorf("get project %s: %w", fullPath, err)
		}
		branch = proj.DefaultBranch
	}

	sha, err = p.branchTipSHA(ctx, fullPath, branch)
	return branch, sha, err
}

// branchTipSHA returns the tip commit SHA for fullPath@branch.
func (p *Provider) branchTipSHA(ctx context.Context, fullPath, branch string) (string, error) {
	enc := encodeProjectPath(fullPath)
	branchEnc := url.PathEscape(branch)

	var b glBranch
	if _, err := p.client.Get(ctx,
		fmt.Sprintf("/projects/%s/repository/branches/%s", enc, branchEnc),
		nil, &b,
	); err != nil {
		return "", fmt.Errorf("branch tip %s@%s: %w", fullPath, branch, err)
	}
	return b.Commit.ID, nil
}

// computeMNA strips junk/generated/docs files and returns the net meaningful additions.
func computeMNA(diffs []forge.FileDiff) int {
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

// featureCommitRatio returns the fraction of commits that are not merge/sync commits.
func featureCommitRatio(commits []forge.AheadCommit) float64 {
	if len(commits) == 0 {
		return 0
	}
	feature := 0
	for _, c := range commits {
		if !isMergeOrSync(c.Message) {
			feature++
		}
	}
	return float64(feature) / float64(len(commits))
}

// isMergeOrSync filters merge/sync commit messages for both GitHub and GitLab.
func isMergeOrSync(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	prefixes := []string{
		"merge branch",
		"merge pull request",
		"merge request",
		"merge remote-tracking",
		"sync with upstream",
		"sync upstream",
		`revert "merge`,
	}
	for _, pfx := range prefixes {
		if strings.HasPrefix(lower, pfx) {
			return true
		}
	}
	return false
}
