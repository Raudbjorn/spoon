package settings

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestEveryConsequenceTaggedFieldGatesItsActualTransition(t *testing.T) {
	for _, field := range Registry {
		if field.Consequence == NoConsequence {
			continue
		}
		before := &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: true, OutputDimension: 1024}}}
		value := ""
		switch field.Key {
		case "embedder.voyage.disabled":
			value = "false"
		case "embedder.voyage.apiKeyFile":
			value = "/tmp/key"
		case "embedder.voyage.outputDimension":
			value = "512"
		default:
			t.Fatalf("uncovered consequence field %q", field.Key)
		}
		after, err := Candidate(field, before, value)
		if err != nil {
			t.Fatalf("%s candidate: %v", field.Key, err)
		}
		if !RequiresConfirmationFor(field, before, after) {
			t.Fatalf("%s did not require confirmation", field.Key)
		}
		if message := ConsequenceMessage(field); message == "" || !strings.Contains(message, map[Consequence]string{Billing: "billed", Reindex: "partitioning", Hostwide: "every user"}[field.Consequence]) {
			t.Fatalf("%s has non-specific message %q", field.Key, message)
		}
	}
}
