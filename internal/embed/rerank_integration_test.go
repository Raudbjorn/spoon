//go:build openvino

package embed

import (
	"context"
	"os"
	"testing"
)

// TestReranker_RealModel exercises the full pair-tokenize→cross-encode path
// against a real exported model. Gated on SPOON_OPENVINO_TEST_RERANKER
// pointing at an OVMS-style rerank model dir; SPOON_OPENVINO_TEST_DEVICE
// overrides the default GPU device.
func TestReranker_RealModel(t *testing.T) {
	modelDir := os.Getenv("SPOON_OPENVINO_TEST_RERANKER")
	if modelDir == "" {
		t.Skip("SPOON_OPENVINO_TEST_RERANKER not set; skipping real-model test")
	}
	r, err := NewReranker(RerankConfig{
		ModelPath: modelDir,
		Device:    os.Getenv("SPOON_OPENVINO_TEST_DEVICE"),
	})
	if err != nil {
		t.Fatalf("NewReranker: %v", err)
	}
	defer r.Close()

	query := "add wayland support to the compositor"
	docs := []string{
		"implement wayland protocol support and drop the x11-only code path in the compositor",
		"fix typo in the README installation section",
		"migrate ci pipeline from travis to github actions",
	}
	scores, err := r.Rerank(context.Background(), query, docs)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != len(docs) {
		t.Fatalf("got %d scores for %d docs", len(scores), len(docs))
	}
	t.Logf("scores: relevant=%.4f typo=%.4f ci=%.4f", scores[0], scores[1], scores[2])
	for i, s := range scores {
		if s < 0 || s > 1 {
			t.Errorf("score %d = %v outside [0,1]", i, s)
		}
	}
	if scores[0] <= scores[1] || scores[0] <= scores[2] {
		t.Errorf("relevant doc must outscore irrelevant ones: %v", scores)
	}

	// Stability across calls. Exact bit-equality is not expected: a batch
	// of 1 pads differently than a batch of 3, and fp16 GPU reductions
	// shift slightly with shape. Scores must still agree closely.
	again, err := r.Rerank(context.Background(), query, docs[:1])
	if err != nil {
		t.Fatalf("re-rank: %v", err)
	}
	if d := scores[0] - again[0]; d > 5e-3 || d < -5e-3 {
		t.Errorf("re-ranking diverged: %v vs %v", scores[0], again[0])
	}
}
