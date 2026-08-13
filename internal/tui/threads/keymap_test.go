package threads

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestThreadHelpMatchesRegistry(t *testing.T) {
	help := renderHelp(theme.DefaultContext())
	for _, binding := range append(append(keymap.ForScopes(keymap.ThreadHelp), keymap.ForScopes(keymap.ThreadList)...), keymap.ForScopes(keymap.ThreadCompose)...) {
		key := keymap.KeyLabel(binding.Keys)
		padding := 14 - len([]rune(key))
		if padding < 1 {
			padding = 1
		}
		if !strings.Contains(help, "  "+key+strings.Repeat(" ", padding)+binding.Label) {
			t.Errorf("thread help omits exact registry action %q", binding.Label)
		}
		for _, key := range binding.Keys {
			if got := keymap.Dispatch(binding.Scope, key); got != binding.Action {
				t.Errorf("registry dispatch %q/%q = %q, want %q", binding.Scope, key, got, binding.Action)
			}
		}
	}
}

func TestFullscreenPreservesThreadSelection(t *testing.T) {
	m := New(nil, "owner", "repo", 1, false)
	m.loaded, m.cursor = true, 0
	m.width, m.height = 80, 24
	before := m.cursor
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = out.(Model)
	if !m.fullscreen || m.cursor != before {
		t.Fatal("fullscreen did not preserve selected thread")
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = out.(Model)
	if m.fullscreen || m.cursor != before {
		t.Fatal("fullscreen did not restore thread browser")
	}
}

func TestThreadHelpIsForegroundAndIgnoresFullscreen(t *testing.T) {
	m := New(nil, "owner", "repo", 1, false)
	m.loaded, m.cursor = true, 0
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = out.(Model)
	if !m.showHelp {
		t.Fatal("help did not open")
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = out.(Model)
	if !m.showHelp || m.fullscreen || m.cursor != 0 {
		t.Fatal("thread-list fullscreen leaked through help")
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = out.(Model)
	if m.showHelp || m.fullscreen || m.cursor != 0 {
		t.Fatal("help did not close without changing list state")
	}
}
