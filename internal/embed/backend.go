package embed

import (
	"fmt"
	"strings"
)

// Backend names accepted by SelectBackend (and the --embedder-backend flag).
const (
	BackendBuiltin  = "builtin"
	BackendOpenVINO = "openvino"
)

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
	switch strings.ToLower(backend) {
	case "", BackendBuiltin, "lexical":
		return LocalEmbedder{}, BuiltinModelName, func() {}, nil
	case BackendOpenVINO:
		e, err := NewOpenVINOEmbedder(cfg)
		if err != nil {
			return nil, "", nil, err
		}
		return e, cfg.EmbedderID(), e.Close, nil
	default:
		return nil, "", nil, fmt.Errorf("unknown embedder backend %q (want %q or %q)", backend, BackendBuiltin, BackendOpenVINO)
	}
}
