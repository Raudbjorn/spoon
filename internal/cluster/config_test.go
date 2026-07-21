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
		{
			name: "negative weight",
			mutate: func(c *Config) {
				c.Weights[0] = -0.1
			},
			wantErr: "negative",
		},
		{
			// A single +Inf weight makes the sum +Inf, which passes the
			// sum > 0 check but is not JSON encodable, so it must be
			// rejected per weight.
			name: "positive infinite weight",
			mutate: func(c *Config) {
				c.Weights[0] = float32(math.Inf(1))
			},
			wantErr: "infinite",
		},
		{
			name: "negative infinite weight",
			mutate: func(c *Config) {
				c.Weights[0] = float32(math.Inf(-1))
			},
			wantErr: "negative",
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
	fingerprint := func(t *testing.T, c Config) string {
		t.Helper()
		got, err := c.Fingerprint()
		if err != nil {
			t.Fatalf("Fingerprint() = %v, want nil error", err)
		}
		return got
	}

	base := validFixtureConfig()
	same := validFixtureConfig()
	if fingerprint(t, base) != fingerprint(t, same) {
		t.Error("identical configs must share a fingerprint")
	}

	changed := validFixtureConfig()
	changed.Epsilon = 0.35
	if fingerprint(t, base) == fingerprint(t, changed) {
		t.Error("changed epsilon must change the fingerprint")
	}
}

// TestConfigFingerprintUnencodable pins that an unencodable config surfaces an
// error instead of a sentinel fingerprint. Two distinct bad configs sharing a
// sentinel would collide as a cache hit.
func TestConfigFingerprintUnencodable(t *testing.T) {
	cfg := validFixtureConfig()
	cfg.Epsilon = math.NaN()
	got, err := cfg.Fingerprint()
	if err == nil {
		t.Fatalf("Fingerprint() = %q, want error for NaN epsilon", got)
	}
	if got != "" {
		t.Errorf("Fingerprint() = %q on error, want empty string", got)
	}
}
