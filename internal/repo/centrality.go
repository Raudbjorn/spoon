// Package repo computes upstream-repository-level signals reused by the fork
// heat and clustering pipelines. DirectoryCentrality is a cheap proxy for
// module centrality from file-count share + commit-message keyword frequency.
package repo

import (
	"context"
	"sort"
	"strings"
	"time"
)

// TreeSource is the abstraction over "give me the recursive list of file paths
// in the upstream's default branch". Implementations may call GitHub's
// /repos/.../git/trees/{sha}?recursive=1 or read from a local clone.
type TreeSource interface {
	Tree(ctx context.Context, owner, repo string) ([]string, error)
}

// CommitSource is the abstraction over "give me a sample of upstream commit
// messages". Implementations typically pull from the default branch's recent
// commits via the provider's commits API.
type CommitSource interface {
	CommitMessages(ctx context.Context, owner, repo string, limit int) ([]string, error)
}

// DirectoryCentrality holds per-directory scores reflecting how "core" each
// directory is in the upstream repository.
type DirectoryCentrality struct {
	DirScore   map[string]float64 `json:"dirScore"`
	CoreDirs   []string           `json:"coreDirs"`
	ComputedAt time.Time          `json:"computedAt"`
	Provider   string             `json:"provider"`
	Owner      string             `json:"owner"`
	Repo       string             `json:"repo"`
}

const (
	// defaultCommitSampleSize is used when callers pass 0 for dirCommitSampleSize.
	defaultCommitSampleSize = 200
	// topK caps the number of entries in CoreDirs.
	topK = 10
	// fileShareWeight + keywordFreqWeight = 1.0.
	fileShareWeight   = 0.6
	keywordFreqWeight = 0.4
)

// Compute builds the centrality table for an upstream repository. Each
// directory d gets two components normalized to [0,1] within this call:
//
//	fileShare(d)   = files-in-d / total-files                 (weight 0.6)
//	keywordFreq(d) = (matches of d's last-path-component in
//	                  commit-message tokens) / total tokens   (weight 0.4)
//
// Directories are the interior nodes of the tree excluding the root. A path
// like "internal/auth/oauth.go" contributes one count to both "internal/" and
// "internal/auth/". dirCommitSampleSize defaults to 200 when 0.
func Compute(
	ctx context.Context,
	treeSrc TreeSource,
	commitSrc CommitSource,
	provider, owner, repo string,
	dirCommitSampleSize int,
) (DirectoryCentrality, error) {
	paths, err := treeSrc.Tree(ctx, owner, repo)
	if err != nil {
		return DirectoryCentrality{}, err
	}

	dirCounts := countDirs(paths)
	totalFiles := 0
	for _, p := range paths {
		if isMeaningfulFilePath(p) {
			totalFiles++
		}
	}

	// fileShare per directory.
	fileShare := make(map[string]float64, len(dirCounts))
	if totalFiles > 0 {
		for d, c := range dirCounts {
			fileShare[d] = float64(c) / float64(totalFiles)
		}
	}

	// Commit-message keyword frequencies. A CommitSource error is tolerated:
	// fall back to fileShare alone.
	if dirCommitSampleSize <= 0 {
		dirCommitSampleSize = defaultCommitSampleSize
	}
	var keywordFreq map[string]float64
	if commitSrc != nil {
		msgs, cerr := commitSrc.CommitMessages(ctx, owner, repo, dirCommitSampleSize)
		if cerr == nil {
			keywordFreq = computeKeywordFreq(dirCounts, msgs)
		}
	}

	// Combine. Each component is normalized to [0,1] by dividing by its own
	// maximum, then mixed with the configured weights.
	maxFileShare := maxOf(fileShare)
	maxKeyword := maxOf(keywordFreq)

	dirScore := make(map[string]float64, len(dirCounts))
	for d := range dirCounts {
		var fs, kw float64
		if maxFileShare > 0 {
			fs = fileShare[d] / maxFileShare
		}
		if maxKeyword > 0 {
			kw = keywordFreq[d] / maxKeyword
		}
		score := fileShareWeight*fs + keywordFreqWeight*kw
		// When CommitSource is missing/errored, the keyword component is
		// effectively zero. Renormalize so a pure-fileShare run still maxes at 1.0.
		if maxKeyword == 0 && fileShareWeight > 0 {
			score = fs
		}
		if score > 1 {
			score = 1
		}
		if score < 0 {
			score = 0
		}
		dirScore[d] = score
	}

	coreDirs := topKDirs(dirScore, topK)

	return DirectoryCentrality{
		DirScore:   dirScore,
		CoreDirs:   coreDirs,
		ComputedAt: time.Now().UTC(),
		Provider:   provider,
		Owner:      owner,
		Repo:       repo,
	}, nil
}

// ScoreFork returns a 0..1 ChangeImpact score for a fork given the directories
// it touched.
//
//	mean(DirScore[d]) for d in touchedDirs after normalization.
//
// If touchedDirs is empty, returns 0.0. If a touched dir is not in DirScore
// (perhaps the fork added a new directory), it contributes 0 to the sum.
// The mean is then clipped to [0, 1].
func (dc DirectoryCentrality) ScoreFork(touchedDirs []string) float64 {
	if len(touchedDirs) == 0 {
		return 0.0
	}
	var sum float64
	for _, d := range touchedDirs {
		if !strings.HasSuffix(d, "/") {
			d += "/"
		}
		if v, ok := dc.DirScore[d]; ok {
			sum += v
		}
	}
	mean := sum / float64(len(touchedDirs))
	if mean < 0 {
		return 0
	}
	if mean > 1 {
		return 1
	}
	return mean
}

// countDirs walks the directory ancestors of every meaningful file path and
// returns a map of directory key (trailing-slash form) → number of files
// contained beneath it (counted at any depth).
func countDirs(paths []string) map[string]int {
	out := make(map[string]int)
	for _, p := range paths {
		if !isMeaningfulFilePath(p) {
			continue
		}
		// Walk ancestors. For "a/b/c.go" emit "a/" and "a/b/".
		idx := strings.IndexByte(p, '/')
		for idx >= 0 {
			out[p[:idx+1]]++
			next := strings.IndexByte(p[idx+1:], '/')
			if next < 0 {
				break
			}
			idx = idx + 1 + next
		}
	}
	return out
}

// isMeaningfulFilePath drops top-level files (no '/') and obviously empty entries.
// Top-level files don't contribute to any directory bucket per the spec.
func isMeaningfulFilePath(p string) bool {
	if p == "" {
		return false
	}
	return strings.Contains(p, "/")
}

// computeKeywordFreq counts how often each directory's last-path-component
// appears as a token in the commit-message corpus, returning frequency
// (matches / total tokens). Empty corpus returns nil.
func computeKeywordFreq(dirCounts map[string]int, msgs []string) map[string]float64 {
	tokens := tokenizeMessages(msgs)
	if len(tokens) == 0 {
		return nil
	}
	// Build token-frequency index once.
	tokenCount := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tokenCount[t]++
	}
	out := make(map[string]float64, len(dirCounts))
	for d := range dirCounts {
		name := dirLastComponent(d)
		if name == "" {
			continue
		}
		c := tokenCount[strings.ToLower(name)]
		out[d] = float64(c) / float64(len(tokens))
	}
	return out
}

// tokenizeMessages lowercases each message, splits on whitespace, and strips
// surrounding punctuation from every token.
func tokenizeMessages(msgs []string) []string {
	var out []string
	for _, m := range msgs {
		m = strings.ToLower(m)
		for _, raw := range strings.Fields(m) {
			t := strings.TrimFunc(raw, isPunct)
			if t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

func isPunct(r rune) bool {
	switch r {
	case '.', ',', ':', ';', '!', '?', '(', ')', '[', ']', '{', '}',
		'"', '\'', '`', '/', '\\', '-', '_', '*', '+', '=', '<', '>', '|', '~', '@', '#', '$', '%', '^', '&':
		return true
	}
	return false
}

// dirLastComponent returns the name of the deepest segment of a "trailing-slash"
// directory key. For "internal/auth/" it returns "auth"; for "cmd/" it returns
// "cmd".
func dirLastComponent(d string) string {
	d = strings.TrimSuffix(d, "/")
	if d == "" {
		return ""
	}
	if i := strings.LastIndexByte(d, '/'); i >= 0 {
		return d[i+1:]
	}
	return d
}

// maxOf returns the max value in m, or 0 if empty.
func maxOf(m map[string]float64) float64 {
	var max float64
	for _, v := range m {
		if v > max {
			max = v
		}
	}
	return max
}

// topKDirs returns the top-k directory keys sorted by score desc, with ties
// broken lexicographically. Empty input → empty slice (never nil).
func topKDirs(scores map[string]float64, k int) []string {
	out := make([]string, 0, len(scores))
	for d := range scores {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if scores[out[i]] != scores[out[j]] {
			return scores[out[i]] > scores[out[j]]
		}
		return out[i] < out[j]
	})
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}
