package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestModelMasksSecretsAndRequiresConfirmation(t *testing.T) {
	const secret = "task-nine-secret-sentinel"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(&config.Config{GitHub: config.GitHubConfig{Tokens: []string{secret}}}, path, nil)
	if got := m.View(); strings.Contains(got, secret) {
		t.Fatalf("secret leaked: %q", got)
	}
	m.section = 4 // Voyage
	m.focus = 4   // output dimension within the Voyage section
	m.editing = true
	m.input = "512"
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if !m.confirming || !strings.Contains(m.View(), "re-partitions") {
		t.Fatalf("missing confirmation: %q", m.View())
	}
}

func TestNoConfigIsReadOnly(t *testing.T) {
	m := New(nil, "", nil)
	if !strings.Contains(m.View(), "disabled") {
		t.Fatalf("%q", m.View())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("no-config mode offered write")
	}
}

func TestEverySectionRedactsConfiguredAndEnvironmentSecrets(t *testing.T) {
	const secret = "task-nine-secret-sentinel"
	t.Setenv("GH_TOKEN", secret)
	t.Setenv("GITLAB_TOKEN", secret)
	t.Setenv("SPOON_GH_COOKIE", secret)
	t.Setenv("VOYAGE_AI_API_KEY", secret)
	t.Setenv("VOYAGE_API_KEY", secret)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(&config.Config{GitHub: config.GitHubConfig{Tokens: []string{secret}}}, path, nil)
	for section := range sections {
		m.section = section
		if got := m.View(); strings.Contains(got, secret) {
			t.Fatalf("secret leaked in %s: %q", sections[section], got)
		}
	}
}

func TestSystemLayerSaveRequiresHostwideConfirmation(t *testing.T) {
	m := New(&config.Config{}, config.SystemPath(), nil)
	m.readOnly = "" // unit-test the confirmation branch without writing /etc.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = updated.(Model)
	if !m.confirming || !strings.Contains(m.View(), "every user") {
		t.Fatalf("missing hostwide confirmation: %q", m.View())
	}
}
