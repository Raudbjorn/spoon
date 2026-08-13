package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/tui/settings"
)

type matrixVoyageCache struct{ err error }

func (c matrixVoyageCache) VoyageCacheGetMany(context.Context, []string) (map[string][]byte, error) {
	return nil, nil
}
func (c matrixVoyageCache) VoyageCachePutMany(context.Context, string, string, map[string][]byte) error {
	return nil
}
func (c matrixVoyageCache) VoyageCacheWritable(context.Context) error { return c.err }

func TestRootSettingsVoyageSixExactDiagnosticInputs(t *testing.T) {
	key := func(t *testing.T, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "voyage.key")
		if err := os.WriteFile(path, []byte("voyage-key"), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	validKey := key(t, 0o600)
	worldReadableKey := key(t, 0o644)
	cases := []struct {
		name, wantState, wantError string
		cfg                        *config.Config
		env                        map[string]string
		cache                      embed.ResponseCache
	}{
		{
			name: "configured-no-key", wantState: "Voyage not configured",
			cfg: &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{EmbedModel: "voyage-code-3"}}},
			env: map[string]string{}, cache: matrixVoyageCache{},
		},
		{
			name: "environment-disabled", wantState: "Voyage not configured",
			cfg: &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: validKey}}},
			env: map[string]string{"SPOON_NO_VOYAGE": "1"}, cache: matrixVoyageCache{},
		},
		{
			name: "config-disabled", wantState: "Voyage not configured",
			cfg: &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: true, APIKeyFile: validKey}}},
			env: map[string]string{}, cache: matrixVoyageCache{},
		},
		{
			name: "world-readable-key-file", wantState: "Voyage configured but unusable", wantError: "readable by group/other",
			cfg: &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: worldReadableKey}}},
			env: map[string]string{}, cache: matrixVoyageCache{},
		},
		{
			name: "key-with-environment-dimension-seven", wantState: "Voyage configured but unusable", wantError: "output dimension 7",
			cfg: &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: validKey}}},
			env: map[string]string{"SPOON_VOYAGE_DIM": "7"}, cache: matrixVoyageCache{},
		},
		{
			name: "unwritable-store", wantState: "Voyage configured but unusable", wantError: "store cannot be written",
			cfg: &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: validKey}}},
			env: map[string]string{}, cache: matrixVoyageCache{err: errors.New("store cannot be written")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effective := config.ResolveEffectiveConfig(tc.cfg, nil, tc.env)
			settingsModel := settings.New(tc.cfg, filepath.Join(t.TempDir(), "config.json"), tc.cache).
				WithEffective(effective).
				WithActionDeps(settings.ActionDeps{Environment: tc.env, HTTPTransport: rootFailingRoundTripper{t}})
			m := NewModel(nil, forge.AuthInfo{}, "", false)
			m.view, m.width, m.height = viewTable, 80, 24
			m = m.WithSettings(settingsModel)
			m = matrixRootKey(t, m, ",")
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
			m = rootModel(t, updated)
			m = matrixDeliverCommand(t, m, cmd)
			frame := m.View()
			if !strings.Contains(frame, tc.wantState) {
				t.Fatalf("rendered state missing %q: %q", tc.wantState, frame)
			}
			if tc.wantError != "" && !strings.Contains(frame, tc.wantError) {
				t.Fatalf("rendered error missing %q: %q", tc.wantError, frame)
			}
		})
	}
}
