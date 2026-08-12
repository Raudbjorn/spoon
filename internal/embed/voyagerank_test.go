package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// rerankScoreScale keeps the synthetic length-derived scores inside [0,1] for
// every document length these tests use, so a clamp never masks a mapping bug.
const rerankScoreScale = 1000

// rerankServer answers /rerank by scoring each document on its length, returning
// results in score-descending order (as the real API does) with correct index
// fields. That ordering is precisely what a client trusting array order gets
// wrong.
func rerankServer(t *testing.T, requests *atomic.Int64, seen *[][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			requests.Add(1)
		}
		if r.URL.Path != "/rerank" {
			t.Errorf("path = %q, want /rerank", r.URL.Path)
		}
		var req voyageRerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if seen != nil {
			*seen = append(*seen, req.Documents)
		}
		type result struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		}
		results := make([]result, len(req.Documents))
		for i, doc := range req.Documents {
			results[i] = result{Index: i, RelevanceScore: float64(len(doc)) / rerankScoreScale}
		}
		// Descending by score, like the real endpoint.
		for i := range results {
			for j := i + 1; j < len(results); j++ {
				if results[j].RelevanceScore > results[i].RelevanceScore {
					results[i], results[j] = results[j], results[i]
				}
			}
		}
		writeJSON(t, w, map[string]any{"results": results, "model": req.Model,
			"usage": map[string]any{"total_tokens": 11}})
	}))
}

func newTestReranker(t *testing.T, srv *httptest.Server) *VoyageReranker {
	t.Helper()
	reranker, err := NewVoyageReranker(voyageTestConfig(t, srv))
	if err != nil {
		t.Fatalf("NewVoyageReranker: %v", err)
	}
	return reranker
}

func TestVoyageRerankMethodLabel(t *testing.T) {
	// The label has to come from the scorer: a hardcoded one at the call site
	// outlived the implementation it named once already.
	var scorer QueryScorer = &VoyageReranker{}
	if got := scorer.Method(); got != VoyageRerankMethod {
		t.Errorf("Method() = %q, want %q", got, VoyageRerankMethod)
	}
	if got := (LexicalQueryScorer{}).Method(); got != LexicalQueryMethod {
		t.Errorf("lexical Method() = %q, want %q", got, LexicalQueryMethod)
	}
}

// TestVoyageRerankPreservesInputOrder is the contract forksops.scoreQuery
// depends on: scores are assigned positionally, so a score returned against the
// wrong document silently mislabels a fork's relevance.
func TestVoyageRerankPreservesInputOrder(t *testing.T) {
	srv := rerankServer(t, nil, nil)
	defer srv.Close()
	reranker := newTestReranker(t, srv)

	docs := []string{"short", "a much longer document here", "mid length doc"}
	scores, err := reranker.Rerank(context.Background(), "query", docs)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != len(docs) {
		t.Fatalf("got %d scores, want %d", len(scores), len(docs))
	}
	for i, doc := range docs {
		want := float64(len(doc)) / rerankScoreScale
		if diff := scores[i] - want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("scores[%d] = %v, want %v (score was matched to the wrong document)", i, scores[i], want)
		}
	}
}

func TestVoyageRerankClampsToUnitRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"results": []map[string]any{
			{"index": 0, "relevance_score": 1.4},
			{"index": 1, "relevance_score": -0.3},
		}})
	}))
	defer srv.Close()
	reranker := newTestReranker(t, srv)

	scores, err := reranker.Rerank(context.Background(), "q", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	// QueryScore is documented as [0,1] and the lexical scorer clamps; a provider
	// that drifts outside the range must not break that contract.
	if scores[0] != 1 {
		t.Errorf("scores[0] = %v, want clamped to 1", scores[0])
	}
	if scores[1] != 0 {
		t.Errorf("scores[1] = %v, want clamped to 0", scores[1])
	}
}

func TestVoyageRerankRejectsBadResults(t *testing.T) {
	cases := []struct {
		name    string
		results []map[string]any
		want    string
	}{
		{name: "duplicate index", results: []map[string]any{
			{"index": 0, "relevance_score": 0.5}, {"index": 0, "relevance_score": 0.4},
		}, want: "duplicate result index"},
		{name: "out of range", results: []map[string]any{
			{"index": 0, "relevance_score": 0.5}, {"index": 5, "relevance_score": 0.4},
		}, want: "out of range"},
		{name: "short result set", results: []map[string]any{
			{"index": 0, "relevance_score": 0.5},
		}, want: "got 1 results for 2 documents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{"results": tc.results})
			}))
			defer srv.Close()
			reranker := newTestReranker(t, srv)
			_, err := reranker.Rerank(context.Background(), "q", []string{"a", "b"})
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestVoyageRerankChunksAcrossRequests checks that splitting a document list
// across requests still maps every score to its own document. Cross-encoder
// scores are per (query, document) pair, so chunking is safe — but only if the
// offset arithmetic is right.
func TestVoyageRerankChunksAcrossRequests(t *testing.T) {
	var requests atomic.Int64
	var batches [][]string
	srv := rerankServer(t, &requests, &batches)
	defer srv.Close()

	reranker := newTestReranker(t, srv)
	// Distinct lengths so each score identifies its document, and enough
	// documents to force more than one request.
	docs := make([]string, voyageRerankMaxDocs+5)
	for i := range docs {
		docs[i] = strings.Repeat("x", i+1)
	}
	scores, err := reranker.Rerank(context.Background(), "q", docs)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (the document count exceeds one batch)", got)
	}
	for _, batch := range batches {
		if len(batch) > voyageRerankMaxDocs {
			t.Errorf("a batch carried %d documents, over the %d cap", len(batch), voyageRerankMaxDocs)
		}
	}
	for i, doc := range docs {
		want := float64(len(doc)) / rerankScoreScale
		if diff := scores[i] - want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("scores[%d] = %v, want %v — chunk offsets are wrong", i, scores[i], want)
		}
	}
}

func TestVoyageRerankEmptyInputs(t *testing.T) {
	srv := rerankServer(t, nil, nil)
	defer srv.Close()
	reranker := newTestReranker(t, srv)

	scores, err := reranker.Rerank(context.Background(), "q", nil)
	if err != nil || scores != nil {
		t.Errorf("Rerank(nil docs) = (%v, %v), want (nil, nil)", scores, err)
	}
	if _, err := reranker.Rerank(context.Background(), "   ", []string{"a"}); err == nil {
		t.Error("expected an error for a blank query, got nil")
	}
}
