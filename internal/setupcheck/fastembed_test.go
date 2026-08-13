package setupcheck

import (
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
