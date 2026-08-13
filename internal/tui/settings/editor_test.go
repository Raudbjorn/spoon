package settings

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestSecretEditorConsumesKeysAndRefusesClipboard(t *testing.T) {
	const sentinel = "secret-界"
	m := New(&config.Config{GitHub: config.GitHubConfig{Tokens: []string{sentinel}}}, filepath.Join(t.TempDir(), "config.json"), nil)
	m.section, m.focus = 1, 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(Model)
	if !m.editing {
		t.Fatal("editor did not open")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = updated.(Model)
	if !m.editing || !strings.Contains(m.secret.Value(), "q") {
		t.Fatal("q escaped editor instead of being text")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	if m.alert != ErrSecretClipboard.Error() {
		t.Fatalf("clipboard refusal = %q", m.alert)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Ω")})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(Model)
	if strings.Contains(m.secret.Value(), "Ω") {
		t.Fatalf("rune backspace corrupted value %q", m.secret.Value())
	}
	if strings.Contains(m.View(), sentinel) {
		t.Fatal("credential reached rendered frame")
	}
}
