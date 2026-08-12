package threads

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/threadsops"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestModelWithThemeReturnsCopy(t *testing.T) {
	base := NewWithFilter(nil, "owner", "repo", 1, threadsops.FilterUnresolved)
	ctx, err := theme.ResolveContext("light", "", "ansi8", "", "ascii")
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
