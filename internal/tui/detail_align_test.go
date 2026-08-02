package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// The detail box is drawn by hand: each line is a "│", some content, padding,
// and a closing "│". The padding used to be computed with byte len() against
// hardcoded offsets, so any line containing a multi-byte or double-width rune
// (🔥, ★, ⑂, →, the box-drawing glyphs) or an ANSI style sequence pushed its
// closing border to the wrong column, leaving stray bars scattered to the
// right of the box.
//
// Alignment is a property of the whole box, not of any one line, so assert it
// as one: every rendered line must occupy exactly the same number of terminal
// cells. lipgloss.Width is the measure that matters — it strips ANSI and
// accounts for double-width runes, which neither len() nor
// utf8.RuneCountInString does.
func detailTestModel(t *testing.T, width int) *Model {
	t.Helper()

	t2 := &forge.T2Data{
		Performed:          true,
		AheadCount:         6,
		BehindCount:        5,
		MNA:                14,
		TotalAdditions:     14,
		TotalDeletions:     9,
		FeatureCommitRatio: 0.65,
		IsBranchWork:       true,
		ActiveBranch:       "newbranch",
		Diffs: []forge.FileDiff{
			{Path: "a.go", Additions: 10, Deletions: 2},
			{Path: "b.go", Additions: 4, Deletions: 7},
			{Path: "c.go", Additions: 0, Deletions: 0},
		},
		Commits: []forge.AheadCommit{
			{SHA: "abc123", Message: "feat: add thing", AuthorLogin: "solo", Timestamp: time.Now()},
		},
	}

	return &Model{
		view:   viewDetail,
		cursor: 0,
		width:  width,
		height: 40,
		parent: &forge.ParentData{FullName: "owner/repo", DefaultBranch: "main"},
		forks: []ScoredFork{{
			Fork: forge.T1Data{
				ID:            "gregums587/proxmox-packer",
				Owner:         "gregums587",
				Name:          "proxmox-packer",
				URL:           "https://github.com/gregums587/proxmox-packer",
				DefaultBranch: "main",
				Description:   "Packer files for building CentOS 7, 8, Rocky Linux 8, 9 and Ubuntu images",
				PushedAt:      time.Now().Add(-2 * 365 * 24 * time.Hour),
			},
			Heat: heat.HeatResult{
				Score: 8,
				Components: []heat.Component{
					{Name: "sync", Points: 9.7, Max: 15, Raw: 0.65},
				},
			},
			T2:       t2,
			Enriched: true,
		}},
	}
}

// boxLines returns only the framed lines of the detail view — the ones that
// must line up. The leading blank line and the trailing help footer sit
// outside the box.
func boxLines(rendered string) []string {
	var out []string
	for _, l := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimLeft(l, " ")
		if trimmed == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "╭"), strings.HasPrefix(trimmed, "├"),
			strings.HasPrefix(trimmed, "╰"), strings.HasPrefix(trimmed, "│"):
			out = append(out, l)
		}
	}
	return out
}

func TestViewDetail_BoxLinesAllHaveEqualWidth(t *testing.T) {
	// Several terminal widths: boxWidth is derived from m.width and clamped at
	// 70, so this exercises both the clamped and the derived branch.
	for _, width := range []int{80, 100, 120} {
		m := detailTestModel(t, width)
		lines := boxLines(m.viewDetail())
		if len(lines) < 5 {
			t.Fatalf("width %d: expected a framed box, got %d lines:\n%s",
				width, len(lines), m.viewDetail())
		}

		want := lipgloss.Width(lines[0]) // the ╭───╮ top border defines the box
		for i, l := range lines {
			if got := lipgloss.Width(l); got != want {
				t.Errorf("width %d: box line %d is %d cells, want %d\n  %q",
					width, i, got, want, l)
			}
		}
	}
}

// Every framed content line must close with a border. A line whose padding
// was miscomputed loses its closing "│" entirely (or grows a second one).
func TestViewDetail_ContentLinesCloseTheirBorder(t *testing.T) {
	m := detailTestModel(t, 100)
	for i, l := range boxLines(m.viewDetail()) {
		trimmed := strings.TrimRight(l, " ")
		if !strings.HasPrefix(strings.TrimLeft(trimmed, " "), "│") {
			continue // a ╭/├/╰ rule, not a content line
		}
		if !strings.HasSuffix(trimmed, "│") {
			t.Errorf("content line %d does not close its border:\n  %q", i, l)
		}
		if n := strings.Count(trimmed, "│"); n != 2 {
			t.Errorf("content line %d has %d vertical bars, want exactly 2:\n  %q", i, n, l)
		}
	}
}
