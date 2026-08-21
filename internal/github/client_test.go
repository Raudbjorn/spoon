package github

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

func TestNextPageURL(t *testing.T) {
	tests := []struct {
		name string
		link string
		want string
	}{
		{
			name: "with next",
			link: `<https://api.github.com/repos/foo/bar/forks?page=2&per_page=100>; rel="next", <https://api.github.com/repos/foo/bar/forks?page=5&per_page=100>; rel="last"`,
			want: "https://api.github.com/repos/foo/bar/forks?page=2&per_page=100",
		},
		{
			name: "no next",
			link: `<https://api.github.com/repos/foo/bar/forks?page=1&per_page=100>; rel="prev"`,
			want: "",
		},
		{
			name: "empty",
			link: "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextPageURL(tt.link)
			if got != tt.want {
				t.Errorf("nextPageURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRESTClientSetsPinnedVersionHeader exercises the production helper
// newVersionedRESTClient end-to-end against a real httptest.NewServer. It must
// prove that every outbound REST request — across all three construction paths
// that NewClientWithOptions uses — carries exactly defaultRESTVersion on the
// restVersionHeader. The helper preserves any caller-supplied Headers (the
// library-native ClientOptions.Headers field) and only adds the version header
// when it is missing.
func TestRESTClientSetsPinnedVersionHeader(t *testing.T) {
	if restVersionHeader != "X-GitHub-Api-Version" {
		t.Errorf("restVersionHeader = %q, want %q", restVersionHeader, "X-GitHub-Api-Version")
	}
	if defaultRESTVersion != "2022-11-28" {
		t.Errorf("defaultRESTVersion = %q, want %q", defaultRESTVersion, "2022-11-28")
	}

	// The production helper must accept a pre-populated Headers map and preserve
	// every entry while still injecting the pinned version header.
	t.Run("preserves caller headers", func(t *testing.T) {
		opts := ghAPI.ClientOptions{
			Headers: map[string]string{"X-Caller": "kept"},
		}
		rest, err := newVersionedRESTClient(opts)
		if err != nil {
			t.Fatalf("newVersionedRESTClient: %v", err)
		}
		if rest == nil {
			t.Fatal("newVersionedRESTClient returned nil client")
		}
		if got := opts.Headers["X-Caller"]; got != "kept" {
			t.Errorf("caller-supplied Headers mutated: X-Caller = %q, want %q", got, "kept")
		}
	})

	// Each sub-test exercises one construction path that NewClientWithOptions
	// uses: default-token, anonymous fallback, explicit token. The httptest
	// server records every outbound request; the assertion is on what hit the wire.
	cases := []struct {
		name      string
		authToken string
		host      string
		transport http.RoundTripper
	}{
		{
			name:      "default_path_empty_token_empty_host",
			authToken: "",
			host:      "",
			transport: http.DefaultTransport,
		},
		{
			name:      "anonymous_fallback_with_unauth_stripper",
			authToken: "x",
			host:      "github.com",
			transport: &unauthTransport{base: http.DefaultTransport},
		},
		{
			name:      "explicit_token_backend",
			authToken: "real-token",
			host:      "github.com",
			transport: http.DefaultTransport,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			var mu sync.Mutex
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Header.Get(restVersionHeader))
				mu.Unlock()
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			u, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatalf("parse server URL: %v", err)
			}
			tr := &rewriteTransport{target: u, base: http.DefaultTransport}
			rest, err := newVersionedRESTClient(ghAPI.ClientOptions{
				AuthToken: tc.authToken,
				Host:      tc.host,
				Transport: tr,
			})
			if err != nil {
				t.Fatalf("newVersionedRESTClient: %v", err)
			}
			for range 3 {
				if err := rest.Get("repos/owner/repo", nil); err != nil {
					t.Fatalf("rest.Get: %v", err)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 3 {
				t.Fatalf("server received %d requests, want 3", len(seen))
			}
			for i, v := range seen {
				if v != defaultRESTVersion {
					t.Errorf("request %d carried %s = %q, want %q", i, restVersionHeader, v, defaultRESTVersion)
				}
			}
		})
	}
}
