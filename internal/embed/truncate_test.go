package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTruncateForEmbed(t *testing.T) {
	if got := truncateForEmbed("short"); got != "short" {
		t.Errorf("short string changed: %q", got)
	}
	long := strings.Repeat("a", maxEmbedModalityChars+500)
	got := truncateForEmbed(long)
	if len([]rune(got)) != maxEmbedModalityChars {
		t.Errorf("len=%d want %d", len([]rune(got)), maxEmbedModalityChars)
	}
	// Rune-safe: multi-byte runes must not be split.
	multi := strings.Repeat("é", maxEmbedModalityChars+10) // 2 bytes each
	g := truncateForEmbed(multi)
	if !json.Valid([]byte(`"` + g + `"`)) {
		t.Error("truncation split a multi-byte rune")
	}
	if len([]rune(g)) != maxEmbedModalityChars {
		t.Errorf("rune count=%d want %d", len([]rune(g)), maxEmbedModalityChars)
	}
}

func TestIsContextLengthError(t *testing.T) {
	for _, s := range []string{
		`ollama http://x/api/embeddings returned 500: {"error":"the input length exceeds the context length"}`,
		"ollama error: input exceeds the context length",
	} {
		if !isContextLengthError(errString(s)) {
			t.Errorf("should match: %q", s)
		}
	}
	if isContextLengthError(errString("connection refused")) {
		t.Error("should not match an unrelated error")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestEmbedOne_ShrinksOnContextLength(t *testing.T) {
	// Server 500s with the context-length error while the prompt is too long,
	// and returns a vector once it has been shrunk enough. limit is above the
	// 256-rune shrink floor (real model contexts are far larger).
	const limit = 300
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaSingleReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Prompt) > limit {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"the input length exceeds the context length"}`))
			return
		}
		_, _ = w.Write([]byte(`{"embedding":[0.1,0.2,0.3]}`))
	}))
	defer srv.Close()

	c := &OllamaClient{Endpoint: srv.URL, Model: "m"}
	v, err := c.embedOne(context.Background(), strings.Repeat("x", 4000))
	if err != nil {
		t.Fatalf("expected adaptive shrink to succeed, got %v", err)
	}
	if len(v) != 3 {
		t.Errorf("dim=%d want 3", len(v))
	}
}
