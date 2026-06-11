package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateHeatWeights_AcceptsNovelty(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "weights.json")
	if err := os.WriteFile(path, []byte(`{"novelty": 1.0}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := validateHeatWeights(path); err != nil {
		t.Errorf("validateHeatWeights({\"novelty\":1.0}) returned error: %v", err)
	}
}

func TestValidateHeatWeights_AcceptsKnownKeys(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "weights.json")
	// All currently-valid keys with mid-range values.
	body := `{
		"recency": 1.0, "stars": 1.0, "sub_forks": 1.0, "releases": 1.0,
		"mna": 1.0, "sync_ratio": 1.0, "feature_ratio": 1.0,
		"lone_wolf": 1.0, "span": 1.0, "novelty": 1.0
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := validateHeatWeights(path); err != nil {
		t.Errorf("validateHeatWeights(all known keys) returned error: %v", err)
	}
}

func TestValidateHeatWeights_RejectsUnknownKey(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "weights.json")
	if err := os.WriteFile(path, []byte(`{"bogus_key": 1.0}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := validateHeatWeights(path)
	if err == nil {
		t.Fatalf("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("expected error to mention bogus_key, got: %v", err)
	}
}

func TestValidateHeatWeights_RejectsOutOfRange(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "weights.json")
	if err := os.WriteFile(path, []byte(`{"novelty": 5.0}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := validateHeatWeights(path)
	if err == nil {
		t.Fatalf("expected error for out-of-range value, got nil")
	}
	if !strings.Contains(err.Error(), "novelty") {
		t.Errorf("expected error to mention novelty, got: %v", err)
	}
}

func TestBackendFor(t *testing.T) {
	if got := backendFor(true); got != "mdg" {
		t.Fatalf("backendFor(true) = %q, want %q", got, "mdg")
	}
	if got := backendFor(false); got != "" {
		t.Fatalf("backendFor(false) = %q, want %q", got, "")
	}
}

func TestSpoonRejectsRemovedJSONFlag(t *testing.T) {
	// Capture printHelp() output in-process rather than spawning `go run .`,
	// which is slow and fragile in restricted CI environments.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	printHelp()
	_ = w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "--json") {
		t.Errorf("--help still advertises --json; expected to be removed:\n%s", out)
	}
	if strings.Contains(out, "--csv") {
		t.Errorf("--help still advertises --csv; expected to be removed:\n%s", out)
	}
	if strings.Contains(out, "--output") {
		t.Errorf("--help still advertises --output; expected to be removed:\n%s", out)
	}
}
