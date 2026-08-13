package tui

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/setupcheck"
	"github.com/svnbjrn/spoon/internal/tui/settings"
)

type rootFailingRoundTripper struct{ t *testing.T }

func (rt rootFailingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.t.Fatal("settings action attempted HTTP")
	return nil, errors.New("HTTP is forbidden in settings actions")
}

type rootActionStore struct{}

func (rootActionStore) Close() error { return nil }

func rootModel(t *testing.T, model tea.Model) Model {
	t.Helper()
	switch value := model.(type) {
	case Model:
		return value
	case *Model:
		return *value
	default:
		t.Fatalf("root update returned %T", model)
		return Model{}
	}
}
func (rootActionStore) VoyageCacheWritable(context.Context) error { return nil }

func TestRootDeliversEverySettingsActionWithoutHTTP(t *testing.T) {
	t.Setenv("VOYAGE_AI_API_KEY", "")
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("SPOON_FASTEMBED_MODEL", "")
	t.Setenv("SPOON_FASTEMBED_CACHE", "")
	path := filepath.Join(t.TempDir(), "config.json")
	var copied string
	deps := settings.ActionDeps{
		HTTPTransport: rootFailingRoundTripper{t},
		Provider: func(_ context.Context, _ setupcheck.ProviderInput, transport http.RoundTripper) (forge.AuthInfo, error) {
			if transport == nil {
				t.Fatal("provider did not receive fail-closed transport")
			}
			return forge.AuthInfo{Provider: forge.ProviderGitHub, Tier: forge.AuthCLI}, nil
		},
		StoreOpen:     func() (setupcheck.Store, error) { return rootActionStore{}, nil },
		Clipboard:     func(value string) error { copied = value; return nil },
		Save:          config.Save,
		RewriteReadme: config.WriteReadme,
	}
	m := NewModel(nil, forge.AuthInfo{}, "", false)
	m.view, m.width, m.height = viewTable, 80, 24
	m = m.WithSettings(settings.New(&config.Config{}, path, nil).WithActionDeps(deps))

	opened, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	m = rootModel(t, opened)
	if m.view != viewSettings {
		t.Fatal("comma did not open settings")
	}
	wantAlert := map[settings.ActionID]string{
		settings.ActionProviderProbe:  "github provider credentials configured",
		settings.ActionStoreCheck:     "Store/cache is available and writable",
		settings.ActionFastEmbedCheck: "FastEmbed configuration is valid",
		settings.ActionVoyageStatus:   "Voyage not configured",
		settings.ActionRewriteReadme:  "Configuration README rewritten",
		settings.ActionCopyConfigPath: "Config path copied to clipboard",
		settings.ActionSave:           "saved",
	}
	for _, action := range settings.Actions {
		t.Run(string(action.ID), func(t *testing.T) {
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(action.Key)})
			m = rootModel(t, updated)
			if cmd == nil {
				t.Fatalf("%s returned no command", action.ID)
			}
			batch, ok := cmd().(tea.BatchMsg)
			if !ok || len(batch) != 2 {
				t.Fatalf("%s command = %T, want action plus spinner", action.ID, cmd())
			}
			completion := batch[0]()
			tick := batch[1]()
			updated, _ = m.Update(tick)
			m = rootModel(t, updated)
			if !strings.Contains(m.View(), "Working") {
				t.Fatalf("%s spinner tick was not delivered through root", action.ID)
			}
			updated, _ = m.Update(completion)
			m = rootModel(t, updated)
			if strings.Contains(m.View(), "Working") {
				t.Fatalf("%s completion left settings busy", action.ID)
			}
			if !strings.Contains(m.View(), wantAlert[action.ID]) {
				t.Fatalf("%s result was not rendered: %q", action.ID, m.View())
			}
		})
	}
	if copied != path {
		t.Fatalf("clipboard = %q, want %q", copied, path)
	}
}
