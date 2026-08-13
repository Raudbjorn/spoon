package config

import (
	"path/filepath"
	"testing"
)

func TestEffectiveConfigPreservesSourceAndInactiveFile(t *testing.T) {
	file := &Config{Embedder: EmbedderConfig{Model: "file-model", MaxLength: 0, BatchSize: 0}, UI: UIConfig{Theme: "light"}}
	RecordFieldValue(file, "embedder.model", "file-model")
	RecordFieldValue(file, "embedder.maxLength", "0")
	RecordFieldValue(file, "embedder.batchSize", "0")
	flagged := ResolveEffectiveConfig(file, map[string]string{"embedder.model": "flag-model"}, nil).Value("embedder.model")
	if flagged.Source != SourceFlag || flagged.Value != "flag-model" || !flagged.Inactive {
		t.Fatalf("flagged value = %#v", flagged)
	}
	got := ResolveEffectiveConfig(file, nil, nil)
	for _, key := range []string{"embedder.maxLength", "embedder.batchSize"} {
		if value := got.Value(key); value.Source != SourceFile || value.Value != "0" {
			t.Fatalf("explicit %s = %#v", key, value)
		}
	}
}

func TestResolveEffectiveConfigBuildsTypedRuntimeValues(t *testing.T) {
	file := &Config{
		Forge:  ForgeConfig{Provider: "gitlab", Host: "gitlab.example"},
		GitHub: GitHubConfig{RequestsPerMinute: 7, Proxy: ProxyConfig{Enabled: true, CacheTTL: "2m"}},
		Embedder: EmbedderConfig{
			Model: "file-model", CacheDir: "/file/cache", MaxLength: 7, BatchSize: 9,
			Voyage: VoyageConfig{Disabled: true, EmbedModel: "file-embed", RerankModel: "file-rerank", OutputDimension: 256, BaseURL: "https://file.example"},
		},
		UI: UIConfig{Theme: "light", Color: "ansi8", Glyphs: "ascii"},
	}
	for _, key := range []string{
		"forge.provider", "forge.host", "github.requestsPerMinute", "github.proxy.enabled", "github.proxy.cacheTtl",
		"embedder.model", "embedder.cacheDir", "embedder.maxLength", "embedder.batchSize",
		"embedder.voyage.disabled", "embedder.voyage.embedModel", "embedder.voyage.rerankModel",
		"embedder.voyage.outputDimension", "embedder.voyage.baseUrl", "ui.theme", "ui.color", "ui.glyphs",
	} {
		RecordFieldValue(file, key, "present")
	}

	effective := ResolveEffectiveConfig(file,
		map[string]string{"forge.provider": "github"},
		map[string]string{
			"SPOON_FASTEMBED_MODEL":    "env-model",
			"SPOON_FASTEMBED_CACHE":    "/env/cache",
			"SPOON_VOYAGE_EMBED_MODEL": "env-embed",
			"SPOON_TUI_THEME":          "amber",
			"SPOON_NO_VOYAGE":          "0",
		})

	if effective.Forge.Provider.Source != SourceFlag || effective.Forge.Provider.Value != "github" || !effective.Forge.Provider.Inactive {
		t.Fatalf("forge provider = %#v", effective.Forge.Provider)
	}
	if effective.FastEmbed.Model.Source != SourceEnvironment || effective.FastEmbed.Model.Environment != "SPOON_FASTEMBED_MODEL" || !effective.FastEmbed.Model.Inactive {
		t.Fatalf("fastembed model = %#v", effective.FastEmbed.Model)
	}
	if effective.Voyage.Disabled.Source != SourceFile || effective.Voyage.Disabled.Value != "true" {
		t.Fatalf("SPOON_NO_VOYAGE=0 must not disable Voyage: %#v", effective.Voyage.Disabled)
	}
	if effective.Appearance.Theme.Source != SourceEnvironment || effective.Appearance.Theme.Value != "amber" {
		t.Fatalf("appearance theme = %#v", effective.Appearance.Theme)
	}
}

func TestResolveEffectiveConfigUsesRuntimeDefaults(t *testing.T) {
	effective := ResolveEffectiveConfig(nil, nil, map[string]string{"XDG_CACHE_HOME": "/cache"})
	for key, want := range map[string]string{
		"embedder.cacheDir":           "/cache/spoon/models/fastembed",
		"embedder.maxLength":          "512",
		"embedder.batchSize":          "32",
		"embedder.voyage.embedModel":  "voyage-code-3",
		"embedder.voyage.rerankModel": "rerank-2.5",
		"embedder.voyage.baseUrl":     "https://api.voyageai.com/v1",
		"github.proxy.cacheTtl":       "1h",
	} {
		if got := effective.Value(key); got.Source != SourceDefault || got.Value != want {
			t.Fatalf("%s = %#v, want default %q", key, got, want)
		}
	}
}

func TestEffectiveConfigEveryEditableLeafRetainsRuntimeSource(t *testing.T) {
	whitelist := true
	file := &Config{
		Forge: ForgeConfig{Provider: "file-provider", Host: "file-host"},
		GitHub: GitHubConfig{Tokens: []string{"file-token"}, RequestsPerMinute: 17, Proxy: ProxyConfig{
			Enabled: true, APIKeyFile: "file-api-key", StaticFile: "file-static", WhitelistPublicIP: &whitelist, CacheTTL: "2m",
		}},
		Embedder: EmbedderConfig{Backend: "fastembed", Model: "file-model", CacheDir: "/file/cache", MaxLength: 128, BatchSize: 4, Voyage: VoyageConfig{
			Disabled: true, APIKeyFile: "file-voyage-key", EmbedModel: "file-embed", RerankModel: "file-rerank", OutputDimension: 256, BaseURL: "https://file.example",
		}},
		UI: UIConfig{Theme: "light", Color: "ansi8", Glyphs: "ascii"},
	}
	editable := []string{
		"forge.provider", "forge.host", "github.tokens", "github.requestsPerMinute", "github.proxy.enabled",
		"github.proxy.apiKeyFile", "github.proxy.staticFile", "github.proxy.whitelistPublicIp", "github.proxy.cacheTtl",
		"embedder.backend", "embedder.model", "embedder.cacheDir", "embedder.maxLength", "embedder.batchSize",
		"embedder.voyage.disabled", "embedder.voyage.apiKeyFile", "embedder.voyage.embedModel",
		"embedder.voyage.rerankModel", "embedder.voyage.outputDimension", "embedder.voyage.baseUrl",
		"ui.theme", "ui.color", "ui.glyphs",
	}
	for _, key := range editable {
		RecordFieldValue(file, key, "present")
		if got := ResolveEffectiveConfig(file, nil, nil).Value(key); got.Source != SourceFile {
			t.Fatalf("%s file source = %#v", key, got)
		}
	}

	for _, key := range []string{"forge.provider", "forge.host", "ui.color"} {
		got := ResolveEffectiveConfig(file, map[string]string{key: "flag-value"}, nil).Value(key)
		if got.Source != SourceFlag || got.Value != "flag-value" || !got.Inactive {
			t.Fatalf("%s flag source = %#v", key, got)
		}
	}

	for _, setting := range Settings() {
		if len(setting.Environment) == 0 {
			continue
		}
		envValue := "env-value"
		switch setting.Key {
		case "embedder.voyage.disabled":
			envValue = "1"
		case "embedder.voyage.outputDimension":
			envValue = "512"
		case "ui.color":
			envValue = "ansi16"
		}
		got := ResolveEffectiveConfig(file, nil, map[string]string{setting.Environment[0]: envValue}).Value(setting.Key)
		want := envValue
		if setting.Key == "embedder.voyage.disabled" {
			want = "true"
		}
		if got.Source != SourceEnvironment || got.Environment != setting.Environment[0] || got.Value != want || !got.Inactive {
			t.Fatalf("%s env source = %#v", setting.Key, got)
		}
	}
}

func TestSaveUsesCurrentExplicitFalseAndZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := &Config{GitHub: GitHubConfig{RequestsPerMinute: 7, Proxy: ProxyConfig{Enabled: true}}, Embedder: EmbedderConfig{MaxLength: 7, BatchSize: 9, Voyage: VoyageConfig{OutputDimension: 1024, Disabled: true}}}
	if err := Save(path, original); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.GitHub.RequestsPerMinute, cfg.GitHub.Proxy.Enabled = 0, false
	cfg.Embedder.MaxLength, cfg.Embedder.BatchSize = 0, 0
	cfg.Embedder.Voyage.OutputDimension, cfg.Embedder.Voyage.Disabled = 0, false
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"github.requestsPerMinute", "github.proxy.enabled", "embedder.maxLength", "embedder.batchSize", "embedder.voyage.outputDimension", "embedder.voyage.disabled"} {
		if !FieldPresent(loaded, key) {
			t.Fatalf("%s lost explicit leaf", key)
		}
	}
	if loaded.GitHub.Proxy.Enabled || loaded.GitHub.RequestsPerMinute != 0 || loaded.Embedder.MaxLength != 0 || loaded.Embedder.BatchSize != 0 || loaded.Embedder.Voyage.OutputDimension != 0 || loaded.Embedder.Voyage.Disabled {
		t.Fatalf("stale values survived save: %#v", loaded)
	}
}
