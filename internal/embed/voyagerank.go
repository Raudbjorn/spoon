package embed

// voyagerank.go implements QueryScorer against Voyage's /rerank endpoint. It
// fills the seam LexicalQueryScorer has stood in for: a cross-encoder scores the
// (query, document) pair jointly rather than comparing two independently
// produced embeddings, which is why it is worth a network round trip on top of
// vector retrieval.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
)

const (
	// VoyageRerankMethod is the queryMethod / rerank label for Voyage-scored
	// results.
	VoyageRerankMethod = "voyage"

	// voyageRerankMaxDocs caps documents per rerank request. The documented
	// ceiling is 1000, but the binding limit is the token budget below; 128
	// keeps a failed request cheap to retry.
	voyageRerankMaxDocs = 128

	// voyageRerankMaxChars approximates rerank-2.5's 600K-token request budget
	// at a conservative ~2 characters per token. The budget is charged as
	// "query tokens × documents + sum of document tokens", so each document is
	// billed the query's length as well — see the cost callback in Rerank.
	voyageRerankMaxChars = 1_200_000
)

// VoyageReranker scores documents against a query with Voyage's cross-encoder.
// Safe for concurrent use.
type VoyageReranker struct {
	client voyageClient
	tokens atomic.Int64
	stats  CacheStats
}

func NewVoyageReranker(cfg VoyageConfig) (*VoyageReranker, error) {
	resolved, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &VoyageReranker{client: voyageClient{cfg: resolved}}, nil
}

// Method implements QueryScorer.
func (r *VoyageReranker) Method() string { return VoyageRerankMethod }

// ModelID names the reranker model, for the rerankModel output field.
func (r *VoyageReranker) ModelID() string { return "voyage:" + r.client.cfg.RerankModel }

// TokensUsed reports the tokens Voyage has billed this reranker so far.
func (r *VoyageReranker) TokensUsed() int64 { return r.tokens.Load() }

// CacheStats reports how many (query, document) pairs were served from the cache
// or deduplicated rather than billed.
func (r *VoyageReranker) CacheStats() (hits, misses, deduped int64) { return r.stats.Snapshot() }

// Rerank implements QueryScorer: one score in [0,1] per input document, in
// input order. Callers depend on both properties — forksops.scoreQuery rejects a
// mismatched count and assigns scores positionally.
func (r *VoyageReranker) Rerank(ctx context.Context, query string, docs []string) ([]float64, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("voyage rerank: query is empty")
	}
	model := r.client.cfg.RerankModel
	out := make([]float64, len(docs))

	// Group positions by (query, document) cache key. Forks carrying identical
	// work produce identical digests — spoon tracks exactly that — so duplicates
	// are scored once and fanned back out.
	positions := make(map[string][]int, len(docs))
	unique := make([]string, 0, len(docs))
	for i, doc := range docs {
		key := voyageRerankCacheKey(model, query, doc)
		if _, seen := positions[key]; !seen {
			unique = append(unique, key)
		}
		positions[key] = append(positions[key], i)
	}
	r.stats.Deduped.Add(int64(len(docs) - len(unique)))

	cached := voyageCacheLookup(ctx, r.client.cfg.Cache, unique)
	missKeys := make([]string, 0, len(unique))
	for _, key := range unique {
		if score, ok := decodeFloat64(cached[key]); ok {
			for _, i := range positions[key] {
				out[i] = score
			}
			r.stats.Hits.Add(1)
			continue
		}
		missKeys = append(missKeys, key)
	}
	r.stats.Misses.Add(int64(len(missKeys)))
	if len(missKeys) == 0 {
		return out, nil
	}

	missDocs := make([]string, len(missKeys))
	for j, key := range missKeys {
		missDocs[j] = docs[positions[key][0]]
	}
	// Each document is charged its own length plus the query's, mirroring how
	// Voyage sizes the request.
	cost := func(i int) int { return len(missDocs[i]) + len(query) }
	for _, bound := range voyageBatchBounds(len(missDocs), voyageRerankMaxDocs, voyageRerankMaxChars, cost) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Cross-encoder scores are computed per (query, document) pair
		// independently of the other documents in the request, so scores from
		// separate batches — and from the cache — remain directly comparable.
		scores, err := r.rerankOnce(ctx, query, missDocs[bound[0]:bound[1]])
		if err != nil {
			return nil, err
		}
		fresh := make(map[string][]byte, len(scores))
		for offset, score := range scores {
			key := missKeys[bound[0]+offset]
			for _, i := range positions[key] {
				out[i] = score
			}
			fresh[key] = encodeFloat64(score)
		}
		// Persist per batch so a later failure does not discard paid-for scores.
		voyageCacheStore(ctx, r.client.cfg.Cache, voyageCacheKindRerank, model, fresh)
	}
	return out, nil
}

type voyageRerankRequest struct {
	Query      string   `json:"query"`
	Documents  []string `json:"documents"`
	Model      string   `json:"model"`
	Truncation bool     `json:"truncation"`
}

type voyageRerankResponse struct {
	Data []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
	Model string `json:"model"`
	Usage struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (r *VoyageReranker) rerankOnce(ctx context.Context, query string, docs []string) ([]float64, error) {
	// top_k is deliberately omitted: every document needs a score, because the
	// caller maps scores back onto its own result list positionally.
	req := voyageRerankRequest{Query: query, Documents: docs, Model: r.client.cfg.RerankModel, Truncation: true}
	var resp voyageRerankResponse
	if err := r.client.post(ctx, "/rerank", "rerank", req, &resp); err != nil {
		return nil, err
	}
	r.tokens.Add(resp.Usage.TotalTokens)
	if len(resp.Data) != len(docs) {
		return nil, fmt.Errorf("voyage rerank: got %d results for %d documents", len(resp.Data), len(docs))
	}
	// Results arrive sorted by score, so the index field — not array order — is
	// what maps a score back to its document.
	scores := make([]float64, len(docs))
	seen := make([]bool, len(docs))
	for _, result := range resp.Data {
		if result.Index < 0 || result.Index >= len(docs) {
			return nil, fmt.Errorf("voyage rerank: result index %d out of range for %d documents", result.Index, len(docs))
		}
		if seen[result.Index] {
			return nil, fmt.Errorf("voyage rerank: duplicate result index %d", result.Index)
		}
		seen[result.Index] = true
		scores[result.Index] = clampScore(result.RelevanceScore)
	}
	return scores, nil
}

// clampScore holds relevance scores inside the [0,1] range that QueryScore is
// documented to carry and that LexicalQueryScorer also guarantees. A non-finite
// score becomes 0 rather than poisoning the sort.
func clampScore(score float64) float64 {
	switch {
	case math.IsNaN(score):
		return 0
	case score < 0:
		return 0
	case score > 1:
		return 1
	default:
		return score
	}
}
