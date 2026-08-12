package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/store"
)

// stubSearchEmbedder stands in for fastembed: a SearchEmbedder with its own model
// identity that needs no ONNX Runtime, so the dual-indexing loop can be exercised
// on any host.
type stubSearchEmbedder struct {
	modelID string
	dim     int
	failOn  error
}

func (s *stubSearchEmbedder) Dim() int        { return s.dim }
func (s *stubSearchEmbedder) ModelID() string { return s.modelID }

func (s *stubSearchEmbedder) Embed(ctx context.Context, texts []string) ([]embed.Vector, error) {
	return s.EmbedPassages(ctx, texts)
}

func (s *stubSearchEmbedder) EmbedPassages(_ context.Context, texts []string) ([]embed.Vector, error) {
	if s.failOn != nil {
		return nil, s.failOn
	}
	out := make([]embed.Vector, len(texts))
	for i, text := range texts {
		v := make(embed.Vector, s.dim)
		v[int(text[0])%s.dim] = 1
		out[i] = v
	}
	return out, nil
}

func (s *stubSearchEmbedder) EmbedQuery(ctx context.Context, text string) (embed.Vector, error) {
	vectors, err := s.EmbedPassages(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

// seedPendingDocuments writes fork documents with no embeddings, so every model
// sees them as pending.
func seedPendingDocuments(t *testing.T, db *store.Store, ids ...string) {
	t.Helper()
	now := time.Now().UTC()
	for _, id := range ids {
		repo := store.RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now}
		fork := store.ForkRecord{ForgeID: id, Owner: "o", Name: id, URL: "https://example/" + id, UpdatedAt: now}
		repoKey := store.RepoKey(repo.Provider, repo.Host, repo.Owner, repo.Name)
		forkKey := store.ForkKey(repoKey, fork.ForgeID)
		doc := store.DocumentRecord{
			DocumentID: store.DocumentID(forkKey), ContentHash: id,
			Body: "fork " + id + " does some work", UpdatedAt: now,
		}
		if err := db.UpsertSnapshot(context.Background(), store.Snapshot{Repo: repo, Fork: fork, Document: doc}); err != nil {
			t.Fatalf("UpsertSnapshot: %v", err)
		}
	}
}

func countIndexed(t *testing.T, db *store.Store, modelID string) int {
	t.Helper()
	rows, err := db.SearchRows(context.Background(), modelID, "", "")
	if err != nil {
		t.Fatalf("SearchRows(%s): %v", modelID, err)
	}
	return len(rows)
}

// TestDualIndexingWritesBothPartitions is the headline behavior of this feature:
// one run indexes the same documents under two model identities, and neither
// displaces the other. Nothing else in the suite covers it — the `forks list`
// tests install embedderHookForTest, which short-circuits embedder resolution
// entirely.
func TestDualIndexingWritesBothPartitions(t *testing.T) {
	isolateSpoonHome(t)
	srv := voyageAPIStub(t, unitVector(embed.VoyageDefaultDimension), "")
	defer srv.Close()
	t.Setenv(embed.VoyageAPIKeyEnv, "sk-test")
	t.Setenv(embed.VoyageBaseURLEnv, srv.URL)

	db, err := store.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	defer db.Close()
	seedPendingDocuments(t, db, "one", "two", "three")

	local := &stubSearchEmbedder{modelID: "fastembed:stub:dim=8", dim: 8}
	voyage := resolveVoyageEmbedder(context.Background(), false, db, &bytes.Buffer{})
	if voyage == nil {
		t.Fatal("Voyage did not activate with a key and a writable store")
	}

	var stderr bytes.Buffer
	emitSemanticIndexWarning(context.Background(), db, []embed.SearchEmbedder{local, voyage}, &stderr)

	if got := countIndexed(t, db, local.ModelID()); got != 3 {
		t.Errorf("fastembed partition has %d rows, want 3", got)
	}
	if got := countIndexed(t, db, voyage.ModelID()); got != 3 {
		t.Errorf("voyage partition has %d rows, want 3", got)
	}

	// The spend must be visible in the output stream, not only on the invoice.
	warnings := stderr.String()
	if !strings.Contains(warnings, `"voyage_indexing"`) {
		t.Errorf("stderr = %s, want a voyage_indexing notice", warnings)
	}
	if !strings.Contains(warnings, `"documents":3`) {
		t.Errorf("stderr = %s, want the document count reported before sending", warnings)
	}
	if !strings.Contains(warnings, `"voyage_tokens"`) {
		t.Errorf("stderr = %s, want a voyage_tokens notice", warnings)
	}

	// A second pass must re-embed nothing: the content-hash join makes documents
	// non-pending, so Voyage is not paid again.
	stderr.Reset()
	emitSemanticIndexWarning(context.Background(), db, []embed.SearchEmbedder{local, voyage}, &stderr)
	if !strings.Contains(stderr.String(), `"documents":0`) {
		t.Errorf("second pass stderr = %s, want documents:0 — a re-run must re-embed nothing", stderr.String())
	}
}

// TestVoyageIndexFailureLeavesFastembedIntact: Voyage is the optional layer, so
// its failure must cost only its own partition.
func TestVoyageIndexFailureLeavesFastembedIntact(t *testing.T) {
	isolateSpoonHome(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"voyage is down"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv(embed.VoyageAPIKeyEnv, "sk-test")
	t.Setenv(embed.VoyageBaseURLEnv, srv.URL)

	db, err := store.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	defer db.Close()
	seedPendingDocuments(t, db, "one", "two")

	local := &stubSearchEmbedder{modelID: "fastembed:stub:dim=8", dim: 8}
	voyage := resolveVoyageEmbedder(context.Background(), false, db, &bytes.Buffer{})
	if voyage == nil {
		t.Fatal("Voyage did not activate")
	}

	var stderr bytes.Buffer
	emitSemanticIndexWarning(context.Background(), db, []embed.SearchEmbedder{local, voyage}, &stderr)

	if got := countIndexed(t, db, local.ModelID()); got != 2 {
		t.Errorf("fastembed partition has %d rows, want 2 — a Voyage outage must not cost the local index", got)
	}
	if got := countIndexed(t, db, voyage.ModelID()); got != 0 {
		t.Errorf("voyage partition has %d rows, want 0", got)
	}
	if !strings.Contains(stderr.String(), `"voyage_unavailable"`) {
		t.Errorf("stderr = %s, want a voyage_unavailable warning", stderr.String())
	}
	// A degrade, never a fatal error: nothing here may look like an error envelope.
	if strings.Contains(stderr.String(), `"error"`) {
		t.Errorf("stderr = %s, want warnings only — Voyage indexing must not fail a run", stderr.String())
	}
}

// TestFastembedIndexFailureStillWarnsAsBefore pins the pre-existing message:
// a fastembed failure must keep its own remediation rather than inheriting
// Voyage's.
func TestFastembedIndexFailureStillWarnsAsBefore(t *testing.T) {
	isolateSpoonHome(t)
	db, err := store.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	defer db.Close()
	seedPendingDocuments(t, db, "one")

	broken := &stubSearchEmbedder{modelID: "fastembed:stub:dim=8", dim: 8, failOn: context.DeadlineExceeded}
	var stderr bytes.Buffer
	emitSemanticIndexWarning(context.Background(), db, []embed.SearchEmbedder{broken}, &stderr)
	if !strings.Contains(stderr.String(), "semantic_index_failed") {
		t.Errorf("stderr = %s, want semantic_index_failed", stderr.String())
	}
	if strings.Contains(stderr.String(), "voyage") {
		t.Errorf("stderr = %s, want no Voyage text for a fastembed failure", stderr.String())
	}
}

// TestVoyageCachePreventsRepaidEmbeddings covers the cross-run guarantee: a
// fresh embedder against the same store re-requests nothing, because the cache
// outlives the process.
func TestVoyageCachePreventsRepaidEmbeddings(t *testing.T) {
	isolateSpoonHome(t)
	requests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = map[string]any{"embedding": unitVector(embed.VoyageDefaultDimension), "index": i}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]any{"total_tokens": 10}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv(embed.VoyageAPIKeyEnv, "sk-test")
	t.Setenv(embed.VoyageBaseURLEnv, srv.URL)

	db, err := store.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	defer db.Close()
	seedPendingDocuments(t, db, "one", "two")

	first := resolveVoyageEmbedder(context.Background(), false, db, &bytes.Buffer{})
	if first == nil {
		t.Fatal("Voyage did not activate")
	}
	texts := []string{"fork one does some work", "fork two does some work"}
	if _, err := first.EmbedPassages(context.Background(), texts); err != nil {
		t.Fatalf("first EmbedPassages: %v", err)
	}
	if requests == 0 {
		t.Fatal("no request was issued; the test is not exercising the API path")
	}
	afterFirst := requests

	// A distinct embedder, as a later process would build: the cache lives in the
	// store, not in the embedder.
	second := resolveVoyageEmbedder(context.Background(), false, db, &bytes.Buffer{})
	if _, err := second.EmbedPassages(context.Background(), texts); err != nil {
		t.Fatalf("second EmbedPassages: %v", err)
	}
	if requests != afterFirst {
		t.Errorf("a fresh embedder issued %d extra request(s); the store cache must survive the process",
			requests-afterFirst)
	}
	if hits, _, _ := second.CacheStats(); hits != int64(len(texts)) {
		t.Errorf("cache hits = %d, want %d", hits, len(texts))
	}
}

// TestNoVoyageFlagSkipsTheProvider: the escape hatch must work even with a key
// and a writable store, so an offline or cost-sensitive run has a way out.
func TestNoVoyageFlagSkipsTheProvider(t *testing.T) {
	isolateSpoonHome(t)
	t.Setenv(embed.VoyageAPIKeyEnv, "sk-test")
	t.Setenv(embed.VoyageBaseURLEnv, "http://127.0.0.1:1")
	db, err := store.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	defer db.Close()

	var stderr bytes.Buffer
	if got := resolveVoyageEmbedder(context.Background(), true /* disable */, db, &stderr); got != nil {
		t.Error("--no-voyage still resolved a Voyage embedder")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %s, want silence — an explicit opt-out is not a problem to report", stderr.String())
	}

	t.Setenv(embed.VoyageDisableEnv, "1")
	if got := resolveVoyageEmbedder(context.Background(), false, db, &stderr); got != nil {
		t.Errorf("%s=1 still resolved a Voyage embedder", embed.VoyageDisableEnv)
	}
}

// TestVoyageModelIDCarriesConfiguredDimension guards the index-partitioning
// property from the CLI's side: reconfiguring the dimension must not let two
// widths share one model key.
func TestVoyageModelIDCarriesConfiguredDimension(t *testing.T) {
	isolateSpoonHome(t)
	t.Setenv(embed.VoyageAPIKeyEnv, "sk-test")
	db, err := store.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	defer db.Close()

	seen := map[string]bool{}
	for _, dim := range []int{256, 512, 1024, 2048} {
		t.Setenv(embed.VoyageDimensionEnv, strconv.Itoa(dim))
		embedder := resolveVoyageEmbedder(context.Background(), false, db, &bytes.Buffer{})
		if embedder == nil {
			t.Fatalf("dim %d did not resolve", dim)
		}
		id := embedder.ModelID()
		if seen[id] {
			t.Fatalf("dimension %d reuses model ID %q", dim, id)
		}
		seen[id] = true
		if embedder.Dim() != dim {
			t.Errorf("Dim() = %d, want %d", embedder.Dim(), dim)
		}
	}
}
