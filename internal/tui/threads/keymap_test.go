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
	for _, binding := range append(keymap.ForScopes(keymap.ThreadList), keymap.ForScopes(keymap.ThreadCompose)...) {
		if !strings.Contains(help, binding.Label) {
			t.Errorf("thread help omits registry action %q", binding.Label)
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
