package settings

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
)

func TestTokenEditorAcceptsPasteNewlinesAndLiteralY(t *testing.T) {
	m := New(&config.Config{GitHub: config.GitHubConfig{Tokens: []string{"old"}}}, t.TempDir()+"/config.json", nil)
	m.section, m.focus = 1, 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y\r\nnext")})
	m = updated.(Model)
	if got := m.secret.Value(); got != "oldy\nnext" {
		t.Fatalf("secret input = %q", got)
	}
}

func TestCredentialPathIsMasked(t *testing.T) {
	m := New(&config.Config{GitHub: config.GitHubConfig{Proxy: config.ProxyConfig{APIKeyFile: "/private/proxy-key"}}}, t.TempDir()+"/config.json", nil)
	m.section = 2
	got := m.View()
	if strings.Contains(got, "/private/proxy-key") {
		t.Fatalf("credential path leaked: %q", got)
	}
	if !strings.Contains(got, credentialMask()) {
		t.Fatalf("credential path did not use fixed mask: %q", got)
	}
}

func TestSecretClipboardRefusalIsLimitedToSecretValues(t *testing.T) {
	m := New(&config.Config{GitHub: config.GitHubConfig{Tokens: []string{"secret"}}}, t.TempDir()+"/config.json", nil)
	m.section, m.focus = 1, 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if !strings.Contains(updated.(Model).alert, ErrSecretClipboard.Error()) {
		t.Fatal("secret clipboard was not refused")
	}
	if keymap.Dispatch(keymap.MainSettings, "ctrl+y") != keymap.Yank {
		t.Fatal("test setup lost yank binding")
	}
}
