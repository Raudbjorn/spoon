package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

func TestViewViewportFloorRendersOnlyFallback(t *testing.T) {
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name          string
		width, height int
		want          string
	}{
		{"width below floor", 79, 24, "Terminal too small - requires 80x24, current 79x24"},
		{"height below floor", 80, 23, "Terminal too small - requires 80x24, current 80x23"},
		{"both below floor", 40, 12, "Terminal too small - requires 80x24, current 40x12"},
		{"unmeasured", 0, 0, "Terminal too small - requires 80x24, current 0x0"},
		{"negative", -1, -2, "Terminal too small - requires 80x24, current -1x-2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Model{
				view:    viewTable,
				width:   tt.width,
				height:  tt.height,
				parent:  &forge.ParentData{FullName: "must-not-render"},
				forks:   []ScoredFork{{Fork: forge.T1Data{ID: "must-not-render"}}},
				loading: true,
			}.WithTheme(ctx).View()
			if got != tt.want {
				t.Fatalf("View() = %q, want only %q", got, tt.want)
			}
			if strings.Contains(got, "\x1b[") {
				t.Fatalf("no-color fallback contains ANSI escape: %q", got)
			}
		})
	}
}

func TestViewViewportFloorAllowsFullViewAtAndAboveBoundary(t *testing.T) {
	for _, tt := range []struct {
		width, height int
	}{
		{80, 24}, {120, 30}, {160, 50},
	} {
		t.Run("full view", func(t *testing.T) {
			got := Model{view: viewInput, width: tt.width, height: tt.height, input: "owner/库e\u0301"}.View()
			if strings.Contains(got, "Terminal too small") {
				t.Fatalf("%dx%d unexpectedly rendered fallback: %q", tt.width, tt.height, got)
			}
			if !strings.Contains(got, "Repository:") {
				t.Fatalf("%dx%d did not render input view: %q", tt.width, tt.height, got)
			}
		})
	}
}

func TestStatusBarContentCapsAtMaximumWidth(t *testing.T) {
	m := Model{
		width:  160,
		height: 50,
		parent: &forge.ParentData{FullName: "owner/repository", DefaultBranch: "main"},
		forks:  []ScoredFork{{Fork: forge.T1Data{ID: "owner/repository"}}},
	}
	if got := lipgloss.Width(m.renderStatusBar()); got != ui.MaxContentWidth {
		t.Fatalf("160-column status width = %d, want cap %d", got, ui.MaxContentWidth)
	}
}

func TestGoldenViewportTableSizes(t *testing.T) {
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name          string
		width, height int
	}{
		{"viewport-table-80x24-no-color-ascii", 80, 24},
		{"viewport-table-120x30-no-color-ascii", 120, 30},
		{"viewport-table-160x50-no-color-ascii", 160, 50},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rendertest.Force(t, termenv.Ascii)
			got := Model{
				view:   viewTable,
				width:  tt.width,
				height: tt.height,
				parent: &forge.ParentData{FullName: "owner/库e\u0301", DefaultBranch: "main"},
				forks: []ScoredFork{{
					Fork: forge.T1Data{ID: "owner/库e\u0301", DefaultBranch: "main"},
				}},
			}.WithTheme(ctx).View()
			if strings.Contains(got, "\x1b[") {
				t.Fatalf("no-color golden contains ANSI escape: %q", got)
			}
			rendertest.Golden(t, tt.name, got)
		})
	}
}

func TestViewportGoldenInputHasExpectedCJKAndCombiningWidth(t *testing.T) {
	const input = "owner/库e\u0301"
	if got, want := lipgloss.Width(input), 9; got != want {
		t.Fatalf("input display width = %d, want %d", got, want)
	}
	got := Model{view: viewInput, width: 80, height: 24, input: input}.View()
	if !strings.Contains(got, input) {
		t.Fatalf("80x24 input view lost CJK/combining content: %q", got)
	}
}
