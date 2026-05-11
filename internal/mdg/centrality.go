// Centrality wraps an MDG Graph + its PageRank scores and adapts them to the
// repo.Centrality interface. The mapping from a fork's touched file paths to
// per-module PageRank scores is derived from the Graph alone — no second
// filesystem walk needed — because every Go package's on-disk directory is
// recoverable from its canonical import path minus the module prefix.

package mdg

import (
	"context"
	"fmt"
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

	// fileToModule maps a slash-separated relative file path to its module
	// path. For Go packages every .go file in a package directory maps to
	// the package's import path. For Python every .py file maps to its
	// dotted module name (file-granular). Populated at build time from the
	// parsers' per-file/per-package data.
	fileToModule map[string]string

	// dirToModule maps a slash-separated directory key (without trailing
	// slash) to the canonical module path of any Go package whose files
	// live there. Used as a fallback in ScoreFork when an exact file match
	// in fileToModule fails — e.g., a touched README.md inside a Go package
	// directory. Python entries are not added here because each .py file is
	// its own module, so there is no useful "dir → one module" mapping.
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

	// Re-derive per-file and per-dir module mappings by calling the parsers
	// once more. parseGoPackages may return (nil, nil) for non-Go repos and
	// parsePythonPackages may return nil for non-Python repos; both are fine.
	fileToModule := make(map[string]string)
	dirToModule := make(map[string]string)

	goPkgs, err := parseGoPackages(repoPath)
	if err != nil {
		return nil, fmt.Errorf("parseGoPackages: %w", err)
	}
	for _, p := range goPkgs {
		for _, f := range p.Files {
			fileToModule[f] = p.ImportPath
		}
		// Derive directory key from the package's first file (any file in
		// the package lives in the same directory).
		if len(p.Files) > 0 {
			d := parentDir(p.Files[0])
			dirToModule[d] = p.ImportPath
		}
	}

	pyPkgs, err := parsePythonPackages(repoPath)
	if err != nil {
		return nil, fmt.Errorf("parsePythonPackages: %w", err)
	}
	for _, p := range pyPkgs {
		fileToModule[p.File] = p.ImportPath
	}

	return &Centrality{
		Provider:     provider,
		Owner:        repoOwner,
		Repo:         repoName,
		HeadSHA:      headSHA,
		Scores:       scores,
		core:         topKByScore(scores, defaultTopK),
		computedAt:   time.Now().UTC(),
		fileToModule: fileToModule,
		dirToModule:  dirToModule,
	}, nil
}

// ScoreFork returns the normalized mean PageRank score across the modules
// touched by the fork's files. Files whose path does not map to any known
// module contribute 0 (and are not counted toward the mean).
//
// Lookup order: exact file match in fileToModule wins; if not found, fall
// back to dirToModule using the file's parent directory (handles non-source
// files such as README.md inside a known Go package directory).
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
		// Exact file match wins. Fall back to dir match for non-source
		// files in known Go package directories.
		mod, ok := c.fileToModule[f]
		if !ok {
			mod = c.dirToModule[parentDir(f)]
		}
		if mod == "" {
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
