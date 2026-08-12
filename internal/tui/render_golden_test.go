package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
)

func TestGoldenInputView(t *testing.T) {
	rendertest.Force(t, termenv.TrueColor)
	got := goldenInputView()

	if !strings.Contains(got, "\x1b[") {
		t.Fatal("forced TrueColor render contained no ANSI escape sequence")
	}
	rendertest.Golden(t, "input-view-truecolor", got)
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
	return Model{
		view:        viewInput,
		input:       "svnbjrn/spoon",
		inputCursor: len([]rune("svnbjrn/spoon")),
		inputErr:    "fixture validation error",
	}.View()
}
