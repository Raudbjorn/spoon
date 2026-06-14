package gitea

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// maxMergeBaseWalk bounds the commit-existence walk that locates a fork's merge
// base. Forks diverged by more than this are rare and are the most interesting,
// but we still cap the per-fork request cost: beyond the cap the ahead count is
// reported as a lower bound and the diff is taken against the deepest walked
// commit (a representative recent slice).
const maxMergeBaseWalk = 60

// Compare implements forge.Forge. Because Gitea's cross-repo compare is broken
// (it 500s on codeberg.org), divergence is computed without it:
//
//  1. Walk the fork branch's commits newest-first; the first one that also
//     exists upstream is the merge base. The number walked before it is the
//     ahead count, and those commits are the ahead set.
//  2. INTRA-upstream compare merge-base...upstream-tip → behind count.
//  3. The raw .diff of merge-base...fork-tip (intra-fork) → per-file +/-.
func (p *Provider) Compare(ctx context.Context, fork forge.T1Data, branch string) (forge.T2Data, error) {
	fo, fr := splitFull(fork.ID)
	if branch == "" {
		branch = fork.DefaultBranch
	}

	srcOwner, srcRepo, srcTip, err := p.upstreamTip(ctx)
	if err != nil {
		return forge.T2Data{}, fmt.Errorf("resolve upstream tip: %w", err)
	}

	commits, err := p.forkCommits(ctx, fo, fr, branch, maxMergeBaseWalk)
	if err != nil {
		return forge.T2Data{}, fmt.Errorf("fork commits %s@%s: %w", fork.ID, branch, err)
	}
	if len(commits) == 0 {
		return forge.T2Data{}, nil
	}

	// 1. Merge-base walk.
	mergeBase := ""
	aheadCommits := make([]forge.AheadCommit, 0, len(commits))
	capped := false
	for _, c := range commits {
		exists, exErr := p.client.Exists(ctx,
			fmt.Sprintf("/repos/%s/%s/git/commits/%s", srcOwner, srcRepo, c.SHA))
		if exErr != nil {
			return forge.T2Data{}, fmt.Errorf("merge-base probe %s: %w", c.SHA, exErr)
		}
		if exists {
			mergeBase = c.SHA
			break
		}
		aheadCommits = append(aheadCommits, forge.AheadCommit{
			SHA:         c.SHA,
			Message:     c.Commit.Message,
			AuthorEmail: c.Commit.Author.Email,
			AuthorLogin: userLogin(c),
			Timestamp:   c.Commit.Committer.Date,
		})
	}
	if mergeBase == "" {
		// No base within the cap: ahead is a lower bound; diff against the
		// deepest commit we saw so we still produce representative stats.
		capped = true
		mergeBase = commits[len(commits)-1].SHA
	}

	t2 := forge.T2Data{
		AheadCount:         len(aheadCommits),
		FeatureCommitRatio: featureCommitRatio(aheadCommits),
		Commits:            aheadCommits,
	}
	if capped {
		t2.AheadCount = len(commits) // lower bound: at least this many
	}

	// 2. Behind: intra-upstream compare (merge-base is in upstream, so this works).
	if !capped {
		behind, bErr := p.intraCompareCount(ctx, srcOwner, srcRepo, mergeBase, srcTip)
		if bErr != nil {
			return forge.T2Data{}, fmt.Errorf("behind count %s: %w", fork.ID, bErr)
		}
		t2.BehindCount = behind
	}

	// 3. Per-file diff via the intra-fork raw .diff (the compare files array has
	// no counts). Skipped when there's nothing ahead.
	if len(aheadCommits) > 0 || capped {
		forkTip := commits[0].SHA
		diff, dErr := p.client.RawDiff(ctx, fo, fr, mergeBase, forkTip)
		if dErr != nil {
			return forge.T2Data{}, fmt.Errorf("raw diff %s: %w", fork.ID, dErr)
		}
		diffs := parseUnifiedDiff(diff)
		t2.Diffs = diffs
		for _, d := range diffs {
			t2.TotalAdditions += d.Additions
			t2.TotalDeletions += d.Deletions
		}
		t2.MNA = computeMNA(diffs)
	}

	return t2, nil
}

// upstreamTip returns the cached upstream owner/repo and resolves its
// default-branch tip SHA once.
func (p *Provider) upstreamTip(ctx context.Context) (owner, repo, tip string, err error) {
	// Hold the lock across the HTTP fetch: every Compare worker blocks on this
	// tip anyway, so serializing here collapses the thundering herd into a
	// single upstream branch-tip request instead of one per concurrent worker.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sourceOwner == "" || p.sourceRepo == "" {
		return "", "", "", fmt.Errorf("upstream not resolved (Parent not called)")
	}
	if p.sourceTip != "" {
		return p.sourceOwner, p.sourceRepo, p.sourceTip, nil
	}
	var b gtBranch
	if _, e := p.client.Get(ctx, fmt.Sprintf("/repos/%s/%s/branches/%s", p.sourceOwner, p.sourceRepo, p.sourceDefault), nil, &b); e != nil {
		return "", "", "", e
	}
	p.sourceTip = b.Commit.ID
	return p.sourceOwner, p.sourceRepo, p.sourceTip, nil
}

// forkCommits returns up to limit commits of fork@branch, newest-first.
func (p *Provider) forkCommits(ctx context.Context, owner, repo, branch string, limit int) ([]gtCommit, error) {
	var commits []gtCommit
	_, err := p.client.Get(ctx,
		fmt.Sprintf("/repos/%s/%s/commits", owner, repo),
		url.Values{"sha": []string{branch}, "limit": []string{strconv.Itoa(limit)}},
		&commits,
	)
	return commits, err
}

// intraCompareCount returns total_commits for an intra-repo base...head compare.
func (p *Provider) intraCompareCount(ctx context.Context, owner, repo, base, head string) (int, error) {
	var cmp gtCompare
	_, err := p.client.Get(ctx,
		fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, repo, base, head), nil, &cmp)
	if err != nil {
		return 0, err
	}
	return cmp.TotalCommits, nil
}

func userLogin(c gtCommit) string {
	if c.Author != nil && c.Author.Login != "" {
		return c.Author.Login
	}
	return ""
}

// parseUnifiedDiff counts per-file additions/deletions from a raw unified diff.
// Files are delimited by "diff --git a/<path> b/<path>"; within a file, lines
// starting with '+'/'-' (but not the '+++'/'---' headers) are added/removed.
func parseUnifiedDiff(diff string) []forge.FileDiff {
	if diff == "" {
		return nil
	}
	// Normalize CRLF so a trailing '\r' can't corrupt parsed paths or be
	// miscounted as a content line.
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	var out []forge.FileDiff
	var cur *forge.FileDiff
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &forge.FileDiff{Path: gitDiffPath(line)}
		case cur == nil:
			// preamble before the first file header
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			// file header lines, not content
		case strings.HasPrefix(line, "+"):
			cur.Additions++
		case strings.HasPrefix(line, "-"):
			cur.Deletions++
		}
	}
	flush()
	return out
}

// gitDiffPath extracts the new path from a "diff --git a/X b/Y" header. It
// locates the " b/" separator with LastIndex rather than splitting on spaces so
// that filenames containing spaces survive intact.
func gitDiffPath(header string) string {
	const prefix = "diff --git "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	line := header[len(prefix):]
	if idx := strings.LastIndex(line, " b/"); idx != -1 {
		return strings.Trim(line[idx+3:], `"`)
	}
	return ""
}

// computeMNA weights net additions per file by heat.FileWeight (junk/generated
// stripped), matching the GitHub/GitLab providers.
func computeMNA(diffs []forge.FileDiff) int {
	var total float64
	for _, d := range diffs {
		net := d.Additions - d.Deletions
		if net < 0 {
			net = 0
		}
		total += float64(net) * heat.FileWeight(d.Path)
	}
	return int(total)
}

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

func isMergeOrSync(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	for _, pfx := range []string{
		"merge branch", "merge pull request", "merge request",
		"merge remote-tracking", "sync with upstream", "sync upstream",
		`revert "merge`,
	} {
		if strings.HasPrefix(lower, pfx) {
			return true
		}
	}
	return false
}
