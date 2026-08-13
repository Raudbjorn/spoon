package settings

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestSettingsClipboardCopiesOrdinaryAndRefusesEveryCredential(t *testing.T) {
	copied := ""
	m := New(&config.Config{Forge: config.ForgeConfig{Provider: "github", Host: "forge.example"}, GitHub: config.GitHubConfig{Tokens: []string{"secret"}}}, t.TempDir()+"/config.json", nil).WithClipboard(func(value string) error { copied = value; return nil })
	m.width, m.height = 80, 24
	m.section = 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = updated.(Model)
	if copied != "github" || m.alert != "copied selected value" {
		t.Fatalf("ordinary copy: %q / %q", copied, m.alert)
	}
	m.section, m.focus, copied = 1, 0, ""
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = updated.(Model)
	if copied != "" || m.alert != ErrSecretClipboard.Error() {
		t.Fatalf("credential copy: %q / %q", copied, m.alert)
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
