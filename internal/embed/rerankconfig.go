package embed

import (
	"fmt"
	"os"
	"path/filepath"
)

// RerankConfig configures the in-process OpenVINO cross-encoder reranker.
// The model directory follows the same OVMS layout as embeddings
// (openvino_model.xml + openvino_tokenizer.xml, as produced by
// `ovms --pull --task rerank` or spoon's own model downloader).
type RerankConfig struct {
	// ModelPath is the cross-encoder model directory.
	ModelPath string

	// Device runs the cross-encoder ("GPU" default; tokenizer stays on CPU).
	Device string

	// MaxTokens caps each assembled (query, document) row. 0 → the model
	// dir config.json's max_position_embeddings, else 512.
	MaxTokens int

	// MaxBatch caps pairs per inference call. 0 → 8.
	MaxBatch int

	// TokenizersLib / CacheDir configure the OpenVINO runtime (tokenizer
	// extension library path and model compile cache directory).
	TokenizersLib string
	CacheDir      string
}

const defaultRerankMaxBatch = 8

func (c RerankConfig) withDefaults() (RerankConfig, error) {
	if c.ModelPath == "" {
		return c, fmt.Errorf("openvino reranker: model path is required")
	}
	for _, f := range []string{"openvino_model.xml", "openvino_tokenizer.xml"} {
		if _, err := os.Stat(filepath.Join(c.ModelPath, f)); err != nil {
			return c, fmt.Errorf("openvino reranker: %s not found in %s (run 'spoon setup' to download the default reranker): %w", f, c.ModelPath, err)
		}
	}
	if c.Device == "" {
		c.Device = "GPU"
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = maxPositionEmbeddings(filepath.Join(c.ModelPath, "config.json"))
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = defaultOVMaxTokens
	}
	if c.MaxBatch == 0 {
		c.MaxBatch = defaultRerankMaxBatch
	}
	if c.TokenizersLib == "" {
		c.TokenizersLib = ResolveTokenizersLib()
	}
	if c.CacheDir == "" {
		c.CacheDir = defaultOVCacheDir()
	}
	return c, nil
}

// RerankerID identifies this reranker configuration (for output metadata).
func (c RerankConfig) RerankerID() string {
	abs, err := filepath.Abs(c.ModelPath)
	if err != nil {
		abs = c.ModelPath
	}
	return "openvino:" + abs
}
