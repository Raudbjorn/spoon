//go:build !openvino

package embed

import (
	"context"
	"fmt"
)

// Reranker is unavailable in builds without the openvino tag.
type Reranker struct{}

// NewReranker always errors: this binary was built without OpenVINO support.
func NewReranker(cfg RerankConfig) (*Reranker, error) {
	return nil, fmt.Errorf("this spoon binary was built without OpenVINO support; rebuild with `go build -tags openvino` to use the reranker")
}

func (r *Reranker) Rerank(ctx context.Context, query string, docs []string) ([]float64, error) {
	return nil, fmt.Errorf("openvino reranker unavailable in this build")
}

// Close is a no-op in builds without the openvino tag.
func (r *Reranker) Close() {}
