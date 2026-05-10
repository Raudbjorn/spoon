package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OllamaClient hits POST {Endpoint}/api/embeddings on a local Ollama instance.
type OllamaClient struct {
	Endpoint string
	Model    string
	HTTP     *http.Client
	dim      int
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

func (c *OllamaClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: defaultTimeout}
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
		if c.dim == 0 {
			c.dim = len(v)
		}
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
	raw, err := io.ReadAll(resp.Body)
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

// Dim returns the embedding dimensionality. Zero until the first successful
// Embed call.
func (c *OllamaClient) Dim() int {
	return c.dim
}
