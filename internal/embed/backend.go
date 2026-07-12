package embed

import (
	"fmt"
	"strings"
)

// Backend names accepted by SelectBackend (and the --embedder-backend flag).
const (
	BackendBuiltin   = "builtin"
	BackendOpenVINO  = "openvino"
	BackendFastEmbed = "fastembed"
)

type BackendConfig struct {
	OpenVINO  OpenVINOConfig
	FastEmbed FastEmbedConfig
}

// SelectBackend constructs the embedder for the named backend.
//
//	"" / "builtin" / "lexical" → the zero-setup lexical embedder
//	"openvino"                 → in-process OpenVINO encoder (cfg required)
//
// It returns the embedder, its cache-identity string (used to key cluster
// caches), and a close func (no-op for the builtin backend). Errors are
// fail-fast: an openvino backend that cannot load its model must abort the
// run rather than silently fall back, so a misconfigured GPU setup is never
// mistaken for lexical clustering.
func SelectBackend(backend string, cfg OpenVINOConfig) (Embedder, string, func(), error) {
	return SelectBackendConfig(backend, BackendConfig{OpenVINO: cfg})
}

func SelectBackendConfig(backend string, cfg BackendConfig) (Embedder, string, func(), error) {
	switch strings.ToLower(backend) {
	case "", BackendBuiltin, "lexical":
		return LocalEmbedder{}, BuiltinModelName, func() {}, nil
	case BackendOpenVINO:
		e, err := NewOpenVINOEmbedder(cfg.OpenVINO)
		if err != nil {
			return nil, "", nil, err
		}
		return e, cfg.OpenVINO.EmbedderID(), e.Close, nil
	case BackendFastEmbed:
		e, err := NewFastEmbedEmbedder(cfg.FastEmbed)
		if err != nil {
			return nil, "", nil, err
		}
		return e, e.ModelID(), func() { _ = e.Close() }, nil
	default:
		return nil, "", nil, fmt.Errorf("unknown embedder backend %q (want %q, %q, %q, or %q)", backend, BackendBuiltin, "lexical", BackendOpenVINO, BackendFastEmbed)
	}
}
