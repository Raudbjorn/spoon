package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testVoyageKey = "sk-test-voyage-secret-key"

// voyageTestConfig points a client at srv with caching off, so a test exercises
// the request path rather than the cache.
func voyageTestConfig(t *testing.T, srv *httptest.Server) VoyageConfig {
	t.Helper()
	// The kill switch is read in withDefaults; clear it so an exported value in
	// the developer's shell cannot change what these tests exercise.
	t.Setenv(VoyageNoCacheEnv, "")
	return VoyageConfig{APIKey: testVoyageKey, BaseURL: srv.URL, HTTP: srv.Client()}
}

// embedServer answers /embeddings with a deterministic vector per input, sized
// from the request's own output_dimension so the response always satisfies
// validation. shuffle reverses the response array while leaving each result's
// index field correct, which is what forces the client to reassemble by index.
func embedServer(t *testing.T, shuffle bool, requests *atomic.Int64, seen *[][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			requests.Add(1)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testVoyageKey {
			t.Errorf("Authorization = %q, want bearer key", got)
		}
		var req voyageEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if seen != nil {
			*seen = append(*seen, req.Input)
		}
		type item struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		items := make([]item, len(req.Input))
		for i, text := range req.Input {
			items[i] = item{Embedding: textVector(text, req.OutputDimension), Index: i}
		}
		if shuffle {
			for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
				items[i], items[j] = items[j], items[i]
			}
		}
		writeJSON(t, w, map[string]any{"data": items, "model": req.Model,
			"usage": map[string]any{"total_tokens": 7}})
	}))
}

// textVector derives a non-zero vector identifying text. The first two
// components carry len(text) and text[0]; their ratio survives L2 normalization,
// so a mis-ordered response is detectable after the client normalizes.
func textVector(text string, dim int) []float32 {
	vec := make([]float32, dim)
	vec[0] = float32(len(text))
	vec[1] = float32(text[0])
	return vec
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestVoyageEmbedRequestShape(t *testing.T) {
	var got voyageEmbedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		writeJSON(t, w, map[string]any{"data": []map[string]any{
			{"embedding": textVector("find the parser", 512), "index": 0}}})
	}))
	defer srv.Close()

	cfg := voyageTestConfig(t, srv)
	cfg.OutputDimension = 512
	embedder, err := NewVoyageEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}
	if _, err := embedder.EmbedQuery(context.Background(), "find the parser"); err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if got.Model != VoyageDefaultEmbedModel {
		t.Errorf("model = %q, want %q", got.Model, VoyageDefaultEmbedModel)
	}
	if got.InputType != voyageInputTypeQuery {
		t.Errorf("input_type = %q, want %q", got.InputType, voyageInputTypeQuery)
	}
	if got.OutputDimension != 512 {
		t.Errorf("output_dimension = %d, want 512", got.OutputDimension)
	}
	if !got.Truncation {
		t.Error("truncation = false, want true (an over-length document must not fail the batch)")
	}
}

func TestVoyageEmbedPassagesUseDocumentInputType(t *testing.T) {
	var got voyageEmbedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeJSON(t, w, map[string]any{"data": []map[string]any{
			{"embedding": textVector("a document", VoyageDefaultDimension), "index": 0}}})
	}))
	defer srv.Close()

	embedder, err := NewVoyageEmbedder(voyageTestConfig(t, srv))
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}
	if _, err := embedder.EmbedPassages(context.Background(), []string{"a document"}); err != nil {
		t.Fatalf("EmbedPassages: %v", err)
	}
	if got.InputType != voyageInputTypeDocument {
		t.Errorf("input_type = %q, want %q", got.InputType, voyageInputTypeDocument)
	}
}

// TestVoyageEmbedReassemblesByIndex is the load-bearing case: Voyage may return
// results in any order, and trusting array order would attribute one fork's
// vector to another — a corruption no downstream check could detect.
func TestVoyageEmbedReassemblesByIndex(t *testing.T) {
	srv := embedServer(t, true /* shuffle */, nil, nil)
	defer srv.Close()

	cfg := voyageTestConfig(t, srv)
	cfg.OutputDimension = 256
	embedder, err := NewVoyageEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}

	texts := []string{"alpha", "bb", "ccc-long-one", "d"}
	vectors, err := embedder.EmbedPassages(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedPassages: %v", err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(vectors), len(texts))
	}
	// The server encodes len(text) and text[0] into the vector; after L2
	// normalization the ratio of the two components is preserved, so it still
	// identifies which text produced which vector.
	for i, text := range texts {
		wantRatio := float64(len(text)) / float64(text[0])
		gotRatio := float64(vectors[i][0]) / float64(vectors[i][1])
		if diff := gotRatio - wantRatio; diff > 1e-4 || diff < -1e-4 {
			t.Errorf("vector %d (%q): component ratio %v, want %v — results were mis-ordered",
				i, text, gotRatio, wantRatio)
		}
	}
}

func TestVoyageEmbedRejectsBadIndices(t *testing.T) {
	cases := []struct {
		name string
		data []map[string]any
		want string
	}{
		{
			name: "duplicate index",
			data: []map[string]any{
				{"embedding": textVector("one", VoyageDefaultDimension), "index": 0},
				{"embedding": textVector("two", VoyageDefaultDimension), "index": 0},
			},
			want: "duplicate result index",
		},
		{
			name: "out of range index",
			data: []map[string]any{
				{"embedding": textVector("one", VoyageDefaultDimension), "index": 0},
				{"embedding": textVector("two", VoyageDefaultDimension), "index": 9},
			},
			want: "out of range",
		},
		{
			name: "wrong result count",
			data: []map[string]any{{"embedding": textVector("one", VoyageDefaultDimension), "index": 0}},
			want: "got 1 results for 2 inputs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{"data": tc.data})
			}))
			defer srv.Close()
			embedder, err := NewVoyageEmbedder(voyageTestConfig(t, srv))
			if err != nil {
				t.Fatalf("NewVoyageEmbedder: %v", err)
			}
			_, err = embedder.EmbedPassages(context.Background(), []string{"one", "two"})
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestVoyageRetriesRateLimitThenSucceeds(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"detail":"rate limit exceeded"}`))
			return
		}
		writeJSON(t, w, map[string]any{"data": []map[string]any{
			{"embedding": textVector("retry me", VoyageDefaultDimension), "index": 0}}})
	}))
	defer srv.Close()

	embedder, err := NewVoyageEmbedder(voyageTestConfig(t, srv))
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}
	if _, err := embedder.EmbedQuery(context.Background(), "retry me"); err != nil {
		t.Fatalf("EmbedQuery after retry: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2 (one 429 then one success)", got)
	}
}

func TestVoyageClassifiesFailures(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		wantAuth  bool
		wantLimit bool
		wantCalls int64
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantAuth: true, wantCalls: 1},
		{name: "forbidden", status: http.StatusForbidden, wantAuth: true, wantCalls: 1},
		// 429 and 5xx exhaust the retry budget: 1 initial + voyageMaxRetries.
		{name: "rate limited", status: http.StatusTooManyRequests, wantLimit: true, wantCalls: voyageMaxRetries + 1},
		{name: "server error", status: http.StatusBadGateway, wantCalls: voyageMaxRetries + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"detail":"nope"}`))
			}))
			defer srv.Close()
			embedder, err := NewVoyageEmbedder(voyageTestConfig(t, srv))
			if err != nil {
				t.Fatalf("NewVoyageEmbedder: %v", err)
			}
			_, err = embedder.EmbedQuery(context.Background(), "classify me")
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if IsVoyageAuthError(err) != tc.wantAuth {
				t.Errorf("IsVoyageAuthError = %v, want %v (err: %v)", IsVoyageAuthError(err), tc.wantAuth, err)
			}
			if IsVoyageRateLimited(err) != tc.wantLimit {
				t.Errorf("IsVoyageRateLimited = %v, want %v (err: %v)", IsVoyageRateLimited(err), tc.wantLimit, err)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestVoyageErrorsNeverLeakAPIKey guards the one failure that cannot be undone:
// a credential written into a log or an NDJSON warning that a user then pastes
// into an issue. The server echoes the key back deliberately.
func TestVoyageErrorsNeverLeakAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"detail":"invalid key %s supplied"}`, testVoyageKey)
	}))
	defer srv.Close()

	embedder, err := NewVoyageEmbedder(voyageTestConfig(t, srv))
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}
	_, err = embedder.EmbedQuery(context.Background(), "leak check")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), testVoyageKey) {
		t.Fatalf("error text contains the API key: %q", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("error = %q, want the key replaced with [REDACTED]", err)
	}
}

// TestVoyageModelIdentityIsPinned is the regression guard the evaluation
// checklist requires before persistence is enabled: the model ID and dimension
// are a stored vector's identity, so a silent change to either would compare
// incomparable vectors under one model key.
func TestVoyageModelIdentityIsPinned(t *testing.T) {
	embedder, err := NewVoyageEmbedder(VoyageConfig{APIKey: testVoyageKey})
	if err != nil {
		t.Fatalf("NewVoyageEmbedder: %v", err)
	}
	const want = "voyage:voyage-code-3:dim=1024:input_type=qd"
	if got := embedder.ModelID(); got != want {
		t.Errorf("ModelID() = %q, want %q", got, want)
	}
	if got := embedder.Dim(); got != VoyageDefaultDimension {
		t.Errorf("Dim() = %d, want %d", got, VoyageDefaultDimension)
	}
	// The dimension must be part of the identity, or a re-configured run would
	// mix 512-dim and 1024-dim vectors under one key.
	narrow, err := NewVoyageEmbedder(VoyageConfig{APIKey: testVoyageKey, OutputDimension: 512})
	if err != nil {
		t.Fatalf("NewVoyageEmbedder(512): %v", err)
	}
	if narrow.ModelID() == embedder.ModelID() {
		t.Errorf("512-dim and 1024-dim embedders share ModelID %q", narrow.ModelID())
	}
}

func TestVoyageConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  VoyageConfig
		want string
	}{
		{name: "no key", cfg: VoyageConfig{}, want: "no API key"},
		{name: "bad dimension", cfg: VoyageConfig{APIKey: "k", OutputDimension: 777}, want: "output dimension"},
		{name: "relative base URL", cfg: VoyageConfig{APIKey: "k", BaseURL: "api.voyageai.com"}, want: "absolute http(s) URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(VoyageNoCacheEnv, "")
			if _, err := tc.cfg.withDefaults(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("withDefaults() error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestRetryAfterDistinguishesZeroFromAbsent(t *testing.T) {
	cases := []struct {
		header   string
		wantWait int
		wantTold bool
	}{
		{header: "5", wantWait: 5, wantTold: true},
		// An explicit zero must mean "retry now", not "server said nothing" —
		// otherwise the caller sleeps a full backoff the server did not ask for.
		{header: "0", wantWait: 0, wantTold: true},
		{header: "", wantTold: false},
		{header: "-1", wantTold: false},
		{header: "Wed, 21 Oct 2015 07:28:00 GMT", wantTold: false},
	}
	for _, tc := range cases {
		wait, told := retryAfter(tc.header)
		if told != tc.wantTold {
			t.Errorf("retryAfter(%q) told = %v, want %v", tc.header, told, tc.wantTold)
		}
		if told && int(wait.Seconds()) != tc.wantWait {
			t.Errorf("retryAfter(%q) wait = %v, want %ds", tc.header, wait, tc.wantWait)
		}
	}
}

func TestVoyageBatchBoundsRespectsBothCaps(t *testing.T) {
	cases := []struct {
		name              string
		costs             []int
		maxCount, maxCost int
		want              [][2]int
	}{
		{name: "empty", costs: nil, maxCount: 4, maxCost: 100, want: nil},
		{name: "single batch", costs: []int{1, 1, 1}, maxCount: 4, maxCost: 100, want: [][2]int{{0, 3}}},
		{name: "count cap", costs: []int{1, 1, 1, 1, 1}, maxCount: 2, maxCost: 100,
			want: [][2]int{{0, 2}, {2, 4}, {4, 5}}},
		{name: "cost cap", costs: []int{60, 60, 60}, maxCount: 10, maxCost: 100,
			want: [][2]int{{0, 1}, {1, 2}, {2, 3}}},
		// A single item over budget still gets its own batch: the API truncates
		// it, which beats dropping it.
		{name: "oversized single item", costs: []int{500, 1}, maxCount: 10, maxCost: 100,
			want: [][2]int{{0, 1}, {1, 2}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost := func(i int) int { return tc.costs[i] }
			got := voyageBatchBounds(len(tc.costs), tc.maxCount, tc.maxCost, cost)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("bounds = %v, want %v", got, tc.want)
			}
			// The properties the callers actually depend on.
			covered := 0
			for _, b := range got {
				if b[1]-b[0] > tc.maxCount {
					t.Errorf("batch %v exceeds count cap %d", b, tc.maxCount)
				}
				if b[0] != covered {
					t.Errorf("batch %v does not continue from %d — coverage has a gap or overlap", b, covered)
				}
				covered = b[1]
				spent := 0
				for i := b[0]; i < b[1]; i++ {
					spent += tc.costs[i]
				}
				if spent > tc.maxCost && b[1]-b[0] > 1 {
					t.Errorf("multi-item batch %v costs %d, over cap %d", b, spent, tc.maxCost)
				}
			}
			if covered != len(tc.costs) {
				t.Errorf("batches cover %d of %d items", covered, len(tc.costs))
			}
		})
	}
}
