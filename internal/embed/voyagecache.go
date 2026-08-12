package embed

// voyagecache.go makes paid Voyage calls idempotent across runs. Voyage bills per
// token and the answer for a given (model, input) pair is stable, so a request
// that has been paid for once should never be paid for again.
//
// Caching is **per item**, not per request body. A per-body cache only helps when
// a later run happens to compose an identical batch; per-item keys hit whenever
// the same text is embedded again in any batch, so adding one new fork to a
// hundred-fork run costs one item instead of a hundred. It also collapses
// duplicates *within* a batch, which is not hypothetical here — spoon explicitly
// tracks forks that carry identical work, and identical work yields an identical
// digest.
//
// Two layers, both keyed the same way:
//
//	1. in-batch dedup — identical texts in one call are sent once.
//	2. the persistent store cache — survives across invocations.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"strconv"
	"sync/atomic"
)

// Cache kinds, recorded on each stored row so entries are attributable.
const (
	voyageCacheKindEmbed  = "embed"
	voyageCacheKindRerank = "rerank"
)

// ResponseCache is the persistence the Voyage provider needs. *store.Store
// satisfies it directly, which is why the methods carry a Voyage-specific name
// and only primitive types: internal/embed must not import internal/store, and a
// named entry struct would force an adapter with no other purpose.
//
// A cache that errors *during* a run is treated as a cache miss, never as a
// request failure — the point of the cache is to save money, and it must not be
// able to break a run it was added to optimize. A cache that cannot be written to
// *at all* is different: see VoyageCacheWritable.
type ResponseCache interface {
	VoyageCacheGetMany(ctx context.Context, keys []string) (map[string][]byte, error)
	VoyageCachePutMany(ctx context.Context, kind, model string, values map[string][]byte) error

	// VoyageCacheWritable reports whether durable writes actually succeed, by
	// performing one. It is a precondition for enabling Voyage at all, not an
	// optimization check: the same store holds the vectors Voyage is paid to
	// produce, so with nowhere durable to write, every request is money spent for
	// a result that is discarded when the process exits — and spent again on the
	// next run. An unwritable store means Voyage stays off.
	VoyageCacheWritable(ctx context.Context) error
}

// voyageEmbedCacheKey derives the cache key for one text. Every input that
// changes the returned vector is folded in: a key that omitted the dimension or
// the input type would serve a 512-dim document vector for a 1024-dim query.
// Fields are length-prefixed so no combination of values can collide by
// concatenation.
func voyageEmbedCacheKey(model, inputType string, dim int, text string) string {
	return hashFields(voyageCacheKindEmbed, model, inputType, strconv.Itoa(dim), text)
}

// voyageRerankCacheKey derives the cache key for one (query, document) pair.
// Cross-encoder scores are computed per pair, independently of the other
// documents in the request, so a pair is the correct cache granularity.
func voyageRerankCacheKey(model, query, doc string) string {
	return hashFields(voyageCacheKindRerank, model, query, doc)
}

// hashFields hashes length-prefixed fields, so ("ab","c") and ("a","bc") cannot
// produce the same key.
func hashFields(fields ...string) string {
	h := sha256.New()
	var lengthBuf [8]byte
	for _, field := range fields {
		binary.LittleEndian.PutUint64(lengthBuf[:], uint64(len(field)))
		_, _ = h.Write(lengthBuf[:])
		_, _ = h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// encodeFloat32s and decodeFloat32s serialize a vector for the cache in the same
// little-endian float32 layout the embeddings table uses. Duplicated rather than
// shared with internal/semantic because semantic imports this package; a shared
// helper would need a third package for two loops.
func encodeFloat32s(values []float32) []byte {
	blob := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(value))
	}
	return blob
}

// decodeFloat32s returns nil when the blob is not exactly dim float32s. A
// wrong-width entry is treated as a miss rather than an error: it can only come
// from a corrupted row, and re-fetching is always safe.
func decodeFloat32s(blob []byte, dim int) []float32 {
	if dim <= 0 || len(blob) != dim*4 {
		return nil
	}
	values := make([]float32, dim)
	for i := range dim {
		values[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return values
}

func encodeFloat64(value float64) []byte {
	var blob [8]byte
	binary.LittleEndian.PutUint64(blob[:], math.Float64bits(value))
	return blob[:]
}

func decodeFloat64(blob []byte) (float64, bool) {
	if len(blob) != 8 {
		return 0, false
	}
	value := math.Float64frombits(binary.LittleEndian.Uint64(blob))
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// CacheStats reports how much of a run was served without paying for it. Counts
// are items, not requests: Hits came from the persistent cache, Deduped were
// satisfied by an identical item in the same call, and only Misses were billed.
type CacheStats struct {
	Hits, Misses, Deduped atomic.Int64
}

// Snapshot returns the current counts as plain integers, safe to format.
func (s *CacheStats) Snapshot() (hits, misses, deduped int64) {
	return s.Hits.Load(), s.Misses.Load(), s.Deduped.Load()
}

// voyageCacheLookup resolves a batch of cache keys, tolerating a broken cache.
func voyageCacheLookup(ctx context.Context, cache ResponseCache, keys []string) map[string][]byte {
	if cache == nil || len(keys) == 0 {
		return nil
	}
	found, err := cache.VoyageCacheGetMany(ctx, keys)
	if err != nil {
		// A cache read failure must not fail the run: fall through and pay.
		return nil
	}
	return found
}

// voyageCacheStore writes new entries, tolerating a broken cache.
func voyageCacheStore(ctx context.Context, cache ResponseCache, kind, model string, values map[string][]byte) {
	if cache == nil || len(values) == 0 {
		return
	}
	_ = cache.VoyageCachePutMany(ctx, kind, model, values)
}
