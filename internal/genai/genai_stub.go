//go:build !genai

package genai

import (
	"context"
	"fmt"
)

// Available reports whether this binary was built with the genai build tag.
const Available = false

// Generator is unavailable in builds without the genai tag.
type Generator struct{}

// New always errors: this binary was built without GenAI support.
func New(cfg Config) (*Generator, error) {
	return nil, fmt.Errorf("this spoon binary was built without GenAI support; rebuild with `go build -tags \"openvino genai\"` (requires the openvino-genai runtime)")
}

func (g *Generator) Generate(ctx context.Context, system, user string) (string, error) {
	return "", fmt.Errorf("genai generator unavailable in this build")
}

// Close is a no-op in builds without the genai tag.
func (g *Generator) Close() {}
