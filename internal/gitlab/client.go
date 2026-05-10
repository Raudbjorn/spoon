package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	apiV4Prefix     = "/api/v4"
	httpTimeout     = 30 * time.Second
	defaultPageSize = 50
)

// Client is a GitLab REST API v4 client with integrated rate-limit tracking.
// It is safe for concurrent use.
type Client struct {
	host  string
	token string // empty string when unauthenticated
	http  *http.Client

	// Rate-limit counters updated atomically from response headers.
	remaining atomic.Int64 // RateLimit-Remaining
	limit     atomic.Int64 // RateLimit-Limit
	resetAt   atomic.Int64 // Unix timestamp of next window reset
}

// NewClient returns a Client for the given host and optional auth token.
// Pass an empty token for unauthenticated access.
func NewClient(host, token string) *Client {
	c := &Client{
		host:  host,
		token: token,
		http:  &http.Client{Timeout: httpTimeout},
	}
	// Seed with unauthenticated defaults so Headroom() returns 1.0 until
	// we receive actual headers.
	c.limit.Store(int64(rateLimitUnauthed))
	c.remaining.Store(int64(rateLimitUnauthed))
	return c
}

// Headroom returns the current rate-limit headroom in [0.0, 1.0].
func (c *Client) Headroom() float64 {
	lim := c.limit.Load()
	if lim <= 0 {
		return 1.0
	}
	rem := c.remaining.Load()
	if rem < 0 {
		return 0.0
	}
	h := float64(rem) / float64(lim)
	if h > 1.0 {
		return 1.0
	}
	return h
}

// Get performs a single GET request to apiPath (relative, e.g. "/projects/123/forks")
// with the given query parameters. It decodes the JSON response into dst (may be nil
// to discard the body) and returns the X-Total header value (0 if absent).
func (c *Client) Get(ctx context.Context, apiPath string, q url.Values, dst any) (total int, err error) {
	rawURL := fmt.Sprintf("https://%s%s%s", c.host, apiV4Prefix, apiPath)
	if len(q) > 0 {
		rawURL += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build GET %s: %w", rawURL, err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	c.absorbRateLimitHeaders(resp)

	switch resp.StatusCode {
	case http.StatusOK:
		// happy path
	case http.StatusTooManyRequests:
		resetAt := c.resetAt.Load()
		return 0, fmt.Errorf("rate limited (429): retry after %s",
			time.Unix(resetAt, 0).UTC().Format(time.RFC3339))
	case http.StatusNotFound:
		return 0, fmt.Errorf("GET %s: 404 not found", rawURL)
	case http.StatusForbidden, http.StatusUnauthorized:
		return 0, fmt.Errorf("GET %s: %d access denied (check token scopes)", rawURL, resp.StatusCode)
	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("GET %s: unexpected status %d: %s",
			rawURL, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	total = parseIntHeader(resp.Header.Get("X-Total"))

	if dst != nil {
		if decErr := json.NewDecoder(resp.Body).Decode(dst); decErr != nil {
			return total, fmt.Errorf("decode response from %s: %w", rawURL, decErr)
		}
	}

	slog.Debug("GitLab GET succeeded",
		"path", apiPath,
		"total", total,
		"headroom", fmt.Sprintf("%.2f", c.Headroom()),
	)
	return total, nil
}

// CountOnly does a GET with per_page=1 and returns only the X-Total count.
func (c *Client) CountOnly(ctx context.Context, apiPath string, q url.Values) (int, error) {
	merged := cloneValues(q)
	merged.Set("per_page", "1")
	total, err := c.Get(ctx, apiPath, merged, nil)
	return total, err
}

// GetPaginated calls yield for each page of results from apiPath.
// It handles GitLab's page-based pagination automatically.
func (c *Client) GetPaginated(ctx context.Context, apiPath string, baseQ url.Values, yield func([]json.RawMessage) error) error {
	q := cloneValues(baseQ)
	q.Set("per_page", strconv.Itoa(defaultPageSize))

	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		q.Set("page", strconv.Itoa(page))

		var items []json.RawMessage
		total, err := c.Get(ctx, apiPath, q, &items)
		if err != nil {
			return err
		}

		if len(items) == 0 {
			break
		}

		if err := yield(items); err != nil {
			return err
		}

		if len(items) < defaultPageSize {
			break
		}
		if total > 0 && page*defaultPageSize >= total {
			break
		}
	}
	return nil
}

// absorbRateLimitHeaders reads GitLab rate-limit headers and stores them atomically.
func (c *Client) absorbRateLimitHeaders(resp *http.Response) {
	if v := parseIntHeader(resp.Header.Get("RateLimit-Remaining")); v >= 0 {
		c.remaining.Store(int64(v))
	}
	if v := parseIntHeader(resp.Header.Get("RateLimit-Limit")); v > 0 {
		c.limit.Store(int64(v))
	}
	if v := parseIntHeader(resp.Header.Get("RateLimit-Reset")); v > 0 {
		c.resetAt.Store(int64(v))
	}
}

func parseIntHeader(v string) int {
	if v == "" {
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// encodeProjectPath URL-encodes a GitLab project fullPath for use in API paths.
// e.g. "group/subgroup/repo" -> "group%2Fsubgroup%2Frepo"
func encodeProjectPath(fullPath string) string {
	return url.PathEscape(fullPath)
}
