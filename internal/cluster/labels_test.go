package cluster

import (
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

func TestHeuristicLabel_NoCommitTokens_FallsBackToPaths(t *testing.T) {
	// All commit words are stopwords; the label must still carry signal:
	// the dominant (deep) dir prefix plus discriminative path segments.
	members := []embed.ForkFeatures{
		{Paths: "internal/auth/oauth.go\ninternal/auth/scope.go", Commits: "the and for with"},
		{Paths: "internal/db/conn.go", Commits: "fix update add"},
	}
	got := HeuristicLabel(members, nil)
	if !strings.HasPrefix(got, "internal/auth/") {
		t.Fatalf("expected deep dir prefix internal/auth/, got %q", got)
	}
	if !strings.Contains(got, "oauth") {
		t.Fatalf("expected path-token fallback to surface %q, got %q", "oauth", got)
	}
}

func TestDirPrefix_PrefersDeepPrefixWhenDominant(t *testing.T) {
	members := []embed.ForkFeatures{
		{Paths: "internal/auth/oauth.go\ninternal/auth/token.go"},
		{Paths: "internal/auth/scope.go\ninternal/cli/main.go"},
	}
	if got := dirPrefix(members); got != "internal/auth/" {
		t.Fatalf("expected internal/auth/, got %q", got)
	}
}

func TestDirPrefix_StaysShallowWhenScattered(t *testing.T) {
	// Depth-2 prefixes each occur once → below the dominance threshold.
	members := []embed.ForkFeatures{
		{Paths: "internal/auth/oauth.go\ninternal/db/conn.go"},
		{Paths: "internal/cli/main.go\ninternal/tui/app.go"},
	}
	if got := dirPrefix(members); got != "internal/" {
		t.Fatalf("expected internal/, got %q", got)
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
	if !strings.HasPrefix(got, "alpha/") {
		t.Fatalf("expected lexicographic tie-break to alpha/, got %q", got)
	}
}

func TestTopTokens_BigramOutranksAndDedupes(t *testing.T) {
	// "rate limit" co-occurs in every member; the corpus has the unigrams
	// scattered but never the phrase. The bigram should rank, and its
	// constituent unigrams must not repeat alongside it.
	members := []embed.ForkFeatures{
		{Commits: "implement rate limit middleware"},
		{Commits: "tune rate limit defaults"},
	}
	corpus := []embed.ForkFeatures{
		{Commits: "limit memory usage during scans"},
		{Commits: "first rate of telemetry flush"},
	}
	got := topTokens(members, corpus, 3)
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "rate limit") {
		t.Fatalf("expected bigram \"rate limit\" in %v", got)
	}
	count := 0
	for _, tok := range got {
		if strings.Contains(tok, "rate") || strings.Contains(tok, "limit") {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("constituents of a picked bigram must be deduped: %v", got)
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
