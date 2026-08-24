package setupcheck

import (
	"github.com/svnbjrn/spoon/internal/config"
	"testing"
)

func TestValidateFastEmbedRejectsRuntimeUnsupportedValues(t *testing.T) {
	for _, cfg := range []config.EmbedderConfig{{Model: "not-a-model"}, {MaxLength: 999}, {BatchSize: -1}} {
		if err := ValidateFastEmbed(cfg); err == nil {
			t.Fatalf("invalid FastEmbed config accepted: %#v", cfg)
		}
	}
}

func TestValidateFastEmbedAcceptsBaseEnglishProfile(t *testing.T) {
	if err := ValidateFastEmbed(config.EmbedderConfig{Model: "fast-bge-base-en-v1.5"}); err != nil {
		t.Fatalf("supported model rejected: %v", err)
	}
}
