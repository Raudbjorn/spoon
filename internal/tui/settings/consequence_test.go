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
		case "embedder.voyage.autoIndex":
			// Turning this on is the single most expensive toggle in the panel:
			// it commits every fork of every list to a per-token billed service
			// with no further prompt, so it must be gated like the rest.
			value = "true"
		case "embedder.model":
			value = "fast-bge-base-en-v1.5"
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

func TestRequiresConfirmationForFastEmbedModel(t *testing.T) {
	field := FieldByMust("embedder.model")
	before := &config.Config{}
	same, err := Candidate(field, before, "fast-bge-small-en-v1.5")
	if err != nil {
		t.Fatal(err)
	}
	if RequiresConfirmationFor(field, before, same) {
		t.Fatal("same-model reselection confirmed")
	}
	other, err := Candidate(field, before, "fast-bge-base-en-v1.5")
	if err != nil {
		t.Fatal(err)
	}
	if !RequiresConfirmationFor(field, before, other) {
		t.Fatal("model change did not confirm")
	}
}
