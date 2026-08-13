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
		wantState           string
		wantErr             string
		wantAlertPrefix     string
	}{
		{"no-config", nil, "", "", nil, "not configured", "", ""},
		{"disabled", &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: true}}}, "", "", nil, "not configured", "", ""},
		{"env-disabled", &config.Config{}, "1", "", nil, "not configured", "", ""},
		{"world-readable-key", &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: key}}}, "", "", nil, "configured but unusable", "embedder.voyage.apiKeyFile " + `"` + key + `" contains credentials and is readable by group/other; run chmod 600 ` + key, "Voyage configured but unusable: "},
		{"invalid-dimension", &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{OutputDimension: 123}}}, "", "key", writableCache{}, "configured but unusable", "voyage: output dimension 123 must be one of 256, 512, 1024, 2048", "Voyage configured but unusable: "},
		{"unwritable-store", &config.Config{}, "", "key", writableCache{err: os.ErrPermission}, "configured but unusable", "the store cannot be written, so Voyage results could not be kept: permission denied", "Voyage configured but unusable: "},
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
			if tc.wantErr != "" {
				alert := updated.(Model).alert
				want := tc.wantAlertPrefix + tc.wantErr
				if alert != want {
					t.Fatalf("alert = %q, want full safe diagnostic %q", alert, want)
				}
			}
		})
	}
}
