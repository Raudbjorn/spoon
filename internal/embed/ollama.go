package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// maxOllamaRespSize caps response bodies to 10 MB to prevent unbounded reads.
const maxOllamaRespSize = 10 * 1024 * 1024

// OllamaClient hits POST {Endpoint}/api/embeddings on a local Ollama instance.
// OllamaClient is safe for concurrent use; Endpoint/Model/HTTP must not be
// mutated after construction.
type OllamaClient struct {
	Endpoint string
	Model    string
	HTTP     *http.Client

	mu  sync.Mutex
	dim int
}

type ollamaSingleReq struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaSingleResp struct {
	Embedding []float32 `json:"embedding"`
	Error     string    `json:"error,omitempty"`
}

func (c *OllamaClient) endpoint() string {
	if c.Endpoint == "" {
		return defaultEndpoint
	}
	return strings.TrimRight(c.Endpoint, "/")
}

func (c *OllamaClient) model() string {
	if c.Model == "" {
		return defaultModel
	}
	return c.Model
}

// defaultOllamaHTTPClient is the package-level fallback when no HTTP is set
// on an OllamaClient. Sharing one *http.Client across the process gives us
// one connection pool, one set of TLS sessions, and no per-call allocation
// (round-3 review m6).
var defaultOllamaHTTPClient = &http.Client{Timeout: defaultTimeout}

func (c *OllamaClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultOllamaHTTPClient
}

// Embed returns one Vector per input text. It calls /api/embeddings once per
// text; the response shape is {"embedding": [...]}. The first successful
// response sets Dim().
func (c *OllamaClient) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	out := make([]Vector, len(texts))
	for i, text := range texts {
		v, err := c.embedOne(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("embed text %d via %s/%s: %w", i, c.endpoint(), c.model(), err)
		}
		out[i] = v
		c.mu.Lock()
		if c.dim == 0 {
			c.dim = len(v)
		}
		c.mu.Unlock()
	}
	return out, nil
}

func (c *OllamaClient) embedOne(ctx context.Context, text string) (Vector, error) {
	body, err := json.Marshal(ollamaSingleReq{Model: c.model(), Prompt: text})
	if err != nil {
		return nil, err
	}
	url := c.endpoint() + "/api/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOllamaRespSize))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("ollama %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed ollamaSingleResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("ollama error: %s", parsed.Error)
	}
	if len(parsed.Embedding) == 0 {
		return nil, fmt.Errorf("ollama returned empty embedding")
	}
	return Vector(parsed.Embedding), nil
}

// Dim returns the embedding dimension. Returns 0 until the first successful Embed call. Safe for concurrent use.
func (c *OllamaClient) Dim() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dim
}
