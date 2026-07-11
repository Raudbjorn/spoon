package cluster

import (
	"math"
	"strings"
	"testing"
)

func validFixtureConfig() Config {
	return Config{
		EmbedderID:        "builtin",
		PreprocessID:      "v1",
		Weights:           [4]float32{0.3, 0.3, 0.2, 0.2},
		WeakSignalsID:     "v1",
		Epsilon:           0.55,
		MinClusterSize:    3,
		TopN:              50,
		MinimumCandidates: 10,
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "valid", mutate: func(*Config) {}},
		{
			name:    "zero epsilon",
			mutate:  func(c *Config) { c.Epsilon = 0 },
			wantErr: "epsilon",
		},
		{
			name:    "epsilon at upper bound",
			mutate:  func(c *Config) { c.Epsilon = 2 },
			wantErr: "epsilon",
		},
		{
			name:    "NaN epsilon",
			mutate:  func(c *Config) { c.Epsilon = math.NaN() },
			wantErr: "epsilon",
		},
		{
			name:    "positive infinite epsilon",
			mutate:  func(c *Config) { c.Epsilon = math.Inf(1) },
			wantErr: "epsilon",
		},
		{
			name:    "min cluster size below two",
			mutate:  func(c *Config) { c.MinClusterSize = 1 },
			wantErr: "minClusterSize",
		},
		{
			name:    "non-positive topN",
			mutate:  func(c *Config) { c.TopN = 0 },
			wantErr: "topN",
		},
		{
			name:    "non-positive minimum candidates",
			mutate:  func(c *Config) { c.MinimumCandidates = 0 },
			wantErr: "minimumCandidates",
		},
		{
			name:    "zero weights",
			mutate:  func(c *Config) { c.Weights = [4]float32{} },
			wantErr: "weights",
		},
		{
			name: "NaN weight",
			mutate: func(c *Config) {
				c.Weights[0] = float32(math.NaN())
			},
			wantErr: "weights",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validFixtureConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestConfigFingerprint(t *testing.T) {
	base := validFixtureConfig()
	same := validFixtureConfig()
	if base.Fingerprint() != same.Fingerprint() {
		t.Error("identical configs must share a fingerprint")
	}

	changed := validFixtureConfig()
	changed.Epsilon = 0.35
	if base.Fingerprint() == changed.Fingerprint() {
		t.Error("changed epsilon must change the fingerprint")
	}
}
