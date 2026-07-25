package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/forge"
)

// blockEmbedderCache points XDG_CACHE_HOME at a regular file so the fastembed
// cache directory cannot be created — forcing NewFastEmbedEmbedder to fail fast
// (no network download) and exercising the embed-unavailable degradation path
// deterministically.
func blockEmbedderCache(t *testing.T) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", blocker)
	t.Setenv("SPOON_NO_CONFIG", "1")
}

func stubTwoForkProvider(t *testing.T) {
	t.Helper()
	prev := providerFactory
	t.Cleanup(func() { providerFactory = prev })
	providerFactory = func(context.Context, string, string, string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
				{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now()},
			},
		}, "o/r", nil
	}
}

// The default-on embedder path must degrade — not fail — when fastembed cannot
// initialize: the run still emits NDJSON and surfaces an embed_unavailable
// warning on stderr (#87).
func TestForksListDegradesWhenEmbedderUnavailable(t *testing.T) {
	blockEmbedderCache(t)
	stubTwoForkProvider(t)

	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0 (degradation must not fail the run); stderr:\n%s", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "embed_unavailable") {
		t.Fatalf("expected an embed_unavailable warning on stderr:\n%s", stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("expected NDJSON output despite the unavailable embedder")
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
	}
}

// --commit-files must set opts.CommitFiles: if it regressed, the paired
// --commit-file-budget would trip the "requires --commit-files" rejection. This
// asserts the run passes that gate and reaches dispatch (#87).
func TestCommitFilesWiringReachesDispatch(t *testing.T) {
	blockEmbedderCache(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(context.Context, string, string, string) (forge.Forge, string, *agentio.Error) {
		return nil, "", agentio.NewError(agentio.CodeUpstream, "STUB_REACHED", "x")
	}

	var stdout, stderr bytes.Buffer
	runForksWith([]string{"list", "o/r", "--commit-files", "--commit-file-budget", "5"}, &stdout, &stderr)
	if strings.Contains(stderr.String(), "requires --commit-files") {
		t.Fatalf("--commit-files did not set opts.CommitFiles (budget gate rejected):\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "STUB_REACHED") {
		t.Fatalf("parsing did not reach dispatch:\n%s", stderr.String())
	}
}

// --web-diff must emit the web_diff_unstable warning on stderr and still
// complete the run: the flag opts into an unsupported scraping fallback, it
// does not gate success (#87).
func TestForksListWebDiffWarnsAndSucceeds(t *testing.T) {
	blockEmbedderCache(t)
	stubTwoForkProvider(t)

	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster", "--web-diff"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "web_diff_unstable") {
		t.Fatalf("expected a web_diff_unstable warning on stderr:\n%s", stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		t.Fatal("expected NDJSON output alongside the web-diff warning")
	}
	for _, line := range strings.Split(out, "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
	}
}

// --no-embed opts out of embedding entirely: the run must succeed with NDJSON
// output and, crucially, must NOT emit embed_unavailable — the user asked to
// skip embedding, so a missing runtime is expected, not a degradation (#87).
func TestForksListNoEmbedSilencesWarning(t *testing.T) {
	blockEmbedderCache(t)
	stubTwoForkProvider(t)

	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster", "--no-embed"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", exit, stderr.String())
	}
	if strings.Contains(stderr.String(), "embed_unavailable") {
		t.Fatalf("--no-embed must not emit embed_unavailable:\n%s", stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		t.Fatal("expected NDJSON output with embedding disabled")
	}
	for _, line := range strings.Split(out, "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
	}
}
