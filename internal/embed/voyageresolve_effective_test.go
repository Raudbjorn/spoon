package embed

import (
	"context"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestResolveVoyageEffectiveUsesSharedDisabledSemantics(t *testing.T) {
	file := &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: false}}}
	config.RecordFieldValue(file, "embedder.voyage.disabled", "false")
	effective := config.ResolveEffectiveConfig(file, nil, map[string]string{"SPOON_NO_VOYAGE": "0"})
	if effective.Voyage.Disabled.Value != "false" || effective.Voyage.Disabled.Source != config.SourceFile {
		t.Fatalf("SPOON_NO_VOYAGE=0 = %#v", effective.Voyage.Disabled)
	}
	active, err := VoyageKeyConfiguredEffective(effective, false, map[string]string{"VOYAGE_AI_API_KEY": "test-key"})
	if err != nil || !active {
		t.Fatalf("effective key state = active:%v err:%v", active, err)
	}
	disabled := config.ResolveEffectiveConfig(file, nil, map[string]string{"SPOON_NO_VOYAGE": "1"})
	cfg, active, err := ResolveVoyageEffective(context.Background(), disabled, false, evalNoopCache{}, map[string]string{"SPOON_NO_VOYAGE": "1"})
	if err != nil || active || cfg.APIKey != "" {
		t.Fatalf("SPOON_NO_VOYAGE=1 = cfg:%#v active:%v err:%v", cfg, active, err)
	}
}
