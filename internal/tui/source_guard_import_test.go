package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestSourceGuardRejectsColorConstructorEvasions(t *testing.T) {
	for _, source := range []string{
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; func f() { _ = colorpkg.Color("1") }`,
		`package fixture; import . "github.com/charmbracelet/lipgloss"; func f() { _ = Color("1") }`,
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; var construct = colorpkg.Color; func f() { _ = construct("1") }`,
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; var construct func(string) colorpkg.Color = colorpkg.Color; func f() { _ = construct("1") }`,
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; func f() { construct := colorpkg.Color; _ = construct("1") }`,
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; var construct func(string) colorpkg.Color; func f() { construct = colorpkg.Color; _ = construct("1") }`,
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; func f() { first := colorpkg.Color; second := first; _ = second("1") }`,
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; func f() { var source func(string) colorpkg.Color; alias := source; source = colorpkg.Color; _ = alias("1") }`,
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; func f() { _ = (colorpkg.Color)("1") }`,
	} {
		if !sourceHasGuardedColorCall(t, source) {
			t.Fatalf("source guard missed %s", source)
		}
	}
}

func TestSourceGuardUsesLexicalScopeAndInvalidatesReassignment(t *testing.T) {
	for _, source := range []string{
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; func f() { construct := colorpkg.Color; { construct := func(string) colorpkg.Color { return "" }; _ = construct("1") } }`,
		`package fixture; import colorpkg "github.com/charmbracelet/lipgloss"; func f() { construct := colorpkg.Color; construct = func(string) colorpkg.Color { return "" }; _ = construct("1") }`,
	} {
		if sourceHasGuardedColorCall(t, source) {
			t.Fatalf("source guard incorrectly followed stale/shadowed alias: %s", source)
		}
	}
}

func sourceHasGuardedColorCall(t *testing.T, source string) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	aliases, dotImport := lipglossImports(file)
	constructors := lipglossColorConstructors(file, aliases, dotImport)
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && (isLipglossColorCall(call.Fun, aliases, dotImport) || isColorConstructorCall(call.Fun, constructors)) {
			found = true
		}
		return true
	})
	return found
}

func TestSourceGuardAllowsDataUnicodeButRejectsCanonicalGlyphFamilies(t *testing.T) {
	for _, value := range []string{"owner/库", "owner/e\u0301.go"} {
		if containsBannedRenderGlyph(value) {
			t.Fatalf("data Unicode must remain allowed: %q", value)
		}
	}
	for _, value := range []string{"status ✓", "heavy ━", "block ▓", "braille ⠋", "cross ❌", "sun ☀", "plane ✈", "fire 🔥", "join 👩‍💻", "private \ue123"} {
		if !containsBannedRenderGlyph(value) {
			t.Fatalf("canonical rendering decoration must be rejected: %q", value)
		}
	}
}

func TestSourceGuardEvaluatesComposedANSI(t *testing.T) {
	for _, source := range []string{
		`package fixture; const esc = 3*9; var color = string(rune(esc)) + "[31m"`,
		`package fixture; func f() { const esc = 3*9; _ = string(rune(esc)) + "[31m" }`,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		constants := stringConstants(file)
		found := false
		ast.Inspect(file, func(node ast.Node) bool {
			expression, ok := node.(ast.Expr)
			if !ok {
				return true
			}
			if value, ok := renderLiteral(expression, constants); ok && strings.ContainsRune(value, '\x1b') {
				found = true
			}
			return true
		})
		if !found {
			t.Fatalf("source guard missed composed ANSI in %s", source)
		}
	}
}
