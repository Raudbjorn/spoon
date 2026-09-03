package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// filterModel builds a table with predictable, distinguishable fork IDs.
func filterModel() *Model {
	return newClusterTestModel([]ScoredFork{
		makeSF("alice/tool", 90, "", "", 0),
		makeSF("bob/tool", 80, "", "", 0),
		makeSF("carol/widget", 70, "", "", 0),
		makeSF("dave/widget", 60, "", "", 0),
	})
}

func applyFilterVia(t *testing.T, m *Model, q string) {
	t.Helper()
	_, _ = m.handleTableKey("/")
	if m.view != viewFilter {
		t.Fatalf("`/` did not open the filter prompt; view = %v", m.view)
	}
	for _, r := range q {
		_, _ = m.handleFilterKey(string(r), string(r))
	}
	_, _ = m.handleFilterKey("enter", "")
	if m.view != viewTable {
		t.Fatalf("enter did not return to the table; view = %v", m.view)
	}
}

func TestFilterNarrowsVisibleRows(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "widget")

	if got := m.visibleCount(); got != 2 {
		t.Errorf("visibleCount() = %d, want 2", got)
	}
	out := m.viewTable()
	for _, want := range []string{"carol/widget", "dave/widget"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q", want)
		}
	}
	for _, unwanted := range []string{"alice/tool", "bob/tool"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("table still renders filtered-out fork %q", unwanted)
		}
	}
}

func TestFilterIsCaseInsensitive(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "WIDGET")
	if got := m.visibleCount(); got != 2 {
		t.Errorf("visibleCount() = %d, want 2 (match must be case-insensitive)", got)
	}
}

// The headline safety property: whatever row is drawn as selected must be the
// fork m.cursor points at, so opening, yanking or exporting acts on what the
// user sees. Getting this wrong is silent and destructive.
func TestFilteredCursorSelectsRenderedRow(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "widget")
	_, _ = m.handleTableKey("down")

	selected := m.forks[m.cursor].Fork.ID
	if !matchesFilter(m.forks[m.cursor], m.filter, m.pathFilter) {
		t.Fatalf("cursor landed on %q, which the filter hides", selected)
	}

	var marker string
	for _, line := range strings.Split(m.viewTable(), "\n") {
		if strings.Contains(line, "▸") {
			marker = line
		}
	}
	if marker == "" {
		t.Fatal("no selected row rendered")
	}
	if !strings.Contains(marker, selected) {
		t.Errorf("selected row %q does not name the cursor's fork %q", marker, selected)
	}

	// The detail view reads the same cursor, so it must agree.
	m.view = viewDetail
	if !strings.Contains(m.viewDetail(), selected) {
		t.Errorf("detail view does not show the selected fork %q", selected)
	}
}

// The cursor must never be left pointing at a fork the filter hides.
func TestFilterSnapsCursorOffHiddenFork(t *testing.T) {
	m := filterModel()
	m.cursor = 0 // alice/tool
	applyFilterVia(t, m, "widget")

	if m.cursor < 0 {
		t.Fatal("cursor went to -1 despite matching forks existing")
	}
	if !matchesFilter(m.forks[m.cursor], m.filter, m.pathFilter) {
		t.Errorf("cursor on %q, which the filter hides", m.forks[m.cursor].Fork.ID)
	}
}

// A filter that keeps the selected fork must not move the selection.
func TestFilterKeepsSelectionWhenItSurvives(t *testing.T) {
	m := filterModel()
	m.cursor = 3 // dave/widget
	want := m.forks[3].Fork.ID
	applyFilterVia(t, m, "widget")
	if got := m.forks[m.cursor].Fork.ID; got != want {
		t.Errorf("selection moved to %q, want %q (it still matches)", got, want)
	}
}

func TestFilterNoMatchRendersMessageAndBlocksActions(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "zzz-nothing")

	if m.cursor != -1 {
		t.Errorf("cursor = %d, want -1 when nothing is visible", m.cursor)
	}
	out := m.viewTable()
	if !strings.Contains(out, "No forks match") {
		t.Errorf("empty-match frame gives the user no explanation: %q", out)
	}
	if !strings.Contains(out, "Esc") {
		t.Error("empty-match frame offers no way to clear the filter")
	}

	// Actions keyed on the cursor must be inert rather than panic.
	_, _ = m.handleTableKey("enter")
	if m.view == viewDetail {
		t.Error("enter opened the detail view with no fork selected")
	}
	if cmd := m.openInBrowser(); cmd != nil {
		t.Error("openInBrowser returned a command with no fork selected")
	}
	if cmd := m.yankCloneCommand(); cmd != nil {
		t.Error("yankCloneCommand returned a command with no fork selected")
	}
}

func TestEscClearsFilterFromTable(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "widget")

	_, _ = m.handleTableKey("esc")
	if m.filter != "" {
		t.Errorf("filter = %q after esc, want cleared", m.filter)
	}
	if m.visibleCount() != len(m.forks) {
		t.Errorf("visibleCount() = %d after clearing, want %d", m.visibleCount(), len(m.forks))
	}
}

// Esc at the prompt cancels the edit; the previously applied filter stands.
func TestEscAtPromptKeepsActiveFilter(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "widget")

	_, _ = m.handleTableKey("/")
	_, _ = m.handleFilterKey("backspace", "")
	_, _ = m.handleFilterKey("esc", "")
	if m.filter != "widget" {
		t.Errorf("filter = %q, want it unchanged at \"widget\"", m.filter)
	}
}

// Submitting an empty query clears the filter. `/` seeds the prompt with the
// active filter so it can be edited, so clearing means emptying the field
// (Ctrl+U) and confirming -- not simply reopening and pressing Enter.
func TestEmptyQueryClearsFilter(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "widget")

	_, _ = m.handleTableKey("/")
	if m.filterInput != "widget" {
		t.Fatalf("prompt seeded with %q, want the active filter for editing", m.filterInput)
	}
	_, _ = m.handleFilterKey("ctrl+u", "")
	_, _ = m.handleFilterKey("enter", "")

	if m.filter != "" {
		t.Errorf("filter = %q, want cleared by an empty query", m.filter)
	}
	if m.visibleCount() != len(m.forks) {
		t.Errorf("visibleCount() = %d, want %d", m.visibleCount(), len(m.forks))
	}
}

func TestFilterComposesWithSort(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "widget")
	selected := m.forks[m.cursor].Fork.ID

	_, _ = m.handleTableKey("s")
	_, _ = m.handleTableKey("S")

	if got := m.forks[m.cursor].Fork.ID; got != selected {
		t.Errorf("selection moved from %q to %q across a resort", selected, got)
	}
	for _, i := range m.visibleIdx() {
		if !matchesFilter(m.forks[i], m.filter, m.pathFilter) {
			t.Errorf("resort admitted a non-matching fork %q", m.forks[i].Fork.ID)
		}
	}
	out := m.viewTable()
	if strings.Contains(out, "alice/tool") {
		t.Error("filtered-out fork reappeared after sorting")
	}
}

func TestFilterComposesWithClusterGrouping(t *testing.T) {
	m := newClusterTestModel([]ScoredFork{
		makeSF("alice/tool", 90, "c0", "label-a", 2),
		makeSF("bob/widget", 80, "c0", "label-a", 2),
		makeSF("carol/widget", 70, "c1", "label-b", 1),
	})
	applyFilterVia(t, m, "widget")
	_, _ = m.handleTableKey("g")

	out := m.viewTable()
	if strings.Contains(out, "alice/tool") {
		t.Error("cluster grouping reintroduced a filtered-out fork")
	}
	// c0 has two members overall but only one visible; the header must say 1.
	if _, count := m.clusterLabelAndCount("c0"); count != 1 {
		t.Errorf("cluster c0 member count = %d, want 1 (visible members only)", count)
	}
}

// Marks are global on purpose: a mark is an explicit act, and dropping marks
// because a filter changed would be worse than keeping them.
func TestMarksSurviveFilterAndExportIgnoresIt(t *testing.T) {
	m := filterModel()
	m.cursor = 0
	_, _ = m.handleTableKey(" ") // mark alice/tool
	if !m.forks[0].Marked {
		t.Fatal("space did not mark the fork")
	}

	applyFilterVia(t, m, "widget") // hides alice/tool
	if !m.forks[0].Marked {
		t.Error("mark was dropped when the fork was filtered out")
	}
	_, _ = m.handleTableKey("e")
	found := false
	for _, sf := range m.exportForks {
		if sf.Fork.ID == "alice/tool" {
			found = true
		}
	}
	if !found {
		t.Error("export of marked forks omitted a marked-but-hidden fork")
	}
}

// Space advances to the next visible fork, not simply cursor+1.
func TestSpaceAdvancesToNextVisibleFork(t *testing.T) {
	m := newClusterTestModel([]ScoredFork{
		makeSF("alice/widget", 90, "", "", 0),
		makeSF("bob/tool", 80, "", "", 0), // hidden by the filter
		makeSF("carol/widget", 70, "", "", 0),
	})
	applyFilterVia(t, m, "widget")
	m.cursor = 0

	_, _ = m.handleTableKey(" ")
	if got := m.forks[m.cursor].Fork.ID; got != "carol/widget" {
		t.Errorf("after marking, cursor on %q, want carol/widget (bob/tool is hidden)", got)
	}
}

// The filter is view state, so it survives a data reload -- and the status bar
// keeps saying so, which is what makes that safe.
func TestFilterSurvivesForksReload(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "widget")

	m.scoreForks(nil)
	m.cursor = clampCursorVisible(0, m.visibleIdx())
	if m.filter != "widget" {
		t.Errorf("filter = %q after reload, want it retained", m.filter)
	}

	bar := m.renderStatusBar()
	if !strings.Contains(bar, "filter") {
		t.Errorf("status bar does not disclose the active filter: %q", bar)
	}
}

func TestStatusBarShowsBothCounts(t *testing.T) {
	m := filterModel()
	applyFilterVia(t, m, "widget")
	bar := m.renderStatusBar()
	if !strings.Contains(bar, "2/4 forks") {
		t.Errorf("status bar = %q, want a visible/total fork count", bar)
	}
}

// `/` must reach the prompt through the real message path, and typing must
// land as text rather than as commands.
func TestUpdate_SlashOpensFilterPromptAndTypes(t *testing.T) {
	m := filterModel()
	m.view = viewTable

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	got := updated.(*Model)
	if got.view != viewFilter {
		t.Fatalf("view = %v after `/`, want viewFilter", got.view)
	}
	for _, r := range "widget" {
		u, _ := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		got = u.(*Model)
	}
	if got.filterInput != "widget" {
		t.Errorf("filterInput = %q, want \"widget\"", got.filterInput)
	}
	u, _ := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if final := u.(*Model); final.filter != "widget" {
		t.Errorf("filter = %q after enter, want \"widget\"", final.filter)
	}
}

// `/` used to be byte-identical to `n`; it must not open the repo search.
func TestSlashDoesNotOpenRepoSearch(t *testing.T) {
	m := filterModel()
	_, _ = m.handleTableKey("/")
	if m.view == viewInput {
		t.Error("`/` still opens the repo-search prompt")
	}
}

func TestPathFilterMatchesTouchedFiles(t *testing.T) {
	hit := ScoredFork{Fork: forge.T1Data{ID: "o/hit"}, T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "cli/registry/antipatterns.mjs"}}}}
	miss := ScoredFork{Fork: forge.T1Data{ID: "o/miss"}, T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "README.md"}}}}
	none := ScoredFork{Fork: forge.T1Data{ID: "o/none"}}
	m := Model{forks: []ScoredFork{hit, miss, none}}
	m.applyFilter("path:**/antipatterns.mjs")
	if got := m.visibleIdx(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("visible = %v", got)
	}
	m.applyFilter("o/m")
	if got := m.visibleIdx(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("substring filter regressed: %v", got)
	}
}

// A bad path: pattern must report an error the user can actually see. The
// footer only renders errMsg while time.Since(errMsgTime) < 5s (table.go), so
// setting errMsg without errMsgTime -- the exact bug refresh_test.go guards
// against for the fetch-error path -- would leave this message unrenderable.
func TestPathFilterBadPatternIsVisible(t *testing.T) {
	m := &Model{forks: []ScoredFork{{Fork: forge.T1Data{ID: "o/x"}}}}
	m.applyFilter("path:/etc")
	if m.errMsg == "" {
		t.Fatal("bad path pattern did not set errMsg")
	}
	if m.errMsgTime.IsZero() {
		t.Fatal("errMsg set without errMsgTime, so the footer can never render it")
	}
}

// TestRenameConsistentAcrossFilterDetailAndPatch guards diffMatches (added in
// filter.go): a fork that renamed a file away from a matched pattern -- the
// old path matches, the new Path does not -- must still pass the
// "path:<glob>" filter (touchesPath), still list the file in the detail
// view's Touches block, and still include it in the rendered patch. Before
// diffMatches, touchesPath alone checked PreviousPath, so a rename could
// pass the filter yet show nothing in the detail view or the patch.
func TestRenameConsistentAcrossFilterDetailAndPatch(t *testing.T) {
	renamed := forge.FileDiff{
		Path:         "cli/registry/newname.mjs",
		PreviousPath: "cli/registry/antipatterns.mjs",
		Status:       "renamed",
		Additions:    2,
		Deletions:    1,
		Patch:        "@@ -1 +1 @@\n-old\n+new\n",
	}
	sf := ScoredFork{Fork: forge.T1Data{ID: "o/renamer"}, T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{renamed}}}
	m := &Model{forks: []ScoredFork{sf}}
	m.applyFilter("path:**/antipatterns.mjs")

	if got := m.visibleIdx(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("filter did not admit the renamed fork: visible = %v", got)
	}

	m.cursor = 0
	body := m.detailBody()
	if !strings.Contains(body, "renamed cli/registry/newname.mjs") {
		t.Errorf("detail Touches block missing the renamed file:\n%s", body)
	}

	out := renderPatch(theme.Context{}, *sf.T2, m.pathFilter, 200_000)
	if !strings.Contains(out, "cli/registry/newname.mjs") {
		t.Errorf("renderPatch dropped the renamed file:\n%s", out)
	}
}
