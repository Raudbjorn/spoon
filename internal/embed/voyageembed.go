package embed

// voyageembed.go implements SearchEmbedder against Voyage's /embeddings
// endpoint. It sits alongside FastEmbedEmbedder rather than replacing it: the
// store keys embeddings by (document_id, model), so both models' vectors
// coexist for the same document and each is re-embedded independently.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
)

const (
	// voyageInputTypeQuery and voyageInputTypeDocument select Voyage's
	// retrieval prompts. Queries and documents are embedded asymmetrically, the
	// same split fastembed makes with BGE's query instruction — which is why
	// input_type is part of the model identity below.
	voyageInputTypeQuery    = "query"
	voyageInputTypeDocument = "document"

	// voyageMaxInputs caps texts per request. The documented ceiling is 1000;
	// staying well under it keeps a single failed request cheap to retry and
	// keeps one request comfortably inside voyageTimeout.
	voyageMaxInputs = 128

	// voyageMaxBatchChars approximates voyage-code-3's 120K-token per-request
	// budget at a deliberately conservative ~2 characters per token, so a batch
	// of dense code never trips the limit. Truncation is left on, so a single
	// over-length text is the server's problem, not ours.
	voyageMaxBatchChars = 240_000
)

// VoyageEmbedder is a SearchEmbedder backed by Voyage's embedding API.
//
// Unlike FastEmbedEmbedder it holds no mutex: the Embedder contract requires
// Embed be safe for concurrent use, and a stateless HTTP client already is.
// fastembed serializes only because ONNX sessions must be.
type VoyageEmbedder struct {
	client voyageClient
	tokens atomic.Int64
	stats  CacheStats
}

func NewVoyageEmbedder(cfg VoyageConfig) (*VoyageEmbedder, error) {
	resolved, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &VoyageEmbedder{client: voyageClient{cfg: resolved}}, nil
}

// Dim reports the configured output width.
func (e *VoyageEmbedder) Dim() int { return e.client.cfg.OutputDimension }

// ModelID is the semantic identity of a stored vector. As with
// FastEmbedModelID, anything that changes the vector must change this string,
// or vectors produced two different ways get compared against each other under
// one model key. Both the output dimension and the query/document prompt scheme
// change the vector, so both appear here.
//
// Changing the dimension therefore re-partitions the index: the old rows stay
// in the embeddings table under the old ID, unreferenced, and every document
// becomes pending under the new one.
func (e *VoyageEmbedder) ModelID() string {
	return VoyageModelID(e.client.cfg.EmbedModel, e.client.cfg.OutputDimension)
}

// VoyageModelID composes the same identity from configuration alone, for
// callers that need to know which partition of the index Voyage writes to
// without paying to construct an embedder -- reporting per-fork coverage, for
// one. It shares an implementation with ModelID so the displayed partition and
// the written one cannot drift apart.
func VoyageModelID(embedModel string, dimension int) string {
	return fmt.Sprintf("voyage:%s:dim=%d:input_type=qd", embedModel, dimension)
}

// RerankModelID names the reranker configured alongside this embedder. Emitted
// as `rerankModel` so a consumer can tell which cross-encoder scored a result.
func (e *VoyageEmbedder) RerankModelID() string { return "voyage:" + e.client.cfg.RerankModel }

// TokensUsed reports the total tokens Voyage has billed this embedder so far,
// summed from each response's usage field. Reported in the indexing warning so
// the spend is visible in the output stream rather than only on the invoice.
func (e *VoyageEmbedder) TokensUsed() int64 { return e.tokens.Load() }

// CacheStats reports how many items were served from the cache or deduplicated
// rather than billed.
func (e *VoyageEmbedder) CacheStats() (hits, misses, deduped int64) { return e.stats.Snapshot() }

// Embed embeds texts as documents, matching FastEmbedEmbedder.Embed.
func (e *VoyageEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	return e.EmbedPassages(ctx, texts)
}

func (e *VoyageEmbedder) EmbedPassages(ctx context.Context, texts []string) ([]Vector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return []Vector{}, nil
	}
	raw, err := e.embedBatched(ctx, texts, voyageInputTypeDocument)
	if err != nil {
		return nil, fmt.Errorf("embed passages: %w", err)
	}
	return e.validate(raw)
}

func (e *VoyageEmbedder) EmbedQuery(ctx context.Context, text string) (Vector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := e.embedBatched(ctx, []string{text}, voyageInputTypeQuery)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	vectors, err := e.validate(raw)
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embed query: got %d vectors, want 1", len(vectors))
	}
	return vectors[0], nil
}

// Close satisfies the close-func shape the backend selector expects. The HTTP
// client owns no resources worth tearing down per embedder.
func (e *VoyageEmbedder) Close() error { return nil }

type voyageEmbedRequest struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type"`
	OutputDimension int      `json:"output_dimension"`
	Truncation      bool     `json:"truncation"`
}

type voyageEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"usage"`
}

// embedBatched returns one vector per input text, paying for as few of them as
// possible. Identical texts within the call collapse to one item, items already
// in the cache are not requested at all, and only the remainder is batched to
// the API.
func (e *VoyageEmbedder) embedBatched(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	for i, text := range texts {
		if strings.TrimSpace(text) == "" {
			// Voyage rejects empty input, which would cost the whole batch.
			// Callers already filter empties (an indexed document always
			// carries at least owner/name; scoreQuery skips empty digests), so
			// this is a contract violation worth naming rather than papering
			// over with a placeholder vector that could then match queries.
			return nil, fmt.Errorf("text %d is empty", i)
		}
	}
	dim := e.Dim()
	model := e.client.cfg.EmbedModel
	out := make([][]float32, len(texts))

	// Group input positions by cache key. Two identical texts share a key, so
	// duplicates are requested once and fanned back out to every position.
	positions := make(map[string][]int, len(texts))
	unique := make([]string, 0, len(texts))
	for i, text := range texts {
		key := voyageEmbedCacheKey(model, inputType, dim, text)
		if _, seen := positions[key]; !seen {
			unique = append(unique, key)
		}
		positions[key] = append(positions[key], i)
	}
	e.stats.Deduped.Add(int64(len(texts) - len(unique)))

	cached := voyageCacheLookup(ctx, e.client.cfg.Cache, unique)
	missKeys := make([]string, 0, len(unique))
	for _, key := range unique {
		if values := decodeFloat32s(cached[key], dim); values != nil {
			fanOut(out, positions[key], values)
			e.stats.Hits.Add(1)
			continue
		}
		missKeys = append(missKeys, key)
	}
	e.stats.Misses.Add(int64(len(missKeys)))
	if len(missKeys) == 0 {
		return out, nil
	}

	missTexts := make([]string, len(missKeys))
	for j, key := range missKeys {
		missTexts[j] = texts[positions[key][0]]
	}
	cost := func(i int) int { return len(missTexts[i]) }
	for _, bound := range voyageBatchBounds(len(missTexts), voyageMaxInputs, voyageMaxBatchChars, cost) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vectors, err := e.embedOnce(ctx, missTexts[bound[0]:bound[1]], inputType)
		if err != nil {
			return nil, err
		}
		fresh := make(map[string][]byte, len(vectors))
		for offset, values := range vectors {
			key := missKeys[bound[0]+offset]
			fanOut(out, positions[key], values)
			fresh[key] = encodeFloat32s(values)
		}
		// Persist per batch, not once at the end: a later batch failing must not
		// discard vectors already paid for.
		voyageCacheStore(ctx, e.client.cfg.Cache, voyageCacheKindEmbed, model, fresh)
	}
	return out, nil
}

// fanOut assigns values to every position, giving each its own copy. Sharing one
// slice across positions would let the in-place L2 normalization in validate run
// over the same backing array once per duplicate.
func fanOut(out [][]float32, indices []int, values []float32) {
	for n, i := range indices {
		if n == 0 {
			out[i] = values
			continue
		}
		clone := make([]float32, len(values))
		copy(clone, values)
		out[i] = clone
	}
}

func (e *VoyageEmbedder) embedOnce(ctx context.Context, batch []string, inputType string) ([][]float32, error) {
	req := voyageEmbedRequest{
		Input: batch, Model: e.client.cfg.EmbedModel, InputType: inputType,
		OutputDimension: e.client.cfg.OutputDimension, Truncation: true,
	}
	var resp voyageEmbedResponse
	if err := e.client.post(ctx, "/embeddings", "embeddings", req, &resp); err != nil {
		return nil, err
	}
	e.tokens.Add(resp.Usage.TotalTokens)
	return scatterByIndex(len(batch), len(resp.Data), func(i int) (int, []float32) {
		return resp.Data[i].Index, resp.Data[i].Embedding
	})
}

// scatterByIndex places n API results back into request order using each
// result's own index field, rather than trusting array order. Duplicate,
// missing and out-of-range indices are all errors: silently mis-ordering
// vectors would attribute one fork's embedding to another, which no downstream
// check could detect.
func scatterByIndex(want, got int, at func(i int) (int, []float32)) ([][]float32, error) {
	if got != want {
		return nil, fmt.Errorf("got %d results for %d inputs", got, want)
	}
	out := make([][]float32, want)
	for i := range got {
		index, values := at(i)
		if index < 0 || index >= want {
			return nil, fmt.Errorf("result index %d out of range for %d inputs", index, want)
		}
		if out[index] != nil {
			return nil, fmt.Errorf("duplicate result index %d", index)
		}
		if values == nil {
			// A nil slot would be indistinguishable from "not yet filled" on
			// the duplicate check above, so reject it here.
			return nil, fmt.Errorf("result index %d has no embedding", index)
		}
		out[index] = values
	}
	return out, nil
}

// voyageBatchBounds returns [start,end) index pairs covering n items, such that
// no batch exceeds maxCount items or maxCost total cost, where cost(i) is item
// i's contribution. A single item costlier than maxCost still gets its own
// batch — the API truncates it rather than us dropping it.
//
// cost is a callback rather than a []string length so the reranker can charge
// each document the query's cost as well, which is exactly how Voyage computes
// a rerank request's size ("query tokens × documents + sum of document tokens").
//
// Split out as a pure function because the property that matters (no batch ever
// exceeds either cap) is otherwise only observable as a 4xx from the live API.
func voyageBatchBounds(n, maxCount, maxCost int, cost func(i int) int) [][2]int {
	if n <= 0 {
		return nil
	}
	if maxCount < 1 {
		maxCount = 1
	}
	bounds := make([][2]int, 0, 1+n/maxCount)
	start, spent := 0, 0
	for i := range n {
		c := cost(i)
		// Close the current batch before adding item i if it would overflow
		// either cap — but never emit an empty batch.
		if i > start && (i-start >= maxCount || spent+c > maxCost) {
			bounds = append(bounds, [2]int{start, i})
			start, spent = i, 0
		}
		spent += c
	}
	return append(bounds, [2]int{start, n})
}

// validate rejects wrong widths, non-finite values and zero-norm vectors, then
// L2-normalizes. Voyage returns unit-length vectors already, so normalization is
// defensive — Cosine does not assume unit length either, but the stored blobs
// should be consistent with fastembed's.
//
// Every check runs on the raw API response, before normalization. That ordering
// is deliberate: it makes each error describe what Voyage actually returned
// rather than a derived value, and it keeps the zero-norm check independent of
// whether L2NormalizeAll happens to leave zero vectors alone (it does — see
// l2Normalize — but relying on that reads as a divide-by-zero bug, and did to
// two reviewers).
func (e *VoyageEmbedder) validate(raw [][]float32) ([]Vector, error) {
	dim := e.Dim()
	vectors := make([]Vector, len(raw))
	for i, row := range raw {
		if len(row) != dim {
			return nil, fmt.Errorf("voyage vector %d has dimension %d, want %d", i, len(row), dim)
		}
		var norm float64
		for _, value := range row {
			v := float64(value)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("voyage vector %d contains non-finite value", i)
			}
			norm += v * v
		}
		if norm == 0 {
			return nil, fmt.Errorf("voyage vector %d has zero norm", i)
		}
		vectors[i] = Vector(row)
	}
	L2NormalizeAll(vectors)
	return vectors, nil
}
