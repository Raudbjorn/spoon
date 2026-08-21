package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func nowFn() time.Time { return time.Now() }

// linearHistoryTableModel builds a Model with three forks whose linear
// history is known (true, false, unknown). Re-uses makeSF so the LIN
// column renders in the same row format TestViewTable_HeaderAndRowsAlign
// already pins.
func linearHistoryTableModel(t *testing.T, withCompare bool) *Model {
	t.Helper()
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	no := false
	now := time.Now()
	mk := func(id string, fork forge.T1Data) ScoredFork {
		return ScoredFork{
			Fork: fork,
			Heat: heat.HeatResult{Score: 20},
		}
	}
	forks := []ScoredFork{
		mk("alice/repo", forge.T1Data{
			ID: "alice/repo", Owner: "alice", Name: "repo",
			PushedAt: now, LinearHistory: &yes, MergeCommits: 0,
		}),
		mk("bob/repo", forge.T1Data{
			ID: "bob/repo", Owner: "bob", Name: "repo",
			PushedAt: now, LinearHistory: &no, MergeCommits: 3,
		}),
		mk("carol/repo", forge.T1Data{
			ID: "carol/repo", Owner: "carol", Name: "repo",
			PushedAt: now, // LinearHistory nil -> unknown
		}),
	}
	if withCompare {
		t2 := &forge.T2Data{Performed: true, AheadCount: 6, BehindCount: 5}
		for i := range forks {
			forks[i].T2 = t2
		}
	}
	m := &Model{
		view:   viewTable,
		width:  120,
		height: 30,
		parent: &forge.ParentData{FullName: "upstream/repo", DefaultBranch: "main"},
		forks:  forks,
		filter: "",
	}
	themed := m.WithTheme(ctx)
	return &themed
}

// The LIN column renders the check / cross / dash glyph for
// true / false / unknown, with a trailing ~ tagging truncated histories.
// ASCII profile degrades to + / x / - so a no-color terminal still
// produces a recognizable row.
func TestViewTable_LinearColumnGlyphs(t *testing.T) {
	m := linearHistoryTableModel(t, true)
	out := m.viewTable()
	if !strings.Contains(out, "LIN") {
		t.Fatalf("LIN header missing:\n%s", out)
	}
	if !strings.Contains(out, " + ") && !strings.Contains(out, " ✓ ") {
		t.Fatalf("linear fork glyph missing:\n%s", out)
	}
	if !strings.Contains(out, " x ") && !strings.Contains(out, " ✗ ") {
		t.Fatalf("non-linear fork glyph missing:\n%s", out)
	}
	if !strings.Contains(out, " - ") {
		t.Fatalf("unknown row glyph missing:\n%s", out)
	}
}

// Truncation: a LinearHistory:true fork whose vector was capped at the
// 100-commit ceiling must show the ~ lower-bound tag appended to its
// glyph, while a non-truncated sibling shows the plain glyph.
func TestViewTable_LinearColumnTruncatedTag(t *testing.T) {
	yes := true
	mk := func(id string, truncated bool) ScoredFork {
		return ScoredFork{
			Fork: forge.T1Data{
				ID: id, Owner: "alice", Name: "repo",
				PushedAt: nowFn(), LinearHistory: &yes,
				MergeCommits:        0,
				MergeCommitTruncated: truncated,
			},
			Heat: heat.HeatResult{Score: 20},
		}
	}
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	m := &Model{
		view:   viewTable,
		width:  120,
		height: 30,
		parent: &forge.ParentData{FullName: "upstream/repo", DefaultBranch: "main"},
		forks:  []ScoredFork{mk("a/full", false), mk("b/cut", true)},
	}
	themed := m.WithTheme(ctx)
	out := (&themed).viewTable()
	aLine, bLine := "", ""
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(l, "a/full"):
			aLine = l
		case strings.Contains(l, "b/cut"):
			bLine = l
		}
	}
	if aLine == "" || bLine == "" {
		t.Fatalf("forks missing from output:\n%s", out)
	}
	if !strings.Contains(bLine, "~") {
		t.Fatalf("truncated fork row does not carry '~': %q", bLine)
	}
	if strings.Contains(aLine, "~") {
		t.Fatalf("non-truncated fork row erroneously tagged '~': %q", aLine)
	}
}

// Sort cycle: when sortCol == "linear", linear rows (✓/+) come first
// in descending order, then non-linear (✗/x), then unknown (-). Nil
// entries sort last so the user sees the known rows first.
func TestSortForks_ByLinear(t *testing.T) {
	yes := true
	no := false
	mk := func(id string, lh *bool) ScoredFork {
		return ScoredFork{
			Fork: forge.T1Data{ID: id, Owner: "alice", Name: "repo", LinearHistory: lh},
			Heat: heat.HeatResult{},
		}
	}
	m := &Model{
		parent: &forge.ParentData{FullName: "u/repo"},
		forks: []ScoredFork{
			mk("none/y", nil),
			mk("no/y", &no),
			mk("yes/y", &yes),
		},
	}
	m.sortCol = "linear"
	m.sortAsc = false
	m.sortForks()
	if m.forks[0].Fork.ID != "yes/y" {
		t.Errorf("descending sort put %s first, want yes/y (linear)", m.forks[0].Fork.ID)
	}
	if m.forks[1].Fork.ID != "no/y" {
		t.Errorf("descending sort put %s second, want no/y (non-linear)", m.forks[1].Fork.ID)
	}
	if m.forks[2].Fork.ID != "none/y" {
		t.Errorf("descending sort put %s last, want none/y (unknown)", m.forks[2].Fork.ID)
	}
}
