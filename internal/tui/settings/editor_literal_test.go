package settings

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/config"
)

// typeInto drives the settings editor one keystroke at a time, the way a user
// does, and returns what the field actually holds afterwards.
func typeInto(t *testing.T, key, text string) string {
	t.Helper()
	cfg := &config.Config{}
	path := filepath.Join(t.TempDir(), "config.json")
	env := map[string]string{}
	m := New(cfg, path, writableCache{}).
		WithEnvironment(env).
		WithEffective(config.ResolveEffectiveConfig(cfg, nil, env))

	m.section = -1
	for section := range sections {
		m.section = section
		for i, field := range m.fields() {
			if field.Key == key {
				m.focus = i
				goto found
			}
		}
	}
found:
	if _, ok := m.selected(); !ok {
		t.Fatalf("field %q not reachable in any section", key)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(Model)
	if !m.editing {
		t.Fatalf("field %q did not enter edit mode", key)
	}
	for _, r := range text {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	field, _ := m.selected()
	if field.IsCredential() {
		return m.secret.Value()
	}
	return m.input
}

// TestSettingsEditorAcceptsEveryPrintableCharacter is a real, user-reported
// data-loss bug, not a hypothetical.
//
// MainSettings is both the list-navigation scope and the editor scope, so it
// binds bare letters: q/e/s/v as commands and j/k as vi movement. updateEdit
// neutralised only the four command letters, so while typing, `j` dispatched to
// Down and `k` to Up and neither ever reached the text.
//
// The user's key-file path is /home/svnbjrn/.config/svnbjrn/voyage-ai. Typing it
// into the settings panel silently produced /home/svnbrn/.config/svnbrn/voyage-ai
// -- a path that does not exist. ReadCredentialFile treats a missing file as
// "no credential, no error", so the result was Voyage staying off with nothing
// to indicate why. Two independent silent failures stacked.
func TestSettingsEditorAcceptsEveryPrintableCharacter(t *testing.T) {
	const path = "/home/svnbjrn/.config/svnbjrn/voyage-ai"
	if got := typeInto(t, "embedder.voyage.apiKeyFile", path); got != path {
		t.Errorf("typed %q, field holds %q", path, got)
	}

	// Every letter, both cases, plus the punctuation a path or token uses. Any
	// binding a future scope adds must not eat one of these.
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:+=~"
	if got := typeInto(t, "embedder.cacheDir", alphabet); got != alphabet {
		t.Errorf("cacheDir lost characters:\n typed %q\n  held %q", alphabet, got)
	}
}

// TestCredentialEditorAcceptsEveryPrintableCharacter covers the case where the
// silence is worst: a masked field shows dots, so a swallowed character is
// invisible while typing AND invisible afterwards. A GitHub token containing a
// `j` or `k` was silently truncated to a token that simply does not work.
func TestCredentialEditorAcceptsEveryPrintableCharacter(t *testing.T) {
	const token = "ghp_jJkK0123456789abcdefghijklmnopqrstuvwxyz"
	if got := typeInto(t, "github.tokens", token); got != token {
		t.Errorf("credential field lost characters:\n typed %q\n  held %q", token, got)
	}
}
