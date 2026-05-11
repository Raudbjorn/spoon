// Centrality wraps an MDG Graph + its PageRank scores and adapts them to the
// repo.Centrality interface. The mapping from a fork's touched file paths to
// per-module PageRank scores is derived from the Graph alone — no second
// filesystem walk needed — because every Go package's on-disk directory is
// recoverable from its canonical import path minus the module prefix.

package mdg

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultDamping    = 0.85
	defaultIterations = 50
	defaultTopK       = 10
)

// Centrality is the MDG-backed centrality implementation. It satisfies the
// repo.Centrality interface structurally — there is no `import "internal/repo"`
// here, only the method set agreement (`ScoreFork`, `Core`, `When`).
type Centrality struct {
	Provider   string
	Owner      string
	Repo       string
	HeadSHA    string
	Scores     map[string]float64 // module path → PageRank score
	core       []string
	computedAt time.Time

	// dirToModule maps a slash-separated directory (relative to repoPath,
	// e.g., "internal/auth") to its canonical module path. Used by ScoreFork
	// to translate a touched file path → its module's score. The root package
	// is keyed by the empty string.
	dirToModule map[string]string
}

// BuildCentrality parses the repo at repoPath, builds the MDG, runs
// personalized PageRank biased toward entry-point packages, and returns the
// resulting Centrality. HeadSHA is opaque to this function — it is stored on
// the result so callers (Task 10) can persist it in the MDG cache.
func BuildCentrality(
	ctx context.Context,
	repoPath, provider, repoOwner, repoName, headSHA string,
	opts BuildOptions,
) (*Centrality, error) {
	g, err := Build(ctx, repoPath, opts)
	if err != nil {
		return nil, err
	}
	tp := g.EntryPointTeleport()
	scores := g.PageRank(tp, defaultDamping, defaultIterations)

	// Build the dir → module map by inverting each in-module Go node's path:
	// the on-disk directory equals ImportPath minus the module prefix.
	// External (Lang == "") nodes don't have an on-disk directory; skip them.
	modulePath, err := readGoModulePath(repoPath)
	if err != nil {
		// Build would normally have failed first, but if Build's pre-checks
		// ever change and a callout gets here without a go.mod, surface a
		// clean error rather than building a half-empty map.
		return nil, err
	}
	dirToModule := make(map[string]string)
	for _, n := range g.Nodes {
		if n.Lang != "go" {
			continue
		}
		rel := strings.TrimPrefix(n.Path, modulePath)
		rel = strings.TrimPrefix(rel, "/")
		dirToModule[rel] = n.Path
	}

	return &Centrality{
		Provider:    provider,
		Owner:       repoOwner,
		Repo:        repoName,
		HeadSHA:     headSHA,
		Scores:      scores,
		core:        topKByScore(scores, defaultTopK),
		computedAt:  time.Now().UTC(),
		dirToModule: dirToModule,
	}, nil
}

// ScoreFork returns the normalized mean PageRank score across the modules
// touched by the fork's files. Files whose directory does not map to any
// known module contribute 0 (and are not counted toward the mean).
//
// The result is in [0,1]: divided by the top PageRank score in the table so
// the most-central module produces a 1.0 score.
func (c *Centrality) ScoreFork(touchedFiles []string) float64 {
	if c == nil || len(touchedFiles) == 0 || len(c.Scores) == 0 {
		return 0
	}
	var top float64
	for _, v := range c.Scores {
		if v > top {
			top = v
		}
	}
	if top <= 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(touchedFiles))
	var sum float64
	for _, f := range touchedFiles {
		f = filepath.ToSlash(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		d := parentDir(f)
		mod, ok := c.dirToModule[d]
		if !ok {
			continue
		}
		if _, dup := seen[mod]; dup {
			continue
		}
		seen[mod] = struct{}{}
		sum += c.Scores[mod]
	}
	if len(seen) == 0 {
		return 0
	}
	mean := (sum / float64(len(seen))) / top
	if mean > 1 {
		return 1
	}
	if mean < 0 {
		return 0
	}
	return mean
}

// Core returns the top-K module paths by PageRank score, ties broken
// lexicographically. Implements repo.Centrality.
func (c *Centrality) Core() []string { return c.core }

// When returns the timestamp at which this Centrality was computed.
// Implements repo.Centrality.
func (c *Centrality) When() time.Time { return c.computedAt }

// parentDir returns the slash-separated parent directory of p, or "" for a
// top-level file. Examples: "internal/auth/x.go" → "internal/auth",
// "main.go" → "".
func parentDir(p string) string {
	idx := strings.LastIndexByte(p, '/')
	if idx < 0 {
		return ""
	}
	return p[:idx]
}

// topKByScore returns the top-K module paths sorted by score desc, ties
// broken lexicographically.
func topKByScore(scores map[string]float64, k int) []string {
	type kv struct {
		k string
		v float64
	}
	pairs := make([]kv, 0, len(scores))
	for path, v := range scores {
		pairs = append(pairs, kv{path, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	out := make([]string, 0, k)
	for i := 0; i < len(pairs) && i < k; i++ {
		out = append(out, pairs[i].k)
	}
	return out
}
