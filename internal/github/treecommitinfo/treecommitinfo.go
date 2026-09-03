// Package treecommitinfo is a best-effort client for GitHub's undocumented
// tree-commit-info endpoint — the call the github.com file browser makes to
// fill in each row's "last commit" column
// (https://github.com/{owner}/{repo}/tree-commit-info/{ref}/{dir}). It is not
// part of any documented API, carries no stability guarantee, and must never
// be load-bearing: every non-OK Outcome means "no information available",
// and callers are expected to fall back to their normal (REST/GraphQL) path
// rather than treat a failure as evidence of anything.
//
// The endpoint is served from github.com, not api.github.com, so it shares
// neither the REST nor the GraphQL rate-limit budget and has no documented
// limit of its own. This package therefore paces itself conservatively (its
// own token bucket, separate from package github's REST/GraphQL pools) and
// trips a run-scoped breaker on the first sign of trouble (403, 429, or a
// 5xx) rather than probing to find the real ceiling.
package treecommitinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// maxResponseBytes bounds a single response body. The endpoint returns a
	// small JSON object (one entry per file/dir in one directory listing), so
	// anything near this size means the response is not what was expected.
	maxResponseBytes = 1 << 20 // 1 MiB

	// requestTimeout bounds a single request end to end.
	requestTimeout = 15 * time.Second

	// limiterRate and limiterBurst pace requests at 1/s with a burst of 3.
	// This is deliberately conservative: the endpoint's real rate limit is
	// unknown, and unlike webdiff (which authenticates with a session
	// cookie) every call here is anonymous, so there is no session to
	// protect from a lockout beyond the run itself.
	limiterRate  = 1.0
	limiterBurst = 3.0
)

// Outcome classifies the result of a LastTouch call. Only OK carries
// information; every other value means the caller learned nothing and must
// proceed as if it had never asked.
type Outcome int

const (
	// OK means entries was parsed successfully from a 2xx response.
	OK Outcome = iota
	// NotFound means the server returned 404 — the ref or the directory
	// does not exist (or the repo is private/gone). Distinct from Disabled:
	// a 404 says nothing about whether the *next* lookup will succeed.
	NotFound
	// Disabled means the run-scoped breaker has tripped (or was already
	// tripped when this call was made): a prior response was 403, 429, or a
	// 5xx, and this client will not attempt another live request for the
	// rest of the run.
	Disabled
	// Error means the request could not be completed or the response could
	// not be parsed for any other reason (network failure, context
	// cancellation, oversized or malformed body, redirect refused, or an
	// unexpected non-2xx status). Not run-scoped: the next call may still
	// succeed.
	Error
)

// String renders o for logging.
func (o Outcome) String() string {
	switch o {
	case OK:
		return "ok"
	case NotFound:
		return "not_found"
	case Disabled:
		return "disabled"
	case Error:
		return "error"
	default:
		return "unknown"
	}
}

// Client is a best-effort, anonymous caller of the tree-commit-info
// endpoint. It sends no cookies and no Authorization header — the endpoint
// is reachable unauthenticated for public repos, and there is no session
// worth protecting the way webdiff.Client protects its cookie.
type Client struct {
	http *http.Client
	gate func(context.Context) error
	lim  *limiter

	mu       sync.Mutex
	disabled bool
	reason   string
}

// New returns a Client. gate, if non-nil, is called (after this client's own
// pacing) before every request; callers can wire it to a shared budget the
// way webdiff.New does, though tree-commit-info traffic is not part of any
// shared budget by default.
func New(gate func(context.Context) error) *Client {
	return &Client{
		gate: gate,
		lim:  newLimiter(limiterRate, limiterBurst),
		http: &http.Client{
			Timeout: requestTimeout,
			// Never follow redirects. A 3xx here (e.g. to a sign-in page)
			// means the request did not land where expected; surfacing it as
			// a non-2xx status lets the caller record a skip rather than
			// silently fetching whatever the redirect target turns out to
			// be.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// treeCommitInfoResponse mirrors the endpoint's JSON shape:
// {"entries":{"<name>":{"oid":"<sha>","date":"...", ...}}}. Only oid is
// used; other per-entry fields (author, message, ...) are ignored.
type treeCommitInfoResponse struct {
	Entries map[string]struct {
		OID string `json:"oid"`
	} `json:"entries"`
}

// LastTouch fetches the last-commit-per-entry listing for dir at ref — the
// same data GitHub's file browser shows in its "last commit" column — and
// returns a map from entry name to that commit's OID. dir may be empty for
// the repository root. Any Outcome other than OK means the returned map is
// nil and carries no information; callers must not treat it as "no
// entries".
func (c *Client) LastTouch(ctx context.Context, owner, repo, ref, dir string) (map[string]string, Outcome) {
	c.mu.Lock()
	disabled := c.disabled
	c.mu.Unlock()
	if disabled {
		return nil, Disabled
	}

	if err := c.lim.Wait(ctx); err != nil {
		return nil, Error
	}
	if c.gate != nil {
		if err := c.gate(ctx); err != nil {
			return nil, Error
		}
	}

	u := buildURL(owner, repo, ref, dir)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, Error
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, Error
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, NotFound
	case resp.StatusCode == http.StatusForbidden,
		resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode/100 == 5:
		c.disable(fmt.Sprintf("HTTP %d from tree-commit-info", resp.StatusCode))
		return nil, Disabled
	case resp.StatusCode/100 != 2:
		// Anything else non-2xx (including a refused redirect, which lands
		// here as a bare 3xx) is a plain miss, not a breaker trip: nothing
		// about it predicts the next ref/dir will fail the same way.
		return nil, Error
	}

	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, Error
	}
	if len(body) > maxResponseBytes {
		return nil, Error
	}

	var parsed treeCommitInfoResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, Error
	}

	out := make(map[string]string, len(parsed.Entries))
	for name, e := range parsed.Entries {
		out[name] = e.OID
	}
	return out, OK
}

// disable trips the run-scoped breaker. Idempotent: only the first reason is
// kept.
func (c *Client) disable(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.disabled {
		c.disabled = true
		c.reason = reason
		// Debug, not Warn: this endpoint is undocumented and never
		// load-bearing, so tripping the breaker is an expected degraded
		// path, not something an operator needs surfaced by default.
		slog.Debug("tree-commit-info: breaker tripped, disabled for the rest of the run", "reason", reason)
	}
}

// DisabledReason returns why the breaker tripped, or "" if it never did.
// Exposed for diagnostics/logging; not required for correctness.
func (c *Client) DisabledReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// buildURL constructs the tree-commit-info URL for owner/repo/ref/dir. ref
// and every dir segment are escaped individually with url.PathEscape so a
// branch name containing "/" (e.g. "feat/x") keeps its path structure while
// each component is still safely encoded. dir == "" addresses the
// repository root and produces no trailing path segment. An empty ref
// segment (e.g. ref == "" or a stray "//") is skipped rather than emitted as
// a bare "/", since a caller passing an unresolved ref should get a 404 from
// GitHub, not a URL two directories short of the one it asked for.
func buildURL(owner, repo, ref, dir string) string {
	var b strings.Builder
	b.WriteString("https://github.com/")
	b.WriteString(url.PathEscape(owner))
	b.WriteByte('/')
	b.WriteString(url.PathEscape(repo))
	b.WriteString("/tree-commit-info")
	for _, seg := range strings.Split(ref, "/") {
		if seg == "" {
			continue
		}
		b.WriteByte('/')
		b.WriteString(url.PathEscape(seg))
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == "" {
			continue
		}
		b.WriteByte('/')
		b.WriteString(url.PathEscape(seg))
	}
	return b.String()
}

// limiter is a minimal mutex-guarded token bucket, safe for concurrent use.
// This duplicates internal/github's limiter rather than importing it: package
// github will import treecommitinfo (to hold a *Client on the shared GitHub
// Client), so the reverse import would be a cycle. Kept intentionally small.
type limiter struct {
	mu     sync.Mutex
	tokens float64
	rate   float64 // tokens per second
	burst  float64 // max tokens
	last   time.Time

	now   func() time.Time                           // injectable; defaults to time.Now
	sleep func(context.Context, time.Duration) error // injectable; defaults to ctxSleep
}

func newLimiter(rate, burst float64) *limiter {
	if burst < 1 {
		burst = 1
	}
	l := &limiter{rate: rate, burst: burst, tokens: burst, now: time.Now, sleep: ctxSleep}
	l.last = l.now()
	return l
}

// ctxSleep sleeps for d or until ctx is cancelled, whichever comes first.
func ctxSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until one token is available or ctx is cancelled.
func (l *limiter) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		now := l.now()
		if elapsed := now.Sub(l.last).Seconds(); elapsed > 0 {
			l.tokens = min(l.burst, l.tokens+elapsed*l.rate)
			l.last = now
		}
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		deficit := 1 - l.tokens
		rate := l.rate
		l.mu.Unlock()

		if rate <= 0 {
			rate = 1e-6
		}
		wait := time.Duration(deficit / rate * float64(time.Second))
		if wait <= 0 {
			wait = time.Nanosecond
		}
		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
	}
}
