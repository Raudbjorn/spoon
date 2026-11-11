package webdiff

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Fixtures carry real hunk ranges: without them context completeness is unverifiable.
// page renders a one-file diff whose "next" link points at start_entry=next.
func pageHTML(path string, next int) string {
	return fmt.Sprintf(`<html><body>
<div class="js-file-header" data-path="%s"></div>
<table><tr><td class="blob-code blob-code-hunk">@@ -0,0 +1 @@</td></tr>
<tr><td class="blob-code blob-code-addition">x()</td></tr></table>
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
<table><tr><td class="blob-code blob-code-hunk">@@ -0,0 +1 @@</td></tr><tr><td class="blob-code blob-code-addition">x()</td></tr></table>
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

// TestParseHTMLPaginationLinkValidation checks case-insensitive next relations and rejects invalid pagination targets.
func TestParseHTMLPaginationLinkValidation(t *testing.T) {
	for _, tc := range []struct {
		name, link string
		wantNext   int
		wantErr    bool
	}{
		{"decoy", `<a href="?start_entry=99">next release</a>`, -1, false},
		// Text alone does not establish pagination; invalid decoys must not
		// abort parsing or hide a later valid pagination link.
		{"text missing cursor", `<a href="/features">Next</a>`, -1, false},
		{"text invalid cursor", `<a href="?start_entry=broken">Next</a>`, -1, false},
		{"text negative cursor", `<a href="?start_entry=-1">Next</a>`, -1, false},
		{"text invalid URL", `<a href="%zz">Next</a>`, -1, false},
		{"decoy before text fallback", `<a href="/features">Next</a><a href="?start_entry=25">Next</a>`, 25, false},
		{"decoy before rel", `<a href="%zz">Next</a><a rel="next" href="?start_entry=25">More</a>`, 25, false},
		{"text fallback before rel", `<a href="?start_entry=99">Next</a><a rel="next" href="?start_entry=25">More</a>`, 25, false},
		{"next label", `<a href="?start_entry=25"> Next </a>`, 25, false},
		{"rel tokens", `<a rel="nofollow next" href="?start_entry=25">More</a>`, 25, false},
		{"uppercase rel", `<a rel="NEXT" href="?start_entry=25">Older</a>`, 25, false},
		{"mixed rel tokens", `<a rel="NoFollow NeXt" href="?start_entry=25">More</a>`, 25, false},
		{"mixed rel outranks text", `<a href="?start_entry=99">Next</a><a rel="NeXt" href="?start_entry=25">Older</a>`, 25, false},
		{"invalid mixed rel", `<a rel="NoFollow NEXT" href="?start_entry=broken">Older</a>`, -1, true},
		{"mixed case rel tokens", `<a rel="nofollow NeXt" href="?start_entry=25">More</a>`, 25, false},
		{"uppercase invalid cursor", `<a rel="NEXT" href="?page=2">More</a>`, -1, true},
		{"invalid URL", `<a rel="next" href="%zz">Next</a>`, -1, true},
		{"missing cursor", `<a rel="next" href="?page=2">Next</a>`, -1, true},
		{"invalid cursor", `<a rel="next" href="?start_entry=broken">Next</a>`, -1, true},
		{"negative cursor", `<a rel="next" href="?start_entry=-1">Next</a>`, -1, true},
		{"invalid rel after text fallback", `<a href="?start_entry=25">Next</a><a rel="next" href="?page=2">More</a>`, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := `<div data-path="a.go"></div><table><tr><td class="blob-code blob-code-hunk">@@ -0,0 +1 @@</td></tr><tr><td class="blob-code blob-code-addition">x()</td></tr></table>` + tc.link
			_, next, err := ParseHTML(strings.NewReader(fixture))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && next != tc.wantNext {
				t.Fatalf("next=%d, want %d", next, tc.wantNext)
			}
		})
	}
}

// newTestClient points a webdiff Client at srv and rewrites the github.com host
// to the test server, bypassing the real endpoint while keeping the code path.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := New("cookie", nil)
	c.http.Transport = &rewriteToServer{srv: srv, base: srv.Client().Transport}
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

// A trailing page of binary / rename / mode-change entries has no blob-code
// cells but is perfectly well-formed. Treating it as a parse failure reported
// truncation and the adapter threw away every patch the earlier pages had
// already produced, so one such page cost a whole fork its web diff (#83).
func TestFetchKeepsPatchesWhenTrailingPageHasNoDiffLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("start_entry"))
		if start == 0 {
			fmt.Fprint(w, pageHTML("a.go", 1))
			return
		}
		// Page 2 explains its own emptiness: a binary entry, no textual hunks.
		fmt.Fprint(w, `<html><body><div class="js-file-header" data-path="logo.png"></div>
<span class="file-info">Binary files a/logo.png and b/logo.png differ</span></body></html>`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	patches, truncated, err := c.Fetch(context.Background(), "o", "r", "b", "h")
	if err != nil {
		t.Fatalf("Fetch errored: %v", err)
	}
	if truncated {
		t.Fatal("truncated=true; an empty-but-valid trailing page must not discard the collected patches")
	}
	// Retained headers carry the evidence used to check context completeness.
	if got := patches["a.go"]; got != "@@ -0,0 +1 @@\n+x()\n" {
		t.Fatalf("a.go patch = %q, want the first page's patch retained", got)
	}
}

// The counterpart: a page with headers, no hunks and no explanation is drift,
// and must truncate. Otherwise the caller persists the earlier pages as a
// complete diff (#83).
func TestFetchTruncatesOnUnexplainedEmptyPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("start_entry"))
		if start == 0 {
			fmt.Fprint(w, pageHTML("a.go", 1))
			return
		}
		// Headers survive, hunks do not, and nothing says why.
		fmt.Fprint(w, `<html><body><div class="js-file-header" data-path="b.go"></div>
<table><tr><td class="diff-hunk-cell">+func New()</td></tr></table></body></html>`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, truncated, err := c.Fetch(context.Background(), "o", "r", "b", "h")
	if err != nil {
		t.Fatalf("Fetch errored: %v", err)
	}
	if !truncated {
		t.Fatal("truncated=false; markup that kept its headers but lost its hunks must not pass as a valid empty page")
	}
}

// A readable sibling must not hide the lost continuation of a.go on page 2;
// otherwise its page-1 fragment is returned as a complete patch (#83).
func TestFetchTruncatesOnPartlyParsedPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("start_entry") == "0" {
			fmt.Fprint(w, pageHTML("a.go", 1))
			return
		}
		fmt.Fprint(w, `<div data-path="b.go"></div>
<table><tr><td class="blob-code blob-code-hunk">@@ -0,0 +1 @@</td></tr><tr><td class="blob-code blob-code-addition">readable()</td></tr></table>
<div data-path="a.go"></div>
<table><tr><td class="diff-hunk-cell">lost()</td></tr></table>`)
	}))
	defer srv.Close()

	patches, truncated, err := newTestClient(t, srv).Fetch(context.Background(), "o", "r", "b", "h")
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatalf("partial patch reported complete: %q", patches["a.go"])
	}
}

func TestFetchTruncatesOnBrokenPagination(t *testing.T) {
	for _, cursor := range []string{"1", "0", "broken"} {
		t.Run(cursor, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page := pageHTML("a.go", 1)
				if r.URL.Query().Get("start_entry") != "0" {
					page = strings.Replace(page, "start_entry=1", "start_entry="+cursor, 1)
				}
				fmt.Fprint(w, page)
			}))
			defer srv.Close()

			patches, truncated, err := newTestClient(t, srv).Fetch(context.Background(), "o", "r", "b", "h")
			if err != nil {
				t.Fatal(err)
			}
			if !truncated {
				t.Fatalf("a next link that cannot advance reported complete: %q", patches["a.go"])
			}
		})
	}
}

// TestParseHTMLAcceptsMixedTextAndBinaryFiles preserves textual hunks beside explicitly nontextual files.
func TestParseHTMLAcceptsMixedTextAndBinaryFiles(t *testing.T) {
	fixture := `<div data-path="a.go"></div>
<table><tr><td class="blob-code blob-code-hunk">@@ -0,0 +1 @@</td></tr><tr><td class="blob-code blob-code-addition">x()</td></tr></table>
<div data-path="logo.png"></div><span>Binary file not shown</span>`
	patches, _, err := ParseHTML(strings.NewReader(fixture))
	// A textual sibling retains its hunk header even beside an empty binary entry.
	if err != nil || patches["a.go"] != "@@ -0,0 +1 @@\n+x()\n" || len(patches) != 1 {
		t.Fatalf("mixed page lost its textual patch: patches=%v, err=%v", patches, err)
	}
}

// TestFetchAccumulatesFileAcrossPages validates a hunk only after all of its pages have arrived.
func TestFetchAccumulatesFileAcrossPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("start_entry") == "0" {
			// The first page ends inside a hunk; validate after concatenation.
			fmt.Fprint(w, strings.Replace(pageHTML("a.go", 1), "@@ -0,0 +1 @@", "@@ -1,2 +1,2 @@", 1))
			return
		}
		fmt.Fprint(w, `<div data-path="a.go"></div><table>
<tr><td class="blob-code blob-code-deletion">old()</td></tr>
<tr><td class="blob-code blob-code-context">kept()</td></tr></table>`)
	}))
	defer srv.Close()
	patches, truncated, err := newTestClient(t, srv).Fetch(context.Background(), "o", "r", "b", "h")
	if err != nil || truncated || patches["a.go"] != "@@ -1,2 +1,2 @@\n+x()\n-old()\n kept()\n" {
		t.Fatalf("complete continuation lost: patches=%v, truncated=%v, err=%v", patches, truncated, err)
	}
	if err := ValidatePatch(patches["a.go"]); err != nil {
		t.Fatalf("complete cross-page hunk rejected: %v", err)
	}
}

// TestFetchAccumulatesSeparateHunksAcrossPages retains ordered hunk headers across pagination.
func TestFetchAccumulatesSeparateHunksAcrossPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("start_entry") == "0" {
			fmt.Fprint(w, pageHTML("a.go", 1))
			return
		}
		// Preserve both headers, with ordered ranges: the second page deletes
		// a later line instead of rewinding to before the first page's insertion.
		fmt.Fprint(w, `<div data-path="a.go"></div><table>
<tr><td class="blob-code blob-code-hunk">@@ -10 +10,0 @@</td></tr>
<tr><td class="blob-code blob-code-deletion">old()</td></tr></table>`)
	}))
	defer srv.Close()
	patches, truncated, err := newTestClient(t, srv).Fetch(context.Background(), "o", "r", "b", "h")
	if err != nil || truncated || patches["a.go"] != "@@ -0,0 +1 @@\n+x()\n@@ -10 +10,0 @@\n-old()\n" {
		t.Fatalf("separate hunks lost across pages: patches=%v truncated=%v err=%v", patches, truncated, err)
	}
}

// TestFetchOperationDeadlineIncludesAllWaits bounds each wait using virtual time, which advances only after the fetch reaches a blocking stage.
func TestFetchOperationDeadlineIncludesAllWaits(t *testing.T) {
	for _, stage := range []string{"queued pacing", "pagination pacing", "gate", "http", "shorter caller deadline"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int32
				c := New("test-cookie", nil)
				c.http.Transport = deadlineRoundTrip(func(r *http.Request) (*http.Response, error) {
					requests.Add(1)
					if stage == "http" {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					w := httptest.NewRecorder()
					fmt.Fprint(w, pageHTML("a.go", 1))
					resp := w.Result()
					resp.Request = r
					return resp, nil
				})
				c.fetchTimeout = 50 * time.Millisecond
				ctx := context.Background()
				var parentDeadline, gateDeadline time.Time
				if stage == "shorter caller deadline" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, c.fetchTimeout)
					defer cancel()
					parentDeadline, _ = ctx.Deadline()
					c.fetchTimeout = time.Hour
				}
				if stage == "queued pacing" {
					c.next = time.Now().Add(time.Hour)
				}
				if stage == "gate" || stage == "shorter caller deadline" {
					c.gate = func(ctx context.Context) error {
						gateDeadline, _ = ctx.Deadline()
						<-ctx.Done()
						return ctx.Err()
					}
				}
				started := time.Now()
				patches, _, err := c.Fetch(ctx, "o", "r", "b", "h")
				if !errors.Is(err, context.DeadlineExceeded) || patches != nil {
					t.Fatalf("deadline did not discard incomplete work: patches=%v, err=%v", patches, err)
				}
				if time.Since(started) > 2*time.Second {
					t.Fatal("fetch exceeded its overall deadline")
				}
				wantRequests := int32(0)
				if stage == "http" || stage == "pagination pacing" {
					wantRequests = 1
				}
				if requests.Load() != wantRequests {
					t.Fatalf("requests=%d, want %d", requests.Load(), wantRequests)
				}
				if stage == "shorter caller deadline" && !gateDeadline.Equal(parentDeadline) {
					t.Fatal("fetch extended the caller's shorter deadline")
				}
			})
		})
	}
}

func TestFetchBoundsResponseBytes(t *testing.T) {
	for _, tc := range []struct {
		name, wantError         string
		pageBytes, wantRequests int
	}{
		{"per page", "bytes", maxResponseBytes + 1, 1},
		{"total", "total bytes", maxResponseBytes, maxTotalBytes/maxResponseBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			prefix := pageHTML("a.go", 1) + "<!--"
			body := prefix + strings.Repeat("x", tc.pageBytes-len(prefix)-3) + "-->"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				start, _ := strconv.Atoi(r.URL.Query().Get("start_entry"))
				fmt.Fprint(w, strings.Replace(body, "start_entry=1", fmt.Sprintf("start_entry=%d", start+1), 1))
			}))
			defer srv.Close()
			patches, _, err := newTestClient(t, srv).Fetch(context.Background(), "o", "r", "b", "h")
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("want byte-budget error, got %v", err)
			}
			if patches != nil || int(requests.Load()) != tc.wantRequests {
				t.Fatalf("budget leaked patches or extra requests: patches=%v, requests=%d", patches, requests.Load())
			}
		})
	}
}

func TestFetchDoesNotFollowRedirects(t *testing.T) {
	for _, location := range []string{"http://github.com/next", "https://other.github.com/next", "https://example.invalid/next"} {
		t.Run(location, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))
			defer srv.Close()
			patches, _, err := newTestClient(t, srv).Fetch(context.Background(), "o", "r", "b", "h")
			if err == nil || !strings.Contains(err.Error(), "HTTP 302") || patches != nil || requests.Load() != 1 {
				t.Fatalf("redirect followed or accepted: requests=%d, patches=%v, err=%v", requests.Load(), patches, err)
			}
		})
	}
}

// TestFetchRejectsMissingContext rejects incomplete hunks and unrecognized code-row markup.
func TestFetchRejectsMissingContext(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing context", `<tr><td class="blob-code blob-code-hunk">@@ -1,2 +1,2 @@</td></tr><tr><td class="blob-code blob-code-deletion">old</td></tr><tr><td class="blob-code blob-code-addition">new</td></tr>`},
		{"changed context markup", `<tr><td class="blob-code blob-code-hunk">@@ -1,2 +1,2 @@</td></tr><tr><td class="changed-context-class">lost context</td></tr><tr><td class="blob-code blob-code-deletion">old</td></tr><tr><td class="blob-code blob-code-addition">new</td></tr>`},
		{"missing header", `<tr><td class="blob-code blob-code-deletion">old</td></tr><tr><td class="blob-code blob-code-addition">new</td></tr>`},
		{"unknown code row", `<tr><td class="blob-code blob-code-hunk">@@ -1 +1 @@</td></tr><tr><td class="blob-code unknown">lost</td></tr><tr><td class="blob-code blob-code-deletion">old</td></tr><tr><td class="blob-code blob-code-addition">new</td></tr>`},
		{"multiline row", "<tr><td class=\"blob-code blob-code-hunk\">@@ -1 +1 @@</td></tr><tr><td class=\"blob-code blob-code-deletion\">old\n+new</td></tr>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `<div data-path="a.go"></div><table>`+tc.body+`</table>`)
			}))
			defer srv.Close()
			patches, truncated, err := newTestClient(t, srv).Fetch(context.Background(), "o", "r", "b", "h")
			if err == nil || patches != nil {
				t.Fatalf("accepted incomplete context despite matching +/- counts: patches=%v truncated=%v err=%v", patches, truncated, err)
			}
		})
	}
}

// TestFetchRejectsMalformedHunkEvenBesideCompleteFile discards an entire scrape containing a malformed hunk.
func TestFetchRejectsMalformedHunkEvenBesideCompleteFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<div data-path="complete.go"></div><table>
<tr><td class="blob-code blob-code-hunk">@@ -0,0 +1 @@</td></tr>
<tr><td class="blob-code blob-code-addition">valid</td></tr></table>
<div data-path="partial.go"></div><table>
<tr><td class="blob-code blob-code-hunk">@@ -1,2 +1,2 @@</td></tr>
<tr><td class="blob-code blob-code-deletion">old</td></tr>
<tr><td class="blob-code blob-code-addition">new</td></tr></table>`)
	}))
	defer srv.Close()
	patches, _, err := newTestClient(t, srv).Fetch(context.Background(), "o", "r", "b", "h")
	// Fetch's published contract rejects an untrustworthy scrape wholesale;
	// the adapter's per-file REST checks are a separate acceptance boundary.
	if err == nil || patches != nil {
		t.Fatalf("malformed scrape leaked valid-looking sibling: patches=%v err=%v", patches, err)
	}
}

// TestFetchDeadlineDiscardsCollectedPatches expires only after collecting a page and reaching the next HTTP or pacing wait.
func TestFetchDeadlineDiscardsCollectedPatches(t *testing.T) {
	for _, waitIn := range []string{"request", "pacing"} {
		t.Run(waitIn, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int32
				c := New("test-cookie", nil)
				c.http.Transport = deadlineRoundTrip(func(r *http.Request) (*http.Response, error) {
					requests.Add(1)
					if waitIn == "request" && r.URL.Query().Get("start_entry") != "0" {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					w := httptest.NewRecorder()
					fmt.Fprint(w, pageHTML("a.go", 1))
					resp := w.Result()
					resp.Request = r
					return resp, nil
				})
				wantRequests := int32(1)
				if waitIn == "request" {
					wantRequests = 2
					// Exercise a later HTTP request without spending the budget pacing.
					c.gate = func(context.Context) error {
						c.next = time.Time{}
						return nil
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				patches, _, err := c.Fetch(ctx, "o", "r", "b", "h")
				if !errors.Is(err, context.DeadlineExceeded) || patches != nil || requests.Load() != wantRequests {
					t.Fatalf("deadline failed: patches=%v requests=%d err=%v", patches, requests.Load(), err)
				}
			})
		})
	}
}

// TestFetchSuppliesOverallDeadline verifies that the request gate receives the whole-fetch deadline.
func TestFetchSuppliesOverallDeadline(t *testing.T) {
	var deadline time.Time
	c := New("cookie", func(ctx context.Context) error {
		deadline, _ = ctx.Deadline()
		return context.Canceled // Inspect the context without contacting GitHub.
	})
	before := time.Now()
	_, _, err := c.Fetch(context.Background(), "o", "r", "b", "h")
	if !errors.Is(err, context.Canceled) || deadline.IsZero() || deadline.After(before.Add(2*time.Minute+time.Second)) {
		t.Fatalf("scrape lacks a bounded overall deadline: deadline=%v err=%v", deadline, err)
	}
}

// TestFetchValidatesHunkAcrossPages checks context completeness after joining a split hunk.
func TestFetchValidatesHunkAcrossPages(t *testing.T) {
	for _, omitContext := range []bool{false, true} {
		t.Run(fmt.Sprintf("omit_context=%v", omitContext), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `<div data-path="a.go"></div><table>`)
				if r.URL.Query().Get("start_entry") == "0" {
					fmt.Fprint(w, `<tr><td class="blob-code blob-code-hunk">@@ -1,2 +1,2 @@</td></tr><tr><td class="blob-code blob-code-deletion">old</td></tr></table><a rel="NEXT" href="?start_entry=1">More</a>`)
					return
				}
				fmt.Fprint(w, `<tr><td class="blob-code blob-code-addition">new</td></tr>`)
				if !omitContext {
					fmt.Fprint(w, `<tr><td class="blob-code blob-code-context">keep</td></tr>`)
				}
				fmt.Fprint(w, `</table>`)
			}))
			defer srv.Close()
			patches, truncated, err := newTestClient(t, srv).Fetch(context.Background(), "o", "r", "b", "h")
			if omitContext {
				if err == nil || patches != nil {
					t.Fatalf("accepted split hunk with lost context: patches=%v err=%v", patches, err)
				}
			} else if err != nil || truncated || patches["a.go"] != "@@ -1,2 +1,2 @@\n-old\n+new\n keep\n" {
				t.Fatalf("lost complete split hunk: patches=%v truncated=%v err=%v", patches, truncated, err)
			}
		})
	}
}

// deadlineRoundTrip serves fixtures in the synctest bubble without real network scheduling.
type deadlineRoundTrip func(*http.Request) (*http.Response, error)

// RoundTrip handles one request synchronously with the test clock.
func (f deadlineRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
