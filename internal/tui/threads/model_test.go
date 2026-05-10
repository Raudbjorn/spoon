package threads

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	gh "github.com/svnbjrn/spoon/internal/github"
)

func TestCursorWraps(t *testing.T) {
	m := New(nil, "owner", "repo", 1)
	m.threads = []gh.ReviewThread{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	m.loaded = true

	// down once
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm := m2.(Model)
	if mm.cursor != 1 {
		t.Fatalf("cursor=%d want 1", mm.cursor)
	}

	// down past end stays at last
	mm.cursor = 2
	m3, _ := mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m3.(Model).cursor != 2 {
		t.Errorf("cursor=%d want 2 (clamped)", m3.(Model).cursor)
	}

	// up below 0 stays at 0
	mm.cursor = 0
	m4, _ := mm.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m4.(Model).cursor != 0 {
		t.Errorf("cursor=%d want 0 (clamped)", m4.(Model).cursor)
	}
}
