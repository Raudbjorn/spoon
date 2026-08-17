package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// frameModel builds a table model with n forks. Every fork carries a badge and a
// cluster ID so the badge legend and the cluster headers -- the two pieces of
// chrome whose height varies with the data -- are exercised rather than assumed
// away.
func frameModel(t *testing.T, width, height, n int) Model {
	t.Helper()
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	forks := make([]ScoredFork, 0, n)
	for i := range n {
		forks = append(forks, ScoredFork{
			Fork: forge.T1Data{
				ID:            fmt.Sprintf("owner%03d/repository-with-a-long-name", i),
				Owner:         fmt.Sprintf("owner%03d", i),
				Name:          "repository-with-a-long-name",
				DefaultBranch: "main",
				Stars:         i,
				OpenPRCount:   1, // forces the badge legend on
			},
			Heat: heat.HeatResult{
				Score: float64(100 - i),
				// A new cluster every third fork, so any window of rows
				// contains several headers.
				ClusterID: fmt.Sprintf("cluster-%d", i/3),
			},
		})
	}
	return Model{
		view:   viewTable,
		width:  width,
		height: height,
		parent: &forge.ParentData{FullName: "upstream-owner/upstream-repository", DefaultBranch: "main"},
		forks:  forks,
	}.WithTheme(ctx)
}

// TestTableFrameNeverExceedsTerminalHeight is the contract that was broken.
//
// Bubble Tea's renderer scrolls when a frame is taller than the terminal, and
// the lines it loses are the ones at the top -- the status bar and the column
// header -- permanently, with no key that brings them back. So the frame height
// is not a cosmetic budget; it is the difference between a usable table and one
// whose header the user can never see.
func TestTableFrameNeverExceedsTerminalHeight(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{80, 24}, // the enforced floor, and the worst case: the legend wraps most here
		{100, 30},
		{120, 40},
		{200, 50},
	} {
		for _, grouped := range []bool{false, true} {
			for _, fullscreen := range []bool{false, true} {
				for _, cursor := range []int{0, 40, 79} {
					name := fmt.Sprintf("%dx%d/grouped=%v/fullscreen=%v/cursor=%d",
						size.width, size.height, grouped, fullscreen, cursor)
					t.Run(name, func(t *testing.T) {
						m := frameModel(t, size.width, size.height, 80)
						m.groupByCluster = grouped
						m.fullscreen = fullscreen
						m.cursor = cursor

						got := lipgloss.Height(m.viewTable())
						if got > size.height {
							t.Fatalf("frame is %d lines in a %d-line terminal: the top %d lines scroll off and cannot be recovered\n%s",
								got, size.height, got-size.height, m.viewTable())
						}
					})
				}
			}
		}
	}
}

// TestTableHeaderSurvivesEveryFrame states the user-visible consequence
// directly: whatever else the frame does, the column header must be in it.
func TestTableHeaderSurvivesEveryFrame(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 40}} {
		for _, grouped := range []bool{false, true} {
			name := fmt.Sprintf("%dx%d/grouped=%v", size.width, size.height, grouped)
			t.Run(name, func(t *testing.T) {
				m := frameModel(t, size.width, size.height, 80)
				m.groupByCluster = grouped
				m.cursor = 60

				view := m.viewTable()
				lines := strings.Split(view, "\n")
				if len(lines) > size.height {
					lines = lines[:size.height]
				}
				visible := strings.Join(lines, "\n")
				if !strings.Contains(visible, "REPOSITORY") {
					t.Fatalf("column header not within the first %d lines:\n%s", size.height, visible)
				}
				if !strings.Contains(visible, "upstream-owner/upstream-repository") {
					t.Fatalf("status bar not within the first %d lines:\n%s", size.height, visible)
				}
			})
		}
	}
}

// TestPageSizeMatchesRenderedRows holds pageSize to what the renderer actually
// draws. They are the same number by construction today; a future change that
// budgets one thing and draws another would desynchronise PgUp/PgDn from the
// frame silently, which is how this class of bug arrives.
func TestPageSizeMatchesRenderedRows(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprintf("grouped=%v", grouped), func(t *testing.T) {
			m := frameModel(t, 80, 24, 80)
			m.groupByCluster = grouped
			m.cursor = 0

			rows := 0
			for _, line := range strings.Split(m.viewTable(), "\n") {
				// The renderer truncates long names, so match the owner prefix,
				// which survives, rather than the full name, which does not.
				if strings.Contains(line, "owner0") && strings.Contains(line, "/repository") {
					rows++
				}
			}
			if want := m.pageSize(); rows != want {
				t.Fatalf("rendered %d fork rows, pageSize() budgets %d", rows, want)
			}
		})
	}
}
