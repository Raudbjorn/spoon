// internal/mdg/parse_python_test.go
package mdg

import (
	"sort"
	"testing"
)

func TestParsePythonPackages_BasicModule(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "main.py", `import os
from pkg.sub import helper
import third_party

def main(): pass
`)
	writeFile(t, root, "pkg/__init__.py", "")
	writeFile(t, root, "pkg/sub/__init__.py", "")
	writeFile(t, root, "pkg/sub/helper.py", `from pkg.sub.util import work

def helper(): pass
`)
	writeFile(t, root, "pkg/sub/util.py", `def work(): pass
`)

	pkgs, err := parsePythonPackages(root)
	if err != nil {
		t.Fatalf("parsePythonPackages: %v", err)
	}

	byPath := map[string]pyPkgInfo{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}

	main, ok := byPath["main"]
	if !ok {
		t.Fatalf("missing top-level 'main'; got modules %v", pyKeys(byPath))
	}
	sort.Strings(main.Imports)
	wantMainImports := []string{"pkg.sub.helper", "third_party"} // stdlib "os" filtered
	if !equalStrSlices(main.Imports, wantMainImports) {
		t.Errorf("main imports: want %v, got %v", wantMainImports, main.Imports)
	}

	helper, ok := byPath["pkg.sub.helper"]
	if !ok {
		t.Fatalf("missing pkg.sub.helper; got %v", pyKeys(byPath))
	}
	if !equalStrSlices(helper.Imports, []string{"pkg.sub.util"}) {
		t.Errorf("helper imports: got %v", helper.Imports)
	}

	// pkg/__init__.py is the package itself; its ImportPath is "pkg".
	if _, ok := byPath["pkg"]; !ok {
		t.Errorf("missing 'pkg' (from pkg/__init__.py); got %v", pyKeys(byPath))
	}
	// pkg/sub/__init__.py → "pkg.sub"
	if _, ok := byPath["pkg.sub"]; !ok {
		t.Errorf("missing 'pkg.sub' (from pkg/sub/__init__.py); got %v", pyKeys(byPath))
	}
}

func TestParsePythonPackages_NoFiles(t *testing.T) {
	root := t.TempDir()
	pkgs, err := parsePythonPackages(root)
	if err != nil {
		t.Fatalf("expected no error for empty repo, got %v", err)
	}
	if len(pkgs) != 0 {
		t.Fatalf("expected 0 packages, got %d", len(pkgs))
	}
}

func TestParsePythonPackages_SkipsExcludedDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.py", "import os\n")
	writeFile(t, root, "venv/site-packages/foo.py", "import sys\n")
	writeFile(t, root, ".venv/bar.py", "import sys\n")
	writeFile(t, root, "node_modules/baz.py", "import sys\n")
	writeFile(t, root, ".git/q.py", "import sys\n")

	pkgs, err := parsePythonPackages(root)
	if err != nil {
		t.Fatalf("parsePythonPackages: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].ImportPath != "main" {
		t.Fatalf("expected exactly the top-level main module, got %+v", pkgs)
	}
}

func TestParsePythonPackages_MainFileMarked(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "app/__init__.py", "")
	writeFile(t, root, "app/__main__.py", "import sys\n")
	writeFile(t, root, "app/lib.py", "")

	pkgs, err := parsePythonPackages(root)
	if err != nil {
		t.Fatalf("parsePythonPackages: %v", err)
	}
	byPath := map[string]pyPkgInfo{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	main, ok := byPath["app.__main__"]
	if !ok {
		t.Fatalf("missing app.__main__; got %v", pyKeys(byPath))
	}
	if !main.IsMain {
		t.Errorf("app.__main__ should be IsMain")
	}
	lib, ok := byPath["app.lib"]
	if !ok {
		t.Fatalf("missing app.lib; got %v", pyKeys(byPath))
	}
	if lib.IsMain {
		t.Errorf("app.lib should not be IsMain")
	}
}

func TestParsePythonPackages_ParenthesizedAndMultiImport(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "m.py", `from pkg.sub import (
    a,
    b,
    c,
)
import x, y
from . import sibling
`)

	pkgs, err := parsePythonPackages(root)
	if err != nil {
		t.Fatalf("parsePythonPackages: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("want 1 package, got %d", len(pkgs))
	}
	imports := pkgs[0].Imports
	sort.Strings(imports)
	// `from pkg.sub import (a,b,c)` emits qualified raw imports `pkg.sub.a`,
	// `pkg.sub.b`, `pkg.sub.c`. Since pkg.sub does not exist in this fixture,
	// neither the qualified forms nor the parent are known modules; they
	// remain as opaque qualified external references. `import x, y` produces
	// edges "x" and "y". Relative `from . import sibling` is recorded as
	// ".sibling" (we do not resolve relative imports in Phase B).
	want := []string{".sibling", "pkg.sub.a", "pkg.sub.b", "pkg.sub.c", "x", "y"}
	if !equalStrSlices(imports, want) {
		t.Errorf("imports: want %v, got %v", want, imports)
	}
}

func TestParsePythonPackages_BrokenFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "good.py", "import os\n")
	writeFile(t, root, "broken.py", "def bad(\n  syntax error here\n")
	pkgs, err := parsePythonPackages(root)
	if err != nil {
		t.Fatalf("broken files should be skipped, not errored: %v", err)
	}
	// good.py is still discovered; broken.py is skipped or yields no imports.
	byPath := map[string]pyPkgInfo{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	if _, ok := byPath["good"]; !ok {
		t.Errorf("good.py should be present; got %v", pyKeys(byPath))
	}
}

func TestParsePythonPackages_SymbolImportCollapses(t *testing.T) {
	// `from pkg.util import MyClass` where MyClass is a symbol (not a
	// MyClass.py file) should collapse the raw "pkg.util.MyClass" import to
	// "pkg.util" via resolveImport's parent-lookup branch.
	root := t.TempDir()
	writeFile(t, root, "main.py", "from pkg.util import MyClass\n")
	writeFile(t, root, "pkg/__init__.py", "")
	writeFile(t, root, "pkg/util.py", "class MyClass: pass\n")

	pkgs, err := parsePythonPackages(root)
	if err != nil {
		t.Fatalf("parsePythonPackages: %v", err)
	}
	byPath := map[string]pyPkgInfo{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	main, ok := byPath["main"]
	if !ok {
		t.Fatalf("missing 'main'; got %v", pyKeys(byPath))
	}
	// "pkg.util.MyClass" is not a known module; "pkg.util" is → collapse.
	// The result should be exactly ["pkg.util"], not ["pkg.util.MyClass"].
	if !equalStrSlices(main.Imports, []string{"pkg.util"}) {
		t.Errorf("main imports: want [pkg.util] (collapsed symbol import), got %v", main.Imports)
	}
}

// pyKeys is a sorted-keys helper. Named distinctly from Phase A's keys() so
// the two test files don't collide.
func pyKeys(m map[string]pyPkgInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
