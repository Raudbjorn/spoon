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
