package threads

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func samplePRs() []gh.PullRequest {
	now := time.Now()
	return []gh.PullRequest{
		{Number: 42, Title: "Fix the thing", Author: "alice", HeadBranch: "fix-thing", State: "open", UpdatedAt: now.Add(-2 * 24 * time.Hour)},
		{Number: 41, Title: "Add new feature", Author: "bob", HeadBranch: "add-feature", State: "open", UpdatedAt: now.Add(-3 * 24 * time.Hour)},
		{Number: 40, Title: "Refactor module", Author: "carol", HeadBranch: "refactor", State: "open", UpdatedAt: now.Add(-5 * 24 * time.Hour)},
	}
}

func TestPicker_Init(t *testing.T) {
	m := NewPicker(samplePRs())
	if m.cursor != 0 {
		t.Errorf("cursor=%d want 0", m.cursor)
	}
	if m.Selected() != nil {
		t.Errorf("Selected() should be nil before any input")
	}
	if m.Cancelled() {
		t.Errorf("Cancelled() should be false before any input")
	}
	if cmd := m.Init(); cmd != nil {
		t.Errorf("Init() should return nil cmd; got %T", cmd)
	}
}

func TestPicker_NavigateDown(t *testing.T) {
	m := NewPicker(samplePRs())
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm := out.(PickerModel)
	if mm.cursor != 1 {
		t.Errorf("cursor=%d want 1", mm.cursor)
	}
}

func TestPicker_NavigateUp_ClampsAtZero(t *testing.T) {
	m := NewPicker(samplePRs())
	// already at 0
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm := out.(PickerModel)
	if mm.cursor != 0 {
		t.Errorf("cursor=%d want 0 (clamped)", mm.cursor)
	}
}

func TestPicker_NavigateDown_ClampsAtEnd(t *testing.T) {
	m := NewPicker(samplePRs())
	m.cursor = 2
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm := out.(PickerModel)
	if mm.cursor != 2 {
		t.Errorf("cursor=%d want 2 (clamped at end)", mm.cursor)
	}
}

func TestPicker_Select(t *testing.T) {
	m := NewPicker(samplePRs())
	m.cursor = 1
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := out.(PickerModel)
	if mm.Selected() == nil {
		t.Fatal("Selected() should not be nil after Enter")
	}
	if mm.Selected().Number != 41 {
		t.Errorf("Selected().Number=%d want 41", mm.Selected().Number)
	}
	if cmd == nil {
		t.Fatal("Enter should return a tea.Cmd (tea.Quit)")
	}
	// Invoke the cmd and confirm it returns a tea.QuitMsg.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("cmd should produce tea.QuitMsg; got %T", msg)
	}
}

func TestPicker_Cancel(t *testing.T) {
	m := NewPicker(samplePRs())
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := out.(PickerModel)
	if !mm.Cancelled() {
		t.Errorf("Cancelled() should be true after Esc")
	}
	if mm.Selected() != nil {
		t.Errorf("Selected() should remain nil after cancel")
	}
	if cmd == nil {
		t.Fatal("Esc should return a tea.Cmd (tea.Quit)")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("cmd should produce tea.QuitMsg; got %T", msg)
	}
}

func TestPicker_CancelCtrlC(t *testing.T) {
	m := NewPicker(samplePRs())
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	mm := out.(PickerModel)
	if !mm.Cancelled() {
		t.Errorf("Cancelled() should be true after Ctrl+C")
	}
}

func TestPicker_View_RendersAllPRs(t *testing.T) {
	m := NewPicker(samplePRs())
	view := m.View()
	for _, p := range samplePRs() {
		if !strings.Contains(view, p.Title) {
			t.Errorf("view missing title %q", p.Title)
		}
		if !strings.Contains(view, p.Author) {
			t.Errorf("view missing author %q", p.Author)
		}
	}
}

func TestPicker_View_EmptyList(t *testing.T) {
	m := NewPicker(nil)
	view := m.View()
	if !strings.Contains(view, "No open PRs") {
		t.Errorf("empty-view should mention 'No open PRs'; got: %q", view)
	}
}

func TestPicker_View_HighlightsCursor(t *testing.T) {
	m := NewPicker(samplePRs())
	m.cursor = 1
	view := m.View()
	lines := strings.Split(view, "\n")
	// Find the lines containing each PR's title; assert the cursor line has
	// the "> " prefix (an explicit highlight marker injected by render) while
	// non-cursor lines have a leading "  ".
	var prLines []string
	for _, ln := range lines {
		if strings.Contains(ln, "PR #") {
			prLines = append(prLines, ln)
		}
	}
	if len(prLines) < 3 {
		t.Fatalf("expected 3 PR lines, got %d", len(prLines))
	}
	// Strip ANSI / styling chars for prefix detection.
	marker := theme.DefaultContext().Glyph(theme.Selected) + " "
	if !strings.Contains(prLines[1], marker) {
		t.Errorf("cursor line should contain %q: %q", marker, prLines[1])
	}
	if strings.Contains(prLines[0], marker) {
		t.Errorf("non-cursor line should not contain %q: %q", marker, prLines[0])
	}
	// And the two rendered lines must differ (styling-wise) so the user sees
	// a visual difference.
	if prLines[0] == prLines[1] {
		t.Errorf("cursor and non-cursor lines render identically: %q", prLines[0])
	}
}

func TestPicker_EnterOnEmptyList(t *testing.T) {
	m := NewPicker(nil)
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := out.(PickerModel)
	if !mm.Cancelled() {
		t.Errorf("Enter on empty list should cancel, not crash")
	}
	if mm.Selected() != nil {
		t.Errorf("Selected() should remain nil on empty list")
	}
	if cmd == nil {
		t.Errorf("Enter on empty list should return tea.Quit cmd")
	}
}
