package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestProductionGlyphsAreCentralized(t *testing.T) {
	root := filepath.Clean(".")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || path == filepath.Join("theme", "glyph.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || (literal.Kind != token.STRING && literal.Kind != token.CHAR) {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				offenders = append(offenders, path+":invalid-literal:"+literal.Value)
				return true
			}
			if !containsNonASCII(value) {
				return true
			}
			offenders = append(offenders, path+":"+literal.Value)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("production rendering glyphs must use theme glyph table: %s", strings.Join(offenders, ", "))
	}
}

func containsNonASCII(value string) bool {
	for _, r := range value {
		if r >= utf8.RuneSelf {
			return true
		}
	}
	return false
}
