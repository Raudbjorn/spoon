package threads

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	gh "github.com/svnbjrn/spoon/internal/github"
)

func TestCursorClamps(t *testing.T) {
	m := New(nil, "owner", "repo", 1, false)
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

func TestEnterReplyMode(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{AuthorType: "User"}}}}
	m.loaded = true
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !out.(Model).composing {
		t.Errorf("expected composing=true after 'r'")
	}
}

func TestResolveKeyOnBotThreadSkipsCompose(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{AuthorType: "Bot"}}}}
	m.loaded = true
	// 'R' on a bot thread should set mutating and not enter composing.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	mm := out.(Model)
	if mm.composing {
		t.Errorf("composing should be false on bot thread")
	}
	if !mm.mutating {
		t.Errorf("mutating should be true on bot thread")
	}
}

func TestResolveAllRequiresConfirm(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a"}}
	m.loaded = true

	// 'a' should set confirm state, NOT fire the cmd.
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	mm := out.(Model)
	if mm.confirm != "resolve-all" {
		t.Errorf("confirm=%q want resolve-all", mm.confirm)
	}
	if cmd != nil {
		t.Errorf("expected no cmd until confirm; got %v", cmd)
	}

	// Anything other than y cancels.
	out2, _ := mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if out2.(Model).confirm != "" {
		t.Errorf("confirm should clear after non-y key; got %q", out2.(Model).confirm)
	}
}

func TestMutatingBlocksReply(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{AuthorType: "User"}}}}
	m.loaded = true
	m.mutating = true

	// 'r' should NOT enter composing mode while a mutation is in flight.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if out.(Model).composing {
		t.Errorf("composing should be false while mutating")
	}
}

func TestHelpKeyToggles(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a"}}
	m.loaded = true

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !out.(Model).showHelp {
		t.Errorf("expected showHelp=true after first '?'")
	}

	out2, _ := out.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if out2.(Model).showHelp {
		t.Errorf("expected showHelp=false after second '?'")
	}
}
