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

// rawPyInfo is an intermediate record before import normalization.
type rawPyInfo struct {
	ImportPath string
	File       string
	IsMain     bool
	RawImports []string // may contain X.Y where Y could be a symbol or sub-module
}

// parsePythonPackages walks rootDir and returns one pyPkgInfo per .py file
// found outside excluded directories. Per-file parse errors emit a stderr
// warning and the file's imports are dropped (but the module still appears
// in the output as a zero-edges node, since its identity is structural).
//
// Returns nil when no .py files are found anywhere in rootDir. Returns an
// error only when rootDir cannot be walked at all.
//
// Import normalization (two-pass): for `from X import Y` (non-parenthesized),
// the raw import is recorded as "X.Y". After all modules are discovered, a
// second pass collapses "X.Y" to "X" when "X.Y" is not a known module path
// but "X" is — distinguishing symbol imports from sub-module imports.
func parsePythonPackages(rootDir string) ([]pyPkgInfo, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(python.GetLanguage())
	defer parser.Close()

	var raw []rawPyInfo
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
			raw = append(raw, rawPyInfo{
				ImportPath: modulePath,
				File:       rel,
				IsMain:     filepath.Base(path) == "__main__.py",
				RawImports: nil,
			})
			return nil
		}
		defer tree.Close()

		imports := extractPythonImports(tree.RootNode(), src)

		raw = append(raw, rawPyInfo{
			ImportPath: modulePath,
			File:       rel,
			IsMain:     filepath.Base(path) == "__main__.py",
			RawImports: imports,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", rootDir, err)
	}

	// Build a set of all known module paths for two-pass normalization.
	knownModules := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		knownModules[r.ImportPath] = struct{}{}
	}

	// Second pass: normalize imports and build final output. Return nil
	// (not an empty slice) when no .py files were found, matching the
	// documented contract.
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]pyPkgInfo, 0, len(raw))
	for _, r := range raw {
		imports := normalizeAndFilterImports(r.RawImports, knownModules)
		out = append(out, pyPkgInfo{
			ImportPath: r.ImportPath,
			File:       r.File,
			IsMain:     r.IsMain,
			Imports:    imports,
		})
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
	if n := len(parts); n > 0 && parts[n-1] == "__init__" {
		parts = parts[:n-1]
	}
	return strings.Join(parts, ".")
}

// extractPythonImports walks a tree-sitter Python parse tree and collects
// the dotted module names referenced by import_statement and
// import_from_statement nodes. The raw results are not yet stdlib-filtered;
// call normalizeAndFilterImports on the output.
//
// For `import a.b, c`           → ["a.b", "c"]
// For `import a as alias`       → ["a"]            (alias ignored)
// For `from a.b import x`       → ["a.b.x"]        (qualified; normalized later)
// For `from a.b import (x,y,z)` → ["a.b"]          (parenthesized → emit module only)
// For `from . import x`         → [".x"]            (relative; prefix "." + name)
// For `from ..pkg import x`     → ["..pkg.x"]       (relative_import content + "." + name)
//
// For non-parenthesized `from X import Y`, "X.Y" is recorded. The
// normalizeAndFilterImports pass then collapses X.Y → X when X.Y is not a
// known module, keeping full qualification only for genuine sub-module imports.
func extractPythonImports(root *sitter.Node, src []byte) []string {
	var out []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "import_statement":
			// `import a.b, c` or `import a as alias`
			for i := 0; i < int(n.NamedChildCount()); i++ {
				ch := n.NamedChild(i)
				switch ch.Type() {
				case "dotted_name":
					out = append(out, ch.Content(src))
				case "aliased_import":
					if name := ch.ChildByFieldName("name"); name != nil {
						out = append(out, name.Content(src))
					}
				}
			}

		case "import_from_statement":
			mod := n.ChildByFieldName("module_name")
			// Determine the module prefix that goes before each imported name.
			// Three shapes:
			//   `from . import x`        → mod is nil; prefix from relative_import sibling
			//   `from .pkg import x`     → mod.Type == "relative_import"; prefix = mod.Content
			//   `from a.b import x`      → mod.Type == "dotted_name"; prefix = mod.Content
			var prefix string
			if mod == nil {
				// Bare relative form. Find the leading relative_import among
				// named children to recover the dots (".", "..", "...").
				for i := 0; i < int(n.NamedChildCount()); i++ {
					if ch := n.NamedChild(i); ch.Type() == "relative_import" {
						prefix = ch.Content(src)
						break
					}
				}
			} else {
				prefix = mod.Content(src)
			}

			// Always emit the qualified `prefix.name`; normalizeAndFilterImports
			// collapses to `prefix` later when `name` is not a known module.
			// Parenthesized and non-parenthesized forms share this path — the
			// two-pass design preserves precision for sub-module imports while
			// still collapsing symbol imports to the parent module.
			for i := 0; i < int(n.NamedChildCount()); i++ {
				ch := n.NamedChild(i)
				if ch.Type() == "relative_import" {
					continue
				}
				if mod != nil && ch == mod {
					continue
				}
				var name *sitter.Node
				switch ch.Type() {
				case "dotted_name":
					name = ch
				case "aliased_import":
					name = ch.ChildByFieldName("name")
				}
				if name == nil {
					continue
				}
				// Use a separator only when prefix doesn't already end in a
				// dot. This handles `..` + `x` → `..x` (not `...x`) and
				// `.pkg` + `x` → `.pkg.x`.
				sep := "."
				if strings.HasSuffix(prefix, ".") {
					sep = ""
				}
				out = append(out, prefix+sep+name.Content(src))
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return out
}

// normalizeAndFilterImports takes raw import strings (which may include
// "X.Y" from non-parenthesized `from X import Y`), stdlib-filters them,
// collapses "X.Y" → "X" when "X.Y" is not a known module path, deduplicates,
// and returns a sorted slice. Returns nil when the result is empty.
func normalizeAndFilterImports(imports []string, knownModules map[string]struct{}) []string {
	if len(imports) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(imports))
	for _, ip := range imports {
		if ip == "" {
			continue
		}
		// Resolve "X.Y": keep as X.Y only if X.Y is a known module;
		// otherwise fall back to X (the container module).
		resolved := resolveImport(ip, knownModules)
		if resolved == "" || isPythonStdlib(resolved) {
			continue
		}
		seen[resolved] = struct{}{}
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

// resolveImport normalizes a raw import string against the known module set.
//
//   - Relative imports (starting with ".") are returned as-is.
//   - Exact known module → returned as-is.
//   - "X.Y" where X.Y is NOT a known module but X IS → collapse to "X"
//     (Y is a symbol inside module X, not a sub-module).
//   - "X.Y" where neither X.Y nor X is a known local module → return "X.Y"
//     as-is (external qualified reference).
//   - Bare "X" with no local match → return as-is (external package).
func resolveImport(ip string, knownModules map[string]struct{}) string {
	if strings.HasPrefix(ip, ".") {
		return ip // relative: opaque
	}
	if _, ok := knownModules[ip]; ok {
		return ip // exact known module (e.g. "pkg.sub.util" or bare "x")
	}
	// For qualified "X.Y": check if parent X is a known module.
	// If so, Y is a symbol inside X — collapse to X.
	if idx := strings.LastIndex(ip, "."); idx >= 0 {
		parent := ip[:idx]
		if _, ok := knownModules[parent]; ok {
			// Parent is a known module; imported name is a symbol, not a sub-module.
			return parent
		}
		// Neither X.Y nor X is a known local module — external qualified import.
		// Return as-is (e.g. "pkg.sub" from an external library).
		return ip
	}
	return ip // bare name, external package
}
