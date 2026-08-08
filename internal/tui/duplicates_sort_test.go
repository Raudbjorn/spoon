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
	var header string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "REPOSITORY") {
			header = l
		}
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
	if header == "" {
		t.Fatalf("header row not rendered:\n%s", out)
	}
	// STATUS (badges) is a deliberately variable-width trailing column, so
	// compare everything up to it: the gutter must shift no column right of it.
	col := func(row, marker string) int {
		idx := strings.Index(row, marker)
		if idx < 0 {
			t.Fatalf("row is missing the PUSHED column:\n  %q", row)
		}
		return lipgloss.Width(row[:idx])
	}
	// The header carries no gutter glyph, only the space the gutter occupies;
	// its PUSHED label must sit at the same cell as every row's PUSHED value.
	want := col(rows[0], "unknown") // the fixture's PUSHED value
	if got := col(header, "PUSHED"); got != want {
		t.Errorf("header has PUSHED at cell %d, rows at cell %d — the gutter shifted rows but not the header\n  %q\n  %q",
			got, want, header, rows[0])
	}
	for i, r := range rows[1:] {
		if got := col(r, "unknown"); got != want {
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

// A fork with zero divergent branches has no work to fingerprint, so its empty
// fingerprint is the correct final state — not missing data. Treating it as
// missing re-swept every inert mirror on every run, which is most of a typical
// fork network, silently defeating the cache.
func TestStartBranchDivergenceSweep_DoesNotResweepInertForks(t *testing.T) {
	zero, three := 0, 3
	m := &Model{forks: []ScoredFork{
		// Inert: counted, legitimately no fingerprint. Must not be re-swept.
		{Fork: forge.T1Data{ID: "inert/x", DivergentBranches: &zero}},
		// Complete: counted and fingerprinted. Must not be re-swept.
		{Fork: forge.T1Data{ID: "done/x", DivergentBranches: &three, BranchFingerprint: "fp"}},
		// Genuinely incomplete: has divergent branches but no fingerprint.
		{Fork: forge.T1Data{ID: "partial/x", DivergentBranches: &three}},
		// Never swept at all.
		{Fork: forge.T1Data{ID: "fresh/x"}},
	}}

	// Exercise the production predicate itself, not a copy of it — an inline
	// re-statement would keep passing if the real one regressed.
	var swept []string
	for _, sf := range m.forks {
		if needsBranchSweep(sf.Fork) {
			swept = append(swept, sf.Fork.ID)
		}
	}

	want := map[string]bool{"partial/x": true, "fresh/x": true}
	if len(swept) != len(want) {
		t.Fatalf("sweep set = %v, want exactly %v", swept, []string{"partial/x", "fresh/x"})
	}
	for _, id := range swept {
		if !want[id] {
			t.Errorf("%s was queued for re-sweep but its data is complete", id)
		}
	}
}

// Go's sort requires a strict weak ordering: less(i,j) and less(j,i) must not
// both be true. Deriving the descending case as !less alone returns true both
// ways on a tie, which frees sort to reorder equal elements and makes the row
// order gatherDuplicateGroups anchors to non-deterministic.
func TestSortForks_ComparatorIsAStrictWeakOrdering(t *testing.T) {
	zero := 0
	mk := func(id string) ScoredFork {
		// Every field the comparator reads is identical across these forks.
		return ScoredFork{
			Fork: forge.T1Data{ID: id, Stars: 7, SubForkCount: 2, DivergentBranches: &zero},
			Heat: heat.HeatResult{Score: 27},
			T2:   &forge.T2Data{Performed: true, AheadCount: 10},
		}
	}
	for _, col := range []string{"heat", "stars", "ahead", "branches", "forks", "pushed"} {
		for _, asc := range []bool{true, false} {
			m := &Model{sortCol: col, sortAsc: asc, forks: []ScoredFork{mk("a/x"), mk("b/x")}}
			cmp := m.forkLess()
			if cmp(0, 1) && cmp(1, 0) {
				t.Errorf("%s asc=%v: comparator reports both a<b and b<a for equal forks", col, asc)
			}
		}
	}
}

// Ties must also keep their input order, which is the whole point of using
// SliceStable — and what makes a duplicate group's anchor position repeatable.
func TestSortForks_TiesKeepInputOrder(t *testing.T) {
	mk := func(id string) ScoredFork {
		return ScoredFork{Fork: forge.T1Data{ID: id}, Heat: heat.HeatResult{Score: 27}}
	}
	for _, asc := range []bool{true, false} {
		m := &Model{sortCol: "heat", sortAsc: asc, forks: []ScoredFork{mk("a"), mk("b"), mk("c")}}
		m.sortForks()
		got := []string{m.forks[0].Fork.ID, m.forks[1].Fork.ID, m.forks[2].Fork.ID}
		if got[0] != "a" || got[1] != "b" || got[2] != "c" {
			t.Errorf("asc=%v: equal-scored forks reordered to %v, want [a b c]", asc, got)
		}
	}
}

// gatherDuplicateGroups must emit every member exactly once. If a second member
// were ever flagged primary, the old shape dropped it and the copy() below
// truncated, leaving a stale row in the tail.
func TestGatherDuplicateGroups_EmitsEveryMemberOnce(t *testing.T) {
	m := &Model{forks: []ScoredFork{
		{Fork: forge.T1Data{ID: "a/x"}, SiblingGroup: "g", SiblingCount: 3, SiblingPrimary: true},
		{Fork: forge.T1Data{ID: "outsider/x"}},
		// Deliberately inconsistent: a second primary in the same group.
		{Fork: forge.T1Data{ID: "b/x"}, SiblingGroup: "g", SiblingCount: 3, SiblingPrimary: true},
		{Fork: forge.T1Data{ID: "c/x"}, SiblingGroup: "g", SiblingCount: 3},
	}}
	m.gatherDuplicateGroups(0, len(m.forks))

	seen := map[string]int{}
	for _, f := range m.forks {
		seen[f.Fork.ID]++
	}
	for _, id := range []string{"a/x", "b/x", "c/x", "outsider/x"} {
		if seen[id] != 1 {
			t.Errorf("%s appears %d times, want exactly 1: %v", id, seen[id], m.forks)
		}
	}
	if len(m.forks) != 4 {
		t.Errorf("list length changed to %d", len(m.forks))
	}
}

// handleBranchDivergence re-sorts the whole list once the sweep lands — an
// async event that can arrive well after the user has moved the cursor onto a
// specific row. Without capturing and restoring the selection, a re-sort
// landing between the user looking at a fork and acting on it (space to mark,
// enter to open) would silently apply to whichever fork the reorder happened
// to leave under the cursor instead.
func TestHandleBranchDivergence_PreservesCursorAcrossResort(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	m := &Model{
		sortCol: "heat",
		parent:  &forge.ParentData{FullName: "owner/repo", DefaultBranch: "main"},
		forks: []ScoredFork{
			{Fork: forge.T1Data{ID: "low/x"}, Heat: heat.HeatResult{Score: 5}},
			{Fork: forge.T1Data{ID: "target/x"}, Heat: heat.HeatResult{Score: 10}},
			{Fork: forge.T1Data{ID: "high/x"}, Heat: heat.HeatResult{Score: 50}},
		},
	}
	// Cursor is on target/x, the middle-scored fork.
	m.cursor = 1

	n := 2
	m.handleBranchDivergence(branchDivergenceMsg{
		counts: map[string]int{"target/x": n},
	})

	if got := m.forks[m.cursor].Fork.ID; got != "target/x" {
		t.Errorf("cursor now points at %q after the resort, want %q", got, "target/x")
	}
}

// processPendingUpdates's post-batch gather can also move rows; the same
// preservation must apply there.
func TestProcessPendingUpdates_PreservesCursorAcrossGather(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	m := &Model{
		sortCol: "heat",
		parent:  &forge.ParentData{FullName: "owner/repo", DefaultBranch: "main"},
		forks: []ScoredFork{
			// Two forks that will become a duplicate group once T2 lands, plus
			// an unrelated fork sitting between them before gather runs.
			{Fork: forge.T1Data{ID: "a/x"}, Heat: heat.HeatResult{Score: 10}},
			{Fork: forge.T1Data{ID: "target/x"}, Heat: heat.HeatResult{Score: 10}},
			{Fork: forge.T1Data{ID: "b/x"}, Heat: heat.HeatResult{Score: 10}},
		},
		pendingUpdates: []tier2ResultMsg{
			{forkID: "a/x", t2: forge.T2Data{Performed: true, AheadCount: 10, HeadSHA: "shared"}},
			{forkID: "b/x", t2: forge.T2Data{Performed: true, AheadCount: 10, HeadSHA: "shared"}},
		},
		enrichTotal: 2,
		enriching:   true,
	}
	m.cursor = 1 // on target/x, between the two forks that are about to group

	m.processPendingUpdates()

	if got := m.forks[m.cursor].Fork.ID; got != "target/x" {
		t.Errorf("cursor now points at %q after gather, want %q", got, "target/x")
	}
}

// BranchCounts.Truncated used to be computed by the sweep and then discarded —
// GHProvider.DivergentBranchCounts dropped it, so a fork with more than 100
// branches silently reported a lower-bound count as if it were exact. It must
// now reach the user.
func TestHandleBranchDivergence_SurfacesTruncatedForks(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	m := &Model{
		parent: &forge.ParentData{FullName: "owner/repo", DefaultBranch: "main"},
		forks:  []ScoredFork{{Fork: forge.T1Data{ID: "big/x"}}},
	}

	m.handleBranchDivergence(branchDivergenceMsg{
		counts:    map[string]int{"big/x": 5},
		truncated: []string{"big/x"},
	})

	if m.errMsg == "" {
		t.Error("truncated forks were reported but nothing was surfaced to the user")
	}
}
