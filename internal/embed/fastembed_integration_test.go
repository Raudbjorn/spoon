package embed

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestFastEmbedIntegration(t *testing.T) {
	if os.Getenv("ONNX_PATH") == "" {
		t.Skip("ONNX_PATH not set")
	}
	model, err := NewFastEmbedEmbedder(FastEmbedConfig{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	first, err := model.EmbedQuery(context.Background(), "oauth rate limiting")
	if err != nil {
		t.Fatal(err)
	}
	second, err := model.EmbedQuery(context.Background(), "oauth rate limiting")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != FastEmbedDimension || len(second) != FastEmbedDimension {
		t.Fatalf("dimensions %d and %d", len(first), len(second))
	}
	var norm, cosine float64
	for i := range first {
		if math.IsNaN(float64(first[i])) || math.IsInf(float64(first[i]), 0) {
			t.Fatalf("non-finite value at %d", i)
		}
		norm += float64(first[i]) * float64(first[i])
		cosine += float64(first[i]) * float64(second[i])
	}
	if math.Abs(norm-1) > 1e-4 || math.Abs(cosine-1) > 1e-4 {
		t.Fatalf("norm=%f repeat cosine=%f", norm, cosine)
	}
}
