package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/embed"
)

// classifyStubEmbedder maps texts onto 3 axes by keyword so anchor/digest
// similarity is controllable: "security" → axis 0, "documentation"/"readme"
// → axis 1, everything else → axis 2.
type classifyStubEmbedder struct{}

func (classifyStubEmbedder) Dim() int { return 3 }

func (classifyStubEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	out := make([]embed.Vector, len(texts))
	for i, t := range texts {
		v := make(embed.Vector, 3)
		lower := strings.ToLower(t)
		switch {
		case strings.Contains(lower, "security") || strings.Contains(lower, "sanitiz"):
			v[0] = 1
		case strings.Contains(lower, "documentation") || strings.Contains(lower, "readme"):
			v[1] = 1
		default:
			v[2] = 1
		}
		out[i] = v
	}
	return out, nil
}

func TestClassifyForks(t *testing.T) {
	features := []embed.ForkFeatures{
		{Commits: "sanitize user input in the auth handler", Paths: "auth/handler.go"},
		{Commits: "update readme with new install steps", Paths: "README.md"},
	}
	cats, scores, err := ClassifyForks(context.Background(), classifyStubEmbedder{}, features)
	if err != nil {
		t.Fatal(err)
	}
	if cats[0] != "security" {
		t.Errorf("fork 0 category = %q, want security (score %v)", cats[0], scores[0])
	}
	if cats[1] != "docs" {
		t.Errorf("fork 1 category = %q, want docs (score %v)", cats[1], scores[1])
	}
	for i, s := range scores {
		if s < classifyMinScore {
			t.Errorf("fork %d score %v below floor yet categorized", i, s)
		}
	}
}

func TestClassifyForks_BelowFloorUnclassified(t *testing.T) {
	// The "misc" axis (2) matches no anchor that maps there exclusively —
	// every anchor lands on axis 0/1/2 per keywords; a digest on axis 2
	// can still hit an axis-2 anchor with cosine 1. To force a no-match,
	// use an embedder that returns zero vectors for digests.
	zero := zeroDigestEmbedder{}
	cats, _, err := ClassifyForks(context.Background(), zero, []embed.ForkFeatures{{Commits: "whatever"}})
	if err != nil {
		t.Fatal(err)
	}
	if cats[0] != "" {
		t.Errorf("zero-similarity digest should stay unclassified, got %q", cats[0])
	}
}

type zeroDigestEmbedder struct{}

func (zeroDigestEmbedder) Dim() int { return 3 }

func (zeroDigestEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	out := make([]embed.Vector, len(texts))
	for i := range texts {
		out[i] = make(embed.Vector, 3) // all zero → cosine 0 with everything
	}
	return out, nil
}

func TestClassifyForks_Empty(t *testing.T) {
	cats, scores, err := ClassifyForks(context.Background(), classifyStubEmbedder{}, nil)
	if err != nil || cats != nil || scores != nil {
		t.Fatalf("empty input: %v %v %v", cats, scores, err)
	}
}
