package settings

import (
	"reflect"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestCandidateFailureLeavesLiveConfigUntouched(t *testing.T) {
	cfg := &config.Config{GitHub: config.GitHubConfig{Tokens: []string{"first", "second"}}}
	before, err := Clone(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(FieldByMust("github.requestsPerMinute"), cfg, "10000"); err == nil {
		t.Fatal("invalid mutation accepted")
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Fatalf("failed mutation changed live config: got %#v want %#v", cfg, before)
	}
}

func TestTokenListPreservesOrderAndClears(t *testing.T) {
	cfg := &config.Config{}
	field := FieldByMust("github.tokens")
	if err := Apply(field, cfg, " first\n\nsecond \n"); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.GitHub.Tokens, []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens=%q want=%q", got, want)
	}
	if err := Apply(field, cfg, "\n"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.GitHub.Tokens) != 0 {
		t.Fatalf("cleared tokens=%q", cfg.GitHub.Tokens)
	}
}

func TestBillingConfirmationOnlyForActivation(t *testing.T) {
	field := FieldByMust("embedder.voyage.disabled")
	on := &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: true}}}
	off := &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: false}}}
	if !RequiresConfirmationFor(field, on, off) {
		t.Fatal("enabling Voyage bypassed confirmation")
	}
	if RequiresConfirmationFor(field, off, on) {
		t.Fatal("disabling Voyage should not require billing confirmation")
	}
}
