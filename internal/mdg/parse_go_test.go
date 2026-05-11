package mdg

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeFile is a test helper.
func writeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func TestParseGoPackages_BasicModule(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "go.mod", "module example.com/m\n\ngo 1.21\n")
	writeFile(t, root, "main.go", `package main

import (
	"example.com/m/internal/auth"
	"fmt"
)

func main() { fmt.Println(auth.Hello()) }
`)
	writeFile(t, root, "internal/auth/auth.go", `package auth

import "example.com/m/internal/util"

func Hello() string { return util.Greet() }
`)
	writeFile(t, root, "internal/util/util.go", `package util

func Greet() string { return "hi" }
`)

	pkgs, err := parseGoPackages(root)
	if err != nil {
		t.Fatalf("parseGoPackages: %v", err)
	}
	if len(pkgs) != 3 {
		t.Fatalf("want 3 packages, got %d: %+v", len(pkgs), pkgs)
	}

	byPath := map[string]goPkgInfo{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	root3, ok := byPath["example.com/m"]
	if !ok {
		t.Fatalf("missing root package; got %v", keys(byPath))
	}
	if !root3.IsMain {
		t.Errorf("root package should be IsMain")
	}
	sort.Strings(root3.Imports)
	want := []string{"example.com/m/internal/auth"} // stdlib filtered
	if !equalStrSlices(root3.Imports, want) {
		t.Errorf("root imports: want %v, got %v", want, root3.Imports)
	}

	auth := byPath["example.com/m/internal/auth"]
	if auth.IsMain {
		t.Errorf("internal/auth should not be IsMain")
	}
	if !equalStrSlices(auth.Imports, []string{"example.com/m/internal/util"}) {
		t.Errorf("auth imports: got %v", auth.Imports)
	}
}

func TestParseGoPackages_NoGoMod(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "x.go", "package x\n")
	pkgs, err := parseGoPackages(root)
	if err == nil {
		t.Fatalf("expected error when go.mod is missing, got %d packages", len(pkgs))
	}
}

func TestParseGoPackages_SkipsVendorAndHidden(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, root, "vendor/foo/bar.go", "package bar\n")
	writeFile(t, root, ".git/x.go", "package x\n")
	writeFile(t, root, "node_modules/y/z.go", "package z\n")

	pkgs, err := parseGoPackages(root)
	if err != nil {
		t.Fatalf("parseGoPackages: %v", err)
	}
	for _, p := range pkgs {
		if containsPathSegment(p.ImportPath, "vendor") ||
			containsPathSegment(p.ImportPath, ".git") ||
			containsPathSegment(p.ImportPath, "node_modules") {
			t.Errorf("package from excluded dir leaked: %q", p.ImportPath)
		}
	}
}

func TestParseGoPackages_SkipsBrokenFiles(t *testing.T) {
	// A syntactically invalid file should be skipped with a warning, not crash.
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, root, "broken/x.go", "package broken\n\nimport \"fmt\nfunc bad(\n")

	pkgs, err := parseGoPackages(root)
	if err != nil {
		t.Fatalf("parseGoPackages should swallow per-file errors: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("want at least the root package, got %d", len(pkgs))
	}
}

// --- test helpers below ---

func keys(m map[string]goPkgInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsPathSegment(p, seg string) bool {
	if p == seg {
		return true
	}
	for i := 0; i+len(seg) <= len(p); i++ {
		if p[i:i+len(seg)] == seg {
			before := i == 0 || p[i-1] == '/' || p[i-1] == filepath.Separator
			after := i+len(seg) == len(p) || p[i+len(seg)] == '/' || p[i+len(seg)] == filepath.Separator
			if before && after {
				return true
			}
		}
	}
	return false
}
