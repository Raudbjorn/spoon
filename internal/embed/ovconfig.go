package embed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// OpenVINOConfig configures the in-process OpenVINO embedding backend. The
// backend loads an OVMS-style model directory (as produced by
// `ovms --pull --task embeddings` or `optimum-cli export openvino`) and runs
// both the tokenizer and the encoder inside the spoon process — no server.
type OpenVINOConfig struct {
	// ModelPath is the directory holding openvino_model.xml/.bin and
	// openvino_tokenizer.xml/.bin (plus optional config.json / graph.pbtxt).
	ModelPath string

	// Device is the OpenVINO device for the encoder ("GPU", "CPU",
	// "GPU.1", ...). The tokenizer always runs on CPU. Default "GPU".
	Device string

	// Pooling selects the hidden-state reduction. Empty → the model dir's
	// graph.pbtxt value when present, else CLS (the OVMS default).
	Pooling Pooling

	// Normalize L2-normalizes pooled vectors. Defaults to true (OVMS
	// default); only a graph.pbtxt with `normalize_embeddings: false`
	// turns it off when the caller leaves NormalizeSet false.
	Normalize    bool
	NormalizeSet bool

	// MaxTokens caps tokenized sequence length. 0 → the model dir
	// config.json's max_position_embeddings, else 512.
	MaxTokens int

	// MaxBatch caps how many texts are tokenized+inferred per inference
	// call. 0 → 16.
	MaxBatch int

	// TokenizersLib is the path (or dlopen-able soname) of
	// libopenvino_tokenizers.so. Empty → searched in well-known locations.
	TokenizersLib string

	// CacheDir is the OpenVINO compiled-kernel cache directory. On Intel
	// Arc the first GPU compile of an encoder takes minutes; the cache
	// makes subsequent loads near-instant. Empty → ~/.cache/spoon/openvino.
	CacheDir string
}

const (
	defaultOVMaxTokens = 512
	defaultOVMaxBatch  = 16
)

// tokenizersLibSearchPath lists where ResolveTokenizersLib looks for
// libopenvino_tokenizers.so, in order. The bare soname comes last so a
// library on the regular loader path still wins over "not found".
var tokenizersLibSearchPath = []string{
	"/usr/lib/libopenvino_tokenizers.so",
	"/usr/lib64/libopenvino_tokenizers.so",
	"/opt/intel/openvino-genai/lib/libopenvino_tokenizers.so",
	"/usr/lib/ovms/lib/libopenvino_tokenizers.so",
}

// withDefaults fills unset fields from the model directory's metadata
// (config.json, graph.pbtxt) and built-in defaults, and validates that the
// model files exist. It does not touch OpenVINO itself, so it is testable
// without the openvino build tag.
func (c OpenVINOConfig) withDefaults() (OpenVINOConfig, error) {
	if c.ModelPath == "" {
		return c, fmt.Errorf("openvino: model path is required (--openvino-model)")
	}
	for _, f := range []string{"openvino_model.xml", "openvino_tokenizer.xml"} {
		if _, err := os.Stat(filepath.Join(c.ModelPath, f)); err != nil {
			return c, fmt.Errorf("openvino: %s not found in %s (export the model with `ovms --pull --task embeddings` or `optimum-cli export openvino`): %w", f, c.ModelPath, err)
		}
	}
	if c.Device == "" {
		c.Device = "GPU"
	}

	graphPooling, graphNormalize, graphNormalizeSet := parseGraphPBTxt(filepath.Join(c.ModelPath, "graph.pbtxt"))
	if c.Pooling == "" {
		c.Pooling = graphPooling
	}
	if c.Pooling == "" {
		c.Pooling = PoolingCLS
	}
	if !c.NormalizeSet {
		if graphNormalizeSet {
			c.Normalize = graphNormalize
		} else {
			c.Normalize = true
		}
		c.NormalizeSet = true
	}

	if c.MaxTokens == 0 {
		c.MaxTokens = maxPositionEmbeddings(filepath.Join(c.ModelPath, "config.json"))
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = defaultOVMaxTokens
	}
	if c.MaxBatch == 0 {
		c.MaxBatch = defaultOVMaxBatch
	}
	if c.TokenizersLib == "" {
		c.TokenizersLib = ResolveTokenizersLib()
	}
	if c.CacheDir == "" {
		c.CacheDir = defaultOVCacheDir()
	}
	return c, nil
}

// EmbedderID returns the cluster-cache model identifier for this
// configuration. Pooling changes the geometry, so it is part of the key;
// device is not (same model+pooling → same vectors).
func (c OpenVINOConfig) EmbedderID() string {
	abs, err := filepath.Abs(c.ModelPath)
	if err != nil {
		abs = c.ModelPath
	}
	pooling := c.Pooling
	if pooling == "" {
		if p, _, _ := parseGraphPBTxt(filepath.Join(c.ModelPath, "graph.pbtxt")); p != "" {
			pooling = p
		} else {
			pooling = PoolingCLS
		}
	}
	return fmt.Sprintf("openvino:%s:%s", abs, pooling)
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

var (
	graphPoolingRe   = regexp.MustCompile(`(?m)^\s*pooling\s*:\s*(CLS|MEAN|LAST)\b`)
	graphNormalizeRe = regexp.MustCompile(`(?m)^\s*normalize_embeddings\s*:\s*(true|false)\b`)
)

// parseGraphPBTxt extracts the pooling and normalize_embeddings options from
// an OVMS embeddings graph.pbtxt (written by `ovms --pull --task
// embeddings`), so a model exported for OVMS carries its intended pooling
// into spoon without extra flags. Missing or unparsable files yield zero
// values.
func parseGraphPBTxt(path string) (pooling Pooling, normalize, normalizeSet bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, false
	}
	if m := graphPoolingRe.FindSubmatch(data); m != nil {
		pooling, _ = ParsePooling(strings.ToLower(string(m[1])))
	}
	if m := graphNormalizeRe.FindSubmatch(data); m != nil {
		normalize = string(m[1]) == "true"
		normalizeSet = true
	}
	return pooling, normalize, normalizeSet
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
