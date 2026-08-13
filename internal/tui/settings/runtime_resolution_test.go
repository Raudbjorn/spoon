package settings

import (
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestSettingsResolveUsesRuntimeDefaultsAndOverrides(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("SPOON_FASTEMBED_MODEL", "override-model")
	t.Setenv("SPOON_NO_VOYAGE", "1")
	cfg := &config.Config{Embedder: config.EmbedderConfig{Model: "file-model", Voyage: config.VoyageConfig{Disabled: false}}}
	config.RecordFieldValue(cfg, "embedder.model", "file-model")
	for _, test := range []struct {
		key, want string
		source    Source
	}{
		{"embedder.cacheDir", filepath.Join(t.TempDir(), "never"), DefaultSource},
		{"embedder.maxLength", "512", DefaultSource},
		{"embedder.batchSize", "32", DefaultSource},
		{"embedder.voyage.baseUrl", "https://api.voyageai.com/v1", DefaultSource},
		{"embedder.model", "override-model", EnvironmentSource},
		{"embedder.voyage.disabled", "true", EnvironmentSource},
	} {
		field := FieldByMust(test.key)
		got := Resolve(field, cfg, nil)
		if test.key == "embedder.cacheDir" {
			if got.Source != test.source || got.Value == "" {
				t.Fatalf("%s = %#v", test.key, got)
			}
			continue
		}
		if got.Value != test.want || got.Source != test.source {
			t.Fatalf("%s = %#v", test.key, got)
		}
	}
}
