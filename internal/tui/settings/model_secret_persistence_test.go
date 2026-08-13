package settings

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestMaskedModelTokenSaveAndClearAre0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := New(&config.Config{}, path, nil)
	m.section, m.focus = 1, 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("token-sentinel")})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if err := Save(path, m.Config); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GitHub.Tokens) != 1 || loaded.GitHub.Tokens[0] != "token-sentinel" {
		t.Fatalf("token did not round trip: %#v", loaded.GitHub.Tokens)
	}

	m = New(loaded, path, nil)
	m.section, m.focus = 1, 0
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if err := Save(path, m.Config); err != nil {
		t.Fatal(err)
	}
	loaded, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GitHub.Tokens) != 0 {
		t.Fatalf("cleared token serialized: %#v", loaded.GitHub.Tokens)
	}
}
