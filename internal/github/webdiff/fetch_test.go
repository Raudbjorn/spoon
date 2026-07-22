package webdiff

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// page renders a one-file diff whose "next" link points at start_entry=next.
func pageHTML(path string, next int) string {
	return fmt.Sprintf(`<html><body>
<div class="js-file-header" data-path="%s"></div>
<table><tr><td class="blob-code blob-code-addition">x()</td></tr></table>
<a rel="next" href="?start_entry=%d">Next</a>
</body></html>`, path, next)
}

// A later page that fails to parse must surface as truncated, not as a silent
// partial map the caller persists as a complete diff (#83).
func TestFetchReportsTruncationOnLatePageFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("start_entry"))
		if start == 0 {
			// Page 1 parses and points at page 2.
			fmt.Fprint(w, pageHTML("a.go", 1))
			return
		}
		// Page 2 is unparseable markup.
		fmt.Fprint(w, `<html><body>drifted</body></html>`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	patches, truncated, err := c.Fetch(context.Background(), "o", "r", "b", "h")
	if err != nil {
		t.Fatalf("Fetch errored: %v", err)
	}
	if !truncated {
		t.Fatal("truncated=false; a late unparseable page must be reported as truncation")
	}
	if patches["a.go"] == "" {
		t.Fatal("expected the first page's patch to have been collected")
	}
}

// A strictly-increasing start_entry (markup drift, stale proxy) must not loop
// forever — the page cap bounds it (#83).
func TestFetchBoundsPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("start_entry"))
		fmt.Fprint(w, pageHTML(fmt.Sprintf("f%d.go", start), start+1)) // always advances
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	c.maxPages = 3
	_, _, err := c.Fetch(context.Background(), "o", "r", "b", "h")
	if err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("want a page-cap error, got %v", err)
	}
}

// A rel="next" link is authoritative and must win over a decoy <a> whose text
// merely contains "next" (e.g. a file or repo named next) (#83).
func TestParseHTMLNextLinkPrefersRel(t *testing.T) {
	fixture := `<html><body>
<div class="js-file-header" data-path="a.go"></div>
<table><tr><td class="blob-code blob-code-addition">x()</td></tr></table>
<a href="?start_entry=99">next release</a>
<a rel="next" href="?start_entry=25">Older</a>
</body></html>`
	_, next, err := ParseHTML(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if next != 25 {
		t.Fatalf("next=%d, want 25 (rel=next must outrank the decoy text link)", next)
	}
}

// newTestClient points a webdiff Client at srv and rewrites the github.com host
// to the test server, bypassing the real endpoint while keeping the code path.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := New("cookie", nil)
	c.http = srv.Client()
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http.Transport = &rewriteToServer{srv: srv, base: c.http.Transport}
	return c
}

// rewriteToServer redirects every request to the httptest server while leaving
// resp.Request.URL.Host as "github.com" so the client's auth-redirect guard
// still passes.
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
		// Present the original github.com URL so the host check passes.
		resp.Request = req
	}
	return resp, err
}
