package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestEveryCredentialSentinelIsAbsentFromEverySettingsFrameAndClipboard(t *testing.T) {
	for _, descriptor := range config.CredentialDescriptors() {
		t.Run(descriptor.Key, func(t *testing.T) {
			field := FieldByMust(descriptor.Key)
			sentinel := "credential-sentinel-" + strings.ReplaceAll(descriptor.Key, ".", "-")
			cfg := &config.Config{}
			value := sentinel
			if descriptor.Key != "github.tokens" {
				keyFile := filepath.Join(t.TempDir(), sentinel)
				if err := os.WriteFile(keyFile, []byte(sentinel), 0o600); err != nil {
					t.Fatal(err)
				}
				value = keyFile
			}
			descriptor.Set(cfg, value)

			clipboardCalls := 0
			m := selectSettingsField(New(cfg, filepath.Join(t.TempDir(), "config.json"), nil).WithClipboard(func(string) error {
				clipboardCalls++
				return nil
			}), descriptor.Key)
			m.width, m.height = 80, 24
			assertCredentialFrameSafe(t, sentinel, "idle", m.View())

			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
			m = updated.(Model)
			if clipboardCalls != 0 {
				t.Fatal("idle credential yank reached clipboard")
			}
			assertCredentialFrameSafe(t, sentinel, "idle alert", m.View())

			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			if !m.editing {
				t.Fatal("credential editor did not open")
			}
			assertCredentialFrameSafe(t, sentinel, "editing", m.View())
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
			m = updated.(Model)
			if clipboardCalls != 0 {
				t.Fatal("editing credential yank reached clipboard")
			}
			assertCredentialFrameSafe(t, sentinel, "editing alert", m.View())

			m.clearEditor()
			m.confirming = true
			m.pending = field
			m.pendingCandidate = cfg
			assertCredentialFrameSafe(t, sentinel, "confirmation", m.View())

			m.confirming = false
			m.pending = Field{}
			m.busy = true
			m.busyAction = ActionSave
			assertCredentialFrameSafe(t, sentinel, "busy", m.View())

			m.busy = false
			m.setAlert("save failed: " + value)
			assertCredentialFrameSafe(t, sentinel, "alert", m.View())
		})
	}
}

func selectSettingsField(m Model, key string) Model {
	for sectionIndex, section := range sections {
		m.section = sectionIndex
		for fieldIndex, field := range m.fields() {
			if field.Section == section && field.Key == key {
				m.focus = fieldIndex
				return m
			}
		}
	}
	panic("missing settings field " + key)
}

func assertCredentialFrameSafe(t *testing.T, sentinel, state, frame string) {
	t.Helper()
	if strings.Contains(frame, sentinel) {
		t.Fatalf("%s frame leaked credential sentinel: %q", state, frame)
	}
}

func TestCredentialEditorFrameDoesNotRevealSecretLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	render := func(value string) string {
		cfg := &config.Config{}
		config.CredentialDescriptors()[0].Set(cfg, value)
		m := selectSettingsField(New(cfg, path, nil), "github.tokens")
		m.width, m.height = 80, 24
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		return updated.(Model).View()
	}

	short := render("x")
	long := render(strings.Repeat("x", 80))
	if short != long {
		t.Fatalf("credential editor frame varies with secret length:\nshort:\n%s\nlong:\n%s", short, long)
	}
}
