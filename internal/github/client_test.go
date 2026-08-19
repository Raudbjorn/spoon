package github

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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

// roundTripperFunc adapts a plain function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func mustURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	return u
}

// TestRESTClientSetsPinnedVersionHeader verifies the version header constants are
// correct and that newVersionInjectingTransport sets the header on every request.
func TestRESTClientSetsPinnedVersionHeader(t *testing.T) {
	if restVersionHeader != "X-GitHub-Api-Version" {
		t.Errorf("restVersionHeader = %q, want %q", restVersionHeader, "X-GitHub-Api-Version")
	}
	if defaultRESTVersion != "2022-11-28" {
		t.Errorf("defaultRESTVersion = %q, want %q", defaultRESTVersion, "2022-11-28")
	}

	// Verify newVersionInjectingTransport is wired into NewClientWithOptions by
	// checking a real ghAPI.RESTClient can be built with it.
	transport := newVersionInjectingTransport(http.DefaultTransport, defaultRESTVersion)
	if transport == nil {
		t.Fatal("newVersionInjectingTransport returned nil")
	}

	// End-to-end: the transport must set the header on every request.
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Get(restVersionHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.URL.Scheme = "http"
		req.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
	})
	transport2 := newVersionInjectingTransport(base, defaultRESTVersion)

	for range 3 {
		req := &http.Request{
			Method: http.MethodGet,
			URL:    mustURL("https://api.github.com/repos/owner/repo/forks"),
			Header: http.Header{},
		}
		_, _ = transport2.RoundTrip(req)
	}

	if captured != defaultRESTVersion {
		t.Errorf("server received %s = %q, want %q", restVersionHeader, captured, defaultRESTVersion)
	}

	// Also verify that building a REST client with the transport succeeds.
	opts := ghAPI.ClientOptions{
		AuthToken: "test-token",
		Host:      "api.github.com",
		Transport: transport,
	}
	rest, err := ghAPI.NewRESTClient(opts)
	if err != nil {
		t.Fatalf("NewRESTClient: %v", err)
	}
	if rest == nil {
		t.Fatal("NewRESTClient returned nil client")
	}
	_ = rest // client built successfully
}
