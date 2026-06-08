package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultOpenAIEmbeddingModel is the model used by the "openai" backend when
// none is given. It matches a model served by the reference OVMS deployment
// (and mirrors the Ollama default family), so the GPU-served path is a
// comparable swap for nomic-embed-text.
const DefaultOpenAIEmbeddingModel = "nomic-ai/nomic-embed-text-v1.5"

// OpenAIEmbedder talks to any OpenAI-compatible embeddings endpoint
// (POST {base}/embeddings). This covers OpenVINO Model Server (OVMS) — which
// serves embedding models on an Intel GPU via /v3/embeddings — as well as
// vLLM, LocalAI, and the OpenAI API itself. It implements the Embedder
// interface.
type OpenAIEmbedder struct {
	Endpoint string       // base URL, e.g. "http://localhost:8978" or "https://api.openai.com/v1"
	Model    string       // model id, e.g. "nomic-ai/nomic-embed-text-v1.5"
	APIKey   string       // optional; falls back to $OPENAI_API_KEY. Sent as Bearer.
	HTTP     *http.Client // optional; defaults to a 5-minute-timeout client

	mu  sync.Mutex
	dim int
}

var defaultOpenAIHTTPClient = &http.Client{Timeout: 5 * time.Minute}

func (e *OpenAIEmbedder) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return defaultOpenAIHTTPClient
}

// baseURL returns the versioned base. OpenAI/vLLM callers pass a base ending in
// "/v1"; OVMS uses "/v3". When no version segment is present we default to
// "/v3" (the OVMS convention), so `--embedder http://localhost:8978` works.
func (e *OpenAIEmbedder) baseURL() string {
	b := strings.TrimRight(e.Endpoint, "/")
	if !strings.HasSuffix(b, "/v1") && !strings.HasSuffix(b, "/v3") {
		b += "/v3"
	}
	return b
}

func (e *OpenAIEmbedder) apiKey() string {
	if e.APIKey != "" {
		return e.APIKey
	}
	return os.Getenv("OPENAI_API_KEY")
}

type openAIEmbedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbedResp struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed POSTs to {base}/embeddings and returns one Vector per input text,
// ordered to match the input (the response is sorted by index defensively).
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	body, err := json.Marshal(openAIEmbedReq{Model: e.Model, Input: texts})
	if err != nil {
		return nil, err
	}
	url := e.baseURL() + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := e.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
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
		return nil, fmt.Errorf("openai embeddings %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed openAIEmbedResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode openai embeddings response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("openai embeddings returned %d vectors for %d texts", len(parsed.Data), len(texts))
	}
	// Order by index so output aligns with input regardless of server ordering.
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	out := make([]Vector, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = Vector(d.Embedding)
	}
	if len(out) > 0 {
		e.mu.Lock()
		if e.dim == 0 {
			e.dim = len(out[0])
		}
		e.mu.Unlock()
	}
	return out, nil
}

// Dim returns the embedding dimension, learned from the first Embed or
// HealthCheck call. Returns 0 before then. Safe for concurrent use.
func (e *OpenAIEmbedder) Dim() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dim
}

// HealthCheck verifies the endpoint is reachable and that e.Model is served,
// by GETting {base}/models. Returns nil when the model is listed.
func (e *OpenAIEmbedder) HealthCheck(ctx context.Context) error {
	url := e.baseURL() + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if key := e.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("openai models probe at %s: %w", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOllamaRespSize))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("openai %s returned %d", url, resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// A 2xx without a parseable model list still means the endpoint is up;
		// don't fail the health check on body shape alone.
		return nil
	}
	for _, m := range parsed.Data {
		if m.ID == e.Model {
			return nil
		}
	}
	if len(parsed.Data) == 0 {
		return nil // endpoint up but lists no models; let Embed surface specifics
	}
	served := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		served = append(served, m.ID)
	}
	return fmt.Errorf("model %q not served at %s (available: %s)", e.Model, url, strings.Join(served, ", "))
}

// ServedModels returns the model ids advertised by an OpenAI-compatible
// endpoint's {base}/models, for diagnostics (e.g. `spoon setup`). A non-nil
// error means the endpoint was unreachable or returned non-2xx.
func (e *OpenAIEmbedder) ServedModels(ctx context.Context) ([]string, error) {
	url := e.baseURL() + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if key := e.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
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
		return nil, fmt.Errorf("openai %s returned %d", url, resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		out = append(out, m.ID)
	}
	return out, nil
}
