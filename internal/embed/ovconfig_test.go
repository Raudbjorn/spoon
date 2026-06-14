package embed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeModelDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func minimalModelFiles() map[string]string {
	return map[string]string{
		"openvino_model.xml":     "<net/>",
		"openvino_tokenizer.xml": "<net/>",
	}
}

func TestOVConfigDefaults(t *testing.T) {
	dir := writeModelDir(t, minimalModelFiles())
	cfg, err := OpenVINOConfig{ModelPath: dir}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Device != "GPU" {
		t.Errorf("Device = %q, want GPU", cfg.Device)
	}
	if cfg.Pooling != PoolingCLS {
		t.Errorf("Pooling = %q, want cls", cfg.Pooling)
	}
	if !cfg.Normalize {
		t.Error("Normalize should default to true")
	}
	if cfg.MaxTokens != defaultOVMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", cfg.MaxTokens, defaultOVMaxTokens)
	}
	if cfg.MaxBatch != defaultOVMaxBatch {
		t.Errorf("MaxBatch = %d, want %d", cfg.MaxBatch, defaultOVMaxBatch)
	}
}

func TestOVConfigMissingModelFiles(t *testing.T) {
	if _, err := (OpenVINOConfig{ModelPath: t.TempDir()}).withDefaults(); err == nil {
		t.Fatal("expected error for empty model dir")
	}
	if _, err := (OpenVINOConfig{}).withDefaults(); err == nil {
		t.Fatal("expected error for unset model path")
	}
}

func TestOVConfigReadsGraphPBTxt(t *testing.T) {
	files := minimalModelFiles()
	files["graph.pbtxt"] = `
node {
    calculator: "EmbeddingsCalculatorOV"
    node_options: {
      [type.googleapis.com/mediapipe.EmbeddingsCalculatorOVOptions]: {
        models_path: "./"
        normalize_embeddings: false
        target_device: "GPU"
        pooling: MEAN
      }
    }
}`
	dir := writeModelDir(t, files)
	cfg, err := OpenVINOConfig{ModelPath: dir}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pooling != PoolingMean {
		t.Errorf("Pooling = %q, want mean (from graph.pbtxt)", cfg.Pooling)
	}
	if cfg.Normalize {
		t.Error("Normalize should be false (from graph.pbtxt)")
	}
	// Explicit settings beat graph.pbtxt.
	cfg2, err := OpenVINOConfig{ModelPath: dir, Pooling: PoolingCLS, Normalize: true, NormalizeSet: true}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Pooling != PoolingCLS || !cfg2.Normalize {
		t.Errorf("explicit settings must win: pooling=%q normalize=%v", cfg2.Pooling, cfg2.Normalize)
	}
}

func TestOVConfigReadsMaxPositionEmbeddings(t *testing.T) {
	files := minimalModelFiles()
	files["config.json"] = `{"model_type": "bert", "max_position_embeddings": 2048}`
	dir := writeModelDir(t, files)
	cfg, err := OpenVINOConfig{ModelPath: dir}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048 (from config.json)", cfg.MaxTokens)
	}
}

func TestOVConfigEmbedderID(t *testing.T) {
	files := minimalModelFiles()
	files["graph.pbtxt"] = "pooling: MEAN\n"
	dir := writeModelDir(t, files)

	id := OpenVINOConfig{ModelPath: dir}.EmbedderID()
	if !strings.HasPrefix(id, "openvino:") || !strings.HasSuffix(id, ":mean") {
		t.Errorf("EmbedderID = %q, want openvino:<abs path>:mean", id)
	}
	// Pooling override changes the cache identity.
	id2 := OpenVINOConfig{ModelPath: dir, Pooling: PoolingCLS}.EmbedderID()
	if id == id2 {
		t.Error("different pooling should yield different EmbedderID")
	}
}
