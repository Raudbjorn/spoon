package webdiff

import (
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

const maxResponseBytes = 16 << 20

type FilePatch struct {
	Path  string
	Patch string
}

type Client struct {
	http   *http.Client
	cookie string
	gate   func(context.Context) error
	mu     sync.Mutex
	next   time.Time
}

func New(cookie string, gate func(context.Context) error) *Client {
	return &Client{http: &http.Client{Timeout: 30 * time.Second}, cookie: strings.TrimSpace(cookie), gate: gate}
}

func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	wait := c.next.Sub(now)
	if wait < 0 {
		wait = 0
	}
	c.next = now.Add(time.Second) // 60 RPM
	c.mu.Unlock()
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

func (c *Client) Fetch(ctx context.Context, owner, repo, base, head string) (map[string]string, error) {
	if c.cookie == "" {
		return nil, fmt.Errorf("SPOON_GH_COOKIE is empty")
	}
	start := 0
	out := map[string]string{}
	for {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		u := fmt.Sprintf("https://github.com/%s/%s/diffs/%s..%s?start_entry=%d", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(base), url.PathEscape(head), start)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Cookie", c.cookie)
		req.Header.Set("Accept", "text/html")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.Request.URL.Host != "github.com" || strings.Contains(resp.Request.URL.Path, "/login") {
			resp.Body.Close()
			return nil, fmt.Errorf("GitHub web diff authentication redirect")
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			return nil, fmt.Errorf("GitHub web diff returned HTTP %d", resp.StatusCode)
		}
		limited := io.LimitReader(resp.Body, maxResponseBytes+1)
		body, err := io.ReadAll(limited)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(body) > maxResponseBytes {
			return nil, fmt.Errorf("GitHub web diff exceeded %d bytes", maxResponseBytes)
		}
		patches, next, err := ParseHTML(strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		for path, patch := range patches {
			out[path] += patch
		}
		if next < 0 || next <= start {
			break
		}
		start = next
	}
	return out, nil
}

func ParseHTML(r io.Reader) (map[string]string, int, error) {
	root, err := html.Parse(r)
	if err != nil {
		return nil, -1, fmt.Errorf("parse GitHub diff HTML: %w", err)
	}
	out := map[string]string{}
	next := -1
	var current string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if path := filePath(n); path != "" {
				current = path
			}
			class := attr(n, "class")
			if current != "" && n.Data == "td" && strings.Contains(class, "blob-code") {
				line := strings.TrimSuffix(text(n), "\n")
				switch {
				case strings.Contains(class, "addition"):
					out[current] += "+" + line + "\n"
				case strings.Contains(class, "deletion"):
					out[current] += "-" + line + "\n"
				case strings.Contains(class, "context"):
					out[current] += " " + line + "\n"
				}
			}
			if n.Data == "a" {
				href := attr(n, "href")
				if strings.Contains(strings.ToLower(text(n)), "next") || attr(n, "rel") == "next" {
					if parsed, e := url.Parse(href); e == nil {
						if value, e := strconv.Atoi(parsed.Query().Get("start_entry")); e == nil {
							next = value
						}
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if len(out) == 0 {
		return nil, next, fmt.Errorf("GitHub web diff markup contained no parseable files")
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
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return b.String()
}
