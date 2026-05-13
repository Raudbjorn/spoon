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
	"time"
)

// SidecarEmbedder talks HTTP to a Python sidecar process loading
// Snowflake/snowflake-arctic-embed-l-v2.0 (or any drop-in successor).
// It implements the Embedder interface.
type SidecarEmbedder struct {
	Endpoint string       // e.g. "http://localhost:8765"
	HTTP     *http.Client // optional; defaults to defaultSidecarHTTPClient

	mu  sync.Mutex
	dim int
}

// defaultSidecarHTTPClient is the package-level fallback for SidecarEmbedder
// when no HTTP is set. The timeout is much longer than Ollama's because
// sidecar embeds run a 568M-parameter model on CPU: a batch of ~50 forks
// with ~5 KB prompts can legitimately take 1-3 minutes. Sharing one client
// across the process gives a single connection pool and avoids per-call
// allocation.
var defaultSidecarHTTPClient = &http.Client{Timeout: 5 * time.Minute}

type sidecarEmbedReq struct {
	Texts []string `json:"texts"`
}

type sidecarEmbedResp struct {
	Vectors [][]float32 `json:"vectors"`
	Dim     int         `json:"dim"`
}

func (e *SidecarEmbedder) endpoint() string {
	return strings.TrimRight(e.Endpoint, "/")
}

func (e *SidecarEmbedder) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return defaultSidecarHTTPClient
}

// Embed POSTs to {endpoint}/embed. Returns one Vector per input text.
func (e *SidecarEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	body, err := json.Marshal(sidecarEmbedReq{Texts: texts})
	if err != nil {
		return nil, err
	}
	url := e.endpoint() + "/embed"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOllamaRespSize))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("sidecar %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed sidecarEmbedResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode sidecar response: %w", err)
	}
	if len(parsed.Vectors) != len(texts) {
		return nil, fmt.Errorf("sidecar returned %d vectors for %d texts", len(parsed.Vectors), len(texts))
	}
	out := make([]Vector, len(parsed.Vectors))
	for i, v := range parsed.Vectors {
		out[i] = Vector(v)
	}
	e.mu.Lock()
	if e.dim == 0 && parsed.Dim > 0 {
		e.dim = parsed.Dim
	}
	e.mu.Unlock()
	return out, nil
}

// Dim returns the embedding dimension. Returns 0 until the first successful
// Embed or HealthCheck call. Safe for concurrent use.
func (e *SidecarEmbedder) Dim() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dim
}

// HealthCheck GETs {endpoint}/health and populates Dim() from the response.
// Returns nil iff the sidecar reports ready.
func (e *SidecarEmbedder) HealthCheck(ctx context.Context) error {
	url := e.endpoint() + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("sidecar health probe at %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sidecar %s returned %d", url, resp.StatusCode)
	}
	var parsed struct {
		Dim int `json:"dim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err == nil && parsed.Dim > 0 {
		e.mu.Lock()
		e.dim = parsed.Dim
		e.mu.Unlock()
	}
	return nil
}
