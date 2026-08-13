package settings

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestCredentialValuesMaskAtRestAndAcceptLiteralYAndCRLFPaste(t *testing.T) {
	const first, second = "secret-y-one", "secret-y-two"
	m := New(&config.Config{GitHub: config.GitHubConfig{Tokens: []string{first}}}, filepath.Join(t.TempDir(), "config.json"), nil)
	m.section = 1
	if got := m.View(); strings.Contains(got, first) || !strings.Contains(got, "************") {
		t.Fatalf("at-rest credential view = %q", got)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\r\n" + second)})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if got := m.Config.GitHub.Tokens; len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("tokens = %#v", got)
	}
}
