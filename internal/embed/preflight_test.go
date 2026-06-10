package embed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreflight_Disabled(t *testing.T) {
	// Clustering off → no-op even with a bogus endpoint.
	if err := Preflight(context.Background(), PreflightOptions{Enabled: false, Endpoint: "http://127.0.0.1:1"}); err != nil {
		t.Errorf("disabled should be a no-op: %v", err)
	}
}

func TestPreflight_DefaultBackendChecked(t *testing.T) {
	// The default Ollama backend (no endpoint given) is validated too. Detect
	// resolves an empty endpoint via $SPOON_EMBEDDER_URL, so point it at a
	// controlled server to keep the test hermetic.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[]}`))
		}
	}))
	defer srv.Close()

	t.Run("default reachable passes", func(t *testing.T) {
		t.Setenv("SPOON_EMBEDDER_URL", srv.URL)
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "", Endpoint: ""}); err != nil {
			t.Errorf("reachable default should pass: %v", err)
		}
	})
	t.Run("default unreachable fails", func(t *testing.T) {
		t.Setenv("SPOON_EMBEDDER_URL", "http://127.0.0.1:1")
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "", Endpoint: ""}); err == nil {
			t.Error("unreachable default backend should fail")
		}
	})
}

func TestPreflight_OllamaExplicit(t *testing.T) {
	// Reachable Ollama-style endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Endpoint: srv.URL}); err != nil {
		t.Errorf("reachable ollama endpoint should pass: %v", err)
	}
	// Unreachable explicit endpoint → error (this is the user's --embedder bug).
	err := Preflight(context.Background(), PreflightOptions{Enabled: true, Endpoint: "http://127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("unreachable ollama endpoint should fail: %v", err)
	}
}

func TestPreflight_OpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()
	if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai", Endpoint: srv.URL, Model: "m"}); err != nil {
		t.Errorf("reachable openai endpoint should pass: %v", err)
	}
	// Missing endpoint for openai → error.
	if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai"}); err == nil {
		t.Error("openai with no endpoint should fail")
	}
	// Unreachable openai endpoint → error.
	if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai", Endpoint: "http://127.0.0.1:1", Model: "m"}); err == nil {
		t.Error("unreachable openai endpoint should fail")
	}
}

func TestPreflight_Sidecar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok","dim":1024}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "sidecar", SidecarEndpoint: srv.URL}); err != nil {
		t.Errorf("reachable sidecar should pass: %v", err)
	}
	if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "sidecar", SidecarEndpoint: "http://127.0.0.1:1"}); err == nil {
		t.Error("unreachable sidecar should fail")
	}
}

func TestPreflight_Labeler(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound) // any HTTP response counts as reachable
	}))
	defer srv.Close()
	// Ollama endpoint reachable + labeler reachable → pass.
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[]}`))
		}
	}))
	defer ollama.Close()
	if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Endpoint: ollama.URL, LabelerEndpoint: srv.URL}); err != nil {
		t.Errorf("reachable labeler should pass: %v", err)
	}
	// Unreachable labeler → error.
	err := Preflight(context.Background(), PreflightOptions{Enabled: true, Endpoint: ollama.URL, LabelerEndpoint: "http://127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "labeler") {
		t.Errorf("unreachable labeler should fail: %v", err)
	}
}
