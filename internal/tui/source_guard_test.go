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
)

// TestProductionRenderSourcesUseTheme verifies the production-only boundary:
// canonical terminal decorations and colors originate in theme, while arbitrary
// Unicode data (repository names, comments, CJK, and combining text) remains
// valid. The lexical object graph catches package aliases, dot imports, and
// indirect Color constructors without confusing a shadowed local identifier.
func TestProductionRenderSourcesUseTheme(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || sourceGuardAllowlisted(path) {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		aliases, dotImport := lipglossImports(file)
		constructors := lipglossColorConstructors(file, aliases, dotImport)
		constants := stringConstants(file)
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CallExpr:
				if isLipglossColorCall(node.Fun, aliases, dotImport) || isColorConstructorCall(node.Fun, constructors) {
					offenders = append(offenders, path+": direct or indirect lipgloss.Color")
				}
				if value, ok := renderLiteral(node, constants); ok {
					appendLiteralOffenses(&offenders, path, value)
				}
			case *ast.BinaryExpr:
				if value, ok := renderLiteral(node, constants); ok {
					appendLiteralOffenses(&offenders, path, value)
				}
			case *ast.BasicLit:
				if value, ok := renderLiteral(node, constants); ok {
					appendLiteralOffenses(&offenders, path, value)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("production render sources must use theme roles and glyphs: %s", strings.Join(offenders, "; "))
	}
}

func sourceGuardAllowlisted(path string) bool {
	path = filepath.ToSlash(path)
	return strings.HasPrefix(path, "theme/") || strings.HasPrefix(path, "internal/rendertest/")
}

func lipglossImports(file *ast.File) (map[string]bool, bool) {
	aliases := map[string]bool{}
	dotImport := false
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != "github.com/charmbracelet/lipgloss" {
			continue
		}
		if spec.Name != nil && spec.Name.Name == "." {
			dotImport = true
			continue
		}
		name := "lipgloss"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		aliases[name] = true
	}
	return aliases, dotImport
}

type colorBinding struct {
	object *ast.Object
	source *ast.Object
	direct bool
}

// lipglossColorConstructors resolves constructor aliases to lexical objects.
// A later assignment replaces the prior edge, while fixed-point propagation
// makes declaration ordering irrelevant for the remaining assignment graph.
func lipglossColorConstructors(file *ast.File, aliases map[string]bool, dotImport bool) map[*ast.Object]bool {
	bindings := map[*ast.Object]colorBinding{}
	assign := func(left ast.Expr, right ast.Expr) {
		ident, ok := left.(*ast.Ident)
		if !ok || ident.Obj == nil {
			return
		}
		right = unwrapParen(right)
		if isLipglossColorCall(right, aliases, dotImport) {
			bindings[ident.Obj] = colorBinding{object: ident.Obj, direct: true}
			return
		}
		if source, ok := right.(*ast.Ident); ok && source.Obj != nil {
			bindings[ident.Obj] = colorBinding{object: ident.Obj, source: source.Obj}
			return
		}
		delete(bindings, ident.Obj) // reassignment invalidates a stale constructor alias
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.ValueSpec:
			for index, right := range node.Values {
				if index < len(node.Names) {
					assign(node.Names[index], right)
				}
			}
		case *ast.AssignStmt:
			for index, right := range node.Rhs {
				if index < len(node.Lhs) {
					assign(node.Lhs[index], right)
				}
			}
		}
		return true
	})
	known := map[*ast.Object]bool{}
	changed := true
	for changed {
		changed = false
		for object, binding := range bindings {
			if known[object] || (!binding.direct && !known[binding.source]) {
				continue
			}
			known[object] = true
			changed = true
		}
	}
	return known
}

func unwrapParen(expression ast.Expr) ast.Expr {
	for {
		paren, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = paren.X
	}
}

func isLipglossColorCall(expression ast.Expr, aliases map[string]bool, dotImport bool) bool {
	switch expression := unwrapParen(expression).(type) {
	case *ast.SelectorExpr:
		pkg, ok := expression.X.(*ast.Ident)
		return ok && aliases[pkg.Name] && expression.Sel.Name == "Color"
	case *ast.Ident:
		return dotImport && expression.Name == "Color"
	default:
		return false
	}
}

func isColorConstructorCall(expression ast.Expr, constructors map[*ast.Object]bool) bool {
	ident, ok := unwrapParen(expression).(*ast.Ident)
	return ok && ident.Obj != nil && constructors[ident.Obj]
}

func stringConstants(file *ast.File) map[*ast.Object]ast.Expr {
	constants := map[*ast.Object]ast.Expr{}
	ast.Inspect(file, func(node ast.Node) bool {
		group, ok := node.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			return true
		}
		for _, specification := range group.Specs {
			value, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, expression := range value.Values {
				if index < len(value.Names) && value.Names[index].Obj != nil {
					constants[value.Names[index].Obj] = expression
				}
			}
		}
		return true
	})
	return constants
}

func renderLiteral(expression ast.Expr, constants map[*ast.Object]ast.Expr) (string, bool) {
	switch expression := unwrapParen(expression).(type) {
	case *ast.BasicLit:
		if expression.Kind != token.STRING && expression.Kind != token.CHAR {
			return "", false
		}
		value, err := strconv.Unquote(expression.Value)
		return value, err == nil
	case *ast.Ident:
		value, ok := constants[expression.Obj]
		if !ok {
			return "", false
		}
		return renderLiteral(value, constants)
	case *ast.BinaryExpr:
		if expression.Op != token.ADD {
			return "", false
		}
		left, leftOK := renderLiteral(expression.X, constants)
		right, rightOK := renderLiteral(expression.Y, constants)
		return left + right, leftOK && rightOK
	case *ast.CallExpr:
		ident, ok := unwrapParen(expression.Fun).(*ast.Ident)
		if !ok || ident.Name != "string" || len(expression.Args) != 1 {
			return "", false
		}
		r, ok := renderRune(expression.Args[0], constants)
		return string(r), ok
	default:
		return "", false
	}
}

func renderRune(expression ast.Expr, constants map[*ast.Object]ast.Expr) (rune, bool) {
	switch expression := unwrapParen(expression).(type) {
	case *ast.Ident:
		value, ok := constants[expression.Obj]
		if !ok {
			return 0, false
		}
		return renderRune(value, constants)
	case *ast.CallExpr:
		if ident, ok := unwrapParen(expression.Fun).(*ast.Ident); ok && ident.Name == "rune" && len(expression.Args) == 1 {
			return renderRune(expression.Args[0], constants)
		}
	case *ast.UnaryExpr:
		value, ok := renderRune(expression.X, constants)
		if ok && expression.Op == token.SUB {
			return -value, true
		}
		if ok && expression.Op == token.ADD {
			return value, true
		}
	case *ast.BinaryExpr:
		left, leftOK := renderRune(expression.X, constants)
		right, rightOK := renderRune(expression.Y, constants)
		if !leftOK || !rightOK {
			return 0, false
		}
		switch expression.Op {
		case token.ADD:
			return left + right, true
		case token.SUB:
			return left - right, true
		case token.MUL:
			return left * right, true
		case token.QUO:
			if right != 0 {
				return left / right, true
			}
		}
	case *ast.BasicLit:
		switch expression.Kind {
		case token.INT:
			value, err := strconv.ParseInt(expression.Value, 0, 32)
			return rune(value), err == nil
		case token.CHAR:
			value, err := strconv.Unquote(expression.Value)
			if err == nil && len([]rune(value)) == 1 {
				return []rune(value)[0], true
			}
		}
	}
	return 0, false
}

func appendLiteralOffenses(offenders *[]string, path, value string) {
	if strings.ContainsRune(value, '\x1b') {
		*offenders = append(*offenders, path+": raw ANSI escape")
	}
	if containsBannedRenderGlyph(value) {
		*offenders = append(*offenders, path+": raw canonical render glyph "+strconv.Quote(value))
	}
}

func containsBannedRenderGlyph(value string) bool {
	for _, r := range value {
		switch {
		case r >= '\u2500' && r <= '\u257f': // box drawing, including heavy forms
			return true
		case r >= '\u2580' && r <= '\u259f': // block elements
			return true
		case r >= '\u2800' && r <= '\u28ff': // Braille patterns
			return true
		case r >= '\u2600' && r <= '\u27bf': // BMP emoji/symbols recognized by theme.isEmoji
			return true
		case r >= '\ue000' && r <= '\uf8ff': // private-use icons
			return true
		case r >= '\U0001f000' && r <= '\U0001faff': // emoji pictographs
			return true
		}
		switch r {
		case '\u200d', '✅', '✓', '✔', '⚠', '✘', '✖', '❌',
			'→', '↗', '⇒', '←', '↙', '⇐', '↑', '⇑', '↓', '⇓',
			'▲', '▼', '▸', '★', '⑂', '·', '—':
			return true
		}
	}
	return false
}
