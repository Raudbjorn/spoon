package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// backendForServer builds a *backend whose REST calls land on srv.
func backendForServer(t *testing.T, login string, srv *httptest.Server) *backend {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	rest, err := newRESTClient("x", &rewriteTransport{target: u, base: http.DefaultTransport})
	if err != nil {
		t.Fatalf("NewRESTClient: %v", err)
	}
	gql := newGraphQLClient("x", defaultHost, &rewriteTransport{target: u, base: http.DefaultTransport})
	b := &backend{Rest: rest, GraphQL: gql, Login: login}
	b.REST.Limiter = newLimiterRPM(6000, 100)
	b.GraphQLBudget.Limiter = newLimiterRPM(6000, 100)
	return b
}

func newPooledTestClient(t *testing.T, backends ...*backend) *Client {
	t.Helper()
	c := &Client{
		backends:      backends,
		pool:          &backendPool{backends: backends},
		authenticated: true,
		maxRateWait:   5 * time.Minute, // above the 60s default reset so the retry actually runs
		sleepFn:       func(context.Context, time.Duration) error { return nil },
	}
	c.global = newLimiterRPM(6000, 100)
	return c
}

// A rate-limited identity must not absorb the retry: doWithRetry calls fn twice,
// so if the backend is chosen outside the closure the retry re-hits the identity
// that was just disabled, and a full second token is never tried. That defeats
// the only path multi-token dispatch exists for.
func TestDoGetRotatesToHealthyBackendOnRateLimit(t *testing.T) {
	var exhaustedHits, healthyHits atomic.Int64
	exhausted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exhaustedHits.Add(1)
		w.Header().Set("Retry-After", "5")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer exhausted.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyHits.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "4999")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer healthy.Close()

	c := newPooledTestClient(t,
		backendForServer(t, "spent", exhausted),
		backendForServer(t, "fresh", healthy),
	)

	resp, err := c.doGet(context.Background(), "repos/o/r")
	if err != nil {
		t.Fatalf("doGet failed: %v (exhaustedHits=%d healthyHits=%d)", err, exhaustedHits.Load(), healthyHits.Load())
	}
	defer resp.Body.Close()
	if healthyHits.Load() == 0 {
		t.Fatalf("healthy backend never received a request (exhausted hits=%d)", exhaustedHits.Load())
	}
}

// Permanent is documented as "confirmed auth failure (401); never rehabilitated"
// and nextBackend never recovers such a backend. Marking it on any probe error
// means one transient blip — a proxy hiccup, a 500, a dropped connection —
// permanently burns a valid token for the life of the process.
func TestProbeExplicitBackendsDoesNotPermanentlyBurnOnTransientError(t *testing.T) {
	transient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer transient.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"login":"octocat"}`)
	}))
	defer good.Close()

	flaky := backendForServer(t, "", transient)
	healthy := backendForServer(t, "", good)
	c := newPooledTestClient(t, flaky, healthy)

	var status AuthStatus
	if err := c.probeExplicitBackends(context.Background(), &status); err != nil {
		t.Fatalf("probeExplicitBackends: %v", err)
	}
	if flaky.Permanent {
		t.Fatal("a transient 500 permanently disabled a valid token")
	}
}

// A genuine 401 must still be permanent — the rehabilitation loop would
// otherwise keep retrying credentials GitHub has already rejected.
func TestProbeExplicitBackendsBurnsOnUnauthorized(t *testing.T) {
	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer rejected.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"login":"octocat"}`)
	}))
	defer good.Close()

	bad := backendForServer(t, "", rejected)
	healthy := backendForServer(t, "", good)
	c := newPooledTestClient(t, bad, healthy)

	var status AuthStatus
	if err := c.probeExplicitBackends(context.Background(), &status); err != nil {
		t.Fatalf("probeExplicitBackends: %v", err)
	}
	if !bad.Permanent {
		t.Fatal("a 401 must permanently disable the credential")
	}
}

// doGraphQL had no retry wrapper at all: it selected one identity and returned
// its error, so a 429 aborted pagination mid-way while other tokens sat at full
// budget. FetchForksGraphQL then discarded the remaining pages.
func TestDoGraphQLRotatesToHealthyBackendOnRateLimit(t *testing.T) {
	var exhaustedHits, healthyHits atomic.Int64
	exhausted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exhaustedHits.Add(1)
		w.Header().Set("Retry-After", "5")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer exhausted.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyHits.Add(1)
		_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"octocat"}}}`)
	}))
	defer healthy.Close()

	c := newPooledTestClient(t,
		backendForServer(t, "spent", exhausted),
		backendForServer(t, "fresh", healthy),
	)

	var out struct {
		Viewer struct{ Login string }
	}
	if err := c.doGraphQL(context.Background(), &out, nil, &out); err != nil {
		t.Fatalf("doGraphQL failed: %v (exhausted=%d healthy=%d)", err, exhaustedHits.Load(), healthyHits.Load())
	}
	if healthyHits.Load() == 0 {
		t.Fatalf("healthy backend never received a GraphQL request (exhausted=%d)", exhaustedHits.Load())
	}
}
