package webdiff

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const (
	maxResponseBytes = 16 << 20  // per page
	maxTotalBytes    = 128 << 20 // cumulative across all pages
	maxPages         = 512       // bound the pagination loop
)

type FilePatch struct {
	Path  string
	Patch string
}

type Client struct {
	http     *http.Client
	cookie   string
	gate     func(context.Context) error
	maxPages int
	mu       sync.Mutex
	next     time.Time
}

func New(cookie string, gate func(context.Context) error) *Client {
	return &Client{
		maxPages: maxPages,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Do not auto-follow redirects: the session cookie is a sensitive
			// header, and following a 301 to http:// or to a *.github.com host
			// would re-send it in cleartext or to an unintended origin. Surface
			// the 3xx as a non-2xx status instead so the caller records a skip
			// reason rather than leaking the cookie.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		cookie: strings.TrimSpace(cookie),
		gate:   gate,
	}
}

// minInterval paces web-diff requests at 60 RPM. These hit github.com's HTML
// endpoints with a session cookie rather than the API, so bursts are what get a
// session flagged — the spacing matters more here than for the REST client.
const minInterval = time.Second

// reserve claims the next request slot and reports how long the caller must
// sleep before using it. Slots are handed out by advancing c.next rather than
// overwriting it: concurrent callers must each take a distinct slot, otherwise
// they all sleep until roughly the same instant and then fire together, which
// is the exact burst the pacing exists to prevent.
func (c *Client) reserve(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.next.After(now) {
		// Queue behind the callers already holding later slots.
		wait := c.next.Sub(now)
		c.next = c.next.Add(minInterval)
		return wait
	}
	// Idle long enough that the previous slot has passed; take it now.
	c.next = now.Add(minInterval)
	return 0
}

// wait blocks until this caller owns the next request slot.
func (c *Client) wait(ctx context.Context) error {
	// Check before reserving: an already-cancelled caller must not consume a
	// slot (which would advance c.next and delay live callers), and on the
	// idle path where reserve returns 0 it must still surface the cancellation
	// rather than returning nil.
	if err := ctx.Err(); err != nil {
		return err
	}
	wait := c.reserve(time.Now())
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.gate != nil {
		return c.gate(ctx)
	}
	return nil
}

// Fetch scrapes the HTML diff pages for base..head. The returned bool reports
// truncation: true means a later page could not be parsed and the collected
// patches are incomplete, so the caller must not persist them as whole diffs.
func (c *Client) Fetch(ctx context.Context, owner, repo, base, head string) (map[string]string, bool, error) {
	if c.cookie == "" {
		return nil, false, fmt.Errorf("SPOON_GH_COOKIE is empty")
	}
	start := 0
	acc := map[string]*strings.Builder{}
	var total int64
	truncated := false
	for page := 0; ; page++ {
		if page >= c.maxPages {
			// A response sequence emitting a strictly-increasing start_entry
			// (markup drift, a stale caching proxy) would otherwise loop forever.
			return nil, false, fmt.Errorf("GitHub web diff exceeded %d pages", c.maxPages)
		}
		if err := c.wait(ctx); err != nil {
			return nil, false, err
		}
		u := fmt.Sprintf("https://github.com/%s/%s/diffs/%s..%s?start_entry=%d", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(base), url.PathEscape(head), start)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Cookie", c.cookie)
		req.Header.Set("Accept", "text/html")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, false, err
		}
		if resp.Request.URL.Host != "github.com" || strings.Contains(resp.Request.URL.Path, "/login") {
			resp.Body.Close()
			return nil, false, fmt.Errorf("GitHub web diff authentication redirect")
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			return nil, false, fmt.Errorf("GitHub web diff returned HTTP %d", resp.StatusCode)
		}
		limited := io.LimitReader(resp.Body, maxResponseBytes+1)
		body, err := io.ReadAll(limited)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if len(body) > maxResponseBytes {
			return nil, false, fmt.Errorf("GitHub web diff exceeded %d bytes", maxResponseBytes)
		}
		total += int64(len(body))
		if total > maxTotalBytes {
			return nil, false, fmt.Errorf("GitHub web diff exceeded %d total bytes", int64(maxTotalBytes))
		}
		patches, next, err := ParseHTML(bytes.NewReader(body))
		if err != nil {
			// A later page failed to parse. We cannot tell which files are
			// complete and which had hunks spanning into the failed page, so
			// report truncation and let the caller discard rather than persist a
			// leading fragment as a whole diff. The first page still fails
			// loudly — an unparseable opening page means the scrape is unreliable.
			if len(acc) > 0 {
				truncated = true
				break
			}
			return nil, false, err
		}
		for path, patch := range patches {
			b := acc[path]
			if b == nil {
				b = &strings.Builder{}
				acc[path] = b
			}
			b.WriteString(patch)
		}
		if next < 0 || next <= start {
			break
		}
		start = next
	}
	out := make(map[string]string, len(acc))
	for path, b := range acc {
		out[path] = b.String()
	}
	return out, truncated, nil
}

func ParseHTML(r io.Reader) (map[string]string, int, error) {
	root, err := html.Parse(r)
	if err != nil {
		return nil, -1, fmt.Errorf("parse GitHub diff HTML: %w", err)
	}
	acc := map[string]*strings.Builder{}
	next := -1
	nextAuthoritative := false // a rel="next" link outranks a text-"next" match
	sawFile := false
	var current string
	// Traverse iteratively with an explicit stack rather than recursing: the
	// DOM is externally supplied, and although html.Parse caps tree depth, an
	// explicit stack makes the bound a heap allocation rather than the Go stack
	// — whose exhaustion is fatal and uncatchable (#83).
	stack := []*html.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.Type == html.ElementNode {
			if path := filePath(n); path != "" {
				current = path
				sawFile = true
			}
			class := attr(n, "class")
			if current != "" && n.Data == "td" && strings.Contains(class, "blob-code") {
				line := strings.TrimSuffix(text(n), "\n")
				b := acc[current]
				if b == nil {
					b = &strings.Builder{}
					acc[current] = b
				}
				var prefix byte
				switch {
				case strings.Contains(class, "addition"):
					prefix = '+'
				case strings.Contains(class, "deletion"):
					prefix = '-'
				case strings.Contains(class, "context"):
					prefix = ' '
				}
				if prefix != 0 {
					b.WriteByte(prefix)
					b.WriteString(line)
					b.WriteByte('\n')
				}
			}
			if n.Data == "a" && !nextAuthoritative {
				rel := attr(n, "rel") == "next"
				// Only a rel="next" link, or the FIRST <a> whose text is "next",
				// drives pagination — otherwise a file or repo literally named
				// "next" could hijack start_entry. rel="next" is authoritative and
				// stops any later text-match from overriding it.
				if rel || (next < 0 && strings.Contains(strings.ToLower(text(n)), "next")) {
					if parsed, e := url.Parse(attr(n, "href")); e == nil {
						if value, e := strconv.Atoi(parsed.Query().Get("start_entry")); e == nil {
							next = value
							if rel {
								nextAuthoritative = true
							}
						}
					}
				}
			}
		}
		// Push right-to-left so the stack pops in document (pre-order) order —
		// the current-file state machine needs a file header visited before its
		// descendant blob-code cells.
		for child := n.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, child)
		}
	}
	// A page whose file headers we recognised but which carries no blob-code
	// cells is well-formed and merely empty — a range of binary, rename or
	// mode-change entries renders exactly like that. Failing it would make Fetch
	// report truncation and cost the fork every patch collected so far. Only a
	// page that resolved no file header at all is markup we no longer understand.
	if len(acc) == 0 && !sawFile {
		return nil, next, fmt.Errorf("GitHub web diff markup contained no parseable files")
	}
	out := make(map[string]string, len(acc))
	for path, b := range acc {
		out[path] = b.String()
	}
	return out, next, nil
}

func filePath(n *html.Node) string {
	for _, key := range []string{"data-path", "data-file-path", "data-tagsearch-path"} {
		if value := strings.TrimSpace(attr(n, key)); value != "" {
			return value
		}
	}
	if strings.Contains(attr(n, "class"), "file-info") || strings.Contains(attr(n, "class"), "js-file-header") {
		return strings.TrimSpace(attr(n, "data-path"))
	}
	return ""
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func text(n *html.Node) string {
	// Fast paths for the common cases — a diff cell is almost always an element
	// with a single text child — so we avoid the stack-slice and Builder
	// allocations on the hot path (called for every td and a element).
	if n == nil {
		return ""
	}
	if n.FirstChild == nil {
		if n.Type == html.TextNode {
			return n.Data
		}
		return ""
	}
	if n.FirstChild.NextSibling == nil && n.FirstChild.Type == html.TextNode {
		return n.FirstChild.Data
	}
	var b strings.Builder
	// Iterative like ParseHTML's walk: this runs its own traversal over the
	// subtree handed to it, so it must not depend on Go stack depth either.
	stack := []*html.Node{n}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for child := node.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, child)
		}
	}
	return b.String()
}
