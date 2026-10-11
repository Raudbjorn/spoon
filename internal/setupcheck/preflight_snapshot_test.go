package setupcheck

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestLocalProviderProbeUsesInjectedEnvironmentSnapshot(t *testing.T) {
	sentinel := installProviderCLITrap(t, "gh")
	t.Setenv("GH_TOKEN", "ambient-token")

	auth, err := LocalProviderProbe(context.Background(), ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: map[string]string{},
	}, rejectProviderHTTP{t})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Tier != forge.AuthNone || auth.Configured {
		t.Fatalf("ambient process state bypassed snapshot: tier=%v", auth.Tier)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("provider probe executed gh: stat error=%v", err)
	}
}

func TestLocalProviderProbeTreatsPresentTokenAsUnverified(t *testing.T) {
	for _, source := range []string{"config", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Run(source, func(t *testing.T) {
			auth, err := LocalProviderProbe(context.Background(), ProviderInput{
				Provider:        forge.ProviderGitHub,
				ConfiguredToken: source == "config",
				Environment:     map[string]string{source: "unverified-token"},
			}, rejectProviderHTTP{t})
			if err != nil {
				t.Fatal(err)
			}
			if auth.Authenticated() || !auth.Configured {
				t.Fatalf("local token must be configured but unverified: %+v", auth)
			}
		})
	}
}

func TestLocalProviderProbeIgnoresGitHubCLIAndReadsGitLabConfig(t *testing.T) {
	tests := []struct {
		name       string
		provider   forge.Provider
		command    string
		path       string
		config     string
		configured bool
	}{
		{
			name:     "github",
			provider: forge.ProviderGitHub,
			command:  "gh",
			path:     filepath.Join("gh", "hosts.yml"),
			config:   "github.com:\n  user: octocat\n  oauth_token: snapshot-token\n",
		},
		{
			name:     "github multi-user",
			provider: forge.ProviderGitHub,
			command:  "gh",
			path:     filepath.Join("gh", "hosts.yml"),
			config:   "github.com:\n  user: octocat\n  users:\n    octocat:\n      oauth_token: snapshot-token\n",
		},
		{
			name:     "github keyring",
			provider: forge.ProviderGitHub,
			command:  "gh",
			path:     filepath.Join("gh", "hosts.yml"),
			config:   "github.com:\n  user: octocat\n",
		},
		{
			name:       "gitlab",
			configured: true,
			provider:   forge.ProviderGitLab,
			command:    "glab",
			path:       filepath.Join("glab-cli", "config.yml"),
			config:     "hosts:\n  gitlab.com:\n    token: snapshot-token\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sentinel := installProviderCLITrap(t, tt.command)
			configHome := t.TempDir()
			path := filepath.Join(configHome, tt.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.config), 0o600); err != nil {
				t.Fatal(err)
			}

			auth, err := LocalProviderProbe(context.Background(), ProviderInput{
				Provider: tt.provider,
				Environment: map[string]string{
					"XDG_CONFIG_HOME":               configHome,
					"GH_CONFIG_DIR":                 filepath.Join(configHome, "gh"),
					"PATH":                          os.Getenv("PATH"),
					"SPOON_PROVIDER_PROBE_SENTINEL": sentinel,
				},
			}, rejectProviderHTTP{t})
			if err != nil {
				t.Fatal(err)
			}
			// GitHub CLI credentials are deliberately ignored; glab credentials remain supported.
			if auth.Tier != forge.AuthNone || auth.Configured != tt.configured {
				t.Fatalf("snapshot CLI config state = tier %v, configured %v; want configured=%v", auth.Tier, auth.Configured, tt.configured)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("provider probe executed %s: stat error=%v", tt.command, err)
			}
		})
	}
}

func TestGitLabConfigPathUsesSnapshotPlatformPrecedence(t *testing.T) {
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{"override", "linux", map[string]string{"GLAB_CONFIG_DIR": "/override", "XDG_CONFIG_HOME": "/xdg"}, filepath.Join("/override", "config.yml")},
		{"xdg", "linux", map[string]string{"XDG_CONFIG_HOME": "/xdg", "HOME": "/home/test"}, filepath.Join("/xdg", "glab-cli", "config.yml")},
		{"home", "linux", map[string]string{"HOME": "/home/test"}, filepath.Join("/home/test", ".config", "glab-cli", "config.yml")},
		{"windows appdata", "windows", map[string]string{"appdata": "/appdata", "HOME": "/home/test"}, filepath.Join("/appdata", "glab-cli", "config.yml")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gitLabConfigPathForOS(tt.env, tt.goos); got != tt.want {
				t.Fatalf("glab config path=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalProviderProbeReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Cancellation is reported even when a credential can be found locally.
	for _, configured := range []bool{false, true} {
		_, err := LocalProviderProbe(ctx, ProviderInput{
			Provider: forge.ProviderGitHub, ConfiguredToken: configured,
		}, rejectProviderHTTP{t})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation was swallowed: %v", err)
		}
	}
}

type rejectProviderHTTP struct{ t *testing.T }

func (tr rejectProviderHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	tr.t.Error("local provider probe attempted HTTP")
	return nil, errors.New("HTTP is disabled")
}

func installProviderCLITrap(t *testing.T, command string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("provider CLI trap requires a POSIX executable")
	}
	binDir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(binDir, command)
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > \"$SPOON_PROVIDER_PROBE_SENTINEL\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("SPOON_PROVIDER_PROBE_SENTINEL", sentinel)
	return sentinel
}
