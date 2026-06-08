// cmd/spoon/setup_test.go
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/sidecar"
)

func TestProviderStatusLines(t *testing.T) {
	tests := []struct {
		name       string
		provider   forge.Provider
		auth       forge.AuthInfo
		err        error
		wantOK     bool
		wantSubstr string
	}{
		{"github authed", forge.ProviderGitHub, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil, true, "gh CLI"},
		{"github unauthed", forge.ProviderGitHub, forge.AuthInfo{Tier: forge.AuthNone, RateLimit: 60, RateUnit: "hour"}, nil, false, "gh auth login"},
		{"gitlab token", forge.ProviderGitLab, forge.AuthInfo{Tier: forge.AuthToken, Username: "alice", RateLimit: 2000, RateUnit: "minute"}, nil, true, "GITLAB_TOKEN"},
		{"gitlab unauthed", forge.ProviderGitLab, forge.AuthInfo{Tier: forge.AuthNone, RateLimit: 500, RateUnit: "minute"}, nil, false, "glab auth login"},
		{"error path", forge.ProviderGitHub, forge.AuthInfo{}, errors.New("boom"), false, "gh auth login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, lines := providerStatusLines(tt.provider, tt.auth, tt.err)
			if ok != tt.wantOK {
				t.Errorf("ok=%v want %v", ok, tt.wantOK)
			}
			if !strings.Contains(strings.Join(lines, "\n"), tt.wantSubstr) {
				t.Errorf("lines missing %q:\n%s", tt.wantSubstr, strings.Join(lines, "\n"))
			}
		})
	}
}

func TestInstalledHas(t *testing.T) {
	installed := []string{"nomic-embed-text:latest", "mxbai-embed-large:latest"}
	cases := map[string]bool{
		"nomic-embed-text":        true,  // base match against :latest
		"nomic-embed-text:latest": true,  // exact
		"mxbai-embed-large":       true,  // base match
		"llama3.2:3b":             false, // absent
		"snowflake-arctic-embed2": false, // absent
	}
	for model, want := range cases {
		if got := installedHas(installed, model); got != want {
			t.Errorf("installedHas(%q)=%v want %v", model, got, want)
		}
	}
}

func TestSidecarStatusLines(t *testing.T) {
	t.Run("error suggests install", func(t *testing.T) {
		ok, lines := sidecarStatusLines("http://localhost:8765", 0, errors.New("conn refused"))
		if ok {
			t.Error("want not ok")
		}
		if !strings.Contains(strings.Join(lines, "\n"), "spoon sidecar install") {
			t.Errorf("missing install guidance:\n%s", strings.Join(lines, "\n"))
		}
	})
	t.Run("healthy reports dim", func(t *testing.T) {
		ok, lines := sidecarStatusLines("http://localhost:8765", 1024, nil)
		if !ok {
			t.Error("want ok")
		}
		if !strings.Contains(strings.Join(lines, "\n"), "dim 1024") {
			t.Errorf("missing dim:\n%s", strings.Join(lines, "\n"))
		}
	})
}

func TestEmbedModelSizeHint(t *testing.T) {
	if got := embedModelSizeHint("snowflake-arctic-embed2"); !strings.Contains(got, "MB") {
		t.Errorf("size hint=%q want a MB figure", got)
	}
	if got := embedModelSizeHint("does-not-exist"); got != "" {
		t.Errorf("unknown model hint=%q want empty", got)
	}
}

// --- run wiring -------------------------------------------------------------

func stubProvider(t *testing.T, auth forge.AuthInfo, err error) {
	t.Helper()
	prev := setupProviderFn
	t.Cleanup(func() { setupProviderFn = prev })
	setupProviderFn = func(_ context.Context, _, _, _ string) (forge.Forge, forge.AuthInfo, string, error) {
		return nil, auth, "", err
	}
}

func stubOllama(t *testing.T, running bool, installed []string) {
	t.Helper()
	prev := setupOllamaDetectFn
	t.Cleanup(func() { setupOllamaDetectFn = prev })
	setupOllamaDetectFn = func(_ context.Context, _ string) (bool, string, []string) {
		return running, "http://localhost:11434", installed
	}
}

func stubSidecar(t *testing.T, dim int, err error) {
	t.Helper()
	prev := setupSidecarFn
	t.Cleanup(func() { setupSidecarFn = prev })
	setupSidecarFn = func(_ context.Context, _ string) (int, error) { return dim, err }
}

func TestRunSetup_allGreenExitsZero(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)
	// Both a suitable embedding model and the labeler model present.
	stubOllama(t, true, []string{"nomic-embed-text:latest", setupLabelerModel})

	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d want 0\nstdout=%s", exit, stdout.String())
	}
	if !strings.Contains(stdout.String(), "All set") {
		t.Errorf("missing success summary:\n%s", stdout.String())
	}
}

func TestRunSetup_ollamaDownTriesSidecar(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)
	stubOllama(t, false, nil)
	stubSidecar(t, 1024, nil) // sidecar healthy

	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d want 0\nstdout=%s", exit, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "using the Python sidecar") || !strings.Contains(out, "dim 1024") {
		t.Errorf("expected sidecar fallback:\n%s", out)
	}
}

func TestRunSetup_nonInteractiveNoModelReportsPull(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)
	stubOllama(t, true, nil) // running, no models installed

	var stdout, stderr bytes.Buffer
	// Non-interactive, no --auto-pull: must NOT pull, just report the command.
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit=%d want 1", exit)
	}
	out := stdout.String()
	if !strings.Contains(out, "ollama pull "+setupEmbedPullModel) {
		t.Errorf("expected embed pull hint:\n%s", out)
	}
	if !strings.Contains(out, "ollama pull "+setupLabelerModel) {
		t.Errorf("expected labeler pull hint:\n%s", out)
	}
}

func TestRunSetup_autoPullInvokesPull(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)
	stubOllama(t, true, nil) // running, nothing installed

	prevPull := setupPullFn
	t.Cleanup(func() { setupPullFn = prevPull })
	pulled := []string{}
	setupPullFn = func(_ context.Context, _ string, model string, progress func(string, float64)) error {
		pulled = append(pulled, model)
		if progress != nil {
			progress("done", 1.0)
		}
		return nil
	}

	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--no-color", "--auto-pull"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d want 0\nstdout=%s", exit, stdout.String())
	}
	// Both the embedding model and the labeler model should have been pulled.
	joined := strings.Join(pulled, ",")
	if !strings.Contains(joined, setupEmbedPullModel) || !strings.Contains(joined, setupLabelerModel) {
		t.Errorf("pulled=%v want both embed+labeler", pulled)
	}
}

func TestRunSetup_interactiveDeclineDoesNotPull(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)
	stubOllama(t, true, nil)

	prevPull := setupPullFn
	t.Cleanup(func() { setupPullFn = prevPull })
	called := false
	setupPullFn = func(_ context.Context, _, _ string, _ func(string, float64)) error {
		called = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	// Interactive, answer "n" to both prompts.
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader("n\nn\n"), true, &stdout, &stderr)
	if called {
		t.Error("pull must not be called when user declines")
	}
	if exit != 1 {
		t.Fatalf("exit=%d want 1", exit)
	}
}

func TestRunSetup_sidecarBackendDownOffersInstall(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)
	stubSidecar(t, 0, errors.New("refused"))

	prevInstall := setupSidecarInstallFn
	t.Cleanup(func() { setupSidecarInstallFn = prevInstall })
	installed := false
	setupSidecarInstallFn = func(_ context.Context, _ sidecar.Options, _ io.Writer) error {
		installed = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	// Non-interactive: should NOT install, just print guidance.
	exit := runSetupWith(context.Background(), []string{"--no-color", "--embedder-backend", "sidecar"}, strings.NewReader(""), false, &stdout, &stderr)
	if installed {
		t.Error("must not auto-install when non-interactive without --auto-pull")
	}
	if exit != 1 {
		t.Fatalf("exit=%d want 1", exit)
	}
	if !strings.Contains(stdout.String(), "spoon sidecar install") {
		t.Errorf("expected install guidance:\n%s", stdout.String())
	}
}

func TestRunSetup_badFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--embedder-backend", "bogus"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d want 2", exit)
	}
}
