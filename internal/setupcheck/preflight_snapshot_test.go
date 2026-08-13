package setupcheck

import (
	"context"
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
	}, DenyHTTPTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Tier != forge.AuthNone {
		t.Fatalf("ambient process state bypassed snapshot: tier=%v", auth.Tier)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("provider probe executed gh: stat error=%v", err)
	}
}

func TestLocalProviderProbeTreatsPresentTokenAsUnverified(t *testing.T) {
	auth, err := LocalProviderProbe(context.Background(), ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: map[string]string{"GH_TOKEN": "unverified-token"},
	}, DenyHTTPTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Authenticated() {
		t.Fatal("local no-network probe reported an unvalidated token as authenticated")
	}
	if !auth.Configured {
		t.Fatal("local no-network probe did not report the token as configured")
	}
}

func TestLocalProviderProbeReadsSnapshotCLIConfigWithoutProcess(t *testing.T) {
	tests := []struct {
		name     string
		provider forge.Provider
		command  string
		path     string
		config   string
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
			name:     "gitlab",
			provider: forge.ProviderGitLab,
			command:  "glab",
			path:     filepath.Join("glab-cli", "config.yml"),
			config:   "hosts:\n  gitlab.com:\n    token: snapshot-token\n",
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
				Provider:    tt.provider,
				Environment: map[string]string{"XDG_CONFIG_HOME": configHome},
			}, DenyHTTPTransport{})
			if err != nil {
				t.Fatal(err)
			}
			if auth.Tier != forge.AuthNone || !auth.Configured {
				t.Fatalf("snapshot CLI config state = tier %v, configured %v; want unverified configured credential", auth.Tier, auth.Configured)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("provider probe executed %s: stat error=%v", tt.command, err)
			}
		})
	}
}

func TestProviderConfigPathUsesSnapshotPlatformPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		provider forge.Provider
		goos     string
		env      map[string]string
		want     string
	}{
		{"github override", forge.ProviderGitHub, "linux", map[string]string{"GH_CONFIG_DIR": "/override"}, filepath.Join("/override", "hosts.yml")},
		{"gitlab override", forge.ProviderGitLab, "linux", map[string]string{"GLAB_CONFIG_DIR": "/override"}, filepath.Join("/override", "config.yml")},
		{"github xdg", forge.ProviderGitHub, "linux", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, filepath.Join("/xdg", "gh", "hosts.yml")},
		{"gitlab xdg", forge.ProviderGitLab, "linux", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, filepath.Join("/xdg", "glab-cli", "config.yml")},
		{"github home", forge.ProviderGitHub, "linux", map[string]string{"HOME": "/home/test"}, filepath.Join("/home/test", ".config", "gh", "hosts.yml")},
		{"gitlab home", forge.ProviderGitLab, "linux", map[string]string{"HOME": "/home/test"}, filepath.Join("/home/test", ".config", "glab-cli", "config.yml")},
		{"github windows appdata", forge.ProviderGitHub, "windows", map[string]string{"appdata": "/appdata"}, filepath.Join("/appdata", "GitHub CLI", "hosts.yml")},
		{"gitlab windows appdata", forge.ProviderGitLab, "windows", map[string]string{"AppData": "/appdata"}, filepath.Join("/appdata", "glab-cli", "config.yml")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerConfigPathForOS(tt.provider, tt.env, tt.goos); got != tt.want {
				t.Fatalf("provider config path=%q, want %q", got, tt.want)
			}
		})
	}
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
