package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestVoyageSixDiagnosticsRetainStateAndError(t *testing.T) {
	key := filepath.Join(t.TempDir(), "voyage.key")
	if err := os.WriteFile(key, []byte("key"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name                string
		cfg                 *config.Config
		envDisabled, apiKey string
		cache               any
		wantState, wantErr  string
	}{
		{"no-config", nil, "", "", nil, "not configured", ""},
		{"disabled", &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: true}}}, "", "", nil, "not configured", ""},
		{"env-disabled", &config.Config{}, "1", "", nil, "not configured", ""},
		{"world-readable-key", &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: key}}}, "", "", nil, "configured but unusable", "readable by group/other"},
		{"invalid-dimension", &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{OutputDimension: 123}}}, "", "key", writableCache{}, "configured but unusable", "output dimension 123 must be one of"},
		{"unwritable-store", &config.Config{}, "", "key", writableCache{err: os.ErrPermission}, "configured but unusable", "store cannot be written"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPOON_NO_VOYAGE", tc.envDisabled)
			t.Setenv("VOYAGE_AI_API_KEY", tc.apiKey)
			t.Setenv("VOYAGE_API_KEY", "")
			cache, _ := tc.cache.(writableCache)
			text, err := voyageDiagnostic(tc.cfg, cache)
			if !strings.Contains(text, tc.wantState) {
				t.Fatalf("state %q", text)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error %v", err)
			}
			m := Model{busy: true}
			updated, _ := m.Update(actionMsg{text: text, err: err})
			if tc.wantErr != "" && !strings.Contains(updated.(Model).alert, tc.wantErr) {
				t.Fatalf("alert lost verbatim error %q", updated.(Model).alert)
			}
		})
	}
}
