package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// readmeMaxBytes caps the README excerpt passed to the labeler at 2 KB. This
// keeps the total prompt comfortably under typical small-LLM context windows
// (≪ 4K tokens) and is the threshold the spec calls out for excerpt length.
const readmeMaxBytes = 2 * 1024

// memberFieldMaxChars caps each per-member Paths / Commits payload in the
// prompt at 200 characters. Three members × two fields = up to 1.2 KB of
// member-derived context, well within budget.
const memberFieldMaxChars = 200

// maxSampleMembers is the maximum number of member ForkFeatures rendered into
// the prompt, per the spec.
const maxSampleMembers = 3

// defaultChatPath is the Ollama chat endpoint path.
const defaultChatPath = "/api/chat"

// defaultLabelerTimeout bounds a single chat call. The labeler makes one
// request per cluster, so this is the per-cluster budget.
const defaultLabelerTimeout = 60 * time.Second

// systemPrompt is the fixed system-role message. The body is identical for
// every cluster — only the user-role payload varies.
const systemPrompt = `You produce concise, factual labels for groups of GitHub repository forks.
Given a heuristic label plus upstream context, produce a one-line label
(<= 60 characters) describing what unifies the forks. Avoid filler words
("various", "miscellaneous"). Do not invent specifics not present in the
context. Output only the label, no quotes, no preamble.`

// OllamaChatLabeler calls a chat-completion endpoint to polish a cluster's
// heuristic label into a human-readable phrase grounded in the upstream
// repository context.
//
// Compatible with Ollama's /api/chat endpoint and any OpenAI-style
// /v1/chat/completions endpoint (set ChatPath accordingly).
type OllamaChatLabeler struct {
	Endpoint string       // base URL, e.g., "http://localhost:11434"
	Model    string       // chat model, e.g., "llama3.2:3b" or "qwen2.5-coder:7b"
	HTTP     *http.Client // nil → default with 60s timeout (single API call)
	ChatPath string       // "" → "/api/chat" (Ollama default)
}

// chatMessage is the {role, content} pair shared by Ollama and OpenAI shapes.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is the unified request body. Both Ollama's /api/chat and the
// OpenAI-style /v1/chat/completions accept these fields (OpenAI ignores
// "stream":false the same way Ollama does).
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// chatResponse covers both response shapes. ollamaResponse fields populate
// when the server is Ollama; choices populates for OpenAI-style endpoints.
type chatResponse struct {
	// Ollama shape
	Message *chatMessage `json:"message,omitempty"`
	// OpenAI shape
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices,omitempty"`
}

// Polish satisfies the Labeler interface. See the OllamaChatLabeler struct
// doc for protocol details. Returns the heuristic label plus an error on any
// failure mode (timeout, non-2xx, malformed response); callers fall back to
// the heuristic in that case.
func (l *OllamaChatLabeler) Polish(ctx context.Context, lc LabelerContext) (string, error) {
	if l == nil {
		return lc.Heuristic, errors.New("nil OllamaChatLabeler")
	}
	if l.Endpoint == "" {
		return lc.Heuristic, errors.New("OllamaChatLabeler: empty Endpoint")
	}
	if l.Model == "" {
		return lc.Heuristic, errors.New("OllamaChatLabeler: empty Model")
	}

	httpClient := l.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultLabelerTimeout}
	}

	path := l.ChatPath
	if path == "" {
		path = defaultChatPath
	}
	url := strings.TrimRight(l.Endpoint, "/") + path

	prompt := buildUserPrompt(lc)
	reqBody := chatRequest{
		Model: l.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Stream: false,
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return lc.Heuristic, fmt.Errorf("OllamaChatLabeler: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return lc.Heuristic, fmt.Errorf("OllamaChatLabeler: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return lc.Heuristic, fmt.Errorf("OllamaChatLabeler: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain a small body excerpt for diagnostics. Avoid reading megabytes
		// from a misbehaving server.
		bodyExcerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return lc.Heuristic, fmt.Errorf("OllamaChatLabeler: status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(bodyExcerpt)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return lc.Heuristic, fmt.Errorf("OllamaChatLabeler: read body: %w", err)
	}
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return lc.Heuristic, fmt.Errorf("OllamaChatLabeler: decode response: %w", err)
	}

	var content string
	switch {
	case parsed.Message != nil && parsed.Message.Content != "":
		content = parsed.Message.Content
	case len(parsed.Choices) > 0 && parsed.Choices[0].Message.Content != "":
		content = parsed.Choices[0].Message.Content
	default:
		return lc.Heuristic, errors.New("OllamaChatLabeler: empty response content")
	}

	polished := trimLabel(content)
	if polished == "" {
		return lc.Heuristic, errors.New("OllamaChatLabeler: label empty after trim")
	}
	return polished, nil
}

// trimLabel strips whitespace and surrounding quotes from an LLM response.
// LLMs commonly wrap the answer in single or double quotes; the system prompt
// asks for no quotes but unreliable models still slip them in.
func trimLabel(s string) string {
	s = strings.TrimSpace(s)
	// Trim repeatedly so "\"'x'\"" → "x".
	for {
		trimmed := strings.Trim(s, `"'` + "`")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == s {
			break
		}
		s = trimmed
	}
	// Drop anything past the first newline — we asked for a single line.
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

// truncateBytes returns s capped to maxBytes. Truncation is byte-level (not
// rune-aware) because the LLM tolerates split UTF-8 sequences and the cap is
// for prompt-size budgeting, not display.
func truncateBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}

// buildUserPrompt assembles the user-role message from a LabelerContext.
// Keeps the format compact but readable; fmt.Sprintf is sufficient here.
func buildUserPrompt(lc LabelerContext) string {
	desc := strings.TrimSpace(lc.UpstreamDesc)
	readme := truncateBytes(strings.TrimSpace(lc.UpstreamReadme), readmeMaxBytes)
	coreDirs := strings.Join(lc.UpstreamCoreDirs, ", ")

	var b strings.Builder
	fmt.Fprintf(&b, "Upstream repo: %s\n", lc.UpstreamRepo)
	fmt.Fprintf(&b, "Upstream description: %s\n", desc)
	fmt.Fprintf(&b, "Upstream core directories: %s\n", coreDirs)
	b.WriteString("\nUpstream README (excerpt):\n")
	b.WriteString(readme)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Heuristic label: %s\n", lc.Heuristic)
	b.WriteString("\nSample forks in this cluster:\n")

	n := len(lc.Members)
	if n > maxSampleMembers {
		n = maxSampleMembers
	}
	for i := 0; i < n; i++ {
		m := lc.Members[i]
		fmt.Fprintf(&b, "- Paths: %s\n", truncateBytes(strings.TrimSpace(m.Paths), memberFieldMaxChars))
		fmt.Fprintf(&b, "  Commits: %s\n", truncateBytes(strings.TrimSpace(m.Commits), memberFieldMaxChars))
	}
	b.WriteString("\nWhat's the best one-line label for this cluster? Answer with just the label.\n")
	return b.String()
}
