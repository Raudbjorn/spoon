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
	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "h", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}
	if c.When().IsZero() || time.Since(c.When()) > time.Minute {
		t.Fatalf("When() unexpected: %v", c.When())
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
