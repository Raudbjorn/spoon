package tui

import (
	"testing"
)

// Baseline coverage for table cursor movement. Before this file the table's
// up/down/home/G had no test at all, while three separate call sites clamped
// the cursor by hand -- so a regression in any of them was invisible. Paging
// and filtering both move through the same clamp, which is why this lands
// first.

func movementModel(n int) *Model {
	forks := make([]ScoredFork, 0, n)
	for i := range n {
		forks = append(forks, makeSF(string(rune('a'+i))+"/repo", float64(100-i), "", "", 0))
	}
	return newClusterTestModel(forks)
}

func TestHandleTableKey_DownStopsAtLastFork(t *testing.T) {
	m := movementModel(3)
	for range 10 {
		_, _ = m.handleTableKey("down")
	}
	if m.cursor != 2 {
		t.Errorf("cursor = %d, want 2 (last fork)", m.cursor)
	}
}

func TestHandleTableKey_UpStopsAtFirstFork(t *testing.T) {
	m := movementModel(3)
	m.cursor = 2
	for range 10 {
		_, _ = m.handleTableKey("up")
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (first fork)", m.cursor)
	}
}

func TestHandleTableKey_HomeAndEnd(t *testing.T) {
	m := movementModel(5)
	m.cursor = 2

	_, _ = m.handleTableKey("G")
	if m.cursor != 4 {
		t.Errorf("after G: cursor = %d, want 4", m.cursor)
	}
	_, _ = m.handleTableKey("home")
	if m.cursor != 0 {
		t.Errorf("after home: cursor = %d, want 0", m.cursor)
	}
	_, _ = m.handleTableKey("end")
	if m.cursor != 4 {
		t.Errorf("after end: cursor = %d, want 4", m.cursor)
	}
}

// j/k must stay interchangeable with the arrow keys -- they share a case, and
// a refactor that splits them would silently drop vim navigation.
func TestHandleTableKey_VimKeysMatchArrows(t *testing.T) {
	arrows, vim := movementModel(4), movementModel(4)
	for _, k := range []string{"down", "down", "up"} {
		_, _ = arrows.handleTableKey(k)
	}
	for _, k := range []string{"j", "j", "k"} {
		_, _ = vim.handleTableKey(k)
	}
	if arrows.cursor != vim.cursor {
		t.Errorf("arrow cursor = %d, vim cursor = %d; the two must agree", arrows.cursor, vim.cursor)
	}
}

// Movement on an empty list must not produce an out-of-range cursor: every
// consumer (detail, open, yank, Space) guards on m.cursor against len(m.forks),
// and a stale positive cursor here would index past the end.
func TestHandleTableKey_MovementOnEmptyListStaysInRange(t *testing.T) {
	m := newClusterTestModel(nil)
	for _, k := range []string{"down", "up", "G", "end", "home"} {
		_, _ = m.handleTableKey(k)
		if m.cursor >= len(m.forks) && m.cursor != 0 {
			t.Fatalf("after %q on an empty list: cursor = %d, out of range", k, m.cursor)
		}
	}
}
