// Package embed provides the Embedder abstraction, the built-in in-process
// lexical embedder, per-fork feature builders, and a multi-modal combiner.
// It is the foundation for embedding-driven fork clustering and novelty
// scoring.
//
// Every default path runs in-process: LocalEmbedder needs nothing installed and
// FastEmbedEmbedder runs a fixed BGE model over ONNX Runtime. The one exception
// is the optional Voyage AI provider (voyage.go, voyageembed.go, voyagerank.go),
// which calls an external service when an API key is configured. It is additive:
// Voyage indexes alongside fastembed rather than replacing it, and clustering
// never uses it.
package embed

import "context"

// Vector is a fixed-length float32 embedding.
type Vector []float32

// Embedder is the abstraction over an embedding implementation.
//
// Dim reports the per-text vector width. Embed must be safe to call
// concurrently from multiple goroutines.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([]Vector, error)
	Dim() int
}

type SearchEmbedder interface {
	Embedder
	EmbedQuery(context.Context, string) (Vector, error)
	EmbedPassages(context.Context, []string) ([]Vector, error)
	ModelID() string
}
