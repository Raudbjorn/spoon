package cluster

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
)

// recordedRequest captures the inbound request body for assertion.
type recordedRequest struct {
	path string
	body []byte
}

// newOllamaServer returns an httptest server that replies with an Ollama-shape
// chat response carrying `content`. The captured request is recorded for
// assertion.
func newOllamaServer(t *testing.T, content string, rec *recordedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if rec != nil {
			rec.path = r.URL.Path
			rec.body = body
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"model":   "llama3.2:3b",
			"message": map[string]string{"role": "assistant", "content": content},
			"done":    true,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func newOpenAIStyleServer(t *testing.T, content string, rec *recordedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if rec != nil {
			rec.path = r.URL.Path
			rec.body = body
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": content}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func defaultContext() LabelerContext {
	return LabelerContext{
		Heuristic:        "auth plugins",
		UpstreamRepo:     "kong/kong",
		UpstreamDesc:     "API gateway",
		UpstreamReadme:   "Kong is an API gateway.",
		UpstreamCoreDirs: []string{"kong/", "spec/"},
		Members: []embed.ForkFeatures{
			{Paths: "kong/plugins/oauth.lua", Commits: "add oauth plugin"},
		},
	}
}

func TestOllamaChatLabeler_Success_Ollama(t *testing.T) {
	var rec recordedRequest
	srv := newOllamaServer(t, "OAuth provider plugins\n", &rec)
	defer srv.Close()

	l := &OllamaChatLabeler{Endpoint: srv.URL, Model: "llama3.2:3b"}
	got, err := l.Polish(context.Background(), defaultContext())
	if err != nil {
		t.Fatalf("Polish: %v", err)
	}
	if got != "OAuth provider plugins" {
		t.Errorf("got label %q, want %q", got, "OAuth provider plugins")
	}
	if rec.path != "/api/chat" {
		t.Errorf("expected default chat path /api/chat, got %q", rec.path)
	}

	// Sanity check: request body has model + messages.
	var req chatRequest
	if err := json.Unmarshal(rec.body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.Model != "llama3.2:3b" {
		t.Errorf("model = %q, want llama3.2:3b", req.Model)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Errorf("messages malformed: %+v", req.Messages)
	}
	if req.Stream {
		t.Errorf("stream should default to false")
	}
}

func TestOllamaChatLabeler_Success_OpenAIStyle(t *testing.T) {
	var rec recordedRequest
	srv := newOpenAIStyleServer(t, "  GraphQL extensions  ", &rec)
	defer srv.Close()

	l := &OllamaChatLabeler{
		Endpoint: srv.URL,
		Model:    "gpt-4o-mini",
		ChatPath: "/v1/chat/completions",
	}
	got, err := l.Polish(context.Background(), defaultContext())
	if err != nil {
		t.Fatalf("Polish: %v", err)
	}
	if got != "GraphQL extensions" {
		t.Errorf("got %q, want %q", got, "GraphQL extensions")
	}
	if rec.path != "/v1/chat/completions" {
		t.Errorf("expected /v1/chat/completions, got %q", rec.path)
	}
}

func TestOllamaChatLabeler_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	l := &OllamaChatLabeler{Endpoint: srv.URL, Model: "llama3.2:3b"}
	lc := defaultContext()
	got, err := l.Polish(context.Background(), lc)
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if got != lc.Heuristic {
		t.Errorf("expected heuristic fallback %q, got %q", lc.Heuristic, got)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error %q", err.Error())
	}
}

func TestOllamaChatLabeler_BadResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json {]["))
	}))
	defer srv.Close()

	l := &OllamaChatLabeler{Endpoint: srv.URL, Model: "llama3.2:3b"}
	lc := defaultContext()
	got, err := l.Polish(context.Background(), lc)
	if err == nil {
		t.Fatal("expected error on garbage response")
	}
	if got != lc.Heuristic {
		t.Errorf("expected heuristic fallback %q, got %q", lc.Heuristic, got)
	}
}

func TestOllamaChatLabeler_EmptyContent(t *testing.T) {
	srv := newOllamaServer(t, "", nil)
	defer srv.Close()

	l := &OllamaChatLabeler{Endpoint: srv.URL, Model: "llama3.2:3b"}
	lc := defaultContext()
	got, err := l.Polish(context.Background(), lc)
	if err == nil {
		t.Fatal("expected error on empty content")
	}
	if got != lc.Heuristic {
		t.Errorf("expected heuristic fallback, got %q", got)
	}
}

func TestOllamaChatLabeler_QuotesTrimmed(t *testing.T) {
	// The LLM "said" the literal four-character string `"plugins"` (with
	// surrounding double quotes). After JSON encoding/decoding the content
	// field stays `"plugins"`, and trimLabel should strip both quotes.
	srv := newOllamaServer(t, `"plugins"`, nil)
	defer srv.Close()

	l := &OllamaChatLabeler{Endpoint: srv.URL, Model: "llama3.2:3b"}
	got, err := l.Polish(context.Background(), defaultContext())
	if err != nil {
		t.Fatalf("Polish: %v", err)
	}
	if got != "plugins" {
		t.Errorf("got %q, want %q", got, "plugins")
	}
}

func TestOllamaChatLabeler_ContextTruncation(t *testing.T) {
	var rec recordedRequest
	srv := newOllamaServer(t, "x", &rec)
	defer srv.Close()

	// Use a unique sentinel character ('Ω') that won't appear in the prompt
	// scaffolding so we can count it precisely in the rendered user message.
	bigReadme := strings.Repeat("Ω", 50*1024) // 50K runes ≈ 100 KB
	lc := defaultContext()
	lc.UpstreamReadme = bigReadme

	l := &OllamaChatLabeler{Endpoint: srv.URL, Model: "llama3.2:3b"}
	if _, err := l.Polish(context.Background(), lc); err != nil {
		t.Fatalf("Polish: %v", err)
	}
	var req chatRequest
	if err := json.Unmarshal(rec.body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(req.Messages) < 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}
	user := req.Messages[1].Content
	omegaCount := strings.Count(user, "Ω")
	// 'Ω' is two bytes in UTF-8 (0xCE 0xA9). After byte-level truncation to
	// 2048 bytes the prompt holds 2048/2 = 1024 Ω runes.
	if omegaCount > readmeMaxBytes/2 {
		t.Errorf("README not truncated: got %d Ω runes in prompt, want ≤ %d", omegaCount, readmeMaxBytes/2)
	}
	if omegaCount < 500 {
		t.Errorf("README appears to be missing from prompt: only %d Ω runes", omegaCount)
	}
}

func TestOllamaChatLabeler_MissingEndpoint(t *testing.T) {
	l := &OllamaChatLabeler{Model: "llama3.2:3b"}
	got, err := l.Polish(context.Background(), defaultContext())
	if err == nil {
		t.Fatal("expected error for empty Endpoint")
	}
	if got != "auth plugins" {
		t.Errorf("expected heuristic fallback, got %q", got)
	}
}

func TestOllamaChatLabeler_MissingModel(t *testing.T) {
	l := &OllamaChatLabeler{Endpoint: "http://example.invalid"}
	got, err := l.Polish(context.Background(), defaultContext())
	if err == nil {
		t.Fatal("expected error for empty Model")
	}
	if got != "auth plugins" {
		t.Errorf("expected heuristic fallback, got %q", got)
	}
}

func TestOllamaChatLabeler_NetworkError(t *testing.T) {
	// 127.0.0.1:1 is reserved and reliably unreachable.
	l := &OllamaChatLabeler{
		Endpoint: "http://127.0.0.1:1",
		Model:    "llama3.2:3b",
		HTTP:     &http.Client{Timeout: 200 * time.Millisecond},
	}
	got, err := l.Polish(context.Background(), defaultContext())
	if err == nil {
		t.Fatal("expected error for unreachable endpoint")
	}
	if got != "auth plugins" {
		t.Errorf("expected heuristic fallback, got %q", got)
	}
}

// TestOllamaChatLabeler_OverlongResponse asserts that a model returning a
// long single-line label is capped to maxLabelChars runes plus a "..."
// indicator. Even if the model ignores the system-prompt's <=60 char
// guidance, downstream consumers receive a bounded string.
func TestOllamaChatLabeler_OverlongResponse(t *testing.T) {
	long := strings.Repeat("x", 200)
	srv := newOllamaServer(t, long, nil)
	defer srv.Close()

	l := &OllamaChatLabeler{Endpoint: srv.URL, Model: "llama3.2:3b"}
	got, err := l.Polish(context.Background(), defaultContext())
	if err != nil {
		t.Fatalf("Polish: %v", err)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	if n := len([]rune(got)); n > 63 {
		t.Errorf("rune count %d, want <= 63 (60 + ...)", n)
	}
}

// TestOllamaChatLabeler_HTTPClientCached pins that the lazy default HTTP
// client is constructed once across multiple Polish calls (M4 round-3
// fix). HTTP set explicitly takes precedence; we test the default branch
// here.
func TestOllamaChatLabeler_HTTPClientCached(t *testing.T) {
	l := &OllamaChatLabeler{Endpoint: "http://example.invalid", Model: "llama3.2:3b"}
	c1 := l.httpClient()
	c2 := l.httpClient()
	if c1 == nil || c2 == nil {
		t.Fatal("httpClient returned nil")
	}
	if c1 != c2 {
		t.Errorf("expected cached client across calls, got distinct pointers %p vs %p", c1, c2)
	}
}

func TestTrimLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  hello  ", "hello"},
		{"\"hello\"", "hello"},
		{"'hello'", "hello"},
		{"`hello`", "hello"},
		{" \"  hello  \" ", "hello"},
		{"hello\nworld", "hello"},
		{"\"hello\nworld\"", "hello"},
		{"", ""},
	}
	for _, c := range cases {
		got := trimLabel(c.in)
		if got != c.want {
			t.Errorf("trimLabel(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestBuildUserPrompt_Shape(t *testing.T) {
	lc := defaultContext()
	p := buildUserPrompt(lc)
	for _, want := range []string{
		"kong/kong",
		"API gateway",
		"kong/, spec/",
		"Kong is an API gateway.",
		"Heuristic label: auth plugins",
		"Paths: kong/plugins/oauth.lua",
		"Commits: add oauth plugin",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q\nprompt:\n%s", want, p)
		}
	}
}

func TestBuildUserPrompt_MemberCap(t *testing.T) {
	lc := defaultContext()
	for i := 0; i < 10; i++ {
		lc.Members = append(lc.Members, embed.ForkFeatures{Paths: "p", Commits: "c"})
	}
	p := buildUserPrompt(lc)
	// First member has Paths "kong/plugins/oauth.lua" — count "- Paths:" lines.
	count := strings.Count(p, "- Paths:")
	if count != maxSampleMembers {
		t.Errorf("expected %d members rendered, got %d", maxSampleMembers, count)
	}
}
