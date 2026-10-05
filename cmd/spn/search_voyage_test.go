package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/store"
)

// isolateSpoonHome points config, store and cache at a temp tree and clears every
// Voyage environment variable, so a key exported in the developer's shell cannot
// silently change what these tests exercise.
func isolateSpoonHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv(embed.VoyageAPIKeyEnv, "")
	t.Setenv(embed.VoyageAPIKeyEnvAlt, "")
	t.Setenv(embed.VoyageBaseURLEnv, "")
	t.Setenv(embed.VoyageDisableEnv, "")
	t.Setenv(embed.VoyageNoCacheEnv, "")
	return root
}

// seedSearchIndex writes one document plus one Voyage embedding per item, so
// `spn search --voyage` has an index to rank without needing ONNX Runtime.
func seedSearchIndex(t *testing.T, modelID string, dim int, items []struct {
	id, body string
	vector   []float32
}) {
	t.Helper()
	db, err := store.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	defer db.Close()
	now := time.Now().UTC()
	ctx := context.Background()
	records := make([]store.EmbeddingRecord, 0, len(items))
	for _, item := range items {
		repo := store.RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now}
		fork := store.ForkRecord{ForgeID: item.id, Owner: "o", Name: item.id, URL: "https://example/" + item.id, UpdatedAt: now}
		repoKey := store.RepoKey(repo.Provider, repo.Host, repo.Owner, repo.Name)
		forkKey := store.ForkKey(repoKey, fork.ForgeID)
		docID := store.DocumentID(forkKey)
		doc := store.DocumentRecord{DocumentID: docID, ContentHash: item.id, Body: item.body, UpdatedAt: now}
		if err := db.UpsertSnapshot(ctx, store.Snapshot{Repo: repo, Fork: fork, Document: doc}); err != nil {
			t.Fatalf("UpsertSnapshot: %v", err)
		}
		records = append(records, store.EmbeddingRecord{
			DocumentID: docID, Model: modelID, Dim: dim,
			Vector: encodeVectorForTest(item.vector), ContentHash: item.id, CreatedAt: now,
		})
	}
	if err := db.UpsertEmbeddings(ctx, records); err != nil {
		t.Fatalf("UpsertEmbeddings: %v", err)
	}
}

func encodeVectorForTest(values []float32) []byte {
	blob := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(v))
	}
	return blob
}

// unitVector returns a dim-wide unit vector along the first axis, so two
// documents sharing it tie exactly on cosine and only the reranker can order
// them.
func unitVector(dim int) []float32 {
	v := make([]float32, dim)
	v[0] = 1
	return v
}

// voyageAPIStub serves both endpoints. Embedding returns a fixed query vector;
// reranking scores documents by a substring the caller nominates, so the expected
// ordering is unambiguous.
func voyageAPIStub(t *testing.T, queryVector []float32, preferred string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = map[string]any{"embedding": queryVector, "index": i}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]any{"total_tokens": 3}})
	})
	mux.HandleFunc("/rerank", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := make([]map[string]any, len(req.Documents))
		for i, doc := range req.Documents {
			score := 0.1
			if strings.Contains(doc, preferred) {
				score = 0.9
			}
			results[i] = map[string]any{"index": i, "relevance_score": score}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results, "usage": map[string]any{"total_tokens": 5}})
	})
	return httptest.NewServer(mux)
}

// TestSearchVoyageRerankReordersResults is the end-to-end case for the second
// stage: retrieval puts both forks in the candidate set with identical cosines,
// so the deterministic fork-ID tiebreak would rank "aaa-garden" first. Only the
// cross-encoder can put the relevant fork on top.
func TestSearchVoyageRerankReordersResults(t *testing.T) {
	isolateSpoonHome(t)
	dim := embed.VoyageDefaultDimension
	srv := voyageAPIStub(t, unitVector(dim), "wayland")
	defer srv.Close()
	t.Setenv(embed.VoyageAPIKeyEnv, "sk-test")
	t.Setenv(embed.VoyageBaseURLEnv, srv.URL)

	modelID := "voyage:" + embed.VoyageDefaultEmbedModel + ":dim=" + strconv.Itoa(dim) + ":input_type=qd"
	seedSearchIndex(t, modelID, dim, []struct {
		id, body string
		vector   []float32
	}{
		{id: "aaa-garden", body: "garden flowers soil watering", vector: unitVector(dim)},
		{id: "zzz-wayland", body: "wayland compositor protocol support", vector: unitVector(dim)},
	})

	var stdout, stderr bytes.Buffer
	if code := runSearchWith([]string{"wayland support", "--voyage", "--top", "2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	records := decodeNDJSON(t, stdout.String())
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2; stderr: %s", len(records), stderr.String())
	}
	if got := records[0]["fork"]; got != "o/zzz-wayland" {
		t.Errorf("first result = %v, want o/zzz-wayland — reranking did not reorder the tied candidates", got)
	}
	// Both signals stay visible: score remains the retrieval cosine.
	if _, ok := records[0]["rerankScore"]; !ok {
		t.Error("rerankScore missing from the output")
	}
	if got := records[0]["rerankModel"]; got != "voyage:"+embed.VoyageDefaultRerankModel {
		t.Errorf("rerankModel = %v, want voyage:%s", got, embed.VoyageDefaultRerankModel)
	}
	if score, ok := records[0]["score"].(float64); !ok || score <= 0 {
		t.Errorf("score = %v, want the retrieval cosine preserved alongside rerankScore", records[0]["score"])
	}
	// --no-rerank must fall back to the cosine tiebreak, proving the reordering
	// above came from the reranker rather than from the seeding.
	stdout.Reset()
	stderr.Reset()
	if code := runSearchWith([]string{"wayland support", "--voyage", "--no-rerank", "--top", "2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--no-rerank exit %d, stderr: %s", code, stderr.String())
	}
	plain := decodeNDJSON(t, stdout.String())
	if got := plain[0]["fork"]; got != "o/aaa-garden" {
		t.Errorf("--no-rerank first result = %v, want o/aaa-garden (the cosine tiebreak)", got)
	}
	if _, ok := plain[0]["rerankScore"]; ok {
		t.Error("rerankScore present with --no-rerank")
	}
}

// TestSearchRerankDegradesOnProviderOutage: losing the second-stage refinement is
// acceptable, losing the search is not.
func TestSearchRerankDegradesOnProviderOutage(t *testing.T) {
	isolateSpoonHome(t)
	dim := embed.VoyageDefaultDimension
	// Embeddings work, reranking is down: the run must still emit ranked results.
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = map[string]any{"embedding": unitVector(dim), "index": i}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("/rerank", func(w http.ResponseWriter, r *http.Request) {
		// Retry-After: 0 keeps the test fast; the retry path itself is covered in
		// internal/embed.
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"reranker is down"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv(embed.VoyageAPIKeyEnv, "sk-test")
	t.Setenv(embed.VoyageBaseURLEnv, srv.URL)

	modelID := "voyage:" + embed.VoyageDefaultEmbedModel + ":dim=" + strconv.Itoa(dim) + ":input_type=qd"
	seedSearchIndex(t, modelID, dim, []struct {
		id, body string
		vector   []float32
	}{{id: "only", body: "some fork work", vector: unitVector(dim)}})

	var stdout, stderr bytes.Buffer
	if code := runSearchWith([]string{"a query", "--voyage", "--top", "5"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, want 0 — a reranker outage must not fail the search; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "rerank_unavailable") {
		t.Errorf("stderr = %s, want a rerank_unavailable warning", stderr.String())
	}
	records := decodeNDJSON(t, stdout.String())
	if len(records) != 1 {
		t.Fatalf("got %d records, want the cosine-ranked result to survive", len(records))
	}
	if _, ok := records[0]["rerankScore"]; ok {
		t.Error("rerankScore present after a failed rerank")
	}
}

func decodeNDJSON(t *testing.T, out string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode NDJSON line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// An empty result is not one state but three: a --repo filter that matched
// nothing in a populated index, an index written by a different embedder, and a
// genuinely empty index. Collapsing them into semantic_index_empty told a
// caller with a typo'd --repo to re-index a repo it already had (#82).
func TestSearchExplainsWhyItReturnedNothing(t *testing.T) {
	seed := func(t *testing.T, modelID string) {
		t.Helper()
		seedSearchIndex(t, modelID, 2, []struct {
			id, body string
			vector   []float32
		}{{id: "only", body: "canvas rendering", vector: []float32{1, 0}}})
	}
	run := func(t *testing.T, stubModel string, args ...string) (string, string, int) {
		t.Helper()
		deps := commandDeps{searchEmbedder: func(bool, config.EmbedderConfig, embed.VoyageConfig) (embed.SearchEmbedder, func(), *agentio.Error) {
			return deterministicSearchEmbedder{model: stubModel}, func() {}, nil
		}}
		var stdout, stderr bytes.Buffer
		exit := dispatchWithDeps(append([]string{"search"}, args...), &stdout, &stderr, deps)
		return stdout.String(), stderr.String(), exit
	}

	t.Run("repo filter matched nothing", func(t *testing.T) {
		isolateSpoonHome(t)
		seed(t, "current-model")
		stdout, stderr, exit := run(t, "current-model", "canvas", "--repo", "up/nope")
		if exit != 0 || stdout != "" {
			t.Fatalf("exit=%d stdout=%q, want exit 0 and no rows", exit, stdout)
		}
		if !strings.Contains(stderr, "semantic_repo_filter_empty") || strings.Contains(stderr, "semantic_index_empty") {
			t.Errorf("stderr = %s, want semantic_repo_filter_empty naming the filter", stderr)
		}
		if !strings.Contains(stderr, "up/nope") {
			t.Errorf("stderr = %s, want the unmatched repo named", stderr)
		}
	})

	t.Run("index written by another embedder", func(t *testing.T) {
		isolateSpoonHome(t)
		seed(t, "stale-model")
		_, stderr, exit := run(t, "current-model", "canvas")
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		if !strings.Contains(stderr, "semantic_model_mismatch") {
			t.Errorf("stderr = %s, want semantic_model_mismatch", stderr)
		}
		if !strings.Contains(stderr, "stale-model") {
			t.Errorf("stderr = %s, want the stored model id named", stderr)
		}
	})

	t.Run("genuinely empty index", func(t *testing.T) {
		isolateSpoonHome(t)
		var stdout, stderr bytes.Buffer
		if code := runSearchWith([]string{"anything"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit %d, stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "semantic_index_empty") {
			t.Errorf("stderr = %s, want semantic_index_empty for an empty store", stderr.String())
		}
	})

	t.Run("matching filter still returns rows", func(t *testing.T) {
		isolateSpoonHome(t)
		seed(t, "current-model")
		stdout, stderr, exit := run(t, "current-model", "canvas", "--repo", "up/repo")
		if exit != 0 || len(decodeNDJSON(t, stdout)) != 1 {
			t.Fatalf("exit=%d stdout=%q stderr=%s, want one ranked row", exit, stdout, stderr)
		}
		if strings.Contains(stderr, "semantic_") {
			t.Errorf("stderr = %s, want no empty-result warning on a hit", stderr)
		}
	})
}

// TestSearchVoyageFlagsRequireAKey covers the one place a Voyage failure is fatal:
// the user named Voyage on this invocation, so silently serving fastembed results
// would answer a different question than the one asked.
func TestSearchVoyageFlagsRequireAKey(t *testing.T) {
	for _, flag := range []string{"--voyage", "--rerank"} {
		t.Run(flag, func(t *testing.T) {
			isolateSpoonHome(t)
			var stdout, stderr bytes.Buffer
			code := runSearchWith([]string{"anything", flag}, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit %d, want 2 (bad_input); stderr: %s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "voyage_unavailable") {
				t.Errorf("stderr = %s, want a voyage_unavailable error", stderr.String())
			}
			if !strings.Contains(stderr.String(), embed.VoyageAPIKeyEnv) {
				t.Errorf("stderr = %s, want the remediation to name %s", stderr.String(), embed.VoyageAPIKeyEnv)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want it empty on a usage error", stdout.String())
			}
		})
	}
}

// TestSearchWithoutVoyageIsUnchanged: with no key, search must behave exactly as
// before this feature existed — no Voyage warnings, no rerank fields.
func TestSearchWithoutVoyageIsUnchanged(t *testing.T) {
	isolateSpoonHome(t)
	var stdout, stderr bytes.Buffer
	if code := runSearchWith([]string{"anything"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "voyage") {
		t.Errorf("stderr mentions voyage with no key configured: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "semantic_index_empty") {
		t.Errorf("stderr = %s, want semantic_index_empty for a fresh install", stderr.String())
	}
}

func TestSearchWithoutVoyageIgnoresMalformedVoyageDimension(t *testing.T) {
	isolateSpoonHome(t)
	t.Setenv(embed.VoyageDimensionEnv, "not-a-number")

	var stdout, stderr bytes.Buffer
	if code := runSearchWith([]string{"anything"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "semantic_index_empty") {
		t.Errorf("stderr = %s, want semantic_index_empty for FastEmbed-only search", stderr.String())
	}
}

func TestSearchRejectsBadRerankOverfetch(t *testing.T) {
	isolateSpoonHome(t)
	for _, args := range [][]string{
		{"q", "--rerank-overfetch"},
		{"q", "--rerank-overfetch", "0"},
		{"q", "--rerank-overfetch", "abc"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runSearchWith(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

// TestSortByCosineIsDeterministic pins the tiebreak the rerank stage relies on:
// without a stable order, two runs over tied cosines could hand the reranker
// different candidate sets.
func TestSortByCosineIsDeterministic(t *testing.T) {
	results := []searchResult{
		{ForkID: "c", Score: 0.5},
		{ForkID: "a", Score: 0.5},
		{ForkID: "b", Score: 0.9},
	}
	sortByCosine(results)
	got := []string{results[0].ForkID, results[1].ForkID, results[2].ForkID}
	want := []string{"b", "a", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
