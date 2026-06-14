package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

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
