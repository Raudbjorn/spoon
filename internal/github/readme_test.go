package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

// rewriteTransport rewrites the scheme/host of outgoing requests to point at
// the httptest server. We can't make go-gh build URLs that already match
// (it forces https://api.<host>/), so we redirect them at the transport.
type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	req.Host = t.target.Host
	req.Header.Del("Authorization")
	return t.base.RoundTrip(req)
}

// newTestClient builds a *Client whose REST calls land on srv.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	rest, err := ghAPI.NewRESTClient(ghAPI.ClientOptions{
		AuthToken: "x",
		Host:      "github.com",
		Transport: &rewriteTransport{target: u, base: http.DefaultTransport},
	})
	if err != nil {
		t.Fatalf("NewRESTClient: %v", err)
	}
	return &Client{rest: rest, authenticated: true}
}

func TestFetchReadme_Success(t *testing.T) {
	const body = "Hello, world\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/foo/bar/readme") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(readmeResponse{
			Name:     "README.md",
			Path:     "README.md",
			Size:     len(body),
			Encoding: "base64",
			Content:  base64.StdEncoding.EncodeToString([]byte(body)),
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.FetchReadme(context.Background(), "foo", "bar")
	if err != nil {
		t.Fatalf("FetchReadme: %v", err)
	}
	if got != body {
		t.Errorf("got %q, want %q", got, body)
	}
}

func TestFetchReadme_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.FetchReadme(context.Background(), "foo", "bar")
	if err != nil {
		t.Fatalf("want nil error on 404, got %v", err)
	}
	if got != "" {
		t.Errorf("want empty content on 404, got %q", got)
	}
}

func TestFetchReadme_UnexpectedEncoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(readmeResponse{
			Name:     "README.md",
			Encoding: "utf-8",
			Content:  "hello",
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.FetchReadme(context.Background(), "foo", "bar")
	if err == nil {
		t.Fatal("want error for unexpected encoding")
	}
	if !strings.Contains(err.Error(), "encoding") {
		t.Errorf("error should mention encoding: %v", err)
	}
}

func TestFetchReadme_Truncation(t *testing.T) {
	// 100 KB of UTF-8 (multi-byte runes mixed in so we exercise the boundary backoff).
	big := strings.Repeat("héllo world ", (100*1024)/12+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(readmeResponse{
			Name:     "README.md",
			Encoding: "base64",
			Size:     len(big),
			Content:  base64.StdEncoding.EncodeToString([]byte(big)),
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.FetchReadme(context.Background(), "foo", "bar")
	if err != nil {
		t.Fatalf("FetchReadme: %v", err)
	}
	if len(got) > maxReadmeBytes {
		t.Errorf("got %d bytes, want <= %d", len(got), maxReadmeBytes)
	}
	if len(got) == 0 {
		t.Errorf("got empty content; truncation should keep most of the prefix")
	}
	// Must still be valid UTF-8 (no split rune at the tail).
	if !utf8.ValidString(got) {
		t.Errorf("truncated content is not valid UTF-8")
	}
}

func TestFetchReadme_BadBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(readmeResponse{
			Name:     "README.md",
			Encoding: "base64",
			Content:  "!!!not-base64!!!",
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.FetchReadme(context.Background(), "foo", "bar")
	if err == nil {
		t.Fatal("want error for malformed base64")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error should mention decode: %v", err)
	}
}

func TestFetchReadme_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.FetchReadme(context.Background(), "foo", "bar")
	if err == nil {
		t.Fatal("want error on 401")
	}
	if got != "" {
		t.Errorf("want empty content on auth error, got %q", got)
	}
}
