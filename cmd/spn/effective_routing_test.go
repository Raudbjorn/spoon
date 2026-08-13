package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
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
