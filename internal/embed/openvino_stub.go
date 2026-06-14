//go:build !openvino

package embed

import (
	"context"
	"fmt"
)

// OpenVINOAvailable reports whether this binary was built with the openvino
// build tag.
const OpenVINOAvailable = false

// OpenVINOEmbedder is unavailable in builds without the openvino tag.
type OpenVINOEmbedder struct{}

// NewOpenVINOEmbedder always errors: this binary was built without OpenVINO
// support.
func NewOpenVINOEmbedder(cfg OpenVINOConfig) (*OpenVINOEmbedder, error) {
	return nil, fmt.Errorf("this spoon binary was built without OpenVINO support; rebuild with `go build -tags openvino` (requires the OpenVINO C runtime)")
}

func (e *OpenVINOEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	return nil, fmt.Errorf("openvino embedder unavailable in this build")
}

func (e *OpenVINOEmbedder) Dim() int { return 0 }

// Close is a no-op in builds without the openvino tag.
func (e *OpenVINOEmbedder) Close() {}

// AvailableDevices returns nil in builds without the openvino tag.
func AvailableDevices() []string { return nil }
