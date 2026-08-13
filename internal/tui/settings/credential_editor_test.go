package settings

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestEveryCredentialFieldRefusesCtrlYYank(t *testing.T) {
	cfg := &config.Config{GitHub: config.GitHubConfig{Tokens: []string{"sentinel-token"}, Proxy: config.ProxyConfig{APIKeyFile: "sentinel-proxy"}}, Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: "sentinel-voyage"}}}
	for _, field := range Registry {
		if !field.Credential {
			continue
		}
		m := New(cfg, filepath.Join(t.TempDir(), "config.json"), nil)
		for section, candidate := range sections {
			if candidate == field.Section {
				m.section = section
				break
			}
		}
		for index, candidate := range m.fields() {
			if candidate.Key == field.Key {
				m.focus = index
				break
			}
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
		m = updated.(Model)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
		m = updated.(Model)
		if m.alert != ErrSecretClipboard.Error() {
			t.Fatalf("%s alert=%q", field.Key, m.alert)
		}
		if field.Secret && strings.Contains(m.View(), "sentinel-token") {
			t.Fatalf("%s leaked secret", field.Key)
		}
	}
}
