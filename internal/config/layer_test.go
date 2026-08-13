package config

import (
	"errors"
	"testing"
)

func TestLoadedLayerStatesAreDistinct(t *testing.T) {
	cases := []struct {
		state            LayerState
		missing, invalid bool
	}{
		{LayerLoaded, false, false},
		{LayerMissing, true, false},
		{LayerDisabled, false, false},
		{LayerInvalid, false, true},
	}
	for _, tc := range cases {
		layer := loadedLayer("", false, tc.state, nil, errors.New("reason"))
		if layer.Missing() != tc.missing || layer.Invalid() != tc.invalid || layer.Disabled != (tc.state == LayerDisabled) {
			t.Fatalf("state %v not classified", tc.state)
		}
	}
}
