package tui

import (
	"fmt"
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

func TestLongStatusStaysOnOneLineAndPagingRemainsAligned(t *testing.T) {
	filter := strings.Repeat("filter", 20)
	forks := make([]ScoredFork, 50)
	for i := range forks {
		forks[i] = ScoredFork{Fork: forge.T1Data{ID: "fork-" + filter}}
	}
	m := Model{
		view:   viewTable,
		width:  160,
		height: 50,
		parent: &forge.ParentData{FullName: "owner/" + strings.Repeat("parent", 25), DefaultBranch: "main"},
		filter: filter,
		forks:  forks,
	}

	status := m.renderStatusBar()
	if strings.Count(status, "\n") != 0 {
		t.Fatalf("long 160-column status wrapped: %q", status)
	}
	if got := lipgloss.Width(status); got != ui.MaxContentWidth {
		t.Fatalf("long status width = %d, want cap %d", got, ui.MaxContentWidth)
	}
	if !strings.Contains(status, "Unauthenticated") {
		t.Fatalf("long status lost the critical auth warning: %q", status)
	}

	_, _ = m.handleTableKey("pgdown")
	if got, want := m.cursor, m.pageSize(); got != want {
		t.Fatalf("PgDown cursor = %d, want page-size %d", got, want)
	}
	if got, want := strings.Count(m.viewTable(), "fork-"), m.pageSize(); got != want {
		t.Fatalf("rendered rows after PgDown = %d, want page-size %d", got, want)
	}
}

func TestNarrowStatusPreservesActionableTailAndElidesVariables(t *testing.T) {
	const forkCount = 50
	filter := "fork-" + strings.Repeat("x", 80)
	forks := make([]ScoredFork, forkCount)
	for i := range forks {
		forks[i] = ScoredFork{
			Fork:        forge.T1Data{ID: fmt.Sprintf("%s-%d", filter, i)},
			Marked:      i == 0,
			TierSkipped: i == 1,
		}
	}
	ctx, err := theme.ResolveContext("", "", "no-color", "", "unicode")
	if err != nil {
		t.Fatal(err)
	}
	m := Model{
		view:        viewTable,
		width:       80,
		height:      24,
		theme:       ctx,
		parent:      &forge.ParentData{FullName: "owner/" + strings.Repeat("parent", 25), DefaultBranch: "main"},
		filter:      filter,
		forks:       forks,
		enriching:   true,
		enrichDone:  1,
		enrichTotal: forkCount,
		auth:        forge.AuthInfo{RateLimit: 10},
		provider:    &tierFakeForge{headroom: 0.5},
	}
	m.setMaxTier(2)

	status := m.renderStatusBar()
	if strings.Count(status, "\n") != 0 {
		t.Fatalf("narrow status wrapped: %q", status)
	}
	if got, want := lipgloss.Width(status), 80; got != want {
		t.Fatalf("narrow status width = %d, want %d", got, want)
	}
	for _, field := range []string{"—", "T2: 1/50", "T<=2 (1 skipped)", "API: 5/10", "Unauthenticated", "1 marked"} {
		if !strings.Contains(status, field) {
			t.Errorf("narrow status lost %q: %q", field, status)
		}
	}

	_, _ = m.handleTableKey("pgdown")
	if got, want := m.cursor, m.pageSize(); got != want {
		t.Fatalf("PgDown cursor = %d, want page-size %d", got, want)
	}
	if got, want := strings.Count(m.viewTable(), "fork-"), m.pageSize(); got != want {
		t.Fatalf("rendered rows after PgDown = %d, want page-size %d", got, want)
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
				parent: &forge.ParentData{FullName: "owner/e\u0301", DefaultBranch: "main"},
				forks: []ScoredFork{{
					Fork: forge.T1Data{ID: "owner/库", DefaultBranch: "main"},
				}},
			}.WithTheme(ctx).View()
			if strings.Contains(got, "\x1b[") {
				t.Fatalf("no-color golden contains ANSI escape: %q", got)
			}
			rendertest.Golden(t, tt.name, trimViewportGolden(got))
		})
	}
}

func TestViewportCJKAndCombiningContentUseDistinctCellWidths(t *testing.T) {
	const (
		cjk       = "owner/库"
		combining = "owner/e\u0301"
		sentinel  = "| sentinel"
	)
	if got, want := lipgloss.Width(cjk), 8; got != want {
		t.Fatalf("CJK display width = %d, want %d", got, want)
	}
	if got, want := lipgloss.Width(combining), 7; got != want {
		t.Fatalf("combining display width = %d, want %d", got, want)
	}

	cjkLine := viewportInputLine(t, cjk+" "+sentinel)
	combiningLine := viewportInputLine(t, combining+"  "+sentinel)
	cjkBefore, _, cjkFound := strings.Cut(cjkLine, sentinel)
	combiningBefore, _, combiningFound := strings.Cut(combiningLine, sentinel)
	if !cjkFound || !combiningFound {
		t.Fatalf("separate input fixtures lost sentinel: CJK=%q combining=%q", cjkLine, combiningLine)
	}
	if got, want := lipgloss.Width(cjkBefore), lipgloss.Width(combiningBefore); got != want {
		t.Fatalf("sentinel columns differ: CJK=%d combining=%d", got, want)
	}
}

func viewportInputLine(t *testing.T, input string) string {
	t.Helper()
	view := Model{view: viewInput, width: 80, height: 24, input: input}.View()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, input) {
			return line
		}
	}
	t.Fatalf("80x24 input view lost %q: %q", input, view)
	return ""
}

func trimViewportGolden(view string) string {
	lines := strings.Split(view, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}
