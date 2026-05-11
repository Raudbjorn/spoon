package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/embed"
)

func TestHeuristicLabel_Empty(t *testing.T) {
	got := HeuristicLabel(nil, nil)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	got = HeuristicLabel([]embed.ForkFeatures{}, []embed.ForkFeatures{})
	if got != "" {
		t.Fatalf("expected empty for zero-length slices, got %q", got)
	}
}

func TestHeuristicLabel_OnlyDirPrefix(t *testing.T) {
	members := []embed.ForkFeatures{
		{Paths: "internal/auth/oauth.go\ninternal/auth/scope.go", Commits: "the and for with"},
		{Paths: "internal/db/conn.go", Commits: "fix update add"},
	}
	got := HeuristicLabel(members, nil)
	if got != "internal/" {
		t.Fatalf("expected %q, got %q", "internal/", got)
	}
}

func TestHeuristicLabel_OnlyTokens(t *testing.T) {
	members := []embed.ForkFeatures{
		{Commits: "oauth provider scope token"},
		{Commits: "oauth provider scope token"},
	}
	got := HeuristicLabel(members, nil)
	want := "oauth, provider, scope"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestHeuristicLabel_BothParts(t *testing.T) {
	members := []embed.ForkFeatures{
		{
			Paths:   "internal/auth/oauth.go\ninternal/auth/scope.go",
			Commits: "implement oauth scope provider",
		},
		{
			Paths:   "internal/auth/token.go",
			Commits: "tighten oauth scope provider validation",
		},
	}
	corpus := []embed.ForkFeatures{
		{Commits: "unrelated database migration"},
		{Commits: "another telemetry rework"},
	}
	got := HeuristicLabel(members, corpus)
	if !strings.HasPrefix(got, "internal/") {
		t.Fatalf("expected dir prefix internal/, got %q", got)
	}
	if !strings.Contains(got, labelSeparator) {
		t.Fatalf("expected separator %q in %q", labelSeparator, got)
	}
	for _, want := range []string{"oauth", "scope", "provider"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected token %q in %q", want, got)
		}
	}
}

func TestHeuristicLabel_DirTieLexBreak(t *testing.T) {
	members := []embed.ForkFeatures{
		{Paths: "alpha/x.go\nalpha/y.go\nbeta/x.go\nbeta/y.go"},
	}
	got := HeuristicLabel(members, nil)
	if got != "alpha/" {
		t.Fatalf("expected alpha/, got %q", got)
	}
}

func TestHeuristicLabel_StopwordsFiltered(t *testing.T) {
	members := []embed.ForkFeatures{
		{Commits: "the and for with fix add use"},
		{Commits: "merge branch main master update"},
	}
	got := HeuristicLabel(members, nil)
	if got != "" {
		t.Fatalf("expected empty label (all stopwords/short), got %q", got)
	}
}

func TestHeuristicLabel_TfIdfRanking(t *testing.T) {
	// "alpha" is the rare token: appears in members but not in the corpus.
	// "betagamma" and "deltakappa" appear in every corpus document.
	members := []embed.ForkFeatures{
		{Commits: "alpha betagamma deltakappa"},
		{Commits: "alpha betagamma deltakappa"},
	}
	corpus := []embed.ForkFeatures{
		{Commits: "betagamma deltakappa elsewhere"},
		{Commits: "betagamma deltakappa elsewhere"},
		{Commits: "betagamma deltakappa elsewhere"},
		{Commits: "betagamma deltakappa elsewhere"},
	}
	got := HeuristicLabel(members, corpus)
	parts := strings.Split(got, ", ")
	if len(parts) == 0 || parts[0] != "alpha" {
		t.Fatalf("expected alpha to rank first, got %q", got)
	}
}

func TestHeuristicLabel_TokenTieLexBreak(t *testing.T) {
	// Two tokens, identical frequency in members, identical (zero) corpus
	// presence → identical tf-idf scores.
	members := []embed.ForkFeatures{
		{Commits: "zebra alpha"},
		{Commits: "zebra alpha"},
	}
	got := HeuristicLabel(members, nil)
	if got != "alpha, zebra" {
		t.Fatalf("expected lex-ordered tokens, got %q", got)
	}
}

func TestHeuristicLabel_Determinism(t *testing.T) {
	members := []embed.ForkFeatures{
		{
			Paths:   "internal/auth/oauth.go\ninternal/auth/scope.go",
			Commits: "oauth scope provider token",
		},
		{
			Paths:   "internal/auth/token.go",
			Commits: "oauth scope provider validation",
		},
	}
	corpus := []embed.ForkFeatures{
		{Commits: "completely unrelated topic"},
	}
	a := HeuristicLabel(members, corpus)
	b := HeuristicLabel(members, corpus)
	if a != b {
		t.Fatalf("non-deterministic: %q vs %q", a, b)
	}
}

func TestHeuristicLabel_FewerThanThreeTokens(t *testing.T) {
	members := []embed.ForkFeatures{
		{Commits: "oauth the and for"},
		{Commits: "oauth merge branch main"},
	}
	got := HeuristicLabel(members, nil)
	if got != "oauth" {
		t.Fatalf("expected just %q, got %q", "oauth", got)
	}
}

// fakeLabeler verifies that the Labeler interface is satisfiable.
type fakeLabeler struct{}

func (fakeLabeler) Polish(_ context.Context, lc LabelerContext) (string, error) {
	return lc.Heuristic, nil
}

func TestLabelerInterface_Exists(t *testing.T) {
	var l Labeler = fakeLabeler{}
	got, err := l.Polish(context.Background(), LabelerContext{Heuristic: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "x" {
		t.Fatalf("expected pass-through %q, got %q", "x", got)
	}
}
