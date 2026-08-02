package github

import (
	"context"
	"testing"
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
