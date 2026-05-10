package embed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaClient_Embed_Success(t *testing.T) {
	var gotReq ollamaSingleReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embedding": []float32{0.1, 0.2, 0.3, 0.4},
		})
	}))
	defer srv.Close()

	c := &OllamaClient{Endpoint: srv.URL, Model: "test-model"}
	got, err := c.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 vector, got %d", len(got))
	}
	if len(got[0]) != 4 {
		t.Fatalf("want dim 4, got %d", len(got[0]))
	}
	if c.Dim() != 4 {
		t.Errorf("Dim() = %d, want 4", c.Dim())
	}
	if gotReq.Model != "test-model" {
		t.Errorf("Model = %q, want test-model", gotReq.Model)
	}
	if gotReq.Prompt != "hello" {
		t.Errorf("Prompt = %q, want hello", gotReq.Prompt)
	}
}

func TestOllamaClient_Embed_Batch(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{float32(calls), 0}})
	}))
	defer srv.Close()
	c := &OllamaClient{Endpoint: srv.URL, Model: "m"}
	got, err := c.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 vectors, got %d", len(got))
	}
	if calls != 3 {
		t.Errorf("want 3 calls, got %d", calls)
	}
}

func TestOllamaClient_Embed_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c := &OllamaClient{Endpoint: srv.URL}
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code: %v", err)
	}
}

func TestOllamaClient_Embed_ContextCancel(t *testing.T) {
	// Use a pre-cancelled context: the request fails before reaching the
	// server, avoiding flaky httptest connection cleanup.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0}})
	}))
	defer srv.Close()
	c := &OllamaClient{Endpoint: srv.URL}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Embed(ctx, []string{"x"})
	if err == nil {
		t.Fatal("want error from cancelled ctx")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context") {
		t.Errorf("want context error, got %v", err)
	}
}

func TestOllamaClient_Embed_EmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{}})
	}))
	defer srv.Close()
	c := &OllamaClient{Endpoint: srv.URL}
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("want error on empty embedding")
	}
}

func TestNewFromEnv_Defaults(t *testing.T) {
	t.Setenv(envEndpoint, "")
	t.Setenv(envModel, "")
	e := NewFromEnv()
	oc, ok := e.(*OllamaClient)
	if !ok {
		t.Fatal("NewFromEnv should return *OllamaClient")
	}
	if oc.Endpoint != defaultEndpoint {
		t.Errorf("Endpoint = %q, want %q", oc.Endpoint, defaultEndpoint)
	}
	if oc.Model != defaultModel {
		t.Errorf("Model = %q, want %q", oc.Model, defaultModel)
	}
}

func TestNewFromEnv_Overrides(t *testing.T) {
	t.Setenv(envEndpoint, "http://example.com:9999")
	t.Setenv(envModel, "custom-model")
	oc := NewFromEnv().(*OllamaClient)
	if oc.Endpoint != "http://example.com:9999" {
		t.Errorf("Endpoint = %q", oc.Endpoint)
	}
	if oc.Model != "custom-model" {
		t.Errorf("Model = %q", oc.Model)
	}
}
