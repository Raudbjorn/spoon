package cluster

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// Config captures all clustering and representation parameters that affect
// output semantics. Two configs with the same fingerprint produce equivalent
// clusters given identical inputs.
type Config struct {
	// Embedder identity.
	EmbedderID   string `json:"embedderId"`
	PreprocessID string `json:"preprocessId"`

	// Modality weights are ordered as paths, commits, README, and diff.
	Weights       [4]float32 `json:"weights"`
	WeakSignalsID string     `json:"weakSignalsId"`

	// Clustering parameters.
	Epsilon           float64 `json:"epsilon"`
	MinClusterSize    int     `json:"minClusterSize"`
	TopN              int     `json:"topN"`
	MinimumCandidates int     `json:"minimumCandidates"`
}

// Validate checks that config is internally consistent.
func (c Config) Validate() error {
	// Negated range so NaN (which fails every comparison) is rejected too;
	// a NaN epsilon would otherwise slip through and break json.Marshal in
	// Fingerprint.
	if !(c.Epsilon > 0 && c.Epsilon < 2) {
		return fmt.Errorf("epsilon %.3f out of range (0, 2)", c.Epsilon)
	}
	if c.MinClusterSize < 2 {
		return fmt.Errorf("minClusterSize %d < 2", c.MinClusterSize)
	}
	if c.TopN < 1 {
		return fmt.Errorf("topN %d < 1", c.TopN)
	}
	if c.MinimumCandidates < 1 {
		return fmt.Errorf("minimumCandidates %d < 1", c.MinimumCandidates)
	}
	var sum float32
	for _, weight := range c.Weights {
		sum += weight
	}
	// Negated comparison so a NaN weight (sum becomes NaN) is rejected.
	if !(sum > 0) {
		return fmt.Errorf("modality weights sum to %.3f, want >0", sum)
	}
	return nil
}

// Fingerprint returns the SHA-256 hash of the config's canonical JSON encoding.
func (c Config) Fingerprint() string {
	data, err := json.Marshal(c)
	if err != nil {
		return "error"
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}
