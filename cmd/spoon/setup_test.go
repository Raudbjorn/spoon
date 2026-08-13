// cmd/spoon/setup_test.go
package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/setupcheck"
	"github.com/svnbjrn/spoon/internal/store"
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

// stubProvider isolates the config file/store and replaces the provider probe.
func stubProvider(t *testing.T, auth forge.AuthInfo, err error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	prev := setupProviderFn
	t.Cleanup(func() { setupProviderFn = prev })
	setupProviderFn = func(_ context.Context, _ setupcheck.ProviderInput, _ http.RoundTripper) (forge.AuthInfo, error) {
		return auth, err
	}
}

func TestRunSetup_allGreenExitsZero(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)

	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d\n%s\n%s", exit, stdout.String(), stderr.String())
	}
	// The success summary text depends on whether onnxruntime is present
	// (fastembed is advisory). Both variants report credentials ready.
	if !strings.Contains(stdout.String(), "✓") || !strings.Contains(stdout.String(), "redentials") {
		t.Errorf("missing success summary:\n%s", stdout.String())
	}
}

func TestRunSetup_unauthedExitsOne(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthNone, RateLimit: 60, RateUnit: "hour"}, nil)

	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit=%d want 1\n%s", exit, stdout.String())
	}
	if !strings.Contains(stdout.String(), "gh auth login") {
		t.Errorf("missing fix guidance:\n%s", stdout.String())
	}
}

func TestRunSetup_badFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--embedder-backend", "bogus"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d want 2", exit)
	}
}

func TestRunSetup_writesConfig(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)

	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d\n%s", exit, stdout.String())
	}
	path, _ := config.DefaultPath()
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("config not written/loadable: %v", err)
	}
	if c.Forge.Provider != "github" {
		t.Errorf("forge.provider=%q", c.Forge.Provider)
	}
	if !strings.Contains(stdout.String(), "Wrote config at") {
		t.Errorf("missing write notice:\n%s", stdout.String())
	}
}

func TestRunSetup_noConfigSkipsWrite(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)

	var stdout, stderr bytes.Buffer
	runSetupWith(context.Background(), []string{"--no-color", "--no-config"}, strings.NewReader(""), false, &stdout, &stderr)
	path, _ := config.DefaultPath()
	if _, err := config.Load(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected no config file with --no-config, got err=%v", err)
	}
}

func TestRunSetup_storeSectionReported(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)

	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d\n%s\n%s", exit, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "✓ Store") {
		t.Errorf("store section missing or failed:\n%s", stdout.String())
	}
}

func TestRunSetup_unusableStoreExitsOne(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)
	prev := setupStoreFn
	t.Cleanup(func() { setupStoreFn = prev })
	setupStoreFn = func() (*store.Store, error) { return nil, errors.New("disk full") }

	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit=%d want 1 — an unusable store fails every run and must fail setup\n%s", exit, stdout.String())
	}
	if !strings.Contains(stdout.String(), "disk full") {
		t.Errorf("store failure not surfaced:\n%s", stdout.String())
	}
}

func TestRunSetup_refreshesReadme(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)

	var stdout, stderr bytes.Buffer
	if exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit=%d\n%s", exit, stdout.String())
	}
	path, _ := config.DefaultPath()
	readme, err := os.ReadFile(filepath.Join(filepath.Dir(path), "README.md"))
	if err != nil {
		t.Fatalf("setup did not write the config README: %v", err)
	}
	if !strings.Contains(string(readme), "SPOON_NO_CONFIG") {
		t.Error("README content missing env var table")
	}
}

func TestRunSetup_loadsConfigAsDefaults(t *testing.T) {
	stubProvider(t, forge.AuthInfo{Tier: forge.AuthCLI, RateLimit: 5000, RateUnit: "hour"}, nil)

	// Pre-write a config selecting gitlab; setup with no --forge flag should
	// pick it up and report the gitlab provider.
	path, _ := config.DefaultPath()
	if err := config.Save(path, &config.Config{
		Forge: config.ForgeConfig{Provider: "gitlab"},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exit := runSetupWith(context.Background(), []string{"--no-color"}, strings.NewReader(""), false, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d\n%s", exit, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Loaded config from") {
		t.Errorf("missing load notice:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Provider (gitlab)") {
		t.Errorf("config provider not used as default:\n%s", stdout.String())
	}
}
