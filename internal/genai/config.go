package genai

import (
	"fmt"
	"os"
	"path/filepath"
)

// Config configures the in-process GenAI generator.
type Config struct {
	// ModelPath is an OpenVINO-converted LLM directory (as downloaded by
	// `spoon setup`, e.g. OpenVINO/Qwen2.5-1.5B-Instruct-int4-ov).
	ModelPath string

	// Device runs the model ("GPU" default).
	Device string

	// MaxNewTokens caps the reply length. 0 → 24 (labels are short).
	MaxNewTokens int

	// CacheDir holds compiled GPU kernels. Empty → ~/.cache/spoon/openvino.
	CacheDir string
}

const defaultMaxNewTokens = 24

func (c Config) withDefaults() (Config, error) {
	if c.ModelPath == "" {
		return c, fmt.Errorf("genai: model path is required (run 'spoon setup' to download the default labeler)")
	}
	if _, err := os.Stat(filepath.Join(c.ModelPath, "openvino_model.xml")); err != nil {
		return c, fmt.Errorf("genai: openvino_model.xml not found in %s: %w", c.ModelPath, err)
	}
	if c.Device == "" {
		c.Device = "GPU"
	}
	if c.MaxNewTokens == 0 {
		c.MaxNewTokens = defaultMaxNewTokens
	}
	if c.CacheDir == "" {
		c.CacheDir = defaultCacheDir()
	}
	return c, nil
}

func defaultCacheDir() string {
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
