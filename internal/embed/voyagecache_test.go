package embed

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

// fakeCache is an in-memory ResponseCache that counts reads and writes and can
// be made to fail, so the "a broken cache must not break the run" contract is
// testable.
type fakeCache struct {
	values      map[string][]byte
	kinds       map[string]string
	gets        atomic.Int64
	puts        atomic.Int64
	getErr      error
	putErr      error
	writableErr error
}

func newFakeCache() *fakeCache {
	return &fakeCache{values: map[string][]byte{}, kinds: map[string]string{}}
}

func (c *fakeCache) VoyageCacheWritable(context.Context) error { return c.writableErr }

func (c *fakeCache) VoyageCacheGetMany(_ context.Context, keys []string) (map[string][]byte, error) {
	c.gets.Add(1)
	if c.getErr != nil {
		return nil, c.getErr
	}
	out := map[string][]byte{}
	for _, key := range keys {
		if value, ok := c.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (c *fakeCache) VoyageCachePutMany(_ context.Context, kind, _ string, values map[string][]byte) error {
	c.puts.Add(1)
	if c.putErr != nil {
		return c.putErr
	}
	for key, value := range values {
		c.values[key] = value
		c.kinds[key] = kind
	}
	return nil
}

// TestVoyageEmbedCacheAvoidsRepaying is the headline behavior: an identical
// second run must issue no requests at all. Voyage bills per token, so a run
// that re-embeds unchanged documents is money spent for a known answer.
func TestVoyageEmbedCacheAvoidsRepaying(t *testing.T) {
	var requests atomic.Int64
	srv := embedServer(t, false, &requests, nil)
	defer srv.Close()

	cache := newFakeCache()
	cfg := voyageTestConfig(t, srv)
	cfg.Cache = cache
	embedder, err := NewVoyageEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}

	texts := []string{"first document", "second document", "third document"}
	first, err := embedder.EmbedPassages(context.Background(), texts)
	if err != nil {
		t.Fatalf("first EmbedPassages: %v", err)
	}
	afterFirst := requests.Load()
	if afterFirst == 0 {
		t.Fatal("the first call issued no request; the test is not exercising the API path")
	}

	second, err := embedder.EmbedPassages(context.Background(), texts)
	if err != nil {
		t.Fatalf("second EmbedPassages: %v", err)
	}
	if got := requests.Load(); got != afterFirst {
		t.Errorf("second call issued %d extra request(s); want 0 — every item was already cached",
			got-afterFirst)
	}
	// Cached vectors must be byte-identical, or ranking would shift between runs.
	for i := range texts {
		for j := range first[i] {
			if first[i][j] != second[i][j] {
				t.Fatalf("vector %d differs between the fresh and cached path at component %d", i, j)
			}
		}
	}
	hits, misses, _ := embedder.CacheStats()
	if misses != int64(len(texts)) {
		t.Errorf("misses = %d, want %d (the first call pays for each text once)", misses, len(texts))
	}
	if hits != int64(len(texts)) {
		t.Errorf("hits = %d, want %d (the second call pays for none)", hits, len(texts))
	}
}

// TestVoyageEmbedCachePartialHit covers the reason caching is per item rather
// than per request body: adding one document to a known set must cost one item,
// not the whole batch.
func TestVoyageEmbedCachePartialHit(t *testing.T) {
	var batches [][]string
	srv := embedServer(t, false, nil, &batches)
	defer srv.Close()

	cache := newFakeCache()
	cfg := voyageTestConfig(t, srv)
	cfg.Cache = cache
	embedder, err := NewVoyageEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}

	if _, err := embedder.EmbedPassages(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatalf("first EmbedPassages: %v", err)
	}
	if _, err := embedder.EmbedPassages(context.Background(), []string{"a", "b", "c"}); err != nil {
		t.Fatalf("second EmbedPassages: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("server saw %d batches, want 2", len(batches))
	}
	if got := batches[1]; len(got) != 1 || got[0] != "c" {
		t.Errorf("second request sent %v, want only the one uncached document [c]", got)
	}
}

// TestVoyageEmbedDedupsWithinBatch matters concretely here: spoon tracks forks
// that carry identical work, and identical work produces an identical digest, so
// duplicates inside one batch are expected rather than hypothetical.
func TestVoyageEmbedDedupsWithinBatch(t *testing.T) {
	var batches [][]string
	srv := embedServer(t, false, nil, &batches)
	defer srv.Close()

	// No cache at all: dedup must work on its own.
	embedder, err := NewVoyageEmbedder(voyageTestConfig(t, srv))
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}
	texts := []string{"same work", "different", "same work", "same work"}
	vectors, err := embedder.EmbedPassages(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedPassages: %v", err)
	}
	if len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("server saw %v, want a single request carrying 2 unique texts", batches)
	}
	if _, _, deduped := embedder.CacheStats(); deduped != 2 {
		t.Errorf("deduped = %d, want 2", deduped)
	}
	// Duplicate positions must still each get a correct vector.
	for _, i := range []int{0, 2, 3} {
		if vectors[i][0] != vectors[0][0] || vectors[i][1] != vectors[0][1] {
			t.Errorf("vector %d does not match the vector for its duplicate text", i)
		}
	}
	// And they must not alias one backing array: in-place normalization over
	// aliased rows would scale the same data once per duplicate.
	if &vectors[0][0] == &vectors[2][0] {
		t.Error("duplicate positions share one backing array; each needs its own copy")
	}
}

// TestVoyageCacheKeysSeparateIncompatibleRequests guards against the cache
// serving a vector produced a different way. Dimension and input type both
// change the vector, so both must change the key.
func TestVoyageCacheKeysSeparateIncompatibleRequests(t *testing.T) {
	base := voyageEmbedCacheKey("voyage-code-3", voyageInputTypeDocument, 1024, "text")
	cases := map[string]string{
		"different model":      voyageEmbedCacheKey("voyage-4", voyageInputTypeDocument, 1024, "text"),
		"different input type": voyageEmbedCacheKey("voyage-code-3", voyageInputTypeQuery, 1024, "text"),
		"different dimension":  voyageEmbedCacheKey("voyage-code-3", voyageInputTypeDocument, 512, "text"),
		"different text":       voyageEmbedCacheKey("voyage-code-3", voyageInputTypeDocument, 1024, "other"),
	}
	for name, key := range cases {
		if key == base {
			t.Errorf("%s produced the same cache key as the base request", name)
		}
	}
	// Rerank keys must separate on the query as well as the document: the same
	// document scores differently against a different query.
	rerankBase := voyageRerankCacheKey("rerank-2.5", "query", "doc")
	if voyageRerankCacheKey("rerank-2.5", "other query", "doc") == rerankBase {
		t.Error("changing the query did not change the rerank cache key")
	}
	if voyageRerankCacheKey("rerank-2.5", "query", "other doc") == rerankBase {
		t.Error("changing the document did not change the rerank cache key")
	}
	// Embed and rerank keys live in one table; their namespaces must not collide.
	if voyageEmbedCacheKey("m", "a", 1024, "b") == voyageRerankCacheKey("m", "a", "b") {
		t.Error("embed and rerank keys collide")
	}
}

// TestHashFieldsIsUnambiguous checks the length-prefixing: without it,
// ("ab","c") and ("a","bc") would hash alike, letting one input's response be
// served for another.
func TestHashFieldsIsUnambiguous(t *testing.T) {
	if hashFields("ab", "c") == hashFields("a", "bc") {
		t.Error("field boundaries are not encoded; concatenation-equal inputs collide")
	}
}

func TestVoyageRerankCacheAvoidsRepaying(t *testing.T) {
	var requests atomic.Int64
	srv := rerankServer(t, &requests, nil)
	defer srv.Close()

	cache := newFakeCache()
	cfg := voyageTestConfig(t, srv)
	cfg.Cache = cache
	reranker, err := NewVoyageReranker(cfg)
	if err != nil {
		t.Fatalf("NewVoyageReranker: %v", err)
	}

	docs := []string{"one doc", "another doc"}
	first, err := reranker.Rerank(context.Background(), "the query", docs)
	if err != nil {
		t.Fatalf("first Rerank: %v", err)
	}
	afterFirst := requests.Load()
	second, err := reranker.Rerank(context.Background(), "the query", docs)
	if err != nil {
		t.Fatalf("second Rerank: %v", err)
	}
	if got := requests.Load(); got != afterFirst {
		t.Errorf("second Rerank issued %d extra request(s), want 0", got-afterFirst)
	}
	for i := range docs {
		if first[i] != second[i] {
			t.Errorf("score %d changed between the fresh (%v) and cached (%v) path", i, first[i], second[i])
		}
	}
	// A different query must not be served from the first query's entries.
	if _, err := reranker.Rerank(context.Background(), "a different query", docs); err != nil {
		t.Fatalf("third Rerank: %v", err)
	}
	if got := requests.Load(); got == afterFirst {
		t.Error("a new query was served from cache; rerank keys must include the query")
	}
	if kind := cache.kinds[voyageRerankCacheKey(VoyageDefaultRerankModel, "the query", docs[0])]; kind != voyageCacheKindRerank {
		t.Errorf("stored kind = %q, want %q", kind, voyageCacheKindRerank)
	}
}

// TestVoyageToleratesBrokenCache encodes the rule that a cache added to save
// money must never be able to fail a run it was added to optimize.
func TestVoyageToleratesBrokenCache(t *testing.T) {
	srv := embedServer(t, false, nil, nil)
	defer srv.Close()

	cache := newFakeCache()
	cache.getErr = errors.New("cache read exploded")
	cache.putErr = errors.New("cache write exploded")
	cfg := voyageTestConfig(t, srv)
	cfg.Cache = cache
	embedder, err := NewVoyageEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}
	vectors, err := embedder.EmbedPassages(context.Background(), []string{"a doc"})
	if err != nil {
		t.Fatalf("EmbedPassages with a failing cache: %v", err)
	}
	if len(vectors) != 1 {
		t.Fatalf("got %d vectors, want 1", len(vectors))
	}
}

// TestVoyageIgnoresCorruptCacheEntry: a wrong-width blob can only come from a
// corrupted row, and re-fetching is always safe, so it must read as a miss
// rather than as an error or a wrong-length vector.
func TestVoyageIgnoresCorruptCacheEntry(t *testing.T) {
	var requests atomic.Int64
	srv := embedServer(t, false, &requests, nil)
	defer srv.Close()

	cache := newFakeCache()
	cfg := voyageTestConfig(t, srv)
	cfg.Cache = cache
	embedder, err := NewVoyageEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}
	key := voyageEmbedCacheKey(VoyageDefaultEmbedModel, voyageInputTypeDocument, VoyageDefaultDimension, "a doc")
	cache.values[key] = []byte{1, 2, 3} // not dim*4 bytes

	vectors, err := embedder.EmbedPassages(context.Background(), []string{"a doc"})
	if err != nil {
		t.Fatalf("EmbedPassages over a corrupt entry: %v", err)
	}
	if len(vectors[0]) != VoyageDefaultDimension {
		t.Errorf("vector width = %d, want %d", len(vectors[0]), VoyageDefaultDimension)
	}
	if requests.Load() == 0 {
		t.Error("the corrupt entry was used instead of re-fetching")
	}
}

func TestVoyageNoCacheEnvDisablesCaching(t *testing.T) {
	t.Setenv(VoyageNoCacheEnv, "1")
	cfg := VoyageConfig{APIKey: "k", Cache: newFakeCache()}
	resolved, err := cfg.withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	if resolved.Cache != nil {
		t.Errorf("%s=1 did not clear the cache", VoyageNoCacheEnv)
	}
}

// TestVoyageRequiresDurableStorage encodes the second precondition: a key alone
// is not enough. The store holds both the response cache and the vectors Voyage
// is paid to produce, so with nowhere durable to write, each run buys results it
// throws away and buys them again next time — worse than not using the provider.
func TestVoyageRequiresDurableStorage(t *testing.T) {
	t.Setenv(VoyageAPIKeyEnv, testVoyageKey)
	t.Setenv(VoyageAPIKeyEnvAlt, "")
	t.Setenv(VoyageDisableEnv, "")
	t.Setenv(VoyageNoCacheEnv, "")

	t.Run("no cache at all", func(t *testing.T) {
		_, active, err := ResolveVoyageConfig(context.Background(), config.VoyageConfig{}, false, nil)
		if active {
			t.Error("Voyage activated with no durable store")
		}
		if err == nil || !strings.Contains(err.Error(), "no durable store") {
			t.Errorf("err = %v, want it to explain that there is nowhere durable to write", err)
		}
	})

	t.Run("unwritable cache", func(t *testing.T) {
		cache := newFakeCache()
		cache.writableErr = errors.New("attempt to write a readonly database")
		_, active, err := ResolveVoyageConfig(context.Background(), config.VoyageConfig{}, false, cache)
		if active {
			t.Error("Voyage activated against an unwritable store")
		}
		if err == nil || !strings.Contains(err.Error(), "readonly database") {
			t.Errorf("err = %v, want it to carry the store's own failure", err)
		}
	})

	t.Run("writable cache activates", func(t *testing.T) {
		_, active, err := ResolveVoyageConfig(context.Background(), config.VoyageConfig{}, false, newFakeCache())
		if err != nil || !active {
			t.Fatalf("ResolveVoyageConfig = (active %v, err %v), want active with no error", active, err)
		}
	})

	t.Run("no key stays silent", func(t *testing.T) {
		// No key is the ordinary zero-configuration case, not a misconfiguration:
		// it must not produce an error the caller has to explain away.
		t.Setenv(VoyageAPIKeyEnv, "")
		_, active, err := ResolveVoyageConfig(context.Background(), config.VoyageConfig{}, false, newFakeCache())
		if active || err != nil {
			t.Errorf("ResolveVoyageConfig with no key = (active %v, err %v), want (false, nil)", active, err)
		}
	})
}

// TestVoyageConfigIsCarriedIntoTheEmbedder checks the cache actually reaches the
// provider: a resolver that validated the store but dropped it would silently
// re-pay for everything.
func TestVoyageConfigIsCarriedIntoTheEmbedder(t *testing.T) {
	t.Setenv(VoyageAPIKeyEnv, testVoyageKey)
	t.Setenv(VoyageNoCacheEnv, "")
	cache := newFakeCache()
	cfg, active, err := ResolveVoyageConfig(context.Background(), config.VoyageConfig{}, false, cache)
	if err != nil || !active {
		t.Fatalf("ResolveVoyageConfig: active=%v err=%v", active, err)
	}
	if cfg.Cache != ResponseCache(cache) {
		t.Error("the resolved config does not carry the cache")
	}
}

func TestVoyageRejectsEmptyText(t *testing.T) {
	srv := embedServer(t, false, nil, nil)
	defer srv.Close()
	embedder, err := NewVoyageEmbedder(voyageTestConfig(t, srv))
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}
	// Rejected before any request: Voyage errors on empty input, which would cost
	// the whole batch for one degenerate document.
	_, err = embedder.EmbedPassages(context.Background(), []string{"fine", "   "})
	if err == nil || !strings.Contains(err.Error(), "text 1 is empty") {
		t.Errorf("error = %v, want it to name the empty text's position", err)
	}
}
