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

	// Rate controls (Feature: rate-limit hardening). lim paces requests; the
	// bounded Retry-After retry uses maxRateWait + sleepFn (injectable in tests).
	lim         *limiter
	maxRateWait time.Duration
	sleepFn     func(context.Context, time.Duration) error
}

// Rate-control tuning.
const (
	minRefillRate   = 0.05             // never fully stall
	refillBurst     = 8.0              // allow short parallelism spikes
	lowHeadroom     = 0.20             // matches branches.go's gate
	lowHeadroomSlow = 0.25             // multiplicative slowdown under pressure
	defaultMaxWait  = 30 * time.Second // cap on Retry-After sleep before failing fast
)

// initRateControls sets up the token bucket + retry knobs. Called by NewClient
// after `authenticated` is set (the default rate depends on it).
func (c *Client) initRateControls() {
	c.maxRateWait = defaultMaxWait
	c.sleepFn = ctxSleep
	c.lim = newLimiter(c.refillRate(), refillBurst)
}

// refillRate computes the token-bucket rate (req/sec): pace Remaining over the
// time until Reset, clamped to [minRefillRate, refillBurst]. Before the first
// response (Limit==0) it falls back to a conservative per-tier default. Slows
// hard when headroom is low to avoid tripping secondary limits.
func (c *Client) refillRate() float64 {
	c.mu.Lock()
	limit, remaining, reset := c.rateLimit.Limit, c.rateLimit.Remaining, c.rateLimit.Reset
	c.mu.Unlock()

	// Unknown budget, or plenty of headroom: run at the burst cap (fast) so
	// small scans aren't needlessly slowed. Only pace down as the window nears
	// exhaustion — that's when throttling actually prevents hitting the limit.
	if limit == 0 || float64(remaining)/float64(limit) >= 0.5 {
		return refillBurst
	}
	secs := time.Until(reset).Seconds()
	var rate float64
	if secs < 1 || remaining <= 0 {
		rate = minRefillRate
	} else {
		rate = float64(remaining) / secs // pace to land near the reset boundary
	}
	if float64(remaining)/float64(limit) < lowHeadroom {
		rate *= lowHeadroomSlow // extra slowdown when very low, dodge secondary limits
	}
	if rate < minRefillRate {
		rate = minRefillRate
	}
	if rate > refillBurst {
		rate = refillBurst
	}
	return rate
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
		client.initRateControls()
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
	c := &Client{rest: rest, authenticated: false}
	c.initRateControls()
	return c, nil
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

// doGet runs a throttled GET (token bucket + bounded Retry-After retry),
// returning the raw response on success and updating rate-limit state/pacing.
func (c *Client) doGet(ctx context.Context, path string) (*http.Response, error) {
	// lim is nil for Client literals constructed in tests (no NewClient); the
	// throttle is simply absent there.
	if c.lim != nil {
		if err := c.lim.Wait(ctx); err != nil {
			return nil, err
		}
	}
	var resp *http.Response
	err := c.doWithRetry(ctx, func() error {
		r, e := c.rest.RequestWithContext(ctx, http.MethodGet, path, nil)
		if e != nil {
			if rl := detectRateLimitFromHTTPError(e); rl != nil {
				return rl
			}
			return e
		}
		resp = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	c.updateRateLimit(resp)
	if c.lim != nil {
		c.lim.SetRate(c.refillRate()) // two sequential statements: never nest c.mu and lim.mu
	}
	return resp, nil
}

// Get performs a GET request and unmarshals the JSON response.
func (c *Client) Get(ctx context.Context, path string, result interface{}) error {
	resp, err := c.doGet(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	return json.Unmarshal(body, result)
}

// GetRaw performs a GET request and returns the raw response for header inspection.
func (c *Client) GetRaw(ctx context.Context, path string) (*http.Response, error) {
	return c.doGet(ctx, path)
}

// GetPaginated fetches all pages of a paginated endpoint.
// The resultFactory should return a pointer to a slice that JSON can unmarshal into.
// onPage is called for each page of results.
func (c *Client) GetPaginated(ctx context.Context, path string, onPage func(json.RawMessage) error) error {
	url := path
	for url != "" {
		resp, err := c.doGet(ctx, url)
		if err != nil {
			return err
		}

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
