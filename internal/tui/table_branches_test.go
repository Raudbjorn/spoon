package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

func intPtr(n int) *int { return &n }

func branchTableModel(t *testing.T, withCompare bool) *Model {
	t.Helper()

	mk := func(id string, stars int, divergent *int, t2 *forge.T2Data) ScoredFork {
		return ScoredFork{
			Fork: forge.T1Data{
				ID: id, Owner: strings.SplitN(id, "/", 2)[0],
				Name:              "proxmox-packer",
				Stars:             stars,
				PushedAt:          time.Now().Add(-48 * time.Hour),
				DivergentBranches: divergent,
			},
			Heat: heat.HeatResult{Score: 20},
			T2:   t2,
		}
	}

	var t2 *forge.T2Data
	if withCompare {
		t2 = &forge.T2Data{Performed: true, AheadCount: 6, BehindCount: 5}
	}

	return &Model{
		view:   viewTable,
		width:  120,
		height: 30,
		parent: &forge.ParentData{FullName: "dustinrue/proxmox-packer", DefaultBranch: "main"},
		forks: []ScoredFork{
			mk("gregums587/proxmox-packer", 0, intPtr(3), t2),
			mk("rsanchez-s/proxmox-packer", 4231, intPtr(0), t2),
			mk("averyveryverylongownername/proxmox-packer", 7, nil, t2),
		},
	}
}

// The count must be visible, and an uncounted fork must read "-" rather than
// "0" — the same distinction the compare fix established for AHEAD/BEHIND.
func TestViewTable_BranchColumnRendersCountAndUnknown(t *testing.T) {
	for _, withCompare := range []bool{true, false} {
		m := branchTableModel(t, withCompare)
		out := m.viewTable()

		if !strings.Contains(out, "BRANCH") {
			t.Errorf("withCompare=%v: BRANCH header missing:\n%s", withCompare, out)
		}

		lines := strings.Split(out, "\n")
		var counted, unknown string
		for _, l := range lines {
			if strings.Contains(l, "gregums587") {
				counted = l
			}
			if strings.Contains(l, "averyveryverylong") || strings.Contains(l, "averyveryv") {
				unknown = l
			}
		}
		if counted == "" {
			t.Fatalf("withCompare=%v: counted fork row not rendered:\n%s", withCompare, out)
		}
		if !strings.Contains(counted, "3") {
			t.Errorf("withCompare=%v: divergent count 3 not rendered:\n  %q", withCompare, counted)
		}
		if unknown != "" && !strings.Contains(unknown, "-") {
			t.Errorf("withCompare=%v: uncounted fork must render '-':\n  %q", withCompare, unknown)
		}
	}
}

// The header and every row are separate hardcoded format strings, so they drift
// apart silently. Pin them to the same width.
func TestViewTable_HeaderAndRowsAlign(t *testing.T) {
	for _, withCompare := range []bool{true, false} {
		m := branchTableModel(t, withCompare)

		// Match on the owner prefix: long names are truncated, so the repo
		// name itself is not present on every row.
		var rows []string
		var header string
		for _, l := range strings.Split(m.viewTable(), "\n") {
			if strings.Contains(l, "REPOSITORY") {
				header = l
			}
			for _, owner := range []string{"gregums587", "rsanchez-s", "averyveryv"} {
				if strings.Contains(l, owner) {
					rows = append(rows, l)
					break
				}
			}
		}
		if len(rows) < 2 {
			t.Fatalf("withCompare=%v: expected fork rows, got %d", withCompare, len(rows))
		}
		if header == "" {
			t.Fatalf("withCompare=%v: header row not rendered", withCompare)
		}

		want := lipgloss.Width(rows[0])
		for i, r := range rows[1:] {
			if got := lipgloss.Width(r); got != want {
				t.Errorf("withCompare=%v: row %d is %d cells, want %d\n  %q\n  %q",
					withCompare, i+1, got, want, rows[0], r)
			}
		}

		// Row-to-row equality alone cannot catch the header drifting from all
		// rows at once. STATUS trails variable-width content, so anchor on the
		// PUSHED column: the header label and every row's value ("2d ago",
		// from the fixture's -48h push) must start at the same cell.
		col := func(line, marker string) int {
			idx := strings.Index(line, marker)
			if idx < 0 {
				t.Fatalf("withCompare=%v: line is missing %q:\n  %q", withCompare, marker, line)
			}
			return lipgloss.Width(line[:idx])
		}
		wantCol := col(header, "PUSHED")
		for i, r := range rows {
			if got := col(r, "2d ago"); got != wantCol {
				t.Errorf("withCompare=%v: row %d has PUSHED at cell %d, header at %d — header and rows drifted\n  %q\n  %q",
					withCompare, i, got, wantCol, header, r)
			}
		}
	}
}

// "branches" must be reachable by the s key, and unknown must not outrank a
// real count.
func TestSortForks_ByBranches(t *testing.T) {
	m := branchTableModel(t, true)
	m.sortCol = "branches"
	m.sortAsc = false
	m.sortForks()

	if got := m.forks[0].Fork.DivergentBranches; got == nil || *got != 3 {
		t.Errorf("descending sort put %v first, want the fork with 3", got)
	}
	// The genuine zero must land between the real count and unknown: nil sorts
	// as -1 precisely so "checked, nothing diverges" outranks "never checked".
	if mid := m.forks[1].Fork.DivergentBranches; mid == nil || *mid != 0 {
		t.Errorf("descending sort put %v second, want the counted 0 above unknown", mid)
	}
	if last := m.forks[len(m.forks)-1].Fork.DivergentBranches; last != nil {
		t.Errorf("unknown count should sort last descending, got %d", *last)
	}
}
