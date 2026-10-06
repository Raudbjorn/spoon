package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
	gogithub "github.com/google/go-github/v90/github"
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

// TestRESTClientSetsPinnedVersionHeader drives restGet, the one REST entry
// point, through the production newRESTClient against a real httptest server.
// Every outbound request must carry exactly defaultRESTVersion, a User-Agent,
// and — only when a token is configured — an Authorization header; the diff
// representation changes Accept and nothing else.
func TestRESTClientSetsPinnedVersionHeader(t *testing.T) {
	if restVersionHeader != "X-GitHub-Api-Version" {
		t.Errorf("restVersionHeader = %q, want %q", restVersionHeader, "X-GitHub-Api-Version")
	}
	if defaultRESTVersion != "2022-11-28" {
		t.Errorf("defaultRESTVersion = %q, want %q", defaultRESTVersion, "2022-11-28")
	}

	cases := []struct {
		name       string
		token      string
		accept     string
		wantAccept string
		wantAuth   string
	}{
		{name: "anonymous", token: "", wantAccept: "application/vnd.github.v3+json", wantAuth: ""},
		{name: "explicit_token", token: "real-token", wantAccept: "application/vnd.github.v3+json", wantAuth: "Bearer real-token"},
		{name: "diff_accept", token: "real-token", accept: diffAcceptHeader, wantAccept: diffAcceptHeader, wantAuth: "Bearer real-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				seen []http.Header
			)
			// A recording transport, not rewriteTransport: that helper strips
			// Authorization, which is exactly what this test asserts on.
			tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				mu.Lock()
				seen = append(seen, req.Header.Clone())
				mu.Unlock()
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{}`)),
					Request:    req,
				}, nil
			})
			rest, err := newRESTClient(tc.token, tr)
			if err != nil {
				t.Fatalf("newRESTClient: %v", err)
			}
			for range 3 {
				resp, err := restGet(context.Background(), rest, "repos/owner/repo", tc.accept)
				if err != nil {
					t.Fatalf("restGet: %v", err)
				}
				resp.Body.Close()
			}

			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 3 {
				t.Fatalf("server received %d requests, want 3", len(seen))
			}
			for i, h := range seen {
				if got := h.Get(restVersionHeader); got != defaultRESTVersion {
					t.Errorf("request %d carried %s = %q, want %q", i, restVersionHeader, got, defaultRESTVersion)
				}
				if got := h.Get("Accept"); got != tc.wantAccept {
					t.Errorf("request %d Accept = %q, want %q", i, got, tc.wantAccept)
				}
				if got := h.Get("Authorization"); got != tc.wantAuth {
					t.Errorf("request %d Authorization = %q, want %q", i, got, tc.wantAuth)
				}
				if h.Get("User-Agent") != restUserAgent {
					t.Errorf("request %d User-Agent = %q, want %q", i, h.Get("User-Agent"), restUserAgent)
				}
			}
		})
	}
}

// A 202 is a success on the raw-path layer (the stats endpoints answer 202 while
// computing), not go-github's AcceptedError, and the payload must survive.
func TestRestGetTreatsAcceptedAsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"computing":true}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	rest, err := newRESTClient("x", &rewriteTransport{target: u, base: http.DefaultTransport})
	if err != nil {
		t.Fatalf("newRESTClient: %v", err)
	}
	resp, err := restGet(context.Background(), rest, "repos/o/r/stats/contributors", "")
	if err != nil {
		t.Fatalf("restGet: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted || string(body) != `{"computing":true}` {
		t.Errorf("got %d %q, want 202 with payload intact", resp.StatusCode, body)
	}
}

// go-github attaches the token at the transport, so a redirect off api.github.com
// would replay it; newRESTClient refuses cross-host redirects instead.
func TestRESTClientRefusesCrossHostRedirect(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization") != ""
	}))
	defer other.Close()
	tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.github.com" {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{other.URL + "/x"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}
		return http.DefaultTransport.RoundTrip(req)
	})
	rest, err := newRESTClient("secret", tr)
	if err != nil {
		t.Fatalf("newRESTClient: %v", err)
	}
	if _, err := restGet(context.Background(), rest, "repos/o/r", ""); err == nil {
		t.Fatal("restGet followed a cross-host redirect; want an error")
	}
	if leaked {
		t.Error("Authorization header reached the redirect target")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// httpFailure must read status and headers off go-github's REST errors and
// go-gh's GraphQL error alike, through wrapping, and report ok=false for errors
// that carry no HTTP response.
func TestHTTPFailureCoversBothClients(t *testing.T) {
	resp := func(code int, h http.Header) *http.Response { return &http.Response{StatusCode: code, Header: h} }
	limited := http.Header{"X-Ratelimit-Remaining": []string{"0"}}
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantOK     bool
	}{
		{"error response", &gogithub.ErrorResponse{Response: resp(404, nil)}, 404, true},
		{"primary rate limit", &gogithub.RateLimitError{Response: resp(403, limited)}, 403, true},
		{"secondary rate limit", &gogithub.AbuseRateLimitError{Response: resp(429, nil)}, 429, true},
		{"wrapped", fmt.Errorf("repo search: %w", &gogithub.ErrorResponse{Response: resp(422, nil)}), 422, true},
		{"graphql http error", &ghAPI.HTTPError{StatusCode: 502}, 502, true},
		{"nil response", &gogithub.ErrorResponse{}, 0, false},
		{"transport", errors.New("connection reset"), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, ok := httpFailure(tc.err)
			if status != tc.wantStatus || ok != tc.wantOK {
				t.Errorf("httpFailure = (%d, %v), want (%d, %v)", status, ok, tc.wantStatus, tc.wantOK)
			}
		})
	}
	if rl := detectRateLimitFromHTTPError(&gogithub.RateLimitError{Response: resp(403, http.Header{
		"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{"2000000000"},
	})}); rl == nil || rl.ResetAt.Unix() != 2000000000 {
		t.Errorf("detectRateLimitFromHTTPError on go-github RateLimitError = %+v, want reset 2000000000", rl)
	}
}
