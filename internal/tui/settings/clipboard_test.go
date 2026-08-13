package settings

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestSettingsClipboardCopiesNonCredentialIdleAndEditor(t *testing.T) {
	copied := ""
	m := New(&config.Config{Forge: config.ForgeConfig{Provider: "github"}}, t.TempDir()+"/config.json", nil).WithClipboard(func(value string) error {
		copied = value
		return nil
	})
	m.width, m.height = 80, 24
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = next.(Model)
	if copied != "github" || m.alert != "copied selected value" {
		t.Fatalf("idle copy: %q / %q", copied, m.alert)
	}
	copied = ""
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = next.(Model)
	if copied != "github" || m.alert != "copied selected value" {
		t.Fatalf("editor copy: %q / %q", copied, m.alert)
	}
}

func TestCredentialPasteNormalizesAllLineSeparators(t *testing.T) {
	m := New(&config.Config{}, t.TempDir()+"/config.json", nil)
	m.section, m.focus, m.editing = 1, 0, true
	m.secret.Set("first")
	m.cursor = len([]rune("first"))
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\rsecond\r\nthird\nfourth")})
	m = updated.(Model)
	if got, want := m.secret.Value(), "first\nsecond\nthird\nfourth"; got != want {
		t.Fatalf("paste = %q, want %q", got, want)
	}
	if strings.Contains(m.secret.Render(), "first") {
		t.Fatal("credential rendered")
	}
}
