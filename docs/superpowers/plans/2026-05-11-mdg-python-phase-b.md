# Full MDG Centrality — Phase B (Python) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the MDG centrality backend to support Python repositories. Phase A shipped Go-only; this plan adds a Python import parser using tree-sitter, generalizes the `Build` orchestrator to combine multiple language backends into a single graph, and refits the `Centrality` bridge to use a per-file → per-module map so Python's file-granular module model is supported precisely.

**Architecture:** A new parser (`internal/mdg/parse_python.go`) walks a repo's `.py` files using the `smacker/go-tree-sitter` Python grammar. Each `.py` file becomes a node (Python modules are file-granular, unlike Go's directory-granular packages); each `import` or `from … import …` statement becomes an edge. A bundled static list of Python 3 standard-library module names filters stdlib imports out of the graph (mirroring what Phase A does for Go). The existing `Build` orchestrator becomes polyglot — `parseGoPackages` is relaxed to return `(nil, nil)` when no `go.mod` is present, and `Build` calls both Go and Python parsers, errors only when neither finds anything. The `Centrality` bridge gains a `fileToModule` map keyed by relative file path (replacing the directory-keyed `dirToModule` from Phase A), populated from each parser's per-file data.

**Tech Stack:** Go 1.26+, `github.com/smacker/go-tree-sitter` and its `python` subpackage (cgo-based; CGO_ENABLED=1 required for builds, which is the Go default). No other new dependencies.

---

## Why tree-sitter (and the cgo cost)

The Phase B brainstorming considered three approaches: hand-written regex, tree-sitter via cgo (`smacker/go-tree-sitter`), and WASM-based tree-sitter (`wazero`). Tree-sitter via cgo was chosen because:

1. Robustness against Python edge cases (multi-line `from x import (a, b, c)` blocks, `f`-strings that contain quotes, line continuations, conditional imports inside `try:` blocks).
2. The Python grammar's import-statement node types are well-defined and stable across grammar versions.
3. `smacker/go-tree-sitter/python` bundles the grammar's C source — no separate fetch step, ~1 MB added to the binary.

The cost: every spoon build now needs a C toolchain. `CGO_ENABLED=1` is the Go default, but some minimal CI images (e.g., distroless or musl-only) ship without `gcc`. Document this in the README at the end of the plan.

**Out of scope (deferred to a later plan or marked as a known limitation):**

- Dynamic imports via `importlib.import_module(name)` where `name` is computed at runtime. Tree-sitter sees them as opaque function calls and we do not synthesize edges from them. Mirrors Phase A's Go decision to ignore `reflect`-style call-based dependencies.
- Conditional imports inside `if … :` blocks (e.g., backports). Tree-sitter parses them correctly; we record them as if they always fire. False-positive edges are preferred over missed edges for centrality.
- Namespace packages without an `__init__.py` (PEP 420). Phase B treats any directory containing `.py` files as a package; no `__init__.py` requirement.
- `*.pyi` stub files. Skipped.
- Python 2 syntax (`print` statement, `except Exception, e:`). Tree-sitter handles them; the import grammar is unchanged. We do not test against Python-2-only repos.
- `__all__` re-export inspection. Out of scope; this plan only models import edges, not visibility.

---

## File structure

**New files:**

- `internal/mdg/parse_python.go` — `parsePythonPackages(rootDir) ([]pyPkgInfo, error)` using tree-sitter
- `internal/mdg/parse_python_test.go`
- `internal/mdg/python_stdlib.go` — bundled stdlib module set + `isPythonStdlib(string) bool`
- `internal/mdg/python_stdlib_test.go` (small — checks a few known names; defensive against accidental list truncation)

**Modified files:**

- `go.mod`, `go.sum` — add `github.com/smacker/go-tree-sitter` and its Python subpackage
- `internal/mdg/parse_go.go` — `parseGoPackages` returns `(nil, nil)` when no `go.mod`; `goPkgInfo` gains `Files []string`
- `internal/mdg/parse_go_test.go` — assert the new `Files` field; rewrite the `TestParseGoPackages_NoGoMod` test to expect a clean nil return rather than an error
- `internal/mdg/build.go` — `Build` runs both parsers and merges; new error message when no parser finds anything
- `internal/mdg/build_test.go` — add Python-only and mixed-language fixtures
- `internal/mdg/centrality.go` — replace `dirToModule` with `fileToModule`; populate from per-file data
- `internal/mdg/centrality_test.go` — add a Python-file scoring test
- `README.md` or wherever the project documents build prerequisites — note CGO requirement

Each file has one responsibility:

- `parse_python.go` owns tree-sitter invocation and Python-specific module derivation.
- `python_stdlib.go` owns the stdlib name set (data-only file; treat as a generated table even though we maintain it by hand).
- `build.go` is the only orchestrator that combines parsers into a graph.
- `centrality.go` owns the file→module resolution and ScoreFork normalization.

---

## Background: Python's module model vs. Go's

This section is required reading before writing code — the conceptual mismatch is the source of most of Phase B's complexity.

| Concept | Go | Python |
|---|---|---|
| Module identifier | Import path: `example.com/m/internal/auth` | Dotted name: `pkg.sub.mod` |
| Module → directory | One package per directory; multiple files share the package | Each `.py` file is its own module; the package is the directory that contains them |
| Package init | None at the directory level | `__init__.py` (or PEP 420 implicit if absent) |
| Entry point | `package main` | `__main__.py` or `if __name__ == "__main__":` (we use the file-name heuristic only) |
| Stdlib detection | Heuristic: no dot in first segment | Static list (Python stdlib is flat-namespaced, the heuristic doesn't apply) |
| External nodes | `github.com/cli/safeexec` etc. (no on-disk dir) | `numpy`, `requests` etc. (no on-disk dir) |

The practical consequences:

1. The Phase A `dirToModule` map is wrong for Python — every `.py` file is its own module, so the map from directory → module is many-to-one. We replace it with `fileToModule` (file → module).
2. To populate `fileToModule` for Go, the parser must record each package's file list. We add `Files []string` to `goPkgInfo`.
3. The `Module.Path` field stays a single string. For Python, the value is the dotted name (e.g., `pkg.sub.mod`). External Python imports become opaque leaves with `Lang = ""`, just like external Go imports.

---

## Task 1: Add the tree-sitter dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the modules**

Run from the repo root:

```bash
go get github.com/smacker/go-tree-sitter@latest
go get github.com/smacker/go-tree-sitter/python@latest
```

Then `go mod tidy`. This pulls in `github.com/smacker/go-tree-sitter` and its `python` subpackage.

- [ ] **Step 2: Confirm the build still works**

Run: `go build ./...`
Expected: clean.

Run: `go test ./...`
Expected: all packages still pass.

Run: `go vet ./...`
Expected: clean.

If `go build` fails with a cgo error like `gcc: command not found`, the build host is missing a C toolchain. Document the requirement (Task 8); do not vendor an alternative parser to dodge cgo.

- [ ] **Step 3: Smoke test that the binding works**

Write a tiny throwaway file `internal/mdg/_treesitter_smoke.go` (note the leading underscore — Go's build tool ignores files starting with `_`) with:

```go
//go:build never

package mdg

import (
	"context"
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
)

func _smoke() {
	p := sitter.NewParser()
	p.SetLanguage(python.GetLanguage())
	tree, _ := p.ParseCtx(context.Background(), nil, []byte("import os\n"))
	fmt.Println(tree.RootNode().String())
}
```

Run `go vet ./internal/mdg/...` — should pass (the `//go:build never` constraint excludes the file from compilation but vet still checks syntax). If vet complains about the unused symbol, that's expected and we'll delete this file before commit anyway.

Then delete the file: `rm internal/mdg/_treesitter_smoke.go`.

This step is just a sanity check that the import path resolves and the API surface is what we expect. Do not commit the smoke file.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "mdg: add go-tree-sitter + Python grammar for Phase B"
```

---

## Task 2: Python stdlib module list

**Files:**
- Create: `internal/mdg/python_stdlib.go`
- Create: `internal/mdg/python_stdlib_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/mdg/python_stdlib_test.go
package mdg

import "testing"

func TestIsPythonStdlib_KnownNames(t *testing.T) {
	// A handful of well-known stdlib modules. The test is intentionally
	// not exhaustive — we just want a smoke-level check that the list is
	// reasonable and the lookup function works.
	for _, name := range []string{"os", "sys", "json", "io", "typing", "asyncio", "pathlib"} {
		if !isPythonStdlib(name) {
			t.Errorf("%q should be classified as stdlib", name)
		}
	}
}

func TestIsPythonStdlib_NotStdlib(t *testing.T) {
	for _, name := range []string{"numpy", "requests", "pkg.sub.mod", "", "django"} {
		if isPythonStdlib(name) {
			t.Errorf("%q should NOT be classified as stdlib", name)
		}
	}
}

func TestIsPythonStdlib_NamespaceSegment(t *testing.T) {
	// "os.path" should classify as stdlib by virtue of its first segment.
	if !isPythonStdlib("os.path") {
		t.Errorf("os.path should be stdlib")
	}
	// "json.tool" — same.
	if !isPythonStdlib("json.tool") {
		t.Errorf("json.tool should be stdlib")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestIsPythonStdlib_`
Expected: FAIL — `isPythonStdlib` not defined.

- [ ] **Step 3: Create the bundled list and the helper**

```go
// internal/mdg/python_stdlib.go
//
// Bundled Python 3 stdlib module names. Sourced from
//   python3 -c "import sys; print('\n'.join(sorted(sys.stdlib_module_names)))"
// on CPython 3.13 plus a handful of long-standing names that predate
// stdlib_module_names. Maintained by hand; refresh when adding language
// support for newer Python versions.

package mdg

import "strings"

// pythonStdlib is the lookup set. Keys are top-level module names.
var pythonStdlib = map[string]struct{}{
	"__future__": {}, "_thread": {}, "abc": {}, "aifc": {}, "argparse": {},
	"array": {}, "ast": {}, "asynchat": {}, "asyncio": {}, "asyncore": {},
	"atexit": {}, "audioop": {}, "base64": {}, "bdb": {}, "binascii": {},
	"bisect": {}, "builtins": {}, "bz2": {}, "cProfile": {}, "calendar": {},
	"cgi": {}, "cgitb": {}, "chunk": {}, "cmath": {}, "cmd": {},
	"code": {}, "codecs": {}, "codeop": {}, "collections": {}, "colorsys": {},
	"compileall": {}, "concurrent": {}, "configparser": {}, "contextlib": {},
	"contextvars": {}, "copy": {}, "copyreg": {}, "crypt": {}, "csv": {},
	"ctypes": {}, "curses": {}, "dataclasses": {}, "datetime": {}, "dbm": {},
	"decimal": {}, "difflib": {}, "dis": {}, "distutils": {}, "doctest": {},
	"email": {}, "encodings": {}, "ensurepip": {}, "enum": {}, "errno": {},
	"faulthandler": {}, "fcntl": {}, "filecmp": {}, "fileinput": {}, "fnmatch": {},
	"fractions": {}, "ftplib": {}, "functools": {}, "gc": {}, "genericpath": {},
	"getopt": {}, "getpass": {}, "gettext": {}, "glob": {}, "graphlib": {},
	"grp": {}, "gzip": {}, "hashlib": {}, "heapq": {}, "hmac": {},
	"html": {}, "http": {}, "idlelib": {}, "imaplib": {}, "imghdr": {},
	"imp": {}, "importlib": {}, "inspect": {}, "io": {}, "ipaddress": {},
	"itertools": {}, "json": {}, "keyword": {}, "lib2to3": {}, "linecache": {},
	"locale": {}, "logging": {}, "lzma": {}, "mailbox": {}, "mailcap": {},
	"marshal": {}, "math": {}, "mimetypes": {}, "mmap": {}, "modulefinder": {},
	"msilib": {}, "msvcrt": {}, "multiprocessing": {}, "netrc": {}, "nis": {},
	"nntplib": {}, "ntpath": {}, "numbers": {}, "opcode": {}, "operator": {},
	"optparse": {}, "os": {}, "ossaudiodev": {}, "pathlib": {}, "pdb": {},
	"pickle": {}, "pickletools": {}, "pipes": {}, "pkgutil": {}, "platform": {},
	"plistlib": {}, "poplib": {}, "posix": {}, "posixpath": {}, "pprint": {},
	"profile": {}, "pstats": {}, "pty": {}, "pwd": {}, "py_compile": {},
	"pyclbr": {}, "pydoc": {}, "pydoc_data": {}, "pyexpat": {}, "queue": {},
	"quopri": {}, "random": {}, "re": {}, "readline": {}, "reprlib": {},
	"resource": {}, "rlcompleter": {}, "runpy": {}, "sched": {}, "secrets": {},
	"select": {}, "selectors": {}, "shelve": {}, "shlex": {}, "shutil": {},
	"signal": {}, "site": {}, "smtpd": {}, "smtplib": {}, "sndhdr": {},
	"socket": {}, "socketserver": {}, "spwd": {}, "sqlite3": {}, "sre_compile": {},
	"sre_constants": {}, "sre_parse": {}, "ssl": {}, "stat": {}, "statistics": {},
	"string": {}, "stringprep": {}, "struct": {}, "subprocess": {}, "sunau": {},
	"symtable": {}, "sys": {}, "sysconfig": {}, "syslog": {}, "tabnanny": {},
	"tarfile": {}, "telnetlib": {}, "tempfile": {}, "termios": {}, "test": {},
	"textwrap": {}, "threading": {}, "time": {}, "timeit": {}, "tkinter": {},
	"token": {}, "tokenize": {}, "tomllib": {}, "trace": {}, "traceback": {},
	"tracemalloc": {}, "tty": {}, "turtle": {}, "turtledemo": {}, "types": {},
	"typing": {}, "unicodedata": {}, "unittest": {}, "urllib": {}, "uu": {},
	"uuid": {}, "venv": {}, "warnings": {}, "wave": {}, "weakref": {},
	"webbrowser": {}, "winreg": {}, "winsound": {}, "wsgiref": {}, "xdrlib": {},
	"xml": {}, "xmlrpc": {}, "zipapp": {}, "zipfile": {}, "zipimport": {},
	"zlib": {}, "zoneinfo": {},
}

// isPythonStdlib reports whether the dotted Python module name belongs to
// the standard library. Multi-segment names match against the top segment:
// "os.path" → look up "os".
func isPythonStdlib(name string) bool {
	if name == "" {
		return false
	}
	top := name
	if i := strings.IndexByte(name, '.'); i >= 0 {
		top = name[:i]
	}
	_, ok := pythonStdlib[top]
	return ok
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -run TestIsPythonStdlib_ -v`
Expected: PASS, 3 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/python_stdlib.go internal/mdg/python_stdlib_test.go
git commit -m "mdg: bundle Python stdlib module set + isPythonStdlib helper"
```

---

## Task 3: Python import parser

**Files:**
- Create: `internal/mdg/parse_python.go`
- Create: `internal/mdg/parse_python_test.go`

This task introduces `parsePythonPackages`, which walks a directory tree and returns a slice of `pyPkgInfo` — one per `.py` file. Each record carries the dotted module name (e.g., `pkg.sub.mod`), the relative file path, whether the file is `__main__.py` (IsMain), and the list of imports (stdlib filtered). Like Phase A's Go parser, this function does **not** mutate a `Graph`; that is the orchestrator's job in Task 5.

- [ ] **Step 1: Write the failing test**

```go
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
	lib := byPath["app.lib"]
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
	// Parenthesized `from pkg.sub import (a,b,c)` produces a single edge
	// "pkg.sub". `import x, y` produces edges "x" and "y". Relative
	// `from . import sibling` is recorded as the literal text ".sibling"
	// (we do not resolve relative imports in Phase B; see Task plan notes).
	// External non-stdlib imports remain.
	want := []string{".sibling", "pkg.sub", "x", "y"}
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestParsePythonPackages_`
Expected: FAIL — `parsePythonPackages` and `pyPkgInfo` not defined.

- [ ] **Step 3: Implement the parser**

```go
// internal/mdg/parse_python.go
//
// Python-specific MDG parser. Walks a repo root, parses every .py file's
// imports via tree-sitter, and emits one pyPkgInfo per file. Each .py file
// is a Python module (file-granular, unlike Go's directory-granular
// packages). Stdlib imports are filtered. Relative imports ("from . import
// x") are recorded as their literal source text and treated as opaque leaf
// nodes by Build — Phase B does not resolve relative imports against the
// importing module's package.

package mdg

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
)

// pyPkgInfo is one Python module as discovered by parsePythonPackages.
type pyPkgInfo struct {
	ImportPath string   // dotted, e.g., "pkg.sub.mod"
	File       string   // relative path, e.g., "pkg/sub/mod.py"
	IsMain     bool     // filename == "__main__.py"
	Imports    []string // dotted module names; stdlib filtered; sorted
}

// parsePythonPackages walks rootDir and returns one pyPkgInfo per .py file
// found outside excluded directories. Per-file parse errors emit a stderr
// warning and the file's imports are dropped (but the module still appears
// in the output as a zero-edges node, since its identity is structural).
//
// Returns nil when no .py files are found anywhere in rootDir. Returns an
// error only when rootDir cannot be walked at all.
func parsePythonPackages(rootDir string) ([]pyPkgInfo, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(python.GetLanguage())

	var out []pyPkgInfo
	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			fmt.Fprintf(os.Stderr, "mdg: skipping %s: %v\n", path, walkErr)
			return nil
		}
		if d.IsDir() {
			if excludedDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != rootDir) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		if strings.HasSuffix(path, "_test.py") {
			return nil // mirror Go: skip test files
		}

		rel, _ := filepath.Rel(rootDir, path)
		rel = filepath.ToSlash(rel)
		modulePath := relToModulePath(rel)

		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mdg: skipping %s: %v\n", path, err)
			return nil
		}

		tree, parseErr := parser.ParseCtx(context.Background(), nil, src)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "mdg: skipping %s: %v\n", path, parseErr)
			// Still emit the module node, with no imports.
			out = append(out, pyPkgInfo{
				ImportPath: modulePath,
				File:       rel,
				IsMain:     filepath.Base(path) == "__main__.py",
				Imports:    nil,
			})
			return nil
		}
		defer tree.Close()

		imports := extractPythonImports(tree.RootNode(), src)
		// Filter stdlib + dedupe + sort for deterministic output.
		imports = filterAndSortImports(imports)

		out = append(out, pyPkgInfo{
			ImportPath: modulePath,
			File:       rel,
			IsMain:     filepath.Base(path) == "__main__.py",
			Imports:    imports,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", rootDir, err)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ImportPath < out[j].ImportPath
	})
	return out, nil
}

// relToModulePath converts a slash-form relative path to a dotted Python
// module name. Examples:
//
//	"main.py"              → "main"
//	"pkg/sub/mod.py"       → "pkg.sub.mod"
//	"pkg/__init__.py"      → "pkg"
//	"pkg/sub/__init__.py"  → "pkg.sub"
//	"pkg/__main__.py"      → "pkg.__main__"
func relToModulePath(rel string) string {
	rel = strings.TrimSuffix(rel, ".py")
	parts := strings.Split(rel, "/")
	// A trailing "__init__" segment means the directory is the module name.
	if n := len(parts); n > 0 && parts[n-1] == "__init__" {
		parts = parts[:n-1]
	}
	return strings.Join(parts, ".")
}

// extractPythonImports walks a tree-sitter Python parse tree and collects
// the dotted module names referenced by import_statement and
// import_from_statement nodes.
//
// For `import a.b, c`     → ["a.b", "c"]
// For `from a.b import x` → ["a.b"]
// For `from . import x`   → [".x"]   (relative import; recorded as literal)
// For `import a as alias` → ["a"]    (alias ignored; we want the module)
func extractPythonImports(root *sitter.Node, src []byte) []string {
	var out []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "import_statement":
			// children: ["import", aliased_import | dotted_name, ",", ...]
			for i := 0; i < int(n.NamedChildCount()); i++ {
				ch := n.NamedChild(i)
				switch ch.Type() {
				case "dotted_name":
					out = append(out, ch.Content(src))
				case "aliased_import":
					// aliased_import has a `name` field that is a dotted_name.
					if name := ch.ChildByFieldName("name"); name != nil {
						out = append(out, name.Content(src))
					}
				}
			}
		case "import_from_statement":
			// `module_name` field is the source module. May be "dotted_name"
			// or "relative_import" (e.g., ". sibling").
			if mod := n.ChildByFieldName("module_name"); mod != nil {
				out = append(out, mod.Content(src))
			} else {
				// `from . import x` has no module_name field; the leading
				// dots are siblings. Emit the names being imported as
				// dotted from the relative root, prefixed with "." so the
				// graph node is a deterministic identifier.
				for i := 0; i < int(n.NamedChildCount()); i++ {
					ch := n.NamedChild(i)
					if ch.Type() == "dotted_name" || ch.Type() == "aliased_import" {
						name := ch
						if ch.Type() == "aliased_import" {
							name = ch.ChildByFieldName("name")
							if name == nil {
								continue
							}
						}
						out = append(out, "."+name.Content(src))
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return out
}

// filterAndSortImports removes stdlib imports, deduplicates, and returns
// the result sorted alphabetically. Sorted output is required for byte-
// identical MDG cache files.
func filterAndSortImports(imports []string) []string {
	if len(imports) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(imports))
	for _, ip := range imports {
		if ip == "" {
			continue
		}
		if isPythonStdlib(ip) {
			continue
		}
		seen[ip] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -run TestParsePythonPackages_ -v`
Expected: PASS, 5 tests.

If `TestParsePythonPackages_ParenthesizedAndMultiImport` fails because the relative-import branch produced unexpected output, inspect the tree-sitter tree directly. Tree-sitter's Python grammar represents `from . import sibling` differently from what the production-mode walk assumes. The test allows the engineer to discover the exact shape and adjust the `extractPythonImports` walk.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/parse_python.go internal/mdg/parse_python_test.go
git commit -m "mdg: add Python import parser via tree-sitter"
```

---

## Task 4: Relax `parseGoPackages` for repos without `go.mod`

**Files:**
- Modify: `internal/mdg/parse_go.go`
- Modify: `internal/mdg/parse_go_test.go`

Currently, `parseGoPackages` errors when `go.mod` is missing. For Phase B, Python-only repos must succeed at the parser level — `Build` will then dispatch to the Python parser and a missing `go.mod` is simply "no Go packages here", not an error.

- [ ] **Step 1: Update the failing test (existing test that will need to change)**

The current `TestParseGoPackages_NoGoMod` asserts that a missing `go.mod` errors. Rewrite it to assert the new contract:

```go
func TestParseGoPackages_NoGoMod(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "x.go", "package x\n")
	pkgs, err := parseGoPackages(root)
	if err != nil {
		t.Fatalf("expected (nil, nil) when go.mod is missing, got error: %v", err)
	}
	if len(pkgs) != 0 {
		t.Fatalf("expected 0 packages when go.mod is missing, got %d (%+v)", len(pkgs), pkgs)
	}
}
```

- [ ] **Step 2: Run tests to verify the test fails against the existing parser**

Run: `go test ./internal/mdg/... -run TestParseGoPackages_NoGoMod`
Expected: FAIL — the existing implementation returns an error.

- [ ] **Step 3: Update `parseGoPackages`**

In `internal/mdg/parse_go.go`, find:

```go
func parseGoPackages(rootDir string) ([]goPkgInfo, error) {
	modulePath, err := readGoModulePath(rootDir)
	if err != nil {
		return nil, err
	}
	// ...
```

Replace the error propagation with a nil-return for the missing-go.mod case. Add `errors` and `io/fs` to the imports if not present.

```go
func parseGoPackages(rootDir string) ([]goPkgInfo, error) {
	modulePath, err := readGoModulePath(rootDir)
	if err != nil {
		// A missing go.mod means there are no Go packages here; that's not
		// an error for a polyglot Build. Callers that need to distinguish
		// "no Go" from "malformed go.mod" should inspect the directory
		// before invoking this function.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		// Some os.ReadFile errors wrap *fs.PathError without ErrNotExist;
		// match the textual prefix as a fallback.
		if strings.HasPrefix(err.Error(), "read go.mod:") {
			return nil, nil
		}
		return nil, err
	}
	// ... rest unchanged
```

Add to the imports block:

```go
	"errors"
```

Note: `io/fs` is already imported.

- [ ] **Step 4: Run all tests**

Run: `go test ./internal/mdg/... -v`
Expected: all tests pass, including the rewritten `TestParseGoPackages_NoGoMod`.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/parse_go.go internal/mdg/parse_go_test.go
git commit -m "mdg: parseGoPackages returns (nil, nil) when go.mod is absent"
```

---

## Task 5: Augment `goPkgInfo` with per-package file list

**Files:**
- Modify: `internal/mdg/parse_go.go`
- Modify: `internal/mdg/parse_go_test.go`

For Task 7's `fileToModule` map, the Go parser must record which files belong to each package. We add a `Files []string` field to `goPkgInfo` (relative file paths) and populate it during the walk.

- [ ] **Step 1: Write the failing test**

Append to `internal/mdg/parse_go_test.go`:

```go
func TestParseGoPackages_RecordsFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, root, "internal/auth/oauth.go", "package auth\nfunc Oauth() {}\n")
	writeFile(t, root, "internal/auth/saml.go", "package auth\nfunc SAML() {}\n")

	pkgs, err := parseGoPackages(root)
	if err != nil {
		t.Fatalf("parseGoPackages: %v", err)
	}

	byPath := map[string]goPkgInfo{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}

	rootPkg := byPath["example.com/m"]
	if !equalStrSlices(rootPkg.Files, []string{"main.go"}) {
		t.Errorf("root pkg Files: want [main.go], got %v", rootPkg.Files)
	}

	auth := byPath["example.com/m/internal/auth"]
	// Files within a package should be sorted for determinism.
	wantAuth := []string{"internal/auth/oauth.go", "internal/auth/saml.go"}
	if !equalStrSlices(auth.Files, wantAuth) {
		t.Errorf("auth Files: want %v, got %v", wantAuth, auth.Files)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestParseGoPackages_RecordsFiles`
Expected: FAIL — `goPkgInfo.Files` does not exist.

- [ ] **Step 3: Add the field and populate it**

In `internal/mdg/parse_go.go`, update `goPkgInfo`:

```go
type goPkgInfo struct {
	ImportPath string   // canonical, e.g., "example.com/m/internal/auth"
	IsMain     bool     // package main
	Imports    []string // in-module + external; stdlib filtered
	Files      []string // relative paths of .go files in this package; sorted
}
```

Update `pkgState` to track files:

```go
	type pkgState struct {
		importPath         string
		isMain             bool
		imports            map[string]struct{}
		files              []string
		seenAtLeastOneFile bool
	}
```

Inside the `filepath.WalkDir` callback, after computing `rel` (the relative path) and finding/creating the `pkgState`, append the file to `st.files`:

```go
		rel, _ := filepath.Rel(rootDir, path)
		rel = filepath.ToSlash(rel)
		st.files = append(st.files, rel)
```

Where the `rel` calculation already happens for the package-path derivation — extract it so both uses can share it. The final emission loop becomes:

```go
	for _, st := range pkgsByDir {
		if !st.seenAtLeastOneFile {
			continue
		}
		imports := make([]string, 0, len(st.imports))
		for k := range st.imports {
			imports = append(imports, k)
		}
		sort.Strings(imports)
		files := append([]string(nil), st.files...)
		sort.Strings(files)
		out = append(out, goPkgInfo{
			ImportPath: st.importPath,
			IsMain:     st.isMain,
			Imports:    imports,
			Files:      files,
		})
	}
```

Read the existing function carefully before editing — the `rel` derivation is currently scoped inside the dir-not-seen branch. You'll need to compute `rel` for the file path (relative to rootDir) at every iteration, not just when first creating the package.

- [ ] **Step 4: Run all tests**

Run: `go test ./internal/mdg/... -v`
Expected: all 35+ tests pass including the new one.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/parse_go.go internal/mdg/parse_go_test.go
git commit -m "mdg: record per-package file lists in goPkgInfo"
```

---

## Task 6: `Build` orchestrator combines Go + Python

**Files:**
- Modify: `internal/mdg/build.go`
- Modify: `internal/mdg/build_test.go`

`Build` runs both parsers, accumulates results into a single `*Graph`, and errors only if **neither** parser found anything. The `Module.Lang` field disambiguates the languages on each node.

- [ ] **Step 1: Update existing tests and add new ones**

In `internal/mdg/build_test.go`, locate `TestBuild_EmptyRepo`. The current assertion is:

```go
func TestBuild_EmptyRepo(t *testing.T) {
	root := t.TempDir()
	// No go.mod, no source files.
	_, err := Build(context.Background(), root, BuildOptions{})
	if err == nil {
		t.Fatalf("Build should error for repo with no go.mod")
	}
}
```

Keep the test name and structure, but update the assertion message and rationale — the error is now "no supported language found", not "no go.mod":

```go
func TestBuild_EmptyRepo(t *testing.T) {
	root := t.TempDir()
	// No source files at all.
	_, err := Build(context.Background(), root, BuildOptions{})
	if err == nil {
		t.Fatalf("Build should error when no supported language is present")
	}
}
```

Append three new tests:

```go
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
	// "os" filtered as stdlib; "pkg.sub.helper" present (from from-import).
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
```

- [ ] **Step 2: Run tests to verify failures**

Run: `go test ./internal/mdg/... -run TestBuild_`
Expected: the three new tests fail because `Build` doesn't yet call `parsePythonPackages`.

- [ ] **Step 3: Update `Build`**

Replace the existing `Build` body with the polyglot version. The current implementation calls only `parseGoPackages`; the new version calls both parsers and merges results.

```go
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
```

- [ ] **Step 4: Run all tests**

Run: `go test ./internal/mdg/... -v`
Expected: all tests pass.

Run: `go vet ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/build.go internal/mdg/build_test.go
git commit -m "mdg: Build orchestrator now combines Go and Python parsers"
```

---

## Task 7: Refit `mdg.Centrality` to `fileToModule`

**Files:**
- Modify: `internal/mdg/centrality.go`
- Modify: `internal/mdg/centrality_test.go`

Phase A's `dirToModule` is replaced with `fileToModule`. Lookup keys are now full relative file paths (e.g., `"internal/auth/oauth.go"`, `"pkg/sub/mod.py"`). `ScoreFork` does exact-match against this map first; if no exact match is found, it falls back to a parent-directory lookup (so that a touched non-source file in a known Go package directory still attributes its impact to that package — preserving Phase A behavior for non-Go touches in mixed repos).

- [ ] **Step 1: Write the failing tests**

Append to `internal/mdg/centrality_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run "TestCentrality_PythonFileScored|TestCentrality_GoFileStillScored|TestCentrality_NonSourceFileFallsBackToDir"`
Expected: FAIL — `fileToModule` does not exist yet, and the Python test relies on Build's Python branch being threaded through to the Centrality.

- [ ] **Step 3: Replace `dirToModule` with `fileToModule` + dir fallback**

In `internal/mdg/centrality.go`:

```go
type Centrality struct {
	Provider   string
	Owner      string
	Repo       string
	HeadSHA    string
	Scores     map[string]float64
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
```

Update `BuildCentrality` to populate both maps. This requires `Build` to expose the per-file/per-package data — but the data is already inside `parseGoPackages` and `parsePythonPackages`. Re-call those parsers from `BuildCentrality` (the cost is a second walk-of-disk, but file IO is cheap for typical repo sizes):

```go
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

	goPkgs, _ := parseGoPackages(repoPath)
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

	pyPkgs, _ := parsePythonPackages(repoPath)
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
```

Update `ScoreFork`:

```go
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
```

Remove the old `dirToModule` map-population code in `BuildCentrality` (the part that did `strings.TrimPrefix(n.Path, modulePath)` for Go nodes — that logic is now handled by re-calling the parsers).

- [ ] **Step 4: Run all tests**

Run: `go test ./internal/mdg/... -v`
Expected: all tests pass.

Run: `go test ./...`
Expected: full suite passes.

Run: `go vet ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/centrality.go internal/mdg/centrality_test.go
git commit -m "mdg: replace dirToModule with fileToModule + dir fallback"
```

---

## Task 8: End-to-end smoke verification + docs

**Files:**
- Modify: `README.md` (or wherever the project documents prerequisites)

This task does not write new functionality. It builds the binary, runs `go test`/`go vet`, smoke-tests against a real Python repo, and documents the CGO requirement.

- [ ] **Step 1: Build the binaries**

Run: `go build -o spoon ./cmd/spoon && go build -o spn ./cmd/spn`
Expected: both binaries build with no warnings.

- [ ] **Step 2: Run the full test suite + vet**

Run: `go vet ./... && go test ./...`
Expected: all 15 packages pass; vet clean.

- [ ] **Step 3: Smoke-test `spn repo centrality --full-mdg` against a small Python repo**

Pick a Python-only repository with 5–50 modules. `psf/requests-cache` or `pallets/click` are decent candidates.

Run: `./spn repo centrality --full-mdg pallets/click`
Expected: JSON output with a `Scores` map containing modules like `click.core`, `click.types`, `click.decorators`. The first run should take 5–20 seconds (clone + parse + PageRank).

Manually verify:
- The top-scoring module(s) include the package that other modules import from (typically the package's `__init__.py` → `click`).
- The cache file is at `~/.cache/spoon/mdg/github/pallets__click.json` (when invoked via the cluster pipeline path; `spn repo centrality` passes an empty HeadSHA and skips the cache by design).
- The output JSON includes both Python module paths and any external (non-stdlib) imports as opaque leaf nodes.

- [ ] **Step 4: Smoke-test against a polyglot repo (optional, time permitting)**

If you have a Go+Python repo handy (or can construct one), verify that both languages produce nodes in the graph. The TUI / cluster pipeline path is the surface that uses both; the `spn repo centrality` JSON dump shows the combined graph.

- [ ] **Step 5: Document the CGO requirement**

If the project has a `README.md`, find a "Building" or "Prerequisites" section and add:

```markdown
### Build prerequisites

Spoon requires CGO_ENABLED=1 (the Go default) because the MDG centrality
backend uses tree-sitter parsers via Go's C interop. Building requires a
working C compiler:

- Linux: `gcc` or `clang`
- macOS: Xcode Command Line Tools (`xcode-select --install`)
- Windows: MinGW-w64 or MSVC

If you build with `CGO_ENABLED=0`, the binary will fail to compile.
```

If no such file or section exists, create a `BUILDING.md` at the repo root with the above.

- [ ] **Step 6: Commit the doc change**

```bash
git add README.md   # or BUILDING.md, depending on what you edited
git commit -m "docs: document CGO build requirement for tree-sitter"
```

- [ ] **Step 7: Final verification**

Run: `git log --oneline f04ccbe..HEAD` (or whatever Phase A's tip SHA was at the start of Phase B)

Verify the commit chain reads as a clean progression: dep add → stdlib list → parser → relax-go-parser → record-files → polyglot-build → centrality-refit → docs.

If any task left uncommitted changes in the working tree (e.g., test fixtures used during step 3), inspect them and either commit or discard.

---

## Self-review summary

**Spec coverage check:**

| Phase B requirement (from `docs/superpowers/specs/future/future-work-full-mdg-centrality.md` step 4) | Implemented in task |
|---|---|
| Bundle tree-sitter Python grammar | Task 1 |
| Implement Python import parsing | Task 3 |
| Filter stdlib imports | Tasks 2 + 3 |
| Handle relative imports gracefully | Task 3 (recorded as literal `.x` leaves) |
| Polyglot Build orchestrator | Task 6 |
| Per-file → per-module mapping for centrality | Task 7 |
| End-to-end smoke verification | Task 8 |

Phase A's acceptance criteria 1 (≥70% overlap with human-judged core modules on 5 hand-curated repos) remains a manual validation activity that the formal experiment will cover; the same applies to Phase B repos and is deliberately not blocked on this plan.

**Type consistency:**

- `pyPkgInfo` and `goPkgInfo` have consistent shapes: both expose `ImportPath`, `IsMain`, `Imports`. They differ in granularity (`Files []string` on Go; `File string` on Python) which reflects the language difference.
- `Module.Lang` values: `"go"`, `"python"`, `""` for opaque external leaves. Consistent across both parsers.
- `Build` does not propagate parser-specific types past the boundary; everything inside the `*Graph` is `Module` + `Edges`, language-agnostic.

**No placeholders.** Every step contains the exact code or command an engineer needs.

**Open items the engineer should know:**

- Relative imports (`from . import x`) are recorded as their literal source text (e.g., `.x`). They appear as opaque leaf nodes in the graph and contribute zero to centrality. A future Phase B+ enhancement could resolve them against the importing module's package, but doing so correctly requires modeling the package hierarchy (which `parsePythonPackages` already does via `__init__.py` discovery) and is more work than is justified for Phase B's initial scope.

- The CGO dependency complicates cross-compilation. Builds on a Linux x86_64 host targeting Linux x86_64 work out of the box. Cross-compiling to e.g. Linux ARM64 requires a cross-compiling C toolchain (`gcc-aarch64-linux-gnu` or similar) and `CC=aarch64-linux-gnu-gcc`. This is not a regression from Phase A (which had no cgo), but it is new friction. The README addition in Task 8 step 5 documents it.

- The `Module.IsMain` heuristic for Python ("filename is `__main__.py`") is conservative. A script with `if __name__ == "__main__":` at module level — without being named `__main__.py` — will not be marked IsMain and won't contribute to the entry-point teleport. This is a known precision tradeoff: detecting the conditional requires a full AST walk for every file, which doubles the parser's work. Phase B accepts the limitation; Phase B+ can revisit.

- `BuildCentrality` re-calls the parsers a second time to populate `fileToModule` / `dirToModule`. This is wasteful (each `.py` file is parsed twice for one run). A cleaner refactor would have `Build` return a struct containing both the `*Graph` and the per-file mappings, but that is a public-API change to `Build`'s return signature. Phase B accepts the duplicate work as the smaller diff; a follow-up plan can lift the mappings into Build's return value.

---

## Execution handoff

**Plan complete and saved to `docs/superpowers/plans/2026-05-11-mdg-python-phase-b.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
