// cmd/spn/embed_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/embed"
)

func TestSpnEmbedStatus_emitsJSON(t *testing.T) {
	prev := detectFn
	defer func() { detectFn = prev }()
	detectFn = func(_ context.Context, ep string) (bool, string, []string) {
		return false, "http://localhost:11434", []string{}
	}

	var stdout, stderr bytes.Buffer
	exit := runEmbedWith([]string{"status"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if got["running"] != false {
		t.Errorf("expected running=false, got %v", got["running"])
	}
	if got["endpoint"] == nil {
		t.Error("missing endpoint field")
	}
	if got["installed"] == nil {
		t.Error("missing installed field")
	}
	if got["recommended"] == nil {
		t.Error("missing recommended field")
	}
}

func TestSpnEmbedStatus_withEndpointFlag(t *testing.T) {
	prev := detectFn
	defer func() { detectFn = prev }()
	var capturedEndpoint string
	detectFn = func(_ context.Context, ep string) (bool, string, []string) {
		capturedEndpoint = ep
		return true, ep, []string{"nomic-embed-text:latest"}
	}

	var stdout, stderr bytes.Buffer
	exit := runEmbedWith([]string{"status", "--endpoint", "http://custom:11434"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if capturedEndpoint != "http://custom:11434" {
		t.Errorf("expected endpoint passed through, got %q", capturedEndpoint)
	}
}

func TestSpnEmbedModels_emitsArray(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runEmbedWith([]string{"models"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}

	var got []embed.ModelSuggestion
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON array: %v\n%s", err, stdout.String())
	}
	if len(got) == 0 {
		t.Error("expected at least one model suggestion")
	}
	// Verify the default model is present.
	hasDefault := false
	for _, m := range got {
		if m.Default {
			hasDefault = true
		}
	}
	if !hasDefault {
		t.Error("expected at least one default model")
	}
}

func TestSpnEmbedPull_emitsNDJSON_streamingProgress(t *testing.T) {
	prev := pullFn
	defer func() { pullFn = prev }()
	pullFn = func(_ context.Context, endpoint, model string, progress func(string, float64)) error {
		progress("downloading", 0.5)
		progress("done", 1.0)
		return nil
	}

	var stdout, stderr bytes.Buffer
	exit := runEmbedWith([]string{"pull", "nomic-embed-text"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d:\n%s", len(lines), stdout.String())
	}

	var first, last map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line 0 not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &last); err != nil {
		t.Fatalf("line 1 not JSON: %v", err)
	}
	if first["phase"] != "downloading" {
		t.Errorf("first phase=%v", first["phase"])
	}
	if last["phase"] != "done" || last["pct"] != 1.0 {
		t.Errorf("last line=%+v", last)
	}
	if first["model"] != "nomic-embed-text" {
		t.Errorf("model=%v", first["model"])
	}
}

func TestSpnEmbedPull_missingModel_badInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runEmbedWith([]string{"pull"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d; stderr=%s", exit, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on error, got %q", stdout.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v", env["error"]["code"])
	}
}

func TestSpnEmbed_unknownVerb_badInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runEmbedWith([]string{"nonsense"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d; stderr=%s", exit, stderr.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v", env["error"]["code"])
	}
}
