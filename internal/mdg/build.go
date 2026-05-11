package mdg

import (
	"context"
	"fmt"
	"os"
)

// BuildOptions tune the Build process. All fields are optional; the zero
// value runs the full polyglot pipeline (Go + Python).
type BuildOptions struct {
	// Languages is a whitelist of language tags ("go", "python") to enable.
	// Reserved for future use; currently all supported languages are always
	// parsed. Passing non-empty values has no effect today.
	Languages []string
}

// Build parses repoPath and returns the MDG for all supported languages
// (currently Go and Python). Both parsers run regardless of which languages
// are present; results are merged into a single graph. Module.Lang identifies
// the language of each node ("go", "python", or "" for external/opaque leaves).
//
// Errors:
//   - repoPath cannot be read
//   - neither Go nor Python source files are found (no supported language detected)
//   - context cancellation (checked before stat and between parser stages)
//
// Per-file or per-package parse failures are logged to stderr and skipped.
func Build(ctx context.Context, repoPath string, opts BuildOptions) (*Graph, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, err := os.Stat(repoPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", repoPath, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("repoPath is not a directory: %s", repoPath)
	}

	goPkgs, err := parseGoPackages(repoPath)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	pyPkgs, err := parsePythonPackages(repoPath)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(goPkgs) == 0 && len(pyPkgs) == 0 {
		return nil, fmt.Errorf("no supported language found in %s", repoPath)
	}

	g := NewGraph()

	// Pass 1: register every in-module package as a node.
	for _, p := range goPkgs {
		g.AddNode(Module{Path: p.ImportPath, Lang: "go", IsMain: p.IsMain})
	}
	for _, p := range pyPkgs {
		g.AddNode(Module{Path: p.ImportPath, Lang: "python", IsMain: p.IsMain})
	}

	// Pass 2: walk imports for both languages. Unknown imports become opaque
	// leaf nodes with Lang="".
	addEdges := func(srcPath string, deps []string) {
		srcIdx, ok := g.Index[srcPath]
		if !ok {
			return
		}
		for _, dep := range deps {
			dstIdx, ok := g.Index[dep]
			if !ok {
				dstIdx = g.AddNode(Module{Path: dep, Lang: ""})
			}
			g.AddEdge(srcIdx, dstIdx)
		}
	}
	for _, p := range goPkgs {
		addEdges(p.ImportPath, p.Imports)
	}
	for _, p := range pyPkgs {
		addEdges(p.ImportPath, p.Imports)
	}

	return g, nil
}
