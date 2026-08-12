package tui

import (
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
