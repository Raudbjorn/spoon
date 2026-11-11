// check-agent-go-anchors reads a tracked-source snapshot; it never runs tests.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type request struct {
	Root    string            `json:"root"`
	Sources map[string]string `json:"sources"`
}

type result struct {
	Error     string            `json:"error,omitempty"`
	Functions map[string]string `json:"functions,omitempty"`
}

func signature(fn *ast.FuncDecl, aliases map[string]bool) string {
	arg := ""
	for prefix, parameter := range map[string]string{"Test": "T", "Fuzz": "F", "Benchmark": "B"} {
		if strings.HasPrefix(fn.Name.Name, prefix) {
			rest := strings.TrimPrefix(fn.Name.Name, prefix)
			first, _ := utf8.DecodeRuneInString(rest)
			if rest != "" && unicode.IsLower(first) {
				return "name suffix starts with a lowercase letter"
			}
			arg = parameter
			break
		}
	}
	if arg == "" {
		return "name must start with Test, Fuzz or Benchmark"
	}
	if fn.Recv != nil || (fn.Type.TypeParams != nil && len(fn.Type.TypeParams.List) != 0) {
		return "methods and generic functions are not test entrypoints"
	}
	if (fn.Type.Results != nil && len(fn.Type.Results.List) != 0) ||
		len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
		return "requires one parameter and no results"
	}
	ptr, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	parameterMatches := func(name string) bool {
		return name == arg || (fn.Name.Name == "TestMain" && name == "M")
	}
	if ok {
		switch typ := ptr.X.(type) {
		case *ast.SelectorExpr:
			pkg, ok := typ.X.(*ast.Ident)
			if ok && aliases[pkg.Name] && parameterMatches(typ.Sel.Name) {
				return ""
			}
		case *ast.Ident:
			if aliases["."] && parameterMatches(typ.Name) {
				return ""
			}
		}
	}
	return "parameter must be *testing." + arg + " (testing import aliases are supported)"
}

func main() {
	var input request
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	context := build.Default
	context.BuildTags = nil
	context.OpenFile = func(path string) (io.ReadCloser, error) {
		relative, err := filepath.Rel(input.Root, path)
		if err != nil {
			return nil, err
		}
		source, ok := input.Sources[filepath.ToSlash(relative)]
		if !ok {
			return nil, fmt.Errorf("source is outside the tracked snapshot: %s", relative)
		}
		return io.NopCloser(strings.NewReader(source)), nil
	}
	results := make(map[string]result)
	parsed := make(map[string]*ast.File)
	packages := make(map[string]map[string]bool)
	for path, source := range input.Sources {
		matched, err := context.MatchFile(filepath.Join(input.Root, filepath.Dir(path)), filepath.Base(path))
		if err != nil {
			results[path] = result{Error: "build selection: " + err.Error()}
			continue
		}
		if !matched {
			results[path] = result{Error: "inactive for default build"}
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			results[path] = result{Error: "invalid Go source: " + err.Error()}
			continue
		}
		parsed[path] = file
		if !strings.HasSuffix(path, "_test.go") {
			directory := filepath.Dir(path)
			if packages[directory] == nil {
				packages[directory] = make(map[string]bool)
			}
			packages[directory][file.Name.Name] = true
		}
	}
	for path, file := range parsed {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		aliases := make(map[string]bool)
		for _, imported := range file.Imports {
			name, _ := strconv.Unquote(imported.Path.Value)
			if name == "testing" {
				alias := "testing"
				if imported.Name != nil {
					alias = imported.Name.Name
				}
				if alias != "_" {
					aliases[alias] = true
				}
			}
		}
		entry := result{Functions: make(map[string]string)}
		for name := range packages[filepath.Dir(path)] {
			if file.Name.Name != name && file.Name.Name != name+"_test" {
				entry.Error = "test package does not match active source package " + name
			}
		}
		for _, declaration := range file.Decls {
			if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Recv == nil {
				entry.Functions[fn.Name.Name] = signature(fn, aliases)
			}
		}
		results[path] = entry
	}
	if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
