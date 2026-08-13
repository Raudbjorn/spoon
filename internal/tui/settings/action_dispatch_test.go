package settings

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestActionKeysStartRegisteredActions(t *testing.T) {
	for _, action := range Actions {
		t.Run(string(action.ID), func(t *testing.T) {
			m := New(&config.Config{}, filepath.Join(t.TempDir(), "config.json"), writableCache{})
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(action.Key)})
			m = updated.(Model)
			if cmd == nil || !m.busy || m.busyAction != action.ID {
				t.Fatalf("key %q did not start %q: busy=%v action=%q", action.Key, action.ID, m.busy, m.busyAction)
			}
		})
	}
}
