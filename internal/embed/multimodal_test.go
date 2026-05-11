package embed

import (
	"context"
	"math"
	"strings"
	"testing"
)

type stubEmbedder struct {
	dim   int
	calls int
	seen  [][]string
}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([]Vector, error) {
	s.calls++
	s.seen = append(s.seen, append([]string(nil), texts...))
	out := make([]Vector, len(texts))
	for i, txt := range texts {
		v := make(Vector, s.dim)
		// Deterministic but text-dependent: first slot = len(text), rest = constants.
		v[0] = float32(len(txt))
		for j := 1; j < s.dim; j++ {
			v[j] = float32(j)
		}
		out[i] = v
	}
	return out, nil
}

func (s *stubEmbedder) Dim() int { return s.dim }

func TestMultiModalEmbed_BlendsAllModalities(t *testing.T) {
	st := &stubEmbedder{dim: 4}
	fs := []ForkFeatures{{
		Paths:     "p",
		Commits:   "c",
		ReadmeDoc: "r",
		DiffChunk: "d",
	}}
	got, err := MultiModalEmbed(context.Background(), st, "nomic-embed-text", fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 vec, got %d", len(got))
	}
	// Result is dim * 4 modalities = 16 floats.
	if len(got[0]) != 16 {
		t.Fatalf("want len 16, got %d", len(got[0]))
	}
	// Result must be L2-normalized.
	var sum float64
	for _, x := range got[0] {
		sum += float64(x) * float64(x)
	}
	if math.Abs(sum-1.0) > 1e-5 {
		t.Errorf("not L2-normalized: |v|^2 = %v", sum)
	}
	// Each of the 4 modality blocks should be non-zero.
	for j := 0; j < 4; j++ {
		block := got[0][j*4 : (j+1)*4]
		anyNonZero := false
		for _, x := range block {
			if x != 0 {
				anyNonZero = true
				break
			}
		}
		if !anyNonZero {
			t.Errorf("block %d should be non-zero", j)
		}
	}
}

func TestMultiModalEmbed_MissingModalitiesZeroBlock(t *testing.T) {
	st := &stubEmbedder{dim: 3}
	fs := []ForkFeatures{{Paths: "p", DiffChunk: "d"}} // commits + readme missing
	got, err := MultiModalEmbed(context.Background(), st, "nomic-embed-text", fs)
	if err != nil {
		t.Fatal(err)
	}
	// dim=3 × 4 modalities = 12.
	if len(got[0]) != 12 {
		t.Fatalf("len = %d", len(got[0]))
	}
	// commits block (j=1) and readme block (j=2) should be zero.
	for _, j := range []int{1, 2} {
		block := got[0][j*3 : (j+1)*3]
		for _, x := range block {
			if x != 0 {
				t.Errorf("modality %d block should be zero, got %v", j, block)
				break
			}
		}
	}
	// paths block (j=0) should be non-zero.
	for _, x := range got[0][0:3] {
		if x != 0 {
			return
		}
	}
	t.Error("paths block should be non-zero")
}

func TestMultiModalEmbed_AllEmpty(t *testing.T) {
	st := &stubEmbedder{dim: 4}
	fs := []ForkFeatures{{}}
	got, err := MultiModalEmbed(context.Background(), st, "nomic-embed-text", fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0]) != 16 {
		t.Fatalf("got %d vecs, first len = %d", len(got), len(got[0]))
	}
	for _, x := range got[0] {
		if x != 0 {
			t.Fatalf("all-empty input should produce zero vector, got %v", got[0])
		}
	}
}

func TestMultiModalEmbed_AllEmptyAndDimZero(t *testing.T) {
	// Regression: when the embedder has not yet observed any embeddings
	// (Dim() == 0) and all four modality blobs are empty, the function must
	// fall back to dim=1 and return a zero vector of length 4 (= 1 * 4
	// modalities). It must not panic and must not change shape.
	st := &stubEmbedder{dim: 0}
	fs := []ForkFeatures{{}}
	got, err := MultiModalEmbed(context.Background(), st, "nomic-embed-text", fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 vector, got %d", len(got))
	}
	if len(got[0]) != 4 {
		t.Fatalf("want len 4 (dim=1 fallback x 4 modalities), got %d", len(got[0]))
	}
	for _, x := range got[0] {
		if x != 0 {
			t.Fatalf("want zero vector, got %v", got[0])
		}
	}
	if st.calls != 0 {
		t.Errorf("no prompts means no Embed calls; got %d", st.calls)
	}
}

func TestMultiModalEmbed_CodeAwareSingleCall(t *testing.T) {
	st := &stubEmbedder{dim: 4}
	fs := []ForkFeatures{
		{Paths: "p1", Commits: "c1", ReadmeDoc: "r1", DiffChunk: "d1"},
		{Paths: "p2", Commits: "c2", ReadmeDoc: "r2", DiffChunk: "d2"},
	}
	got, err := MultiModalEmbed(context.Background(), st, "jina-embeddings-v2-base-code", fs)
	if err != nil {
		t.Fatal(err)
	}
	if st.calls != 1 {
		t.Errorf("CodeAware should make exactly 1 batch call, got %d", st.calls)
	}
	if len(st.seen) != 1 || len(st.seen[0]) != 2 {
		t.Errorf("expected 1 batch with 2 prompts, got %v", st.seen)
	}
	// Prompts should contain the structural tags.
	for _, p := range st.seen[0] {
		for _, tag := range []string{"<paths>", "</paths>", "<commits>", "</commits>", "<readme>", "</readme>", "<diff>", "</diff>"} {
			if !strings.Contains(p, tag) {
				t.Errorf("prompt missing %q: %s", tag, p)
			}
		}
	}
	if len(got) != 2 || len(got[0]) != 4 {
		t.Errorf("unexpected output shape: %d vectors, first len %d", len(got), len(got[0]))
	}
	// CodeAware path returns L2-normalized vectors of dim (not 4*dim).
	for _, v := range got {
		var s float64
		for _, x := range v {
			s += float64(x) * float64(x)
		}
		if math.Abs(s-1.0) > 1e-5 {
			t.Errorf("CodeAware vector not L2-normalized: %v", s)
		}
	}
}

func TestMultiModalEmbed_CodeAwareDetectsTaggedModel(t *testing.T) {
	st := &stubEmbedder{dim: 4}
	fs := []ForkFeatures{{Paths: "p"}}
	_, err := MultiModalEmbed(context.Background(), st, "jina-embeddings-v2-base-code:latest", fs)
	if err != nil {
		t.Fatal(err)
	}
	if st.calls != 1 {
		t.Errorf("CodeAware (tagged) should make 1 call, got %d", st.calls)
	}
}

func TestMultiModalEmbed_NilEmbedder(t *testing.T) {
	_, err := MultiModalEmbed(context.Background(), nil, "nomic", []ForkFeatures{{}})
	if err == nil {
		t.Fatal("want error for nil embedder")
	}
}

func TestMultiModalEmbed_EmptyInput(t *testing.T) {
	st := &stubEmbedder{dim: 4}
	got, err := MultiModalEmbed(context.Background(), st, "nomic", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("want nil, got %v", got)
	}
	if st.calls != 0 {
		t.Errorf("want 0 calls, got %d", st.calls)
	}
}

