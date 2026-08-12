package threads

import (
	"strings"
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

func TestModelAndPickerUseResolvedMonoAndNoColorASCIIContexts(t *testing.T) {
	for _, color := range []string{"mono", "no-color"} {
		ctx, err := theme.ResolveContext("", "", color, "", "ascii")
		if err != nil {
			t.Fatal(err)
		}
		model := NewWithFilter(nil, "owner", "repo", 1, threadsops.FilterUnresolved).WithTheme(ctx)
		if got := model.themeContext(); !got.IsResolved() || got.GlyphProfile != theme.Ascii {
			t.Fatalf("%s thread model discarded context: %#v", color, got)
		}
		picker := NewPicker(nil).WithTheme(ctx)
		if got := picker.themeContext(); !got.IsResolved() || got.GlyphProfile != theme.Ascii {
			t.Fatalf("%s picker discarded context: %#v", color, got)
		}
		if got := picker.View(); strings.Contains(got, "↑") || strings.Contains(got, "↓") || strings.Contains(got, "\x1b[") {
			t.Fatalf("%s/ascii picker lost context: %q", color, got)
		}
	}
}
