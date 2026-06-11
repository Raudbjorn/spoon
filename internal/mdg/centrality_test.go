package mdg

import (
	"context"
	"testing"
	"time"
)

func TestCentrality_ScoreFork(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import "example.com/m/internal/auth"

func main() { auth.Hello() }
`)
	writeFile(t, root, "internal/auth/auth.go", `package auth

func Hello() {}
`)

	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "head-sha", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}

	// A file in the auth package — score should be > 0.
	got := c.ScoreFork([]string{"internal/auth/auth.go"})
	if got <= 0 || got > 1 {
		t.Fatalf("ScoreFork(auth file) out of (0,1]: %v", got)
	}
	// An untouched / unknown file path — score should be 0 (we map only to
	// modules we know).
	if got := c.ScoreFork([]string{"docs/intro.md"}); got != 0 {
		t.Fatalf("ScoreFork(unknown file) = %v, want 0", got)
	}
	// A top-level file (main.go) should map to the root package via the
	// empty-string key in dirToModule.
	if got := c.ScoreFork([]string{"main.go"}); got <= 0 || got > 1 {
		t.Fatalf("ScoreFork(root file) out of (0,1]: %v", got)
	}
	// Empty input.
	if got := c.ScoreFork(nil); got != 0 {
		t.Fatalf("ScoreFork(nil) = %v, want 0", got)
	}
}

func TestCentrality_When(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")
	start := time.Now()
	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "h", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}
	// When() must be at or after start, and within a wide-enough window to
	// tolerate slow CI runners.
	if c.When().IsZero() || c.When().Before(start) || time.Since(start) > 5*time.Minute {
		t.Fatalf("When() unexpected: %v (start=%v)", c.When(), start)
	}
}

func TestCentrality_CoreReturnsTopModules(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import (
	"example.com/m/core"
	"example.com/m/leaf"
)

func main() { core.A(); leaf.B() }
`)
	writeFile(t, root, "core/a.go", "package core\nfunc A() {}\n")
	writeFile(t, root, "leaf/b.go", "package leaf\nfunc B() {}\n")

	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "h", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}
	if len(c.Core()) == 0 {
		t.Fatalf("Core() should return at least one module")
	}
}

func TestCentrality_SatisfiesRepoInterface(t *testing.T) {
	// Compile-time check that *Centrality implements repo.Centrality. If the
	// import path or method set ever drifts, this test fails at compile time.
	var _ interface {
		ScoreFork(touchedFiles []string) float64
		Core() []string
		When() time.Time
	} = (*Centrality)(nil)
}

func TestCentrality_PythonFileScored(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.py", `import pkg.helper
`)
	writeFile(t, root, "pkg/__init__.py", "")
	writeFile(t, root, "pkg/helper.py", "")

	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "h", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}
	// pkg/helper.py should score > 0 (it's a real module in the graph).
	got := c.ScoreFork([]string{"pkg/helper.py"})
	if got <= 0 || got > 1 {
		t.Fatalf("ScoreFork(pkg/helper.py) out of (0,1]: %v", got)
	}
	// main.py is the entry; it should also score > 0.
	if got := c.ScoreFork([]string{"main.py"}); got <= 0 || got > 1 {
		t.Fatalf("ScoreFork(main.py) out of (0,1]: %v", got)
	}
}

func TestCentrality_GoFileStillScoredAfterRefactor(t *testing.T) {
	// Regression test: Phase A's Go scoring must keep working after the
	// dirToModule → fileToModule migration.
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import "example.com/m/internal/auth"

func main() { auth.Hello() }
`)
	writeFile(t, root, "internal/auth/auth.go", "package auth\nfunc Hello() {}\n")

	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "h", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}
	if got := c.ScoreFork([]string{"internal/auth/auth.go"}); got <= 0 || got > 1 {
		t.Fatalf("Go file score out of range: %v", got)
	}
}

func TestCentrality_NonSourceFileFallsBackToDir(t *testing.T) {
	// A README file inside a Go package directory should score against that
	// package even though it's not in the parser's file list.
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import "example.com/m/internal/auth"

func main() { auth.Hello() }
`)
	writeFile(t, root, "internal/auth/auth.go", "package auth\nfunc Hello() {}\n")

	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "h", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}
	// Touch a hypothetical README.md inside the auth dir. Exact file match
	// fails, but the dir fallback should kick in.
	got := c.ScoreFork([]string{"internal/auth/README.md"})
	if got <= 0 || got > 1 {
		t.Fatalf("fallback score out of range: %v", got)
	}
}
