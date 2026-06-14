package heat

import (
	"encoding/json"
	"fmt"
	"os"
)

// ValidWeightKeys are the component names accepted in a heat-weights file.
var ValidWeightKeys = map[string]bool{
	"recency": true, "stars": true, "sub_forks": true, "releases": true,
	"mna": true, "sync_ratio": true, "feature_ratio": true,
	"lone_wolf": true, "span": true, "novelty": true,
}

func loadWeightsFile(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}
	var weights map[string]float64
	if err := json.Unmarshal(data, &weights); err != nil {
		return nil, fmt.Errorf("parsing JSON: %w", err)
	}
	for key, val := range weights {
		if !ValidWeightKeys[key] {
			return nil, fmt.Errorf("unknown key %q", key)
		}
		if val < 0 || val > 2.0 {
			return nil, fmt.Errorf("value for %q must be in [0.0, 2.0], got %v", key, val)
		}
	}
	return weights, nil
}
