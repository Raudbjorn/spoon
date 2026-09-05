package setupcheck

import (
	"reflect"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestPrepareFastEmbedUsesSetupDefaults(t *testing.T) {
	cfg := &config.Config{}
	got, err := PrepareFastEmbed(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedder.Backend != "fastembed" || got.MaxLength != 512 || got.BatchSize != 32 || got.Model != "fast-bge-small-en-v1.5" {
		t.Fatalf("unexpected setup config: %#v %#v", cfg.Embedder, got)
	}
}

func TestPrepareFastEmbedDoesNotMutateConfigWhenDefaultsFail(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	cfg := &config.Config{Embedder: config.EmbedderConfig{
		Backend: "custom", Model: "custom-model", MaxLength: 7, BatchSize: 9,
		Voyage: config.VoyageConfig{EmbedModel: "keep-me"},
	}}
	before := *cfg

	if _, err := PrepareFastEmbed(cfg, ""); err == nil {
		t.Fatal("PrepareFastEmbed error = nil, want cache-directory discovery failure")
	}
	if !reflect.DeepEqual(*cfg, before) {
		t.Fatalf("config mutated on failure:\n got: %#v\nwant: %#v", *cfg, before)
	}
}

func TestPrepareFastEmbedKeepsSelectedProfile(t *testing.T) {
	cfg := &config.Config{Embedder: config.EmbedderConfig{Model: "fast-bge-base-en-v1.5"}}
	got, err := PrepareFastEmbed(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "fast-bge-base-en-v1.5" || cfg.Embedder.Model != "fast-bge-base-en-v1.5" {
		t.Fatalf("model rewritten: got=%#v cfg=%#v", got, cfg.Embedder)
	}
}
