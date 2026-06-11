//
// Go-specific MDG parser. Walks a repo root that contains a go.mod, collects
// every Go package, and resolves their imports against the module path so
// in-module imports become canonical paths. Stdlib imports are filtered out;
// external dependencies are retained as opaque nodes (Lang = "" until a
// future task labels them).

package mdg

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// goPkgInfo is one Go package as discovered by parseGoPackages.
type goPkgInfo struct {
	ImportPath string   // canonical, e.g., "example.com/m/internal/auth"
	IsMain     bool     // package main
	Imports    []string // in-module + external; stdlib filtered out
	Files      []string // relative paths of .go files in this package; sorted
}

// excludedDirs is true when the named segment must not be descended into.
var excludedDirs = map[string]bool{
	".git":         true,
	".hg":          true,
	".svn":         true,
	"vendor":       true,
	"node_modules": true,
	"venv":         true,
	".venv":        true,
	"third_party":  true,
	"testdata":     true,
}

// parseGoPackages walks rootDir, requires a go.mod at rootDir, and returns
// one goPkgInfo per Go package found. Per-file parse errors are logged to
// stderr and the file is skipped; whole-package failures emit a warning and
// the package is omitted. Returns an error only when rootDir has no go.mod
// or cannot be read.
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
		return nil, err
	}

	type pkgState struct {
		importPath         string
		isMain             bool
		imports            map[string]struct{}
		files              []string
		seenAtLeastOneFile bool
	}
	pkgsByDir := map[string]*pkgState{}

	err = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if excludedDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != rootDir) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // skip test files; they pollute the dependency graph
		}

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "mdg: skipping %s: %v\n", path, perr)
			return nil
		}

		dir := filepath.Dir(path)
		rel, _ := filepath.Rel(rootDir, path)
		rel = filepath.ToSlash(rel)

		st, ok := pkgsByDir[dir]
		if !ok {
			pkgRel, _ := filepath.Rel(rootDir, dir)
			pkgRel = filepath.ToSlash(pkgRel)
			importPath := modulePath
			if pkgRel != "." && pkgRel != "" {
				importPath = modulePath + "/" + pkgRel
			}
			st = &pkgState{
				importPath: importPath,
				imports:    map[string]struct{}{},
			}
			pkgsByDir[dir] = st
		}
		if f.Name != nil && f.Name.Name == "main" {
			st.isMain = true
		}
		st.seenAtLeastOneFile = true
		st.files = append(st.files, rel)
		for _, imp := range f.Imports {
			if imp.Path == nil {
				continue
			}
			ip := strings.Trim(imp.Path.Value, `"`)
			if isStdlibImport(ip) {
				continue
			}
			st.imports[ip] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", rootDir, err)
	}

	out := make([]goPkgInfo, 0, len(pkgsByDir))
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
	sort.Slice(out, func(i, j int) bool {
		return out[i].ImportPath < out[j].ImportPath
	})
	return out, nil
}

// readGoModulePath reads `module X` from rootDir/go.mod and returns X.
func readGoModulePath(rootDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(rootDir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("go.mod missing `module` directive")
}

// isStdlibImport approximates "is this import from the Go standard library?"
// The heuristic: stdlib imports never contain a dot in the first path
// segment. Imports like "fmt", "encoding/json", "go/parser" return true;
// "github.com/foo/bar", "example.com/m/internal/auth" return false.
func isStdlibImport(p string) bool {
	if p == "" {
		return false
	}
	first := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		first = p[:i]
	}
	return !strings.Contains(first, ".")
}
