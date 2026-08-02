package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// dupFork builds a fork with an explicit fingerprint so grouping is exact.
func dupFork(id string, score float64, ahead, behind int, fingerprint string) ScoredFork {
	return ScoredFork{
		Fork: forge.T1Data{ID: id, BranchFingerprint: fingerprint},
		Heat: heat.HeatResult{Score: score},
		T2:   &forge.T2Data{Performed: true, AheadCount: ahead, BehindCount: behind},
	}
}

// groupSpans returns, per group key, the row indices it occupies after sorting.
func groupSpans(forks []ScoredFork) map[string][]int {
	out := map[string][]int{}
	for i, f := range forks {
		if f.SiblingGroup != "" {
			out[f.SiblingGroup] = append(out[f.SiblingGroup], i)
		}
	}
	return out
}

// The core requirement: members render adjacently, primary first, and an
// unrelated fork that merely shares stats must never land inside the group's
// gutter span. Sorting alone does not guarantee this — duplicates share a
// score, and tie order is not group-aware.
func TestSortForks_DuplicateGroupsAreContiguousAndPrimaryFirst(t *testing.T) {
	for _, col := range []string{"heat", "stars", "ahead", "branches", "forks", "pushed"} {
		t.Run(col, func(t *testing.T) {
			m := &Model{
				sortCol: col,
				sortAsc: false,
				forks: []ScoredFork{
					dupFork("emtee40/nonraid", 27, 10, 356, "fp-shared"),
					// An impostor: identical heat/ahead/behind, different work.
					dupFork("impostor/nonraid", 27, 10, 356, "fp-unique"),
					dupFork("ghenry22/nonraid", 27, 10, 356, "fp-shared"),
					dupFork("Gelma/nonraid", 19, 9, 356, "fp-gelma"),
					dupFork("jsebean/nonraid", 27, 10, 356, "fp-shared"),
				},
			}
			m.assignDuplicateGroups()
			m.sortForks()

			spans := groupSpans(m.forks)
			if len(spans) != 1 {
				t.Fatalf("expected exactly one duplicate group, got %d: %v", len(spans), spans)
			}

			var idxs []int
			for _, v := range spans {
				idxs = v
			}
			if len(idxs) != 3 {
				t.Fatalf("group has %d members, want 3", len(idxs))
			}

			// Contiguous.
			for k := 1; k < len(idxs); k++ {
				if idxs[k] != idxs[k-1]+1 {
					t.Errorf("group is not contiguous: rows %v\n%s", idxs, dumpRows(m.forks))
				}
			}
			// Primary on top.
			if !m.forks[idxs[0]].SiblingPrimary {
				t.Errorf("first group row is not the primary: %s", m.forks[idxs[0]].Fork.ID)
			}
			// The impostor is outside the span, despite identical stats.
			for _, i := range idxs {
				if m.forks[i].Fork.ID == "impostor/nonraid" {
					t.Errorf("a same-stats non-duplicate was absorbed into the group\n%s", dumpRows(m.forks))
				}
			}
		})
	}
}

func dumpRows(forks []ScoredFork) string {
	var b strings.Builder
	for i, f := range forks {
		b.WriteString("  ")
		b.WriteString(strings.TrimSpace(f.Fork.ID))
		b.WriteString("  group=")
		b.WriteString(f.SiblingGroup)
		if f.SiblingPrimary {
			b.WriteString(" (primary)")
		}
		if i < len(forks)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// Adjacent groups must be visually separable, or two lines read as one group.
// Colours cycle by appearance order precisely so this is guaranteed rather than
// probable — a hash of the group key could collide for neighbours.
//
// Asserted on the assigned colour rather than the rendered cell: lipgloss
// strips colour when tests run without a TTY, so every gutter would render as a
// bare "┃" and the test would pass vacuously.
func TestDuplicateGutter_AdjacentGroupsAlwaysDiffer(t *testing.T) {
	forks := []ScoredFork{
		{SiblingGroup: "f:aaa", SiblingCount: 2},
		{SiblingGroup: "f:aaa", SiblingCount: 2},
		{SiblingGroup: "f:bbb", SiblingCount: 2},
		{SiblingGroup: "f:bbb", SiblingCount: 2},
	}
	ord := gutterOrdinals(forks)

	if ord["f:aaa"] == ord["f:bbb"] {
		t.Error("two groups share an ordinal; their gutters would be the same colour")
	}
	if c0, c1 := gutterColour(ord["f:aaa"]), gutterColour(ord["f:bbb"]); c0 == c1 {
		t.Errorf("adjacent groups both drew %v; they would merge into one line", c0)
	}

	if got := duplicateGutter(ScoredFork{}, ord); got != " " {
		t.Errorf("ungrouped fork gutter = %q, want a single space", got)
	}
	// One cell wide either way, or every column to the right shifts.
	if w := lipgloss.Width(duplicateGutter(forks[0], ord)); w != 1 {
		t.Errorf("gutter is %d cells wide, want 1", w)
	}
	// Members of one group must share a colour, or the line breaks mid-group.
	if gutterColour(ord["f:aaa"]) != gutterColour(ord[forks[1].SiblingGroup]) {
		t.Error("members of one group were assigned different colours")
	}

	// More groups than palette entries must still never repeat a neighbour.
	n := 2*len(gutterColors) + 2
	many := make([]ScoredFork, 0, n)
	for i := range n {
		many = append(many, ScoredFork{SiblingGroup: fmt.Sprintf("f:%d", i), SiblingCount: 2})
	}
	ordMany := gutterOrdinals(many)
	for i := 1; i < len(many); i++ {
		prev := gutterColour(ordMany[many[i-1].SiblingGroup])
		cur := gutterColour(ordMany[many[i].SiblingGroup])
		if prev == cur {
			t.Fatalf("groups %d and %d share colour %v", i-1, i, cur)
		}
	}
}

// The gutter adds a column to every row; the header must move with it.
func TestViewTable_GutterKeepsHeaderAndRowsAligned(t *testing.T) {
	m := &Model{
		view:    viewTable,
		width:   140,
		height:  30,
		sortCol: "heat",
		parent:  &forge.ParentData{FullName: "qvr/nonraid", DefaultBranch: "main"},
		forks: []ScoredFork{
			dupFork("emtee40/nonraid", 27, 10, 356, "fp-shared"),
			dupFork("ghenry22/nonraid", 27, 10, 356, "fp-shared"),
			dupFork("Gelma/nonraid", 19, 9, 356, "fp-gelma"),
		},
	}
	m.assignDuplicateGroups()
	m.sortForks()

	out := m.viewTable()

	var rows []string
	for _, l := range strings.Split(out, "\n") {
		for _, owner := range []string{"emtee40", "ghenry22", "Gelma"} {
			if strings.Contains(l, owner) {
				rows = append(rows, l)
				break
			}
		}
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 fork rows, got %d:\n%s", len(rows), out)
	}
	// STATUS (badges) is a deliberately variable-width trailing column, so
	// compare everything up to it: the gutter must shift no column right of it.
	col := func(row string) int {
		idx := strings.Index(row, "unknown") // the fixture's PUSHED value
		if idx < 0 {
			t.Fatalf("row is missing the PUSHED column:\n  %q", row)
		}
		return lipgloss.Width(row[:idx])
	}
	want := col(rows[0])
	for i, r := range rows[1:] {
		if got := col(r); got != want {
			t.Errorf("row %d has PUSHED at cell %d, want %d — the gutter shifted a column\n  %q\n  %q",
				i+1, got, want, rows[0], r)
		}
	}

	// The badge carries the group size, and only for members.
	if !strings.Contains(rows[0], "👯2") {
		t.Errorf("duplicate row is missing the 👯2 badge:\n  %q", rows[0])
	}
	for _, r := range rows {
		if strings.Contains(r, "Gelma") && strings.Contains(r, "👯") {
			t.Errorf("Gelma is not a duplicate and must not carry the badge:\n  %q", r)
		}
	}
	if !strings.Contains(out, "👯 duplicate work") {
		t.Error("legend is missing the 👯 entry")
	}
}
