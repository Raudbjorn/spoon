package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/setupcheck"
	"github.com/svnbjrn/spoon/internal/tui/settings"
)

func TestRootDeliversSettingsActionErrors(t *testing.T) {
	m := NewModel(nil, forge.AuthInfo{}, "", false)
	m.view, m.width, m.height = viewTable, 80, 24
	m = m.WithSettings(settings.New(&config.Config{}, filepath.Join(t.TempDir(), "config.json"), nil).WithActionDeps(settings.ActionDeps{
		StoreOpen: func() (setupcheck.Store, error) { return nil, errors.New("disk full") },
	}))
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	m = rootModel(t, updated)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	m = rootModel(t, updated)
	batch := cmd().(tea.BatchMsg)
	updated, _ = m.Update(batch[0]())
	m = rootModel(t, updated)
	if !strings.Contains(m.View(), "disk full") {
		t.Fatalf("root did not render action error: %q", m.View())
	}
}
