package heat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWeights(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "weights.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadWeights_AcceptsAllKnownKeys(t *testing.T) {
	w, err := LoadWeights(writeWeights(t, `{
		"recency": 1.0, "stars": 1.0, "sub_forks": 1.0, "releases": 1.0,
		"mna": 1.0, "sync_ratio": 1.0, "feature_ratio": 1.0,
		"lone_wolf": 1.0, "span": 1.0, "novelty": 1.0
	}`))
	if err != nil {
		t.Fatalf("all known keys rejected: %v", err)
	}
	if len(w) != 10 {
		t.Errorf("expected 10 weights, got %d", len(w))
	}
}

func TestLoadWeights_RejectsUnknownKey(t *testing.T) {
	_, err := LoadWeights(writeWeights(t, `{"bogus_key": 1.0}`))
	if err == nil || !strings.Contains(err.Error(), "bogus_key") {
		t.Fatalf("expected bogus_key rejection, got %v", err)
	}
}

func TestLoadWeights_RejectsOutOfRange(t *testing.T) {
	_, err := LoadWeights(writeWeights(t, `{"novelty": 5.0}`))
	if err == nil || !strings.Contains(err.Error(), "novelty") {
		t.Fatalf("expected out-of-range rejection, got %v", err)
	}
}
