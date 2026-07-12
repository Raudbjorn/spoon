package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
	"github.com/svnbjrn/spoon/internal/github/webdiff"
)

type ProxyOptions struct {
	Enabled           bool
	APIKeyFile        string
	StaticFile        string
	WhitelistPublicIP bool
	CacheTTL          time.Duration
}

type ClientOptions struct {
	Tokens            []string
	RequestsPerMinute float64
	Proxy             ProxyOptions
}

type apiResource uint8

const (
	resourceREST apiResource = iota
	resourceGraphQL
)

type budgetState struct {
	RateLimit RateLimit
	Cost      int
	Limiter   *limiter
}

type backend struct {
	Rest          *ghAPI.RESTClient
	GraphQL       *ghAPI.GraphQLClient
	Login         string
	REST          budgetState
	GraphQLBudget budgetState
	Successes     uint64
	Failures      uint64
	Disabled      bool
	DisabledUntil time.Time
}

// Client dispatches GitHub requests across distinct authenticated identities.
// The legacy fields remain private aliases for package tests and are folded into
// one backend lazily; production traffic always passes through a backend pool.
type Client struct {
	backends []*backend
	pool     *backendPool
	proxies  *proxyPool
	global   *limiter
	webDiff  *webdiff.Client

	rest                *ghAPI.RESTClient
	gql                 *ghAPI.GraphQLClient
	authenticated       bool
	duplicateIdentities int

	currentUserLogin     string
	currentUserLoginOnce sync.Once
	currentUserLoginErr  error

	mu        sync.Mutex
	rateLimit RateLimit

	maxRateWait time.Duration
	sleepFn     func(context.Context, time.Duration) error
}

const (
	minRefillRate   = 0.05
	refillBurst     = 8.0
	lowHeadroom     = 0.20
	lowHeadroomSlow = 0.25
	defaultMaxWait  = 30 * time.Second
	defaultRPM      = 300.0
	globalBurst     = 5.0
)

func (c *Client) initRateControls() {
	c.maxRateWait = defaultMaxWait
	c.sleepFn = ctxSleep
	if c.global == nil {
		c.global = newLimiterRPM(defaultRPM, globalBurst)
	}
}

// refillRate maps observed REST rate-limit headroom to a per-second refill
// rate. Production pacing uses the per-backend limiters (restRPM); this method
// is retained as the unit-tested reference for the headroom→rate curve.
func (c *Client) refillRate() float64 {
	c.mu.Lock()
	rl := c.rateLimit
	c.mu.Unlock()
	return rateForREST(rl) / 60
}

func restRPM(b *backend) float64 { return rateForREST(b.REST.RateLimit) }

func rateForREST(rl RateLimit) float64 {
	if rl.Limit == 0 || float64(rl.Remaining)/float64(rl.Limit) >= 0.5 {
		return refillBurst * 60
	}
	secs := time.Until(rl.Reset).Seconds()
	rate := minRefillRate
	if secs >= 1 && rl.Remaining > 0 {
		rate = float64(rl.Remaining) / secs
	}
	if float64(rl.Remaining)/float64(rl.Limit) < lowHeadroom {
		rate *= lowHeadroomSlow
	}
	if rate < minRefillRate {
		rate = minRefillRate
	}
	if rate > refillBurst {
		rate = refillBurst
	}
	return rate * 60
}

func NewClient() (*Client, error) { return NewClientWithOptions(ClientOptions{}) }

func NewClientWithOptions(opts ClientOptions) (*Client, error) {
	rpm := opts.RequestsPerMinute
	if rpm == 0 {
		rpm = defaultRPM
	}
	if rpm <= 0 || rpm > 900 {
		return nil, fmt.Errorf("requests per minute must be in (0, 900]")
	}
	proxies, _ := bootstrapProxyPool(context.Background(), opts.Proxy)
	rotating := newRotatingProxyTransport(proxies)
	c := &Client{proxies: proxies, global: newLimiterRPM(rpm, globalBurst), authenticated: len(opts.Tokens) > 0}

	if len(opts.Tokens) == 0 {
		// Proxy routing only attaches to explicit config-token backends (and the
		// unauthenticated fallback). go-gh's DefaultRESTClient builds its own
		// transport, so a proxy configured without config tokens silently does
		// nothing — warn rather than mislead.
		if opts.Proxy.Enabled {
			slog.Warn("github: proxy configured but no github.tokens set; proxy routing is inactive on the gh-default token path (add github.tokens to enable it)")
		}
		rest, err := ghAPI.DefaultRESTClient()
		if err == nil {
			b := &backend{Rest: rest, REST: newBudget(), GraphQLBudget: newBudget()}
			if gql, gqlErr := ghAPI.DefaultGraphQLClient(); gqlErr == nil {
				b.GraphQL = gql
			}
			c.authenticated = true
			c.installBackends([]*backend{b})
			c.initRateControls()
			return c, nil
		}
		rest, err = ghAPI.NewRESTClient(ghAPI.ClientOptions{AuthToken: "x", Host: "github.com", Transport: &unauthTransport{base: rotating}})
		if err != nil {
			return nil, fmt.Errorf("creating unauthenticated client: %w", err)
		}
		c.authenticated = false
		c.installBackends([]*backend{{Rest: rest, REST: newBudget(), GraphQLBudget: newBudget()}})
		c.initRateControls()
		return c, nil
	}

	backends := make([]*backend, 0, len(opts.Tokens))
	for _, raw := range opts.Tokens {
		token := strings.TrimSpace(raw)
		if token == "" {
			return nil, fmt.Errorf("github token list contains an empty entry")
		}
		clientOpts := ghAPI.ClientOptions{AuthToken: token, Host: "github.com", Transport: rotating}
		rest, err := ghAPI.NewRESTClient(clientOpts)
		if err != nil {
			return nil, fmt.Errorf("creating GitHub REST backend: %w", err)
		}
		gql, err := ghAPI.NewGraphQLClient(clientOpts)
		if err != nil {
			return nil, fmt.Errorf("creating GitHub GraphQL backend: %w", err)
		}
		backends = append(backends, &backend{Rest: rest, GraphQL: gql, REST: newBudget(), GraphQLBudget: newBudget()})
	}
	c.installBackends(backends)
	c.initRateControls()
	return c, nil
}

func newBudget() budgetState {
	return budgetState{Cost: 1, Limiter: newLimiterRPM(refillBurst*60, refillBurst)}
}

func (c *Client) installBackends(backends []*backend) {
	c.backends = backends
	c.pool = &backendPool{backends: backends}
	if len(backends) > 0 {
		c.rest = backends[0].Rest
		c.gql = backends[0].GraphQL
	}
}

func (c *Client) ensurePool() {
	if c.pool != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pool != nil {
		return
	}
	b := &backend{Rest: c.rest, GraphQL: c.gql, REST: newBudget(), GraphQLBudget: newBudget()}
	c.backends = []*backend{b}
	c.pool = &backendPool{backends: c.backends}
	if c.global == nil {
		c.global = newLimiterRPM(defaultRPM, globalBurst)
	}
}

func (c *Client) Close() { newRotatingProxyTransport(c.proxies).CloseIdleConnections() }

// EnableWebDiff enables the explicitly unstable, cookie-authenticated HTML
// fallback. The cookie remains memory-only.
func (c *Client) EnableWebDiff(cookie string) {
	c.webDiff = webdiff.New(cookie, func(ctx context.Context) error {
		if c.global == nil {
			return nil
		}
		return c.global.Wait(ctx)
	})
}

type unauthTransport struct{ base http.RoundTripper }

func (t *unauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Del("Authorization")
	return t.base.RoundTrip(req)
}

func (c *Client) IsAuthenticated() bool { return c.authenticated }

func (c *Client) DuplicateIdentities() int { return c.duplicateIdentities }

func (c *Client) GetRateLimit() RateLimit {
	c.ensurePool()
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	return aggregateRateLimit(c.backends, resourceREST)
}

func (c *Client) Headroom() float64 {
	rest := c.resourceHeadroom(resourceREST)
	if c.HasGraphQL() {
		if gql := c.resourceHeadroom(resourceGraphQL); gql < rest {
			return gql
		}
	}
	return rest
}

func (c *Client) resourceHeadroom(resource apiResource) float64 {
	c.ensurePool()
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	rl := aggregateRateLimit(c.backends, resource)
	if rl.Limit == 0 {
		return 1
	}
	return float64(rl.Remaining) / float64(rl.Limit)
}

func aggregateRateLimit(backends []*backend, resource apiResource) RateLimit {
	var out RateLimit
	for _, b := range backends {
		if b.Disabled {
			continue
		}
		rl := b.REST.RateLimit
		if resource == resourceGraphQL {
			rl = b.GraphQLBudget.RateLimit
		}
		out.Limit += rl.Limit
		out.Remaining += rl.Remaining
		out.Used += rl.Used
		if rl.Reset.After(out.Reset) {
			out.Reset = rl.Reset
		}
	}
	return out
}

func (c *Client) HasGraphQL() bool {
	c.ensurePool()
	for _, b := range c.backends {
		if !b.Disabled && b.GraphQL != nil {
			return true
		}
	}
	return false
}

func (c *Client) HasBudget() bool {
	rl := c.GetRateLimit()
	if rl.Limit == 0 {
		return true
	}
	threshold := max(10, rl.Limit/10)
	return rl.Remaining > threshold
}

func (c *Client) waitRequest(ctx context.Context, lim *limiter) error {
	if lim != nil {
		if err := lim.Wait(ctx); err != nil {
			return err
		}
	}
	if c.global != nil {
		return c.global.Wait(ctx)
	}
	return nil
}

func (c *Client) doGet(ctx context.Context, path string) (*http.Response, error) {
	c.ensurePool()
	b, err := c.pool.nextBackend(time.Now())
	if err != nil {
		return nil, err
	}
	var resp *http.Response
	err = c.doWithRetry(ctx, func() error {
		var requestErr error
		for attempt := range 3 {
			if err := c.waitRequest(ctx, b.REST.Limiter); err != nil {
				return err
			}
			resp, requestErr = b.Rest.RequestWithContext(ctx, http.MethodGet, path, nil)
			if requestErr == nil || !isGatewayOrTransportError(requestErr) || attempt == 2 {
				break
			}
			if err := proxyBackoff(ctx, c.sleep(), attempt); err != nil {
				return err
			}
		}
		if requestErr != nil {
			if rl := detectRateLimitFromHTTPError(requestErr); rl != nil {
				c.pool.disableUntil(b, rl.ResetAt, false)
				return rl
			}
			if statusCode(requestErr) == http.StatusUnauthorized {
				c.pool.disableUntil(b, time.Time{}, true)
			} else if !isGatewayOrTransportError(requestErr) {
				c.pool.reportFailure(b)
			}
			return requestErr
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	c.pool.reportSuccess(b)
	c.updateRateLimitFor(b, resp)
	return resp, nil
}

func (c *Client) Get(ctx context.Context, path string, result interface{}) error {
	resp, err := c.doGet(ctx, path)
	if err != nil {
		var rlErr *RateLimitError
		if errors.As(err, &rlErr) {
			return err
		}
		if rl := detectRateLimitFromHTTPError(err); rl != nil {
			return rl
		}
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	return json.Unmarshal(body, result)
}

func (c *Client) GetRaw(ctx context.Context, path string) (*http.Response, error) {
	return c.doGet(ctx, path)
}

func (c *Client) GetPaginated(ctx context.Context, path string, onPage func(json.RawMessage) error) error {
	for path != "" {
		resp, err := c.doGet(ctx, path)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("reading response: %w", readErr)
		}
		if err := onPage(body); err != nil {
			return err
		}
		path = nextPageURL(resp.Header.Get("Link"))
	}
	return nil
}

func (c *Client) updateRateLimitFor(b *backend, resp *http.Response) {
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	updateRateLimitHeaders(&b.REST.RateLimit, resp.Header)
	b.REST.Limiter.SetRPM(restRPM(b))
	c.mu.Lock()
	c.rateLimit = aggregateRateLimit(c.backends, resourceREST)
	c.mu.Unlock()
}

func updateRateLimitHeaders(rl *RateLimit, h http.Header) {
	if v := h.Get("X-RateLimit-Limit"); v != "" {
		rl.Limit, _ = strconv.Atoi(v)
	}
	if v := h.Get("X-RateLimit-Remaining"); v != "" {
		rl.Remaining, _ = strconv.Atoi(v)
	}
	if v := h.Get("X-RateLimit-Used"); v != "" {
		rl.Used, _ = strconv.Atoi(v)
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		ts, _ := strconv.ParseInt(v, 10, 64)
		rl.Reset = time.Unix(ts, 0)
	}
}

func (c *Client) updateRateLimit(resp *http.Response) {
	c.ensurePool()
	c.updateRateLimitFor(c.backends[0], resp)
}

type gqlRateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Used      int       `json:"used"`
	ResetAt   time.Time `json:"resetAt"`
	Cost      int       `json:"cost"`
}

type gqlRateLimitCarrier interface {
	graphqlRateLimit() *gqlRateLimit
}

func (c *Client) doGraphQL(ctx context.Context, query string, variables map[string]interface{}, out interface{}) error {
	c.ensurePool()
	b, err := c.pool.nextBackend(time.Now())
	if err != nil {
		return err
	}
	if b.GraphQL == nil {
		return fmt.Errorf("GraphQL client not available")
	}
	if err := c.waitRequest(ctx, b.GraphQLBudget.Limiter); err != nil {
		return err
	}
	if err := b.GraphQL.DoWithContext(ctx, query, variables, out); err != nil {
		if rl := detectRateLimitFromHTTPError(err); rl != nil {
			c.pool.disableUntil(b, rl.ResetAt, false)
			return rl
		}
		if statusCode(err) == http.StatusUnauthorized {
			c.pool.disableUntil(b, time.Time{}, true)
		} else if !isGatewayOrTransportError(err) {
			c.pool.reportFailure(b)
		}
		return err
	}
	c.pool.reportSuccess(b)
	if carrier, ok := out.(gqlRateLimitCarrier); ok {
		c.updateGraphQLBudget(b, carrier.graphqlRateLimit())
	}
	return nil
}

func (c *Client) updateGraphQLBudget(b *backend, observed *gqlRateLimit) {
	if observed == nil || observed.Limit == 0 {
		return
	}
	cost := max(1, observed.Cost)
	minutes := time.Until(observed.ResetAt).Minutes()
	rpm := minRefillRate * 60
	if minutes > 0 && observed.Remaining > 0 {
		rpm = (float64(observed.Remaining) / float64(cost)) / minutes
	}
	c.global.mu.Lock()
	globalRPM := c.global.rate * 60
	c.global.mu.Unlock()
	if rpm > globalRPM {
		rpm = globalRPM
	}
	c.pool.mu.Lock()
	b.GraphQLBudget.RateLimit = RateLimit{Limit: observed.Limit, Remaining: observed.Remaining, Used: observed.Used, Reset: observed.ResetAt}
	b.GraphQLBudget.Cost = cost
	b.GraphQLBudget.Limiter.SetRPM(rpm)
	c.pool.mu.Unlock()
}

func (c *Client) sleep() func(context.Context, time.Duration) error {
	if c.sleepFn != nil {
		return c.sleepFn
	}
	return ctxSleep
}

func statusCode(err error) int {
	var httpErr *ghAPI.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

func isGatewayOrTransportError(err error) bool {
	code := statusCode(err)
	return code == 0 || code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func nextPageURL(linkHeader string) string {
	for _, part := range strings.Split(linkHeader, ",") {
		if matches := linkNextRe.FindStringSubmatch(strings.TrimSpace(part)); len(matches) == 2 {
			return matches[1]
		}
	}
	return ""
}
