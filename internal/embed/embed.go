// Package embed provides the Embedder abstraction, the built-in in-process
// lexical embedder, per-fork feature builders, and a multi-modal combiner.
// It is the foundation for embedding-driven fork clustering and novelty
// scoring. No external services are involved: embedding runs entirely
// in-process (see LocalEmbedder).
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
