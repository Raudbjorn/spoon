package embed

import (
	"context"
	"testing"
)

func cosine(a, b Vector) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot // inputs are L2-normalized
}

func TestLocalEmbedderDim(t *testing.T) {
	var e LocalEmbedder
	if e.Dim() != localDim {
		t.Fatalf("Dim() = %d, want %d", e.Dim(), localDim)
	}
	vecs, err := e.Embed(context.Background(), []string{"hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 1 || len(vecs[0]) != localDim {
		t.Fatalf("got %d vecs, dim %d; want 1 vec of dim %d", len(vecs), len(vecs[0]), localDim)
	}
}

func TestLocalEmbedderDeterministic(t *testing.T) {
	var e LocalEmbedder
	texts := []string{"internal/cluster/pipeline.go\ninternal/embed/local.go", "fix rate limit handling"}
	a, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				t.Fatalf("vector %d differs at component %d: %v vs %v", i, j, a[i][j], b[i][j])
			}
		}
	}
}

func TestLocalEmbedderEmptyText(t *testing.T) {
	var e LocalEmbedder
	vecs, err := e.Embed(context.Background(), []string{"", "something"})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range vecs[0] {
		if x != 0 {
			t.Fatal("empty text should produce a zero vector")
		}
	}
}

func TestLocalEmbedderNormalized(t *testing.T) {
	var e LocalEmbedder
	vecs, err := e.Embed(context.Background(), []string{"add oauth login support to the auth module"})
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, x := range vecs[0] {
		sum += float64(x) * float64(x)
	}
	if sum < 0.999 || sum > 1.001 {
		t.Fatalf("vector norm² = %v, want ~1", sum)
	}
}

func TestLocalEmbedderSimilarityOrdering(t *testing.T) {
	var e LocalEmbedder
	texts := []string{
		"add oauth2 token refresh to auth middleware\nsupport oauth login providers",
		"oauth token refresh fixes for the auth middleware",
		"bump dockerfile base image and ci pipeline cache settings",
	}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	same := cosine(vecs[0], vecs[1])
	diff := cosine(vecs[0], vecs[2])
	if same <= diff {
		t.Fatalf("same-topic cosine (%v) should exceed different-topic cosine (%v)", same, diff)
	}
}

func TestLocalEmbedderPathSimilarity(t *testing.T) {
	var e LocalEmbedder
	texts := []string{
		"internal/auth/oauth.go\ninternal/auth/token.go\ninternal/auth/middleware.go",
		"internal/auth/oauth.go\ninternal/auth/session.go",
		"docs/README.md\n.github/workflows/ci.yml",
	}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := cosine(vecs[0], vecs[1]), cosine(vecs[0], vecs[2]); same <= diff {
		t.Fatalf("overlapping-path cosine (%v) should exceed disjoint-path cosine (%v)", same, diff)
	}
}

func TestLocalEmbedderCancelledContext(t *testing.T) {
	var e LocalEmbedder
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Embed(ctx, []string{"x"}); err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestSplitAlnum(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"internal/cluster/pipeline.go", []string{"internal", "cluster", "pipeline", "go"}},
		{"fix #1234 and l42", []string{"fix", "and", "l42"}},
		{"a b cd", []string{"cd"}},
		{"", nil},
	}
	for _, c := range cases {
		got := splitAlnum(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitAlnum(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitAlnum(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}
