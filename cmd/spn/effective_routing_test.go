package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

func TestDispatchPassesConfigOnlyGitHubSettingsToRuntimeFactories(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	whitelist := true
	file := &config.Config{GitHub: config.GitHubConfig{
		Tokens:            []string{"config-token"},
		RequestsPerMinute: 123,
		Proxy: config.ProxyConfig{
			Enabled:           true,
			APIKeyFile:        "/config/proxy-key",
			StaticFile:        "/config/proxies",
			WhitelistPublicIP: &whitelist,
			CacheTTL:          "37m",
		},
	}}
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(path, file); err != nil {
		t.Fatal(err)
	}

	prevAPI := apiFactoryWithEffective
	defer func() { apiFactoryWithEffective = prevAPI }()
	var seen []config.EffectiveConfig
	apiFactoryWithEffective = func(got config.EffectiveConfig) (threadsops.API, *agentio.Error) {
		seen = append(seen, got)
		return &stubAPI{}, nil
	}

	var stdout, stderr bytes.Buffer
	if exit := dispatch([]string{"threads", "list", "owner/repo#1"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("threads exit = %d, stderr = %s", exit, stderr.String())
	}

	prevStatus := fetchPRStatus
	defer func() { fetchPRStatus = prevStatus }()
	fetchPRStatus = func(_ context.Context, _ threadsops.API, _, _ string, _ int) (github.PullRequestStatus, *agentio.Error) {
		return github.PullRequestStatus{}, nil
	}
	stdout.Reset()
	stderr.Reset()
	if exit := dispatch([]string{"pr", "status", "owner/repo#1"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("pr exit = %d, stderr = %s", exit, stderr.String())
	}

	prevRepoAuth := repoCheckAuthWithEffective
	defer func() { repoCheckAuthWithEffective = prevRepoAuth }()
	repoCheckAuthWithEffective = func(got config.EffectiveConfig) (*github.Client, github.AuthStatus, error) {
		seen = append(seen, got)
		return nil, github.AuthStatus{}, errors.New("stop after capture")
	}
	stdout.Reset()
	stderr.Reset()
	if exit := dispatch([]string{"repo", "centrality", "owner/repo"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("repo exit = %d, stderr = %s", exit, stderr.String())
	}

	if len(seen) != 3 {
		t.Fatalf("effective config factory calls = %d, want 3", len(seen))
	}
	for _, got := range seen {
		if got.GitHub.Tokens.Value != "config-token" ||
			got.GitHub.RequestsPerMinute.Value != "123" ||
			got.GitHub.Proxy.Enabled.Value != "true" ||
			got.GitHub.Proxy.APIKeyFile.Value != "/config/proxy-key" ||
			got.GitHub.Proxy.StaticFile.Value != "/config/proxies" ||
			got.GitHub.Proxy.WhitelistPublicIP.Value != "true" ||
			got.GitHub.Proxy.CacheTTL.Value != "37m" {
			t.Fatalf("factory received incomplete effective GitHub config: %+v", got.GitHub)
		}
	}
}

func TestForkRoutesUseEffectiveForgeDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SPOON_NO_EMBED", "1")
	t.Setenv("SPOON_NO_VOYAGE", "1")
	file := &config.Config{Forge: config.ForgeConfig{Provider: "gitlab", Host: "git.example"}}
	effective := config.ResolveEffectiveConfig(file, nil, map[string]string{})
	env := map[string]string{"SNAPSHOT": "present"}

	judgments := filepath.Join(t.TempDir(), "judgments.json")
	if err := os.WriteFile(judgments, []byte(`{"forks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	prev := providerFactory
	t.Cleanup(func() { providerFactory = prev })
	var calls int
	providerFactory = func(ctx context.Context, _ string, provider, host string) (forge.Forge, string, *agentio.Error) {
		calls++
		if provider != "gitlab" || host != "git.example" {
			t.Fatalf("forge defaults = (%q, %q), want (gitlab, git.example)", provider, host)
		}
		got, gotEnv, ok := effectiveFromContext(ctx)
		if !ok || got.Forge != effective.Forge || gotEnv["SNAPSHOT"] != "present" {
			t.Fatalf("provider context = (%+v, %+v, %v), want startup effective config and environment", got, gotEnv, ok)
		}
		return nil, "", agentio.NewError(agentio.CodeUpstream, "stop after capture", "test")
	}

	var stdout, stderr bytes.Buffer
	if exit := doForksListWithDeps([]string{"owner/repo", "--no-embed", "--no-voyage", "--no-cluster"}, &stdout, &stderr, effective, env, defaultCommandDeps()); exit == 0 {
		t.Fatal("forks list unexpectedly succeeded")
	}
	stdout.Reset()
	stderr.Reset()
	if exit := runEvalWithEffective([]string{"owner/repo", "--judgments", judgments, "--no-cluster"}, &stdout, &stderr, effective, env); exit == 0 {
		t.Fatal("forks eval unexpectedly succeeded")
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want list and eval", calls)
	}
}
