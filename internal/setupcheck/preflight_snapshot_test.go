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
			if auth.Tier != forge.AuthCLI {
				t.Fatalf("snapshot CLI config tier=%v, want %v", auth.Tier, forge.AuthCLI)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("provider probe executed %s: stat error=%v", tt.command, err)
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
