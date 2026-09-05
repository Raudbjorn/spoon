package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestV1ToV2SavePreservesEveryExistingLeaf(t *testing.T) {
	dir := t.TempDir()
	proxyKey := filepath.Join(dir, "proxy.key")
	voyageKey := filepath.Join(dir, "voyage.key")
	for _, path := range []string{proxyKey, voyageKey} {
		if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "config.json")
	before := Config{Version: 1, Forge: ForgeConfig{Provider: "gitlab", Host: "git.example"}, GitHub: GitHubConfig{Tokens: []string{"one", "two"}, RequestsPerMinute: 333, Proxy: ProxyConfig{Enabled: true, APIKeyFile: proxyKey, StaticFile: proxyKey, WhitelistPublicIP: boolPtr(true), CacheTTL: "5m"}}, Embedder: EmbedderConfig{Backend: "fastembed", Model: "fast-bge-base-en-v1.5", CacheDir: dir, MaxLength: 512, BatchSize: 8, Voyage: VoyageConfig{APIKeyFile: voyageKey, EmbedModel: "embed", RerankModel: "rerank", OutputDimension: 1024, BaseURL: "https://example.test"}}}
	if err := Save(path, &before); err != nil {
		t.Fatal(err)
	}
	after, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := before
	want.Version = CurrentVersion
	want.present = nil
	got := *after
	got.present = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migration lost fields\nwant %#v\ngot  %#v", want, got)
	}
	if after.UI != (UIConfig{}) {
		t.Fatalf("v1 save invented UI values: %#v", after.UI)
	}
}

func boolPtr(v bool) *bool { return &v }
