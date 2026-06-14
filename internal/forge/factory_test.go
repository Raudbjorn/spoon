package forge

import "testing"

func TestParse_shorthandForcedProviderHost(t *testing.T) {
	// A scheme-less shorthand ("owner/repo") with a forced provider but no
	// explicit --forge-host must default to that provider's canonical public
	// host, not github.com — otherwise the Gitea/GitLab client would point at
	// GitHub's API.
	tests := []struct {
		name      string
		force     Provider
		wantHost  string
		wantProv  Provider
		wantOwner string
		wantRepo  string
	}{
		{"gitea shorthand -> codeberg", ProviderGitea, "codeberg.org", ProviderGitea, "owner", "repo"},
		{"gitlab shorthand -> gitlab.com", ProviderGitLab, "gitlab.com", ProviderGitLab, "owner", "repo"},
		{"unforced shorthand -> github", 0, "github.com", ProviderGitHub, "owner", "repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(Config{RepoURL: "owner/repo", ForceProvider: tt.force})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", got.Host, tt.wantHost)
			}
			if got.Provider != tt.wantProv {
				t.Errorf("Provider = %v, want %v", got.Provider, tt.wantProv)
			}
			if got.Owner != tt.wantOwner || got.Repo != tt.wantRepo {
				t.Errorf("owner/repo = %q/%q, want %q/%q", got.Owner, got.Repo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

func TestParse_explicitHostWinsOverForcedDefault(t *testing.T) {
	// An explicit --forge-host must override the provider-derived default.
	got, err := Parse(Config{RepoURL: "owner/repo", ForceProvider: ProviderGitea, ForgeHost: "gitea.example.com"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Host != "gitea.example.com" {
		t.Errorf("Host = %q, want gitea.example.com", got.Host)
	}
	if got.Provider != ProviderGitea {
		t.Errorf("Provider = %v, want ProviderGitea", got.Provider)
	}
}

func TestParse_forcedProviderFullURLKeepsURLHost(t *testing.T) {
	// A host-prefixed URL must keep its own host; the forced-provider default
	// only applies to scheme-less shorthand.
	got, err := Parse(Config{RepoURL: "https://codeberg.org/owner/repo", ForceProvider: ProviderGitea})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Host != "codeberg.org" {
		t.Errorf("Host = %q, want codeberg.org", got.Host)
	}
}
