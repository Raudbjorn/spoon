package settings

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestCredentialDescriptorsNeverReachFramesOrClipboard(t *testing.T) {
	for _, descriptor := range config.CredentialDescriptors() {
		t.Run(descriptor.Key, func(t *testing.T) {
			const sentinel = "credential-sentinel"
			cfg := &config.Config{}
			descriptor.Set(cfg, sentinel)
			for _, state := range []string{"idle", "editing", "confirming", "busy"} {
				t.Run(state, func(t *testing.T) {
					calls := 0
					m := New(cfg, filepath.Join(t.TempDir(), "config.json"), nil).WithClipboard(func(string) error {
						calls++
						return nil
					})
					selectFieldForTest(t, &m, descriptor.Key)
					switch state {
					case "editing":
						next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
						m = next.(Model)
					case "confirming":
						m.confirming, m.pending = true, FieldByMust(descriptor.Key)
					case "busy":
						m.busy = true
					}
					m.setAlert("alert " + sentinel)
					if got := m.View(); strings.Contains(got, sentinel) {
						t.Fatalf("%s frame leaked credential: %q", state, got)
					}
					next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
					m = next.(Model)
					if calls != 0 {
						t.Fatalf("%s clipboard called %d times", state, calls)
					}
					if state == "idle" || state == "editing" {
						if m.alert != ErrSecretClipboard.Error() {
							t.Fatalf("%s yank alert = %q", state, m.alert)
						}
					}
				})
			}
		})
	}
}

func selectFieldForTest(t *testing.T, m *Model, key string) {
	t.Helper()
	field, ok := FieldByKey(key)
	if !ok {
		t.Fatalf("missing field %q", key)
	}
	for section, candidate := range sections {
		if candidate == field.Section {
			m.section = section
			break
		}
	}
	for index, candidate := range m.fields() {
		if candidate.Key == key {
			m.focus = index
			return
		}
	}
	t.Fatalf("field %q missing from its section", key)
}
