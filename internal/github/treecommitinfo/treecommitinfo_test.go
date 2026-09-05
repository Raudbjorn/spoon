package treecommitinfo

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newTestClient points a Client at srv and rewrites the github.com host to
// the test server, mirroring webdiff/fetch_test.go's rewriteToServer.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := New(nil)
	c.http = srv.Client()
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http.Transport = &rewriteToServer{srv: srv, base: c.http.Transport}
	return c
}

// rewriteToServer redirects every request to the httptest server while
// leaving resp.Request untouched, so code that inspects the response's
// original request (none here, but kept symmetric with webdiff) still sees
// the intended github.com URL.
type rewriteToServer struct {
	srv  *httptest.Server
	base http.RoundTripper
}

func (rt *rewriteToServer) RoundTrip(req *http.Request) (*http.Response, error) {
	target, _ := http.NewRequestWithContext(req.Context(), req.Method, rt.srv.URL+req.URL.Path+"?"+req.URL.RawQuery, req.Body)
	maps.Copy(target.Header, req.Header)
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(target)
	if err == nil && resp != nil {
		resp.Request = req
	}
	return resp, err
}

func TestLastTouch_ParsesEntries(t *testing.T) {
	var gotPath, gotAccept, gotXRW string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		gotXRW = r.Header.Get("X-Requested-With")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":{"a.mjs":{"oid":"abc123","date":"2026-01-01T00:00:00Z"},"b.mjs":{"oid":"def456","date":"2026-01-02T00:00:00Z"}}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	entries, outcome := c.LastTouch(context.Background(), "o", "r", "main", "src")
	if outcome != OK {
		t.Fatalf("outcome = %v, want OK", outcome)
	}
	if entries["a.mjs"] != "abc123" || entries["b.mjs"] != "def456" {
		t.Fatalf("entries = %+v, want a.mjs=abc123 b.mjs=def456", entries)
	}

	if want := "/o/r/tree-commit-info/main/src"; gotPath != want {
		t.Errorf("request path = %q, want %q", gotPath, want)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept header = %q, want application/json", gotAccept)
	}
	if gotXRW != "XMLHttpRequest" {
		t.Errorf("X-Requested-With header = %q, want XMLHttpRequest", gotXRW)
	}
}

// A repo-root lookup (empty dir) must not leave a trailing "/" — the
// endpoint is .../tree-commit-info/{ref} with no further path segment.
func TestLastTouch_RootDirHasNoTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, outcome := c.LastTouch(context.Background(), "o", "r", "main", ""); outcome != OK {
		t.Fatalf("outcome = %v, want OK", outcome)
	}
	if want := "/o/r/tree-commit-info/main"; gotPath != want {
		t.Errorf("request path = %q, want %q", gotPath, want)
	}
}

// A branch ref containing "/" must keep its path structure (each segment
// escaped individually) rather than being escaped as one opaque segment.
func TestLastTouch_RefWithSlashKeepsSegments(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, outcome := c.LastTouch(context.Background(), "o", "r", "feat/x", "a/b"); outcome != OK {
		t.Fatalf("outcome = %v, want OK", outcome)
	}
	if want := "/o/r/tree-commit-info/feat/x/a/b"; gotPath != want {
		t.Errorf("request path = %q, want %q", gotPath, want)
	}
}

func TestLastTouch_404IsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	entries, outcome := c.LastTouch(context.Background(), "o", "r", "no-such-ref", "")
	if outcome != NotFound {
		t.Fatalf("outcome = %v, want NotFound", outcome)
	}
	if entries != nil {
		t.Errorf("entries = %+v, want nil on NotFound", entries)
	}
}

// 429 must trip the run-scoped breaker: the first call reports Disabled and
// every subsequent call must short-circuit without another HTTP round trip.
func TestLastTouch_429DisablesForRestOfRun(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)

	_, outcome := c.LastTouch(context.Background(), "o", "r", "main", "")
	if outcome != Disabled {
		t.Fatalf("first call outcome = %v, want Disabled", outcome)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("server saw %d calls after the first LastTouch, want 1", n)
	}

	_, outcome = c.LastTouch(context.Background(), "other", "repo", "dev", "x")
	if outcome != Disabled {
		t.Fatalf("second call outcome = %v, want Disabled (breaker must stay tripped)", outcome)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("server saw %d calls after the second LastTouch, want 1 (no live request once disabled)", n)
	}
	if reason := c.DisabledReason(); !strings.Contains(reason, "429") {
		t.Errorf("DisabledReason() = %q, want it to mention 429", reason)
	}
}

// A 403 or 5xx trips the same breaker as 429.
func TestLastTouch_403And5xxAlsoDisable(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			if _, outcome := c.LastTouch(context.Background(), "o", "r", "main", ""); outcome != Disabled {
				t.Fatalf("outcome = %v, want Disabled for HTTP %d", outcome, status)
			}
		})
	}
}

// A redirect must be refused, not followed — CheckRedirect returns
// ErrUseLastResponse, so the bare 3xx surfaces as a non-2xx status.
func TestLastTouch_RedirectRefused(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path == "/login" {
			t.Fatal("client followed the redirect to /login")
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	entries, outcome := c.LastTouch(context.Background(), "o", "r", "main", "")
	if outcome != Error {
		t.Fatalf("outcome = %v, want Error (redirect refused, not a breaker trip)", outcome)
	}
	if entries != nil {
		t.Errorf("entries = %+v, want nil", entries)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("server saw %d requests, want 1 (redirect must not be followed)", n)
	}
}

// A response exceeding the body cap must not be parsed, however malformed
// or well-formed its prefix.
func TestLastTouch_BodyCapExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":{`))
		// Pad well past maxResponseBytes with a single long "name" so the
		// handler doesn't need a real 1MiB literal in the source.
		padding := strings.Repeat("a", maxResponseBytes+1)
		_, _ = io.WriteString(w, `"`+padding+`":{"oid":"x"}}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	entries, outcome := c.LastTouch(context.Background(), "o", "r", "main", "")
	if outcome != Error {
		t.Fatalf("outcome = %v, want Error (body exceeds cap)", outcome)
	}
	if entries != nil {
		t.Errorf("entries = %+v, want nil", entries)
	}
}

// The companion to TestLastTouch_BodyCapExceeded: a body that is well-formed
// JSON and sits just under the cap must still parse. Oversized input above
// alone doesn't pin the cap's location -- an oversized *valid* JSON document
// would fail to unmarshal (truncated mid-token) whether or not the explicit
// len(body) > maxResponseBytes check exists, so this test is what actually
// exercises the boundary rather than the JSON parser's own strictness.
func TestLastTouch_ValidBodyJustUnderCapParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A single entry whose oid padding brings the whole body to just
		// under maxResponseBytes, still valid JSON throughout.
		prefix := `{"entries":{"a.mjs":{"oid":"`
		suffix := `"}}}`
		padding := strings.Repeat("a", maxResponseBytes-len(prefix)-len(suffix)-64)
		_, _ = io.WriteString(w, prefix+padding+suffix)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	entries, outcome := c.LastTouch(context.Background(), "o", "r", "main", "")
	if outcome != OK {
		t.Fatalf("outcome = %v, want OK (valid body under the cap)", outcome)
	}
	if !strings.HasPrefix(entries["a.mjs"], "aaa") {
		t.Errorf("entries[a.mjs] = %q (len %d), want the padded oid", entries["a.mjs"][:min(10, len(entries["a.mjs"]))], len(entries["a.mjs"]))
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name                  string
		owner, repo, ref, dir string
		want                  string
	}{
		{"root dir", "o", "r", "main", "", "https://github.com/o/r/tree-commit-info/main"},
		{"nested dir", "o", "r", "main", "a/b", "https://github.com/o/r/tree-commit-info/main/a/b"},
		{"ref with slash", "o", "r", "feat/x", "src", "https://github.com/o/r/tree-commit-info/feat/x/src"},
		{"escapes special chars", "o w", "r", "main", "a b", "https://github.com/o%20w/r/tree-commit-info/main/a%20b"},
		{"empty ref produces no bare slash", "o", "r", "", "", "https://github.com/o/r/tree-commit-info"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildURL(tt.owner, tt.repo, tt.ref, tt.dir); got != tt.want {
				t.Errorf("buildURL(%q,%q,%q,%q) = %q, want %q", tt.owner, tt.repo, tt.ref, tt.dir, got, tt.want)
			}
		})
	}
}
