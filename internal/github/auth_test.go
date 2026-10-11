package github

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestGHProviderAuth_reportsHost(t *testing.T) {
	// Regression: AuthStatus.Host was never populated, so AuthInfo.Host came out
	// empty and forge.CompareURL built "https:///owner/repo/compare/...". The
	// REST/GraphQL clients hardcode the host separately, so nothing else noticed.
	p := NewGHProvider(nil, AuthStatus{Authenticated: true, Host: defaultHost})

	info, err := p.Auth(context.Background())
	if err != nil {
		t.Fatalf("Auth() error: %v", err)
	}
	if info.Host == "" {
		t.Fatal("AuthInfo.Host is empty; compare URLs will be built without a host")
	}
	if info.Host != "github.com" {
		t.Errorf("AuthInfo.Host = %q, want github.com", info.Host)
	}
	if info.Tier != forge.AuthToken {
		t.Errorf("auth tier = %v, want token auth", info.Tier)
	}
}

func TestClientAuthenticationWithoutGH(t *testing.T) {
	for _, key := range []string{"GH_HOST", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(key, "")
	}
	// A gh login must neither authenticate Spoon nor select its default host.
	ghConfig := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", ghConfig)
	t.Setenv("PATH", t.TempDir())
	if err := os.WriteFile(filepath.Join(ghConfig, "hosts.yml"), []byte("other.example.com:\n  oauth_token: ignored-gh-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ghToken, githubToken, enterpriseToken, host, wantToken string
		tokens                                                       []string
	}{
		{name: "gh login ignored"},
		{name: "GH_TOKEN first", ghToken: "gh-env", githubToken: "github-env", wantToken: "gh-env"},
		{name: "GITHUB_TOKEN fallback", githubToken: "github-env", wantToken: "github-env"},
		{name: "Spoon OAuth token", ghToken: "gh-env", tokens: []string{"spoon-oauth"}, wantToken: "spoon-oauth"},
		{name: "enterprise environment", host: "git.example.com", enterpriseToken: "enterprise-env", wantToken: "enterprise-env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_HOST", tc.host)
			t.Setenv("GH_TOKEN", tc.ghToken)
			t.Setenv("GITHUB_TOKEN", tc.githubToken)
			t.Setenv("GH_ENTERPRISE_TOKEN", tc.enterpriseToken)
			c, err := NewClientWithOptions(ClientOptions{Tokens: tc.tokens})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			wantAuth := tc.wantToken != ""
			if c.IsAuthenticated() != wantAuth || c.HasGraphQL() != wantAuth {
				t.Fatalf("authenticated=%v GraphQL=%v, want %v", c.IsAuthenticated(), c.HasGraphQL(), wantAuth)
			}
			host := tc.host
			if host == "" {
				host = defaultHost
			}
			var tokens []string
			if wantAuth {
				tokens = []string{tc.wantToken}
			}
			if c.AuthScopeID() != computeAuthScopeID("github", host, tokens) {
				t.Fatal("client used the wrong credential source or host")
			}
		})
	}
}

func TestHasScope(t *testing.T) {
	cases := []struct {
		name   string
		status AuthStatus
		want   string
		ok     bool
	}{
		{"present", AuthStatus{Scopes: []string{"gist", "repo", "workflow"}}, "repo", true},
		{"absent", AuthStatus{Scopes: []string{"gist", "workflow"}}, "repo", false},
		{"empty", AuthStatus{Scopes: nil}, "repo", false},
		{"first match", AuthStatus{Scopes: []string{"repo"}}, "repo", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.HasScope(tc.want); got != tc.ok {
				t.Errorf("got %v, want %v", got, tc.ok)
			}
		})
	}
}
