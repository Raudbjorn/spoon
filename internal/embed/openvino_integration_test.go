package embed

import (
	"context"
	"math"
	"os"
	"testing"
)

// TestOpenVINOEmbedder_RealModel exercises the full tokenize→encode→pool
// path against a real exported model. Gated on SPOON_OPENVINO_TEST_MODEL
// pointing at an OVMS-style model dir; set SPOON_OPENVINO_TEST_DEVICE to
// override the default GPU device (e.g. CPU for machines without one).
func TestOpenVINOEmbedder_RealModel(t *testing.T) {
	modelDir := os.Getenv("SPOON_OPENVINO_TEST_MODEL")
	if modelDir == "" {
		t.Skip("SPOON_OPENVINO_TEST_MODEL not set; skipping real-model test")
	}
	if !OpenVINOAvailable() {
		t.Skip("OpenVINO runtime not loadable; skipping real-model test")
	}
	device := os.Getenv("SPOON_OPENVINO_TEST_DEVICE")

	e, err := NewOpenVINOEmbedder(OpenVINOConfig{ModelPath: modelDir, Device: device})
	if err != nil {
		t.Fatalf("NewOpenVINOEmbedder: %v", err)
	}
	defer e.Close()

	texts := []string{
		"add oauth2 token refresh to the auth middleware",
		"fix oauth token refresh handling in auth middleware",
		"bump dockerfile base image and ci cache settings",
		"", // empty input must not error
	}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(vecs), len(texts))
	}

	dim := e.Dim()
	if dim <= 0 {
		t.Fatalf("Dim() = %d after embedding", dim)
	}
	for i, v := range vecs {
		if len(v) != dim {
			t.Fatalf("vector %d: len %d, want %d", i, len(v), dim)
		}
	}

	// Normalized output: |v| ≈ 1 for non-empty inputs.
	norm := func(v Vector) float64 {
		var s float64
		for _, x := range v {
			s += float64(x) * float64(x)
		}
		return math.Sqrt(s)
	}
	for i := 0; i < 3; i++ {
		if n := norm(vecs[i]); math.Abs(n-1) > 1e-3 {
			t.Errorf("vector %d norm = %v, want ~1", i, n)
		}
	}

	cos := func(a, b Vector) float64 {
		var s float64
		for i := range a {
			s += float64(a[i]) * float64(b[i])
		}
		return s
	}
	same, diff := cos(vecs[0], vecs[1]), cos(vecs[0], vecs[2])
	t.Logf("dim=%d cos(same-intent)=%.4f cos(diff-intent)=%.4f", dim, same, diff)
	if same <= diff {
		t.Errorf("same-intent cosine (%v) should exceed different-intent cosine (%v)", same, diff)
	}

	// Batching boundary: a second call must produce identical vectors for
	// identical input (deterministic inference).
	again, err := e.Embed(context.Background(), texts[:1])
	if err != nil {
		t.Fatalf("re-embed: %v", err)
	}
	if c := cos(vecs[0], again[0]); c < 0.9999 {
		t.Errorf("re-embedding the same text diverged: cos = %v", c)
	}
}
