package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestHandleTableKey_PageDownMovesOnePage(t *testing.T) {
	m := movementModel(60)
	m.height = 20
	want := m.pageSize()

	_, _ = m.handleTableKey("pgdown")
	if m.cursor != want {
		t.Errorf("cursor = %d, want %d (one page)", m.cursor, want)
	}
	_, _ = m.handleTableKey("pgup")
	if m.cursor != 0 {
		t.Errorf("after pgup: cursor = %d, want 0", m.cursor)
	}
}

func TestHandleTableKey_PagingClampsAtBothEnds(t *testing.T) {
	m := movementModel(10)
	m.height = 40 // page larger than the list

	_, _ = m.handleTableKey("pgup")
	if m.cursor != 0 {
		t.Errorf("pgup at top: cursor = %d, want 0", m.cursor)
	}
	_, _ = m.handleTableKey("pgdown")
	if m.cursor != 9 {
		t.Errorf("pgdown past end: cursor = %d, want 9 (last fork)", m.cursor)
	}
}

// The renderer and the paging key must agree on how tall a page is. They used
// to compute it separately, and the badge legend made them disagree by a row.
func TestPageSizeMatchesRenderedWindow(t *testing.T) {
	for _, height := range []int{12, 20, 33} {
		m := movementModel(100)
		m.height = height

		// Walk one page with pgdown and confirm the cursor lands exactly at
		// pageSize -- the property the renderer's window is built from.
		before := m.cursor
		_, _ = m.handleTableKey("pgdown")
		if got := m.cursor - before; got != m.pageSize() {
			t.Errorf("height %d: pgdown moved %d rows, pageSize() = %d", height, got, m.pageSize())
		}
	}
}

// pageSize must account for the optional badge-legend line. The old inline
// "m.height - 4" assumed it away and overdrew the frame by a row.
func TestPageSizeAccountsForBadgeLegend(t *testing.T) {
	plain := movementModel(20)
	plain.height = 30
	if plain.badgeLegend() != "" {
		t.Skip("fixture unexpectedly carries badges")
	}
	base := plain.pageSize()

	badged := movementModel(20)
	badged.height = 30
	badged.forks[0].SiblingGroup = "grp"
	badged.forks[0].SiblingCount = 2
	badged.forks[1].SiblingGroup = "grp"
	badged.forks[1].SiblingCount = 2
	if badged.badgeLegend() == "" {
		t.Skip("could not produce a badge legend with this fixture")
	}
	if badged.pageSize() != base-1 {
		t.Errorf("with a legend: pageSize() = %d, want %d (one row less than %d)",
			badged.pageSize(), base-1, base)
	}
}

// Pins the key strings. bubbletea emits "pgup"/"pgdown"; a "pageup"/"pgdn"
// typo in the switch would compile and silently do nothing.
func TestUpdate_PageKeysReachTheTableHandler(t *testing.T) {
	m := movementModel(60)
	m.height = 20
	m.view = viewTable

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	got := updated.(*Model)
	if got.cursor == 0 {
		t.Error("PgDown through Update did not move the cursor; the case string does not match tea's key name")
	}

	updated2, _ := got.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if c := updated2.(*Model).cursor; c != 0 {
		t.Errorf("PgUp through Update: cursor = %d, want 0", c)
	}
}

func TestViewDetail_ScrollsAndKeepsFooterPinned(t *testing.T) {
	m := movementModel(3)
	// Short enough that the ~9-line detail box genuinely overflows; a window
	// taller than the body has nothing to scroll and would pass vacuously.
	m.height = 6
	m.view = viewDetail
	m.cursor = 0

	top := m.viewDetail()
	if !strings.Contains(top, "Back") {
		t.Fatal("detail footer missing at offset 0")
	}

	_, _ = m.handleDetailKey("pgdown")
	if m.detailOffset == 0 {
		t.Fatal("pgdown did not scroll the detail view")
	}
	scrolled := m.viewDetail()
	if !strings.Contains(scrolled, "Back") {
		t.Error("detail footer scrolled off; it must stay pinned below the window")
	}
	if scrolled == top {
		t.Error("scrolled detail body is identical to the unscrolled one")
	}

	// Clamp: paging far past the end must not blank the view.
	for range 20 {
		_, _ = m.handleDetailKey("pgdown")
	}
	if max := maxScrollOffset(m.detailBody(), m.detailViewHeight()); m.detailOffset != max {
		t.Errorf("detailOffset = %d, want clamp at %d", m.detailOffset, max)
	}
}

// A stale offset from a tall fork must not blank the next fork's detail view.
func TestViewDetail_OffsetResetsOnEnterAndExit(t *testing.T) {
	m := movementModel(3)
	m.height = 12
	m.detailOffset = 5

	_, _ = m.handleDetailKey("esc")
	if m.detailOffset != 0 {
		t.Errorf("after esc: detailOffset = %d, want 0", m.detailOffset)
	}

	m.detailOffset = 5
	_, _ = m.handleTableKey("enter")
	if m.detailOffset != 0 {
		t.Errorf("after enter: detailOffset = %d, want 0", m.detailOffset)
	}
}

func TestViewHelp_Scrolls(t *testing.T) {
	m := movementModel(3)
	m.height = 10
	m.view = viewHelp

	top := m.viewHelp()
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.helpOffset == 0 {
		t.Fatal("pgdown did not scroll the help overlay")
	}
	if m.viewHelp() == top {
		t.Error("scrolled help body is identical to the unscrolled one")
	}
	if !strings.Contains(m.viewHelp(), "go back") {
		t.Error("help dismiss hint scrolled off; it must stay pinned")
	}

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	if m.helpOffset != 0 {
		t.Errorf("after esc: helpOffset = %d, want 0", m.helpOffset)
	}
}

// The generated help remains contextual and must surface the moved keys and
// every major navigation alternate from the same registry as dispatch.
func TestHelpAdvertisesRegistryActions(t *testing.T) {
	body := helpBody(theme.DefaultContext())
	for _, k := range []string{"PgUp", "/", "Esc", "t", "c", "d", "Toggle dark/light theme", "Cycle enrichment ceiling"} {
		if !strings.Contains(body, k) {
			t.Errorf("help body missing %q", k)
		}
	}
}
