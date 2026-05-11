package mdg

import (
	"context"
	"fmt"
	"os"
)

// BuildOptions tune the Build process. All fields are optional; the zero
// value runs the default Phase-A Go-only pipeline.
type BuildOptions struct {
	// Languages is a whitelist of language tags ("go") to enable. Ignored in
	// Phase A (Go only); reserved for Phase B/C. Passing non-empty values has
	// no effect today.
	Languages []string
}

// Build parses repoPath and returns the MDG. Phase A supports Go only.
//
// Errors:
//   - repoPath cannot be read
//   - no go.mod (Phase A's only supported entry condition)
//   - context cancellation (checked before stat and after parsing; the parse
//     phase itself is not interruptible in Phase A)
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

	pkgs, err := parseGoPackages(repoPath)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	g := NewGraph()
	// Pass 1: register every in-module package as a node.
	for _, p := range pkgs {
		g.AddNode(Module{Path: p.ImportPath, Lang: "go", IsMain: p.IsMain})
	}
	// Pass 2: walk imports. Unknown imports become opaque leaf nodes.
	for _, p := range pkgs {
		srcIdx := g.Index[p.ImportPath]
		for _, dep := range p.Imports {
			dstIdx, ok := g.Index[dep]
			if !ok {
				dstIdx = g.AddNode(Module{Path: dep, Lang: ""})
			}
			g.AddEdge(srcIdx, dstIdx)
		}
	}
	return g, nil
}
