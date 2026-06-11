// cmd/spoon/embed_test.go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/embed"
)

func TestSpoonEmbedStatus_containsOllamaAndRecommended(t *testing.T) {
	prev := spoonDetectFn
	defer func() { spoonDetectFn = prev }()
	spoonDetectFn = func(_ context.Context, ep string) (bool, string, []string) {
		return true, "http://localhost:11434", []string{"nomic-embed-text:latest"}
	}

	var stdout, stderr bytes.Buffer
	exit := runSpoonEmbedWith([]string{"status"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "Ollama") {
		t.Errorf("stdout missing 'Ollama':\n%s", out)
	}
	// At least one of the preferred model names should be present.
	models := embed.PreferredEmbeddingModelsCopy()
	found := false
	for _, m := range models {
		if strings.Contains(out, m.Name) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("stdout missing any recommended model name:\n%s", out)
	}
}

func TestSpoonEmbedStatus_notRunning(t *testing.T) {
	prev := spoonDetectFn
	defer func() { spoonDetectFn = prev }()
	spoonDetectFn = func(_ context.Context, ep string) (bool, string, []string) {
		return false, "http://127.0.0.1:1", nil
	}

	var stdout, stderr bytes.Buffer
	exit := runSpoonEmbedWith([]string{"status"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "not running") {
		t.Errorf("expected 'not running' in output:\n%s", out)
	}
}

func TestSpoonEmbedStatus_withEndpointFlag(t *testing.T) {
	prev := spoonDetectFn
	defer func() { spoonDetectFn = prev }()
	var capturedEndpoint string
	spoonDetectFn = func(_ context.Context, ep string) (bool, string, []string) {
		capturedEndpoint = ep
		return false, ep, nil
	}

	var stdout, stderr bytes.Buffer
	exit := runSpoonEmbedWith([]string{"status", "--endpoint", "http://custom:11434"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if capturedEndpoint != "http://custom:11434" {
		t.Errorf("expected custom endpoint passed through, got %q", capturedEndpoint)
	}
}

func TestSpoonEmbed_unknownVerb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runSpoonEmbedWith([]string{"pull"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d", exit)
	}
}

func TestSpoonEmbed_noVerb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runSpoonEmbedWith([]string{}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d", exit)
	}
}
