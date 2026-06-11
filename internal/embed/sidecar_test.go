package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSidecarDefaultHTTPClient_TimeoutAtLeast5Minutes(t *testing.T) {
	// Sidecar embeds run a 568M model on CPU; per-batch latency for ~50
	// forks at ~5 KB prompts can reach 1-3 minutes. The default client
	// timeout must accommodate that, NOT inherit the 30s Ollama default.
	// Pinning the lower bound here prevents accidental regressions if
	// somebody refactors and shares the Ollama client again.
	if got := defaultSidecarHTTPClient.Timeout; got < 5*time.Minute {
		t.Fatalf("defaultSidecarHTTPClient.Timeout = %v; want >= 5m to allow batch CPU inference", got)
	}
}

func TestSidecarEmbed_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		var req sidecarEmbedReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := sidecarEmbedResp{Dim: 4}
		for range req.Texts {
			out.Vectors = append(out.Vectors, []float32{0.1, 0.2, 0.3, 0.4})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	e := &SidecarEmbedder{Endpoint: srv.URL, HTTP: srv.Client()}
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("want 2 vectors, got %d", len(vecs))
	}
	if e.Dim() != 4 {
		t.Errorf("want Dim=4, got %d", e.Dim())
	}
}

func TestSidecarEmbed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"model not loaded"}`))
	}))
	defer srv.Close()
	e := &SidecarEmbedder{Endpoint: srv.URL, HTTP: srv.Client()}
	_, err := e.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code; got %v", err)
	}
}

func TestSidecarHealth_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "dim": 1024})
	}))
	defer srv.Close()
	e := &SidecarEmbedder{Endpoint: srv.URL, HTTP: srv.Client()}
	if err := e.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}
