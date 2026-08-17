package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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

// unwrapRendered reverses the 80-column hard wrap so a substring assertion does
// not depend on where a line break happened to land.
func unwrapRendered(rendered string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(rendered, "\n") {
		b.WriteString(strings.TrimRight(line, " "))
	}
	return b.String()
}

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
		name, wantState, wantError, wantAlert string
		cfg                                   *config.Config
		env                                   map[string]string
		cache                                 embed.ResponseCache
	}{
		// The first three cases all produced the single string "Voyage not
		// configured" before. That is precisely the defect: a host with no key,
		// a host that disabled Voyage in the environment, and a host that
		// disabled it in the config each need a different action from the user,
		// and each was told the same thing. The expectations below are
		// deliberately distinct from one another -- if a future change collapses
		// any two of them again, this test fails.
		{
			name: "configured-no-key", wantState: "Voyage inactive",
			wantAlert: "Voyage inactive: no key: set embedder.voyage.apiKeyFile to a 0600 file, or export VOYAGE_AI_API_KEY",
			cfg:       &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{EmbedModel: "voyage-code-3"}}},
			env:       map[string]string{}, cache: matrixVoyageCache{},
		},
		{
			name: "environment-disabled", wantState: "Voyage inactive",
			wantAlert: "Voyage inactive: disabled by SPOON_NO_VOYAGE",
			cfg:       &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: validKey}}},
			env:       map[string]string{"SPOON_NO_VOYAGE": "1"}, cache: matrixVoyageCache{},
		},
		{
			name: "config-disabled", wantState: "Voyage inactive",
			wantAlert: "Voyage inactive: disabled by embedder.voyage.disabled",
			cfg:       &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: true, APIKeyFile: validKey}}},
			env:       map[string]string{}, cache: matrixVoyageCache{},
		},
		{
			// The remediation sentence is carried by the error, so the state
			// line names only the source. It used to be printed twice.
			name: "world-readable-key-file", wantState: "Voyage unusable",
			wantError: "readable by group/other",
			wantAlert: `Voyage unusable (key from embedder.voyage.apiKeyFile): embedder.voyage.apiKeyFile "************" contains credentials and is readable by group/other; run chmod 600 ************`,
			cfg:       &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: worldReadableKey}}},
			env:       map[string]string{}, cache: matrixVoyageCache{},
		},
		{
			name: "key-with-environment-dimension-seven", wantState: "Voyage unusable", wantError: "output dimension 7",
			wantAlert: "Voyage unusable (key from embedder.voyage.apiKeyFile): voyage: output dimension 7 must be one of 256, 512, 1024, 2048",
			cfg:       &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: validKey}}},
			env:       map[string]string{"SPOON_VOYAGE_DIM": "7"}, cache: matrixVoyageCache{},
		},
		{
			name: "unwritable-store", wantState: "Voyage unusable", wantError: "store cannot be written",
			wantAlert: "Voyage unusable (key from embedder.voyage.apiKeyFile): the store cannot be written, so Voyage results could not be kept: store cannot be written",
			cfg:       &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: validKey}}},
			env:       map[string]string{}, cache: matrixVoyageCache{err: errors.New("store cannot be written")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effective := config.ResolveEffectiveConfig(tc.cfg, nil, tc.env)
			settingsModel := settings.New(tc.cfg, filepath.Join(t.TempDir(), "config.json"), tc.cache).
				WithEnvironment(tc.env).
				WithEffective(effective).
				WithActionDeps(settings.ActionDeps{HTTPTransport: rootFailingRoundTripper{t}})
			m := NewModel(nil, forge.AuthInfo{}, "", false)
			m.view, m.width, m.height = viewTable, 80, 24
			m = m.WithSettings(settingsModel)
			m = matrixRootKey(t, m, ",")
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
			m = rootModel(t, updated)
			m = matrixDeliverCommand(t, m, cmd)
			rendered := ansi.Strip(m.settings.View())
			if !strings.Contains(rendered, tc.wantState) {
				t.Fatalf("rendered state missing %q: %q", tc.wantState, rendered)
			}
			// The alert is hard-wrapped at 80 columns and the break can land
			// mid-word, so a plain substring check against the rendered frame
			// depends on where the text happens to fall. Undo the wrap first:
			// Hardwrap pads each line to the width, so trimming that padding and
			// joining with no separator reassembles the original run.
			if tc.wantError != "" && !strings.Contains(unwrapRendered(rendered), tc.wantError) {
				t.Fatalf("rendered error missing %q: %q", tc.wantError, rendered)
			}
			if tc.wantAlert != "" {
				want := ansi.Hardwrap("info: Settings: "+tc.wantAlert, 80, false)
				if !strings.Contains(rendered, want) {
					t.Fatalf("rendered error changed: want full safe diagnostic %q in %q", want, rendered)
				}
			}
		})
	}
}
