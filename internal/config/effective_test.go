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
