// Package github is the GitHub forge backend.
//
// Acquisition contract (see docs/research/2026-08-19-spoon-endpoint-networking.md):
// REST clients pin X-GitHub-Api-Version to 2022-11-28; GraphQL does not send
// that header. Default fork inventory is direct children. Whole-network
// discovery is opt-in and bounded. Fork-list cache keys include API version,
// auth mode, and a non-reversible AuthScopeID. Undocumented /network/meta,
// /network/chunk, /network_meta, /network_data_chunk, and /networks/.../events
// routes are rejected.
package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gogithub "github.com/google/go-github/v90/github"
	"github.com/shurcooL/githubv4"
	"github.com/svnbjrn/spoon/internal/github/treecommitinfo"
	"github.com/svnbjrn/spoon/internal/github/webdiff"
)

// requestTimeout bounds a single GitHub HTTP request end to end.
//
// An http.Client with a zero Timeout has no deadline at all. A backend that completes the TCP
// handshake and then never sends response headers — the characteristic failure
// of a free datacenter proxy — otherwise blocks forever: the gateway-retry loop
// never runs because no error is returned, and the proxy is never scored
// unhealthy because scoring happens only after RoundTrip returns. The run hangs
// with no output until killed.
//
// Generous enough for a large compare or a slow GraphQL page, short enough that
// a stalled backend is retried against a different one within a minute.
const requestTimeout = 60 * time.Second

// responseHeaderTimeout bounds the wait for response headers specifically. It
// is the tighter of the two guards and the one that actually catches a silent
// stall: http.DefaultTransport leaves it unset, so a cloned proxy transport
// inherits no header deadline.
const responseHeaderTimeout = 30 * time.Second

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

// defaultHost is the only GitHub host this provider talks to. It is also what
// AuthStatus.Host reports, which callers interpolate into user-facing URLs.
const defaultHost = "github.com"

// computeAuthScopeID returns the first 16 lowercase hex chars of SHA-256 over
// provider, normalized host, and the sorted trimmed credential token set joined
// by NUL bytes. Anonymous clients receive the same treatment for an empty token
// set — the scope ID is deterministic for (provider, host, no-credentials).
func computeAuthScopeID(provider, host string, tokens []string) string {
	// Sort tokens so the same credential set always produces the same ID regardless
	// of config order.
	sorted := make([]string, len(tokens))
	copy(sorted, tokens)
	sort.Strings(sorted)
	h := sha256.New()
	h.Write([]byte(provider))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(host)))
	h.Write([]byte{0})
	for _, tok := range sorted {
		h.Write([]byte{0})
		h.Write([]byte(strings.TrimSpace(tok)))
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

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
	Rest          *gogithub.Client
	GraphQL       *githubv4.Client
	Login         string
	REST          budgetState
	GraphQLBudget budgetState
	Successes     uint64
	Failures      uint64
	Disabled      bool      // statistical circuit-breaker (recoverable)
	Permanent     bool      // confirmed auth failure (401); never rehabilitated
	DisabledUntil time.Time // timed rate-limit cooldown
}

// Client dispatches GitHub requests across distinct authenticated identities.
// The legacy fields remain private aliases for package tests and are folded into
// one backend lazily; production traffic always passes through a backend pool.
type Client struct {
	backends []*backend
	pool     *backendPool
	proxies  *proxyPool
	rotating *rotatingProxyTransport
	global   *limiter
	webDiff  *webdiff.Client

	// treeCommitInfo is the opt-in client for GitHub's undocumented
	// tree-commit-info endpoint (internal/github/treecommitinfo), used to
	// resolve a fork's own last-touch commit for a path without a REST
	// compare. Nil until EnableTreeCommitInfo is called.
	treeCommitInfo *treecommitinfo.Client

	// localBranchScan gates ScanBranchesLocal (localbranchscan.go), the
	// git-ls-remote/fetch/merge-base alternative to the REST/GraphQL-only
	// ScanBranches. Opt-in: shells out to a git subprocess, a real failure
	// mode ScanBranches doesn't have.
	localBranchScan bool

	rest                *gogithub.Client
	gql                 *githubv4.Client
	authenticated       bool
	authScopeID         string // computed once; non-reversible scope fingerprint
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
	if math.IsNaN(rpm) || math.IsInf(rpm, 0) || rpm <= 0 || rpm > 900 {
		return nil, fmt.Errorf("requests per minute must be a finite value in (0, 900]")
	}
	proxies, _ := bootstrapProxyPool(context.Background(), opts.Proxy)
	rotating := newRotatingProxyTransport(proxies)
	c := &Client{proxies: proxies, rotating: rotating, global: newLimiterRPM(rpm, globalBurst), authenticated: len(opts.Tokens) > 0}
	// Compute the AuthScopeID once per Client lifetime. It is deterministic
	// for (provider, host, sorted trimmed credential token set); anonymous
	// clients get a stable scope ID for an empty token set.
	host, envToken := githubEnvironmentAuth()
	tokens := append([]string(nil), opts.Tokens...)
	if len(tokens) == 0 && envToken != "" {
		tokens = []string{envToken}
	}
	c.authScopeID = computeAuthScopeID("github", host, tokens)

	if len(opts.Tokens) == 0 {
		// Spoon OAuth tokens arrive through opts.Tokens. With no saved token,
		// use the environment or an anonymous REST-only backend.
		if envToken != "" {
			rest, err := newRESTClient(envToken, rotating)
			if err == nil {
				b := &backend{Rest: rest, REST: newBudget(), GraphQLBudget: newBudget()}
				b.GraphQL = newGraphQLClient(envToken, host, rotating)
				c.authenticated = true
				c.installBackends([]*backend{b})
				c.initRateControls()
				return c, nil
			}
			slog.Debug("github: building authenticated REST client failed; falling back to anonymous", "error", err)
		}
		rest, err := newRESTClient("", rotating)
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
		rest, err := newRESTClient(token, rotating)
		if err != nil {
			return nil, fmt.Errorf("creating GitHub REST backend: %w", err)
		}
		gql := newGraphQLClient(token, defaultHost, rotating)
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

func (c *Client) Close() {
	if c.rotating != nil {
		c.rotating.CloseIdleConnections()
	}
}

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

// EnableLocalBranchScan turns on the git-ls-remote/fetch/merge-base branch
// scan (see ScanBranchesLocal in localbranchscan.go) in place of the
// REST/GraphQL-only ScanBranches when the default branch shows no work.
func (c *Client) EnableLocalBranchScan() {
	c.localBranchScan = true
}

// EnableTreeCommitInfo turns on the explicitly undocumented, best-effort
// lookup against GitHub's tree-commit-info endpoint (see
// internal/github/treecommitinfo), used to resolve a fork's own last-touch
// commit for a path without a REST compare. Unlike EnableWebDiff there is no
// cookie: the endpoint is called anonymously.
//
// No shared gate is wired in: unlike webDiff, whose gate gives it a share of
// c.global (the REST pacing pool), tree-commit-info traffic must not draw
// from that budget at all -- it hits github.com outside the REST/GraphQL
// surface entirely, with its own unknown limit, which is exactly why the
// package carries its own conservative bucket and run-scoped breaker.
func (c *Client) EnableTreeCommitInfo() {
	c.treeCommitInfo = treecommitinfo.New(nil)
}

// TreeCommitInfo returns the tree-commit-info client, or nil when
// EnableTreeCommitInfo was never called.
func (c *Client) TreeCommitInfo() *treecommitinfo.Client {
	return c.treeCommitInfo
}

func (c *Client) IsAuthenticated() bool { return c.authenticated }

// AuthScopeID returns the deterministic scope fingerprint computed once per
// Client lifetime. It is a 16-character hex string derived from SHA-256 over
// (provider, normalized host, sorted trimmed credential token set). It is safe
// to log and to store; it is not a secret and cannot be reversed to recover
// any token.
func (c *Client) AuthScopeID() string { return c.authScopeID }

// AuthMode returns "authenticated" when a real token is present, "anonymous" otherwise.
func (c *Client) AuthMode() string {
	if c.authenticated {
		return "authenticated"
	}
	return "anonymous"
}

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
		if b.Disabled || b.Permanent {
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
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	for _, b := range c.backends {
		if !b.Disabled && !b.Permanent && b.GraphQL != nil {
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
	return c.doGetAccept(ctx, path, "")
}

// doGetDiff is doGet's counterpart for FetchCompareDiff: identical backend
// selection, retry, limiter wait, and rate-limit accounting, but with Accept:
// diffAcceptHeader so GitHub returns a unified diff instead of JSON. It still
// draws on the REST budget/limiter — GitHub bills this as one ordinary core API
// call; only the Accept header differs, not the resource.
func (c *Client) doGetDiff(ctx context.Context, path string) (*http.Response, error) {
	return c.doGetAccept(ctx, path, diffAcceptHeader)
}

// restGet issues one GET through a go-github client and returns the raw
// response, so callers keep the path-based, raw-JSON contract the rest of the
// package is written against. go-github reports 202 Accepted as an error with
// the payload attached; here it is an ordinary success, as the stats endpoints
// (FetchContributors) expect.
func restGet(ctx context.Context, rc *gogithub.Client, path, accept string) (*http.Response, error) {
	req, err := rc.NewRequest(ctx, http.MethodGet, path, nil, restRequestOptions(accept)...)
	if err != nil {
		return nil, err
	}
	resp, err := rc.BareDo(req)
	if err != nil {
		var accepted *gogithub.AcceptedError
		if errors.As(err, &accepted) && resp != nil && resp.Response != nil {
			resp.Response.Body = io.NopCloser(bytes.NewReader(accepted.Raw))
			return resp.Response, nil
		}
		return nil, err
	}
	return resp.Response, nil
}

// doGetAccept is the shared implementation of doGet and doGetDiff. The backend
// is selected inside doWithRetry's closure so a retry after a rate-limit sleep
// can land on a different backend, not silently reuse the one just disabled.
func (c *Client) doGetAccept(ctx context.Context, path, accept string) (*http.Response, error) {
	c.ensurePool()
	var (
		resp *http.Response
		b    *backend
	)
	err := c.doWithRetry(ctx, func() error {
		var berr error
		if b, berr = c.pool.nextBackend(time.Now()); berr != nil {
			return berr
		}
		rc := b.Rest
		if rc == nil {
			return fmt.Errorf("github: no REST client configured for this backend/request")
		}
		var requestErr error
		for attempt := range 3 {
			if err := c.waitRequest(ctx, b.REST.Limiter); err != nil {
				return err
			}
			resp, requestErr = restGet(ctx, rc, path, accept)
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
	_, err := c.getPaginated(ctx, path, 0, onPage)
	return err
}

// getPaginated walks rel="next" links like GetPaginated but, when
// maxPages > 0, stops after that many pages without requesting the
// next one. truncated reports whether a next link was still on offer
// at the stop, so a caller can tell "exactly maxPages of results" from
// "more results existed". maxPages <= 0 means unbounded.
func (c *Client) getPaginated(ctx context.Context, path string, maxPages int, onPage func(json.RawMessage) error) (truncated bool, err error) {
	for pages := 1; path != ""; pages++ {
		resp, err := c.doGet(ctx, path)
		if err != nil {
			return false, err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return false, fmt.Errorf("reading response: %w", readErr)
		}
		if err := onPage(body); err != nil {
			return false, err
		}
		path = nextPageURL(resp.Header.Get("Link"))
		if maxPages > 0 && pages >= maxPages {
			return path != "", nil
		}
	}
	return false, nil
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

func (c *Client) doGraphQL(ctx context.Context, query interface{}, variables map[string]interface{}, out interface{}) error {
	c.ensurePool()
	var b *backend
	// Wrapped in doWithRetry for the same reason as doGet, and selecting inside
	// the closure so the retry lands on a different identity. Without this a
	// 429 aborted GraphQL pagination outright while other tokens sat at full
	// budget, discarding the remaining pages.
	err := c.doWithRetry(ctx, func() error {
		var berr error
		if b, berr = c.pool.nextBackend(time.Now()); berr != nil {
			return berr
		}
		if b.GraphQL == nil {
			return fmt.Errorf("GraphQL client not available")
		}
		if err := c.waitRequest(ctx, b.GraphQLBudget.Limiter); err != nil {
			return err
		}
		if err := queryGraphQL(ctx, b.GraphQL, query, variables, out); err != nil {
			if rl := detectRateLimitFromHTTPError(err); rl != nil {
				c.pool.disableUntil(b, rl.ResetAt, false)
				return rl
			}
			if statusCode(err) == http.StatusUnauthorized {
				c.pool.disableUntil(b, time.Time{}, true)
			} else if !isGatewayOrTransportError(err) && !isGraphQLResponseError(err) {
				// A GraphQL-level error rides on an HTTP 200: the backend
				// answered correctly and the query was at fault. Counting it
				// against the token would, after five partial-result queries,
				// trip the 70% failure ratio and disable a healthy identity.
				c.pool.reportFailure(b)
			}
			return err
		}
		return nil
	})
	if err != nil {
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
	status, _, _ := httpFailure(err)
	return status
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
