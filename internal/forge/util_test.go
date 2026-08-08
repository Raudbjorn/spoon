package forge

import "strings"

import "testing"

func TestCompareURL_emptyHostFallsBackToCanonicalHost(t *testing.T) {
	// Regression: AuthStatus.Host was never populated on the GitHub path, so
	// CompareURL interpolated an empty host and produced "https:///owner/repo/
	// compare/..." — a dead link. Every exported fork row carried one. An empty
	// host must degrade to the provider's canonical host instead.
	tests := []struct {
		name     string
		provider Provider
		host     string
		want     string
	}{
		{
			"github with empty host",
			ProviderGitHub, "",
			"https://github.com/qvr/nonraid/compare/main...Raudbjorn:main",
		},
		{
			"gitlab with empty host",
			ProviderGitLab, "",
			"https://gitlab.com/qvr/nonraid/-/compare/main...Raudbjorn:main",
		},
		{
			"gitea with empty host",
			ProviderGitea, "",
			"https://codeberg.org/qvr/nonraid/compare/main...Raudbjorn:main",
		},
		{
			"explicit host is preserved",
			ProviderGitHub, "github.example.com",
			"https://github.example.com/qvr/nonraid/compare/main...Raudbjorn:main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompareURL(tt.provider, tt.host, "qvr/nonraid", "main", "Raudbjorn", "main")
			if got != tt.want {
				t.Errorf("CompareURL() = %q, want %q", got, tt.want)
			}
			if strings.HasPrefix(got, "https:///") {
				t.Errorf("CompareURL() produced a hostless URL: %q", got)
			}
		})
	}
}

func TestDefaultHost(t *testing.T) {
	if got := DefaultHost(ProviderGitLab); got != "gitlab.com" {
		t.Errorf("DefaultHost(GitLab) = %q, want gitlab.com", got)
	}
	if got := DefaultHost(ProviderGitHub); got != "github.com" {
		t.Errorf("DefaultHost(GitHub) = %q, want github.com", got)
	}
	// Gitea has no single canonical instance; codeberg.org matches what
	// persistForkSnapshot already assumed for a hostless Gitea caller.
	if got := DefaultHost(ProviderGitea); got != "codeberg.org" {
		t.Errorf("DefaultHost(Gitea) = %q, want codeberg.org", got)
	}
}
