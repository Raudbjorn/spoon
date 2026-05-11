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
	// No source files at all.
	_, err := Build(context.Background(), root, BuildOptions{})
	if err == nil {
		t.Fatalf("Build should error when no supported language is present")
	}
}

func TestBuild_PythonOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.py", `import os
from pkg.sub import helper
`)
	writeFile(t, root, "pkg/__init__.py", "")
	writeFile(t, root, "pkg/sub/__init__.py", "")
	writeFile(t, root, "pkg/sub/helper.py", "")

	g, err := Build(context.Background(), root, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := g.Index["main"]; !ok {
		t.Errorf("expected node 'main'; nodes=%+v", g.Nodes)
	}
	if _, ok := g.Index["pkg.sub.helper"]; !ok {
		t.Errorf("expected node 'pkg.sub.helper'; nodes=%+v", g.Nodes)
	}
	imports := g.Edges["main"]
	// "os" filtered as stdlib; "pkg.sub.helper" present (from from-import,
	// resolved to the sub-module).
	found := false
	for _, e := range imports {
		if e == "pkg.sub.helper" {
			found = true
		}
	}
	if !found {
		t.Errorf("main → pkg.sub.helper edge missing; edges=%v", imports)
	}
}

func TestBuild_PolyglotGoAndPython(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, root, "scripts/tool.py", "import os\n")

	g, err := Build(context.Background(), root, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := g.Index["example.com/m"]; !ok {
		t.Errorf("expected Go node example.com/m")
	}
	if _, ok := g.Index["scripts.tool"]; !ok {
		t.Errorf("expected Python node scripts.tool")
	}
}

func TestBuild_LanguageTagsCorrect(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, root, "tool.py", "import os\n")

	g, err := Build(context.Background(), root, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Find the Go module and confirm Lang=="go", IsMain==true.
	for _, n := range g.Nodes {
		if n.Path == "example.com/m" {
			if n.Lang != "go" {
				t.Errorf("Go module Lang: want %q, got %q", "go", n.Lang)
			}
			if !n.IsMain {
				t.Errorf("Go main package should be IsMain")
			}
		}
		if n.Path == "tool" {
			if n.Lang != "python" {
				t.Errorf("Python module Lang: want %q, got %q", "python", n.Lang)
			}
		}
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
