package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

func openCacheTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestVoyageCacheRoundTrip(t *testing.T) {
	db := openCacheTestStore(t)
	ctx := context.Background()

	values := map[string][]byte{"key-a": []byte("alpha"), "key-b": []byte("beta")}
	if err := db.VoyageCachePutMany(ctx, "embed", "voyage-code-3", values); err != nil {
		t.Fatalf("VoyageCachePutMany: %v", err)
	}
	got, err := db.VoyageCacheGetMany(ctx, []string{"key-a", "key-b", "key-absent"})
	if err != nil {
		t.Fatalf("VoyageCacheGetMany: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (an absent key must simply be missing)", len(got))
	}
	if string(got["key-a"]) != "alpha" || string(got["key-b"]) != "beta" {
		t.Errorf("round trip returned %v", got)
	}

	// Re-putting the same key must overwrite rather than fail: the same request
	// can legitimately be paid for twice (two processes racing, a bypassed cache).
	if err := db.VoyageCachePutMany(ctx, "embed", "voyage-code-3", map[string][]byte{"key-a": []byte("updated")}); err != nil {
		t.Fatalf("second VoyageCachePutMany: %v", err)
	}
	got, err = db.VoyageCacheGetMany(ctx, []string{"key-a"})
	if err != nil {
		t.Fatalf("VoyageCacheGetMany after overwrite: %v", err)
	}
	if string(got["key-a"]) != "updated" {
		t.Errorf("key-a = %q, want the overwritten value", got["key-a"])
	}
}

// TestVoyageCacheChunksLargeKeySets guards the bound-parameter ceiling: a lookup
// that exceeded it would error, and the caller would silently re-pay for every
// item in the batch.
func TestVoyageCacheChunksLargeKeySets(t *testing.T) {
	db := openCacheTestStore(t)
	ctx := context.Background()

	const count = voyageCacheKeyChunk*2 + 7
	values := make(map[string][]byte, count)
	keys := make([]string, 0, count)
	for i := range count {
		key := "key-" + strconv.Itoa(i)
		keys = append(keys, key)
		values[key] = []byte{byte(i)}
	}
	if err := db.VoyageCachePutMany(ctx, "rerank", "rerank-2.5", values); err != nil {
		t.Fatalf("VoyageCachePutMany: %v", err)
	}
	got, err := db.VoyageCacheGetMany(ctx, keys)
	if err != nil {
		t.Fatalf("VoyageCacheGetMany over %d keys: %v", count, err)
	}
	if len(got) != count {
		t.Errorf("got %d of %d entries back", len(got), count)
	}
}

func TestVoyageCacheEmptyInputs(t *testing.T) {
	db := openCacheTestStore(t)
	ctx := context.Background()
	if err := db.VoyageCachePutMany(ctx, "embed", "m", nil); err != nil {
		t.Errorf("VoyageCachePutMany(nil) = %v, want nil", err)
	}
	got, err := db.VoyageCacheGetMany(ctx, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("VoyageCacheGetMany(nil) = (%v, %v), want (empty, nil)", got, err)
	}
}

// TestVoyageCacheWritableSucceedsOnAnOpenStore and its nil counterpart cover the
// precondition Voyage is gated on: it is enabled only where results can actually
// be kept.
func TestVoyageCacheWritableSucceedsOnAnOpenStore(t *testing.T) {
	db := openCacheTestStore(t)
	if err := db.VoyageCacheWritable(context.Background()); err != nil {
		t.Errorf("VoyageCacheWritable on an open store = %v, want nil", err)
	}
}

func TestVoyageCacheWritableRejectsNoStore(t *testing.T) {
	var db *Store
	if err := db.VoyageCacheWritable(context.Background()); err == nil {
		t.Error("VoyageCacheWritable on a nil store returned nil; Voyage would be enabled with nowhere to write")
	}
	// The read and write paths, by contrast, treat a nil store as "no cache" so a
	// missing cache degrades to cache misses rather than panicking mid-run.
	if got, err := db.VoyageCacheGetMany(context.Background(), []string{"k"}); err != nil || len(got) != 0 {
		t.Errorf("VoyageCacheGetMany on a nil store = (%v, %v), want (empty, nil)", got, err)
	}
	if err := db.VoyageCachePutMany(context.Background(), "embed", "m", map[string][]byte{"k": {1}}); err != nil {
		t.Errorf("VoyageCachePutMany on a nil store = %v, want nil", err)
	}
}

// TestVoyageCacheStoreSatisfiesTheProviderInterface pins the structural coupling:
// internal/embed declares the cache it needs and *Store implements it without an
// adapter, which only holds while the method names and signatures match exactly.
func TestVoyageCacheStoreSatisfiesTheProviderInterface(t *testing.T) {
	var cache interface {
		VoyageCacheGetMany(ctx context.Context, keys []string) (map[string][]byte, error)
		VoyageCachePutMany(ctx context.Context, kind, model string, values map[string][]byte) error
		VoyageCacheWritable(ctx context.Context) error
	} = openCacheTestStore(t)
	_ = cache
}
