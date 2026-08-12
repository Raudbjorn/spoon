package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestGoldenInputView(t *testing.T) {
	rendertest.Force(t, termenv.TrueColor)
	got := goldenInputView()

	if !strings.Contains(got, "\x1b[") {
		t.Fatal("forced TrueColor render contained no ANSI escape sequence")
	}
	rendertest.Golden(t, "input-view-truecolor", got)
}

func TestGoldenInputViewProfiles(t *testing.T) {
	cases := []struct {
		name    string
		color   string
		glyphs  string
		profile termenv.Profile
		ansi    bool
	}{
		{name: "input-view-truecolor-unicode", color: "truecolor", glyphs: "unicode", profile: termenv.TrueColor, ansi: true},
		{name: "input-view-ansi16-unicode", color: "ansi16", glyphs: "unicode", profile: termenv.ANSI, ansi: true},
		{name: "input-view-mono-ascii", color: "mono", glyphs: "ascii", profile: termenv.Ascii},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rendertest.Force(t, tt.profile)
			ctx, err := theme.ResolveContext("", "", tt.color, "", tt.glyphs)
			if err != nil {
				t.Fatal(err)
			}
			got := goldenInputViewWithTheme(ctx)
			if strings.Contains(got, "\x1b[") != tt.ansi {
				t.Fatalf("ANSI presence for %s = %t, want %t", tt.name, strings.Contains(got, "\x1b["), tt.ansi)
			}
			rendertest.Golden(t, tt.name, got)
		})
	}
}

func TestGoldenForcedProfileNonVacuity(t *testing.T) {
	previousRenderer := lipgloss.DefaultRenderer()
	previous := previousRenderer.ColorProfile()

	t.Run("truecolor emits ANSI", func(t *testing.T) {
		rendertest.Force(t, termenv.TrueColor)
		if got := goldenInputView(); !strings.Contains(got, "\x1b[") {
			t.Fatal("forced TrueColor render contained no ANSI escape sequence")
		}
	})

	t.Run("plain emits no ANSI", func(t *testing.T) {
		rendertest.Force(t, termenv.Ascii)
		if got := goldenInputView(); strings.Contains(got, "\x1b[") {
			t.Fatalf("forced plain render contained ANSI escape sequence: %q", got)
		}
	})

	if got := lipgloss.DefaultRenderer().ColorProfile(); got != previous {
		t.Fatalf("Force did not restore color profile: got %v, want %v", got, previous)
	}

	if got := lipgloss.DefaultRenderer(); got != previousRenderer {
		t.Fatal("Force did not restore the default renderer")
	}
}

func goldenInputView() string {
	return goldenInputViewWithTheme(theme.DefaultContext())
}

func goldenInputViewWithTheme(ctx theme.Context) string {
	return Model{
		view:        viewInput,
		width:       120,
		height:      30,
		input:       "svnbjrn/spoon",
		inputCursor: len([]rune("svnbjrn/spoon")),
		inputErr:    "fixture validation error",
	}.WithTheme(ctx).View()
}
