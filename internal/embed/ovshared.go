package embed

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Shared OpenVINO runtime configuration helpers used by the in-process
// reranker (RerankConfig). The OpenVINO embedding backend was removed; these
// helpers survived because the reranker resolves its tokenizer library,
// compile cache, and default token budget the same way the embedder did.

const defaultOVMaxTokens = 512

// tokenizersLibSearchPath lists where ResolveTokenizersLib looks for
// libopenvino_tokenizers.so, in order. The bare soname comes last so a
// library on the regular loader path still wins over "not found".
var tokenizersLibSearchPath = []string{
	"/usr/lib/libopenvino_tokenizers.so",
	"/usr/lib64/libopenvino_tokenizers.so",
	"/opt/intel/openvino-genai/lib/libopenvino_tokenizers.so",
	"/usr/lib/ovms/lib/libopenvino_tokenizers.so",
}

// ResolveTokenizersLib returns the first existing well-known location of
// libopenvino_tokenizers.so, or the bare soname (resolved by the dynamic
// loader) when none is found.
func ResolveTokenizersLib() string {
	for _, p := range tokenizersLibSearchPath {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "libopenvino_tokenizers.so"
}

func defaultOVCacheDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".cache")
		}
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, "spoon", "openvino")
}

// maxPositionEmbeddings reads max_position_embeddings from a HuggingFace
// config.json. Returns 0 when absent or unreadable.
func maxPositionEmbeddings(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var cfg struct {
		MaxPositionEmbeddings int `json:"max_position_embeddings"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0
	}
	if cfg.MaxPositionEmbeddings < 0 {
		return 0
	}
	return cfg.MaxPositionEmbeddings
}
