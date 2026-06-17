package cluster

import (
	"context"
	"testing"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
)

// TestSearchSiblings_EmptyEmbedder covers the no-embedder path:
// when the embedder is nil, SearchSiblings returns (0, 0, nil) without
// touching the searcher.
func TestSearchSiblings_EmptyEmbedder(t *testing.T) {
	sim, n, err := SearchSiblings(context.Background(), nil, forge.ParentData{}, nil, nil, 50)
	if err != nil {
		t.Errorf("err: got %v, want nil", err)
	}
	if sim != 0 || n != 0 {
		t.Errorf("got (%v, %d), want (0, 0)", sim, n)
	}
}

// TestSearchSiblings_DefaultSearcher covers the default behavior:
// the placeholder SiblingSearcher always returns (0, 0, nil) so the
// cluster pipeline degrades to "no signal" without surfacing a
// warning.
func TestSearchSiblings_DefaultSearcher(t *testing.T) {
	sim, n, err := SearchSiblings(context.Background(), nil, forge.ParentData{}, nil, nil, 50)
	if err != nil {
		t.Errorf("err: got %v, want nil", err)
	}
	if sim != 0 || n != 0 {
		t.Errorf("default: got (%v, %d), want (0, 0)", sim, n)
	}
}

// fakeSiblingSearcher is a test-only SiblingSearcher that returns a
// fixed sim and candidate count.
type fakeSiblingSearcher struct {
	sim float64
	n   int
}

func (f fakeSiblingSearcher) SearchSiblings(_ context.Context, _ forge.ParentData, _ embed.Embedder, _ ReadmeFetcher, _ int) (float64, int, error) {
	return f.sim, f.n, nil
}

// TestSearchSiblings_FakeSearcher covers the dispatch path: a
// non-default SiblingSearcher is called and its result is propagated.
func TestSearchSiblings_FakeSearcher(t *testing.T) {
	searcher := fakeSiblingSearcher{sim: 0.42, n: 50}
	sim, n, err := SearchSiblings(context.Background(), searcher, forge.ParentData{}, &dummyEmbedder{}, &dummyReadmeFetcher{}, 50)
	if err != nil {
		t.Fatalf("err: got %v, want nil", err)
	}
	if sim != 0.42 || n != 50 {
		t.Errorf("got (%v, %d), want (0.42, 50)", sim, n)
	}
}

// dummyEmbedder satisfies embed.Embedder with a deterministic output.
type dummyEmbedder struct{}

func (d *dummyEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	out := make([]embed.Vector, len(texts))
	for i := range texts {
		v := make(embed.Vector, 4)
		for j := range v {
			v[j] = float32(i + 1)
		}
		out[i] = v
	}
	return out, nil
}
func (d *dummyEmbedder) Dim() int { return 4 }

// dummyReadmeFetcher satisfies ReadmeFetcher by returning a non-empty
// README for any owner/repo.
type dummyReadmeFetcher struct{}

func (d *dummyReadmeFetcher) FetchReadme(_ context.Context, _, _ string) (string, error) {
	return "# README\n\nSome text.\n", nil
}
