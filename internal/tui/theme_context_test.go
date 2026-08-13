package tui

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestModelWithThemeReturnsCopyWithResolvedContext(t *testing.T) {
	base := NewModel(nil, forge.AuthInfo{}, "", false)
	ctx, err := theme.ResolveContext("amber", "", "ansi16", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	got := base.WithTheme(ctx)
	if got.theme != ctx {
		t.Fatalf("theme context = %#v, want %#v", got.theme, ctx)
	}
	if base.theme == ctx {
		t.Fatal("WithTheme mutated the source model")
	}
}

func TestModelUsesResolvedMonoAndNoColorASCIIContexts(t *testing.T) {
	for _, color := range []string{"mono", "no-color"} {
		ctx, err := theme.ResolveContext("", "", color, "", "ascii")
		if err != nil {
			t.Fatal(err)
		}
		got := Model{view: viewInput, width: 100, height: 30, input: "owner/repo", inputCursor: 10}.WithTheme(ctx).View()
		if strings.Contains(got, "→") || strings.Contains(got, "—") || strings.Contains(got, "\x1b[") {
			t.Fatalf("%s/ascii view lost resolved context: %q", color, got)
		}
	}
}
