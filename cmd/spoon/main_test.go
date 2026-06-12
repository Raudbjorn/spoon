package main

import (
	"os/exec"
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
	cmd := exec.Command("go", "run", ".")
	cmd.Args = append(cmd.Args, "--help")
	cmd.Dir = "."
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "--json") {
		t.Errorf("--help still advertises --json; expected to be removed:\n%s", out)
	}
	if strings.Contains(string(out), "--csv") {
		t.Errorf("--help still advertises --csv; expected to be removed:\n%s", out)
	}
	if strings.Contains(string(out), "--output") {
		t.Errorf("--help still advertises --output; expected to be removed:\n%s", out)
	}
}
