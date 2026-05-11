package mdg

import (
	"context"
	"sort"
	"testing"
)

func TestBuild_GoModule(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import "example.com/m/internal/auth"

func main() { auth.Hello() }
`)
	writeFile(t, root, "internal/auth/auth.go", `package auth

func Hello() {}
`)

	g, err := Build(context.Background(), root, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Expect 2 nodes: example.com/m (main), example.com/m/internal/auth.
	if got := len(g.Nodes); got != 2 {
		t.Fatalf("Nodes: want 2, got %d (%+v)", got, g.Nodes)
	}
	root3, ok := g.Index["example.com/m"]
	if !ok {
		t.Fatalf("missing root node")
	}
	if !g.Nodes[root3].IsMain {
		t.Errorf("root should be IsMain")
	}
	imports := g.Edges["example.com/m"]
	sort.Strings(imports)
	if !equalStrSlices(imports, []string{"example.com/m/internal/auth"}) {
		t.Errorf("root edges: got %v", imports)
	}
}

func TestBuild_ExternalImportBecomesLeaf(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import "github.com/cli/go-gh/v2"

func main() { _ = gh.Foo }
`)

	g, err := Build(context.Background(), root, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := g.Index["github.com/cli/go-gh/v2"]; !ok {
		t.Fatalf("external import should become a node; nodes=%+v", g.Nodes)
	}
	if got := g.Edges["github.com/cli/go-gh/v2"]; len(got) != 0 {
		t.Errorf("external import should be a leaf, got %v", got)
	}
	if got := g.Edges["example.com/m"]; len(got) != 1 || got[0] != "github.com/cli/go-gh/v2" {
		t.Errorf("root should import the external leaf: got %v", got)
	}
}

func TestBuild_EmptyRepo(t *testing.T) {
	root := t.TempDir()
	// No go.mod, no source files. With the polyglot Build, this is not an
	// error — it just produces an empty graph (no languages detected).
	g, err := Build(context.Background(), root, BuildOptions{})
	if err != nil {
		t.Fatalf("Build should not error for an empty repo, got: %v", err)
	}
	if len(g.Nodes) != 0 {
		t.Fatalf("expected empty graph for empty repo, got %d nodes", len(g.Nodes))
	}
}

func TestBuild_ContextCancellation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Build(ctx, root, BuildOptions{})
	if err == nil {
		t.Fatalf("Build should respect context cancellation")
	}
}
