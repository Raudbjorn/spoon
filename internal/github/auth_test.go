package github

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

func TestClientAuthenticationPrecedence(t *testing.T) {
	for _, key := range []string{"GH_HOST", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(key, "")
	}
	// The gh login is a fallback only; its token must not affect the cache
	// identity or trigger a keyring lookup when a preferred source exists.
	for _, tc := range []struct {
		name, ghToken, githubToken, enterpriseToken, host, cliToken, wantToken, wantSource string
		tokens                                                                             []string
		wantLookup                                                                         bool
	}{
		{name: "anonymous", wantLookup: true, wantSource: "none"},
		{name: "gh login fallback", cliToken: "cli-token", wantLookup: true, wantToken: "cli-token", wantSource: "gh"},
		{name: "GH_TOKEN first", ghToken: "gh-env", githubToken: "github-env", cliToken: "cli-token", wantToken: "gh-env", wantSource: "GH_TOKEN"},
		{name: "GITHUB_TOKEN fallback", githubToken: "github-env", cliToken: "cli-token", wantToken: "github-env", wantSource: "GITHUB_TOKEN"},
		{name: "Spoon token first", ghToken: "gh-env", githubToken: "github-env", cliToken: "cli-token", tokens: []string{"spoon-oauth"}, wantToken: "spoon-oauth", wantSource: "config"},
		{name: "whitespace env falls through", ghToken: " ", githubToken: "\t", cliToken: "cli-token", wantLookup: true, wantToken: "cli-token", wantSource: "gh"},
		{name: "enterprise environment", host: "git.example.com", enterpriseToken: "enterprise-env", cliToken: "cli-token", wantToken: "enterprise-env", wantSource: "GH_ENTERPRISE_TOKEN"},
		{name: "enterprise gh fallback", host: "git.example.com", cliToken: "enterprise-cli", wantLookup: true, wantToken: "enterprise-cli", wantSource: "gh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_HOST", tc.host)
			t.Setenv("GH_TOKEN", tc.ghToken)
			t.Setenv("GITHUB_TOKEN", tc.githubToken)
			t.Setenv("GH_ENTERPRISE_TOKEN", tc.enterpriseToken)
			host := tc.host
			if host == "" {
				host = defaultHost
			}
			lookups := 0
			c, err := newClientWithOptions(ClientOptions{Tokens: tc.tokens}, func(_ context.Context, gotHost string) string {
				lookups++
				if gotHost != host {
					t.Fatalf("lookup host = %q, want %q", gotHost, host)
				}
				return tc.cliToken
			})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if (lookups == 1) != tc.wantLookup || lookups > 1 {
				t.Fatalf("gh lookups = %d, wanted lookup=%v", lookups, tc.wantLookup)
			}
			wantAuth := tc.wantToken != ""
			if c.IsAuthenticated() != wantAuth || c.HasGraphQL() != wantAuth {
				t.Fatalf("authenticated=%v GraphQL=%v, want %v", c.IsAuthenticated(), c.HasGraphQL(), wantAuth)
			}
			if c.tokenSource != tc.wantSource {
				t.Fatalf("source = %q, want %q", c.tokenSource, tc.wantSource)
			}
			var tokens []string
			if wantAuth {
				tokens = []string{tc.wantToken}
			}
			if c.AuthScopeID() != computeAuthScopeID("github", host, tokens) {
				t.Fatal("client used the wrong credential source or host")
			}
			p := NewGHProvider(c, AuthStatus{Authenticated: wantAuth, TokenSource: c.tokenSource, Host: host})
			info, err := p.Auth(context.Background())
			if err != nil || info.TokenSource != tc.wantSource || info.Authenticated() != wantAuth {
				t.Fatalf("provider lost source/auth: %+v, %v", info, err)
			}
			if tc.wantSource == "gh" && info.Tier != forge.AuthCLI {
				t.Fatal("gh login must be reported as CLI authentication")
			}
		})
	}
}

func TestGitHubCLIToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(key, " ")
	}
	for _, tc := range []struct{ name, script, want string }{
		{"active login", `test "$1 $2 $3 $4" = "auth token --hostname git.example.com" || exit 1
test -z "$GH_TOKEN$GITHUB_TOKEN$GH_ENTERPRISE_TOKEN$GITHUB_ENTERPRISE_TOKEN" || exit 1
printf '  synthetic-cli-token\n'`, "synthetic-cli-token"},
		{"failed lookup discards output", "printf partial-token; exit 1", ""},
		{"no gh executable", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			if tc.script != "" {
				if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if got := githubCLIToken(context.Background(), "git.example.com"); got != tc.want {
				t.Fatal("unexpected credential lookup result")
			}
		})
	}
	t.Run("deadline", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("PATH", dir)
		if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		started := time.Now()
		if token := githubCLIToken(ctx, "github.com"); token != "" {
			t.Fatal("timed-out lookup returned a token")
		}
		if ctx.Err() != context.DeadlineExceeded || time.Since(started) > 2*time.Second {
			t.Fatal("credential lookup did not honor the deadline")
		}
	})
}

func TestClientUnitTestsDoNotReadGHLogin(t *testing.T) {
	// Normal constructors in unrelated tests must never touch a real keyring.
	for _, key := range []string{"GH_HOST", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(key, "")
	}
	c, err := NewClientWithOptions(ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.IsAuthenticated() || c.tokenSource != "none" {
		t.Fatal("unit test inherited an ambient gh login")
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
