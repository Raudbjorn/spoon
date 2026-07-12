package embed

import (
	"fmt"
	"strings"
)

// Backend names. fastembed is the only user-selectable persistent/semantic
// embedder; builtin remains available to internal callers (clustering, the
// --query lexical fallback) as the zero-setup, portable engine.
const (
	BackendBuiltin   = "builtin"
	BackendFastEmbed = "fastembed"
)

type BackendConfig struct {
	FastEmbed FastEmbedConfig
}

// SelectBackendConfig constructs an embedder for the named backend.
//
//	"" / "fastembed"      → the fixed fastembed model (semantic search default)
//	"builtin" / "lexical" → the zero-setup lexical embedder (internal use)
//
// It returns the embedder, its cache-identity string (used to key cluster
// caches), and a close func (no-op for the builtin backend). A fastembed
// backend that cannot initialize (e.g. onnxruntime not installed) is a
// fail-fast error; callers that want to degrade catch it and fall back to the
// lexical engine.
func SelectBackendConfig(backend string, cfg BackendConfig) (Embedder, string, func(), error) {
	switch strings.ToLower(backend) {
	case BackendBuiltin, "lexical":
		return LocalEmbedder{}, BuiltinModelName, func() {}, nil
	case "", BackendFastEmbed:
		e, err := NewFastEmbedEmbedder(cfg.FastEmbed)
		if err != nil {
			return nil, "", nil, err
		}
		return e, e.ModelID(), func() { _ = e.Close() }, nil
	default:
		return nil, "", nil, fmt.Errorf("unknown embedder backend %q (want %q or %q)", backend, BackendFastEmbed, BackendBuiltin)
	}
}
