// Package embed provides an Embedder abstraction and a default Ollama
// HTTP client, plus per-fork feature builders and a multi-modal combiner.
// It is the foundation for embedding-driven fork clustering and novelty
// scoring.
package embed

import (
	"context"
	"net/http"
	"os"
	"time"
)

// Vector is a fixed-length float32 embedding.
type Vector []float32

// Embedder is the abstraction over any embedding provider.
//
// Dim() may return 0 until the first successful Embed call. Implementations
// should populate Dim once a real embedding has been seen. Embed must be safe
// to call concurrently from multiple goroutines.
// Implementations must use atomic/mutex synchronization to set Dim once
// populated; concurrent reads must be safe.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([]Vector, error)
	Dim() int
}

// ModelSuggestion describes a known-good embedding model.
type ModelSuggestion struct {
	Name      string
	SizeMB    int
	Dim       int
	Default   bool
	OnOllama  bool
	CodeAware bool
}

// PreferredEmbeddingModels is the ranked list, best-quality-first per category.
// Read-only. Do not mutate — use PreferredEmbeddingModelsCopy() for any
// mutating caller. Retained as an exported var for backwards compatibility.
var PreferredEmbeddingModels = []ModelSuggestion{
	{Name: "nomic-embed-text", SizeMB: 274, Dim: 768, Default: true, OnOllama: true},
	{Name: "snowflake-arctic-embed2", SizeMB: 1200, Dim: 1024, OnOllama: true}, // arctic-embed-l-v2.0 on Ollama
	{Name: "mxbai-embed-large", SizeMB: 670, Dim: 1024, OnOllama: true},
	{Name: "bge-m3", SizeMB: 1200, Dim: 1024, OnOllama: true},
	{Name: "snowflake-arctic-embed", SizeMB: 670, Dim: 1024, OnOllama: true},
	{Name: "jina-embeddings-v2-base-code", Dim: 768, CodeAware: true},
	{Name: "codebert-base", Dim: 768, CodeAware: true},
	{Name: "unixcoder-base", Dim: 768, CodeAware: true},
}

// PreferredEmbeddingModelsCopy returns a defensive copy of
// PreferredEmbeddingModels. Use this in any caller that may mutate the
// returned slice (e.g., reordering, appending).
func PreferredEmbeddingModelsCopy() []ModelSuggestion {
	out := make([]ModelSuggestion, len(PreferredEmbeddingModels))
	copy(out, PreferredEmbeddingModels)
	return out
}

const (
	defaultEndpoint = "http://localhost:11434"
	defaultModel    = "nomic-embed-text"
	envEndpoint     = "SPOON_EMBEDDER_URL"
	envModel        = "SPOON_EMBEDDER_MODEL"
	defaultTimeout  = 30 * time.Second
)

// NewFromEnv returns an OllamaClient configured from SPOON_EMBEDDER_URL and
// SPOON_EMBEDDER_MODEL, falling back to http://localhost:11434 and
// "nomic-embed-text".
func NewFromEnv() Embedder {
	endpoint := os.Getenv(envEndpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	model := os.Getenv(envModel)
	if model == "" {
		model = defaultModel
	}
	return &OllamaClient{
		Endpoint: endpoint,
		Model:    model,
		HTTP:     &http.Client{Timeout: defaultTimeout},
	}
}
