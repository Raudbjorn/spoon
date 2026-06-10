package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// openaiEmbedServer serves an OpenAI-compatible endpoint. embedOK controls
// whether /v3/embeddings succeeds; /v3/models always lists the model (so the
// "reachable but cannot embed" case is representable — the OVMS bug).
func openaiEmbedServer(embedOK bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
		case "/v3/embeddings":
			if !embedOK {
				http.Error(w, `{"error":"Mediapipe ... RET_CHECK failure"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func ollamaEmbedServer(hasModel, embedOK bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			if hasModel {
				_, _ = w.Write([]byte(`{"models":[{"name":"nomic-embed-text"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"models":[]}`))
			}
		case "/api/embeddings":
			if !embedOK {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"embedding":[0.1,0.2]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestPreflight_Disabled(t *testing.T) {
	if err := Preflight(context.Background(), PreflightOptions{Enabled: false, Endpoint: "http://127.0.0.1:1"}); err != nil {
		t.Errorf("disabled should be a no-op: %v", err)
	}
}

func TestPreflight_OpenAI(t *testing.T) {
	t.Run("embeds OK passes", func(t *testing.T) {
		srv := openaiEmbedServer(true)
		defer srv.Close()
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai", Endpoint: srv.URL, Model: "m"}); err != nil {
			t.Errorf("want pass, got %v", err)
		}
	})
	t.Run("reachable but cannot embed fails (the OVMS case)", func(t *testing.T) {
		srv := openaiEmbedServer(false) // /v3/models 200, /v3/embeddings 400
		defer srv.Close()
		err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai", Endpoint: srv.URL, Model: "m"})
		if err == nil || !strings.Contains(err.Error(), "cannot embed") {
			t.Errorf("reachable-but-broken endpoint must fail; got %v", err)
		}
	})
	t.Run("no endpoint fails", func(t *testing.T) {
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai"}); err == nil {
			t.Error("openai with no endpoint should fail")
		}
	})
	t.Run("empty model defaults (does not send empty model)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v3/embeddings" {
				http.NotFound(w, r)
				return
			}
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Model == "" {
				http.Error(w, "empty model", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}]}`))
		}))
		defer srv.Close()
		// Model omitted → must default to DefaultOpenAIEmbeddingModel.
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai", Endpoint: srv.URL}); err != nil {
			t.Errorf("empty model should default and pass: %v", err)
		}
	})
	t.Run("unreachable fails", func(t *testing.T) {
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai", Endpoint: "http://127.0.0.1:1", Model: "m"}); err == nil {
			t.Error("unreachable openai should fail")
		}
	})
}

func TestPreflight_Sidecar(t *testing.T) {
	t.Run("embeds OK passes", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/embed" {
				_, _ = w.Write([]byte(`{"vectors":[[0.1,0.2]],"dim":2}`))
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "sidecar", SidecarEndpoint: srv.URL}); err != nil {
			t.Errorf("want pass, got %v", err)
		}
	})
	t.Run("unreachable fails", func(t *testing.T) {
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "sidecar", SidecarEndpoint: "http://127.0.0.1:1"}); err == nil {
			t.Error("unreachable sidecar should fail")
		}
	})
}

func TestPreflight_Ollama(t *testing.T) {
	t.Run("default backend embeds OK passes", func(t *testing.T) {
		srv := ollamaEmbedServer(true, true)
		defer srv.Close()
		t.Setenv("SPOON_EMBEDDER_URL", srv.URL) // Detect resolves empty endpoint via env
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: ""}); err != nil {
			t.Errorf("want pass, got %v", err)
		}
	})
	t.Run("running but cannot embed fails", func(t *testing.T) {
		srv := ollamaEmbedServer(true, false)
		defer srv.Close()
		t.Setenv("SPOON_EMBEDDER_URL", srv.URL)
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true}); err == nil {
			t.Error("ollama that can't embed should fail")
		}
	})
	t.Run("no model installed fails", func(t *testing.T) {
		srv := ollamaEmbedServer(false, true)
		defer srv.Close()
		t.Setenv("SPOON_EMBEDDER_URL", srv.URL)
		err := Preflight(context.Background(), PreflightOptions{Enabled: true})
		if err == nil || !strings.Contains(err.Error(), "no usable embedding model") {
			t.Errorf("want no-model failure, got %v", err)
		}
	})
	t.Run("unreachable fails", func(t *testing.T) {
		t.Setenv("SPOON_EMBEDDER_URL", "http://127.0.0.1:1")
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true}); err == nil {
			t.Error("unreachable ollama should fail")
		}
	})
}

func TestPreflight_Labeler(t *testing.T) {
	ok := openaiEmbedServer(true) // valid embedder so we reach the labeler check
	defer ok.Close()
	t.Run("reachable labeler passes", func(t *testing.T) {
		lab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound) // any HTTP response = reachable
		}))
		defer lab.Close()
		if err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai", Endpoint: ok.URL, Model: "m", LabelerEndpoint: lab.URL}); err != nil {
			t.Errorf("want pass, got %v", err)
		}
	})
	t.Run("unreachable labeler fails", func(t *testing.T) {
		err := Preflight(context.Background(), PreflightOptions{Enabled: true, Backend: "openai", Endpoint: ok.URL, Model: "m", LabelerEndpoint: "http://127.0.0.1:1"})
		if err == nil || !strings.Contains(err.Error(), "labeler") {
			t.Errorf("want labeler failure, got %v", err)
		}
	})
}
