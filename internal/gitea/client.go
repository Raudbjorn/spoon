package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiV1Prefix     = "/api/v1"
	httpTimeout     = 30 * time.Second
	defaultPageSize = 50
	maxDiffSize     = 10 * 1024 * 1024 // cap raw .diff reads at 10 MB
)

// Client is a Gitea/Forgejo REST API v1 client. Safe for concurrent use; the
// fields are set at construction and never mutated.
type Client struct {
	scheme string // "https"
	host   string // e.g. "codeberg.org"
	token  string // empty when unauthenticated
	http   *http.Client
}

// NewClient returns a Client for host (e.g. "codeberg.org") with an optional
// token. Pass an empty token for unauthenticated public access.
func NewClient(host, token string) *Client {
	return &Client{
		scheme: "https",
		host:   host,
		token:  token,
		http:   &http.Client{Timeout: httpTimeout},
	}
}

func (c *Client) setAuth(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
}

// apiURL builds {scheme}://{host}/api/v1{apiPath}?{q}.
func (c *Client) apiURL(apiPath string, q url.Values) string {
	u := fmt.Sprintf("%s://%s%s%s", c.scheme, c.host, apiV1Prefix, apiPath)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// Get performs a GET to apiPath, decoding JSON into dst (nil to discard). It
// returns the X-Total-Count header value (0 when absent).
func (c *Client) Get(ctx context.Context, apiPath string, q url.Values, dst any) (total int, err error) {
	rawURL := c.apiURL(apiPath, q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build GET %s: %w", rawURL, err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// happy path
	case http.StatusNotFound:
		return 0, fmt.Errorf("GET %s: 404 not found", rawURL)
	case http.StatusForbidden, http.StatusUnauthorized:
		return 0, fmt.Errorf("GET %s: %d access denied (check token)", rawURL, resp.StatusCode)
	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("GET %s: unexpected status %d: %s",
			rawURL, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	total = parseIntHeader(resp.Header.Get("X-Total-Count"))
	if dst != nil {
		if decErr := json.NewDecoder(resp.Body).Decode(dst); decErr != nil {
			return total, fmt.Errorf("decode response from %s: %w", rawURL, decErr)
		}
	}
	return total, nil
}

// Exists reports whether a GET to apiPath returns 2xx. Used for the cheap
// commit-existence probe that locates the merge base. A non-404 transport error
// is surfaced so a network blip isn't mistaken for "absent".
func (c *Client) Exists(ctx context.Context, apiPath string) (bool, error) {
	rawURL := c.apiURL(apiPath, nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, err
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode/100 == 2 {
		return true, nil
	}
	return false, fmt.Errorf("GET %s: unexpected status %d", rawURL, resp.StatusCode)
}

// RawDiff fetches the raw unified diff for a compare range from the web
// endpoint ({scheme}://{host}/{owner}/{repo}/compare/{base}...{head}.diff),
// which — unlike the API's files array — carries per-file +/- lines. base and
// head must already be path-safe (SHAs / simple branch names).
func (c *Client) RawDiff(ctx context.Context, owner, repo, base, head string) (string, error) {
	rawURL := fmt.Sprintf("%s://%s/%s/%s/compare/%s...%s.diff",
		c.scheme, c.host, owner, repo, base, head)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiffSize))
	if err != nil {
		return "", fmt.Errorf("read diff %s: %w", rawURL, err)
	}
	return string(body), nil
}

// GetPaginated calls yield for each page from apiPath using Gitea's page/limit
// pagination, stopping when a short page is returned or X-Total-Count is reached.
func (c *Client) GetPaginated(ctx context.Context, apiPath string, baseQ url.Values, yield func([]json.RawMessage) error) error {
	q := cloneValues(baseQ)
	q.Set("limit", strconv.Itoa(defaultPageSize))
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

func parseIntHeader(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
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
