package forge

import "testing"

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		defaultHost   string
		forceProvider Provider
		wantProvider  Provider
		wantHost      string
		wantOwner     string
		wantRepo      string
		wantErr       bool
	}{
		{
			name:         "github full URL",
			raw:          "https://github.com/golang/go",
			defaultHost:  "github.com",
			wantProvider: ProviderGitHub,
			wantHost:     "github.com",
			wantOwner:    "golang",
			wantRepo:     "go",
		},
		{
			name:         "gitlab full URL",
			raw:          "https://gitlab.com/inkscape/inkscape",
			defaultHost:  "github.com",
			wantProvider: ProviderGitLab,
			wantHost:     "gitlab.com",
			wantOwner:    "inkscape",
			wantRepo:     "inkscape",
		},
		{
			name:         "gitlab nested groups",
			raw:          "https://gitlab.com/group/subgroup/repo",
			defaultHost:  "github.com",
			wantProvider: ProviderGitLab,
			wantHost:     "gitlab.com",
			wantOwner:    "group/subgroup",
			wantRepo:     "repo",
		},
		{
			name:         "schemeless github",
			raw:          "github.com/charmbracelet/bubbletea",
			defaultHost:  "github.com",
			wantProvider: ProviderGitHub,
			wantHost:     "github.com",
			wantOwner:    "charmbracelet",
			wantRepo:     "bubbletea",
		},
		{
			// Regression: a dot in the repo name must not be mistaken for a host.
			name:         "shorthand with dot in repo name",
			raw:          "ggml-org/llama.cpp",
			defaultHost:  "github.com",
			wantProvider: ProviderGitHub,
			wantHost:     "github.com",
			wantOwner:    "ggml-org",
			wantRepo:     "llama.cpp",
		},
		{
			name:         "schemeless host with dot in repo name",
			raw:          "github.com/ggml-org/llama.cpp",
			defaultHost:  "github.com",
			wantProvider: ProviderGitHub,
			wantHost:     "github.com",
			wantOwner:    "ggml-org",
			wantRepo:     "llama.cpp",
		},
		{
			name:         "shorthand with dot resolves to gitlab default host",
			raw:          "group/my.project",
			defaultHost:  "gitlab.com",
			wantProvider: ProviderGitLab,
			wantHost:     "gitlab.com",
			wantOwner:    "group",
			wantRepo:     "my.project",
		},
		{
			name:         "shorthand owner/repo defaults to github",
			raw:          "golang/go",
			defaultHost:  "github.com",
			wantProvider: ProviderGitHub,
			wantHost:     "github.com",
			wantOwner:    "golang",
			wantRepo:     "go",
		},
		{
			name:         "self-hosted with gitlab in hostname",
			raw:          "https://gitlab.example.com/team/project",
			defaultHost:  "github.com",
			wantProvider: ProviderGitLab,
			wantHost:     "gitlab.example.com",
			wantOwner:    "team",
			wantRepo:     "project",
		},
		{
			name:         ".git suffix stripped",
			raw:          "https://github.com/owner/repo.git",
			defaultHost:  "github.com",
			wantProvider: ProviderGitHub,
			wantHost:     "github.com",
			wantOwner:    "owner",
			wantRepo:     "repo",
		},
		{
			name:          "force provider overrides detection",
			raw:           "https://code.company.com/team/project",
			defaultHost:   "github.com",
			forceProvider: ProviderGitLab,
			wantProvider:  ProviderGitLab,
			wantHost:      "code.company.com",
			wantOwner:     "team",
			wantRepo:      "project",
		},
		{
			name:    "too few path segments",
			raw:     "https://github.com/justowner",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, host, owner, repo, err := ParseRepoURL(tt.raw, tt.defaultHost, tt.forceProvider)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if provider != tt.wantProvider {
				t.Errorf("provider = %v, want %v", provider, tt.wantProvider)
			}
			if host != tt.wantHost {
				t.Errorf("host = %q, want %q", host, tt.wantHost)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}
