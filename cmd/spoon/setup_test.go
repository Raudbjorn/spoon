// cmd/spoon/setup_test.go
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/models"
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

// stubProvider isolates the config file and replaces the provider probe.
func stubProvider(t *testing.T, auth forge.AuthInfo, err error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prev := setupProviderFn
	t.Cleanup(func() { setupProviderFn = prev })
	setupProviderFn = func(_ context.Context, _, _, _ string) (forge.Forge, forge.AuthInfo, string, error) {
		return nil, auth, "", err
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
	// (fastembed is advisory). Both variants report OpenVINO features ready.
	if !strings.Contains(stdout.String(), "OpenVINO features") {
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

func stubEnsure(t *testing.T, called *bool) {
	t.Helper()
	prev := setupEnsureFn
	t.Cleanup(func() { setupEnsureFn = prev })
	setupEnsureFn = func(_ context.Context, f models.Feature, _ models.Progress) (string, error) {
		*called = true
		dir, err := models.LocalDir(models.DefaultRepo(f))
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "openvino_model.xml"), []byte("<net/>"), 0o644); err != nil {
			return "", err
		}
		return dir, nil
	}
}

func TestEnsureFeatureModel_AutoPullDownloadsAndAdopts(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	called := false
	stubEnsure(t, &called)

	var out bytes.Buffer
	modelPath := ""
	ok := ensureFeatureModel(context.Background(), setupFlags{autoPull: true, noColor: true},
		models.FeatureEmbedder, "Embedder", &modelPath, false, strings.NewReader(""), &out)
	if !ok || !called {
		t.Fatalf("ok=%v called=%v\n%s", ok, called, out.String())
	}
	if modelPath == "" || !models.IsDownloaded(modelPath) {
		t.Fatalf("model path not adopted: %q", modelPath)
	}
}

func TestEnsureFeatureModel_NoPromptReportsOnly(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	called := false
	stubEnsure(t, &called)

	var out bytes.Buffer
	modelPath := ""
	ok := ensureFeatureModel(context.Background(), setupFlags{noPrompt: true, noColor: true},
		models.FeatureReranker, "Reranker", &modelPath, true, strings.NewReader(""), &out)
	if ok || called || modelPath != "" {
		t.Fatalf("no-prompt must not download: ok=%v called=%v path=%q", ok, called, modelPath)
	}
	if !strings.Contains(out.String(), "--auto-pull") {
		t.Errorf("expected --auto-pull hint:\n%s", out.String())
	}
}

func TestEnsureFeatureModel_InteractiveDecline(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	called := false
	stubEnsure(t, &called)

	var out bytes.Buffer
	modelPath := ""
	ok := ensureFeatureModel(context.Background(), setupFlags{noColor: true},
		models.FeatureEmbedder, "Embedder", &modelPath, true, strings.NewReader("n\n"), &out)
	if ok || called {
		t.Fatalf("declined prompt must not download: ok=%v called=%v", ok, called)
	}
}

func TestEnsureFeatureModel_ExistingDefaultAdopted(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := models.LocalDir(models.DefaultRepo(models.FeatureEmbedder))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openvino_model.xml"), []byte("<net/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	stubEnsure(t, &called)

	var out bytes.Buffer
	modelPath := ""
	ok := ensureFeatureModel(context.Background(), setupFlags{noPrompt: true, noColor: true},
		models.FeatureEmbedder, "Embedder", &modelPath, false, strings.NewReader(""), &out)
	if !ok || called {
		t.Fatalf("present default must be adopted without download: ok=%v called=%v", ok, called)
	}
	if modelPath != dir {
		t.Fatalf("modelPath = %q, want %q", modelPath, dir)
	}
}
