package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

// Client wraps go-gh's REST and GraphQL clients with rate limit tracking.
type Client struct {
	rest             *ghAPI.RESTClient
	gql              *ghAPI.GraphQLClient
	authenticated    bool
	currentUserLogin string // populated lazily by CurrentUserLogin (gated by currentUserLoginOnce)

	currentUserLoginOnce sync.Once
	currentUserLoginErr  error

	mu        sync.Mutex
	rateLimit RateLimit
}

// NewClient creates a new GitHub client. It tries go-gh's default client first
// (which reuses gh CLI tokens). If that fails, it falls back to an
// unauthenticated client using raw HTTP.
func NewClient() (*Client, error) {
	rest, err := ghAPI.DefaultRESTClient()
	if err == nil {
		client := &Client{rest: rest, authenticated: true}
		// Also create GraphQL client for batched T1 queries
		if gql, gqlErr := ghAPI.DefaultGraphQLClient(); gqlErr == nil {
			client.gql = gql
		}
		return client, nil
	}

	// Fall back to unauthenticated client.
	// go-gh requires AuthToken, Host, and Transport all set to skip resolution.
	// We set a dummy token "x" and use a custom transport that strips the auth header.
	rest, err = ghAPI.NewRESTClient(ghAPI.ClientOptions{
		AuthToken: "x",
		Host:      "github.com",
		Transport: &unauthTransport{base: http.DefaultTransport},
	})
	if err != nil {
		return nil, fmt.Errorf("creating unauthenticated client: %w", err)
	}
	return &Client{rest: rest, authenticated: false}, nil
}

// unauthTransport strips the Authorization header so requests are unauthenticated.
type unauthTransport struct {
	base http.RoundTripper
}

func (t *unauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Del("Authorization")
	return t.base.RoundTrip(req)
}

// IsAuthenticated returns whether the client has a valid auth token.
func (c *Client) IsAuthenticated() bool {
	return c.authenticated
}

// GetRateLimit returns the current known rate limit state.
func (c *Client) GetRateLimit() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rateLimit
}

// Headroom returns the fraction of rate limit remaining (0.0-1.0).
// Returns 1.0 if rate limit hasn't been fetched yet.
func (c *Client) Headroom() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rateLimit.Limit == 0 {
		return 1.0
	}
	return float64(c.rateLimit.Remaining) / float64(c.rateLimit.Limit)
}

// HasGraphQL returns true if the GraphQL client is available.
func (c *Client) HasGraphQL() bool {
	return c.gql != nil
}

// HasBudget returns true if there is remaining rate limit budget.
func (c *Client) HasBudget() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rateLimit.Limit == 0 {
		return true // haven't fetched rate limit yet
	}
	threshold := c.rateLimit.Limit / 10
	if threshold < 10 {
		threshold = 10
	}
	return c.rateLimit.Remaining > threshold
}

// Get performs a GET request and unmarshals the JSON response.
func (c *Client) Get(ctx context.Context, path string, result interface{}) error {
	resp, err := c.rest.RequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	c.updateRateLimit(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	return json.Unmarshal(body, result)
}

// GetRaw performs a GET request and returns the raw response for header inspection.
func (c *Client) GetRaw(ctx context.Context, path string) (*http.Response, error) {
	resp, err := c.rest.RequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	c.updateRateLimit(resp)
	return resp, nil
}

// GetPaginated fetches all pages of a paginated endpoint.
// The resultFactory should return a pointer to a slice that JSON can unmarshal into.
// onPage is called for each page of results.
func (c *Client) GetPaginated(ctx context.Context, path string, onPage func(json.RawMessage) error) error {
	url := path
	for url != "" {
		resp, err := c.rest.RequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		c.updateRateLimit(resp)

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("reading response: %w", err)
		}

		if err := onPage(json.RawMessage(body)); err != nil {
			return err
		}

		url = nextPageURL(resp.Header.Get("Link"))
	}
	return nil
}

// updateRateLimit extracts rate limit info from response headers.
func (c *Client) updateRateLimit(resp *http.Response) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if v := resp.Header.Get("X-RateLimit-Limit"); v != "" {
		c.rateLimit.Limit, _ = strconv.Atoi(v)
	}
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		c.rateLimit.Remaining, _ = strconv.Atoi(v)
	}
	if v := resp.Header.Get("X-RateLimit-Used"); v != "" {
		c.rateLimit.Used, _ = strconv.Atoi(v)
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		ts, _ := strconv.ParseInt(v, 10, 64)
		c.rateLimit.Reset = time.Unix(ts, 0)
	}
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextPageURL extracts the next page URL from the Link header.
func nextPageURL(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	for _, part := range strings.Split(linkHeader, ",") {
		matches := linkNextRe.FindStringSubmatch(strings.TrimSpace(part))
		if len(matches) == 2 {
			return matches[1]
		}
	}
	return ""
}
