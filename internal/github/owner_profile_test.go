package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

// newTestClientREST builds a *Client whose REST endpoint targets srv.
// Used by the owner-profile fetch tests, which only need the REST
// path (/users/{login}/repos).
func newTestClientREST(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	tr := &rewriteTransport{target: u, base: http.DefaultTransport}
	rest, err := ghAPI.NewRESTClient(ghAPI.ClientOptions{
		AuthToken: "x",
		Host:      "github.com",
		Transport: tr,
	})
	if err != nil {
		t.Fatalf("NewRESTClient: %v", err)
	}
	return &Client{rest: rest, authenticated: true}
}

// TestFetchUserRepos_BuildsProfileFromPages covers the happy path:
// two pages of user-repo JSON walk through the fetcher, and the
// resulting record has the expected totals. Cache TTL of -1 forces
// a live fetch.
func TestFetchUserRepos_BuildsProfileFromPages(t *testing.T) {
	recent := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	ancient := time.Now().Add(-3 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/users/alice/repos") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		switch r.URL.Query().Get("page") {
		case "", "1":
			next := *r.URL
			q := next.Query()
			q.Set("page", "2")
			next.RawQuery = q.Encode()
			w.Header().Set("Link", "<"+next.String()+">; rel=\"next\"")
			_, _ = w.Write([]byte(`[
				{"fork": true,  "pushed_at": "` + recent + `"},
				{"fork": true,  "pushed_at": "` + ancient + `"},
				{"fork": false, "pushed_at": "` + recent + `"}
			]`))
		case "2":
			_, _ = w.Write([]byte(`[{"fork": true, "pushed_at": "` + recent + `"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c := newTestClientREST(t, srv)
	rec, cached, err := c.FetchUserRepos(context.Background(), "alice", -1)
	if err != nil {
		t.Fatalf("FetchUserRepos: %v", err)
	}
	if cached {
		t.Error("expected cached=false for a live fetch")
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if rec.Login != "alice" {
		t.Errorf("Login: got %q, want %q", rec.Login, "alice")
	}
	if rec.TotalPublicRepos != 4 {
		t.Errorf("TotalPublicRepos: got %d, want 4", rec.TotalPublicRepos)
	}
	if rec.ForkCount != 3 {
		t.Errorf("ForkCount: got %d, want 3", rec.ForkCount)
	}
	if rec.SignalForkCount != 2 {
		// Only the two recent forks are within the 1-year window.
		t.Errorf("SignalForkCount: got %d, want 2", rec.SignalForkCount)
	}
	if rec.NonForkRepoCount != 1 {
		t.Errorf("NonForkRepoCount: got %d, want 1", rec.NonForkRepoCount)
	}
	if rec.FetchedAt.IsZero() {
		t.Error("FetchedAt should be set")
	}
}

// TestFetchUserRepos_RateLimitReturnsNil covers the rate-limit path:
// 403 with X-RateLimit-Remaining: 0 yields a *RateLimitError, a nil
// record, and cached=false. The caller (forksops) treats this as
// "no signal".
func TestFetchUserRepos_RateLimitReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	c := newTestClientREST(t, srv)
	rec, cached, err := c.FetchUserRepos(context.Background(), "alice", -1)
	if rec != nil {
		t.Errorf("expected nil record on rate limit, got %+v", rec)
	}
	if cached {
		t.Error("expected cached=false on rate limit")
	}
	if err == nil {
		t.Fatal("expected error on rate limit, got nil")
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Errorf("expected *RateLimitError, got %T", err)
	}
}

// TestFetchUserRepos_NotFoundReturnsNil covers the 404 path: a
// private or renamed user yields (nil, false, nil) so the pipeline
// treats it as "no signal" without logging a warning.
func TestFetchUserRepos_NotFoundReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := newTestClientREST(t, srv)
	rec, cached, err := c.FetchUserRepos(context.Background(), "ghost", -1)
	if rec != nil {
		t.Errorf("expected nil record on 404, got %+v", rec)
	}
	if cached {
		t.Error("expected cached=false on 404")
	}
	if err != nil {
		t.Errorf("expected nil error on 404, got %v", err)
	}
}

// TestFetchUserRepos_EmptyLoginReturnsNil covers the defensive
// path: an empty login yields (nil, false, nil) without an HTTP call.
func TestFetchUserRepos_EmptyLoginReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("should not hit server for empty login")
	}))
	defer srv.Close()

	c := newTestClientREST(t, srv)
	rec, cached, err := c.FetchUserRepos(context.Background(), "", -1)
	if rec != nil || cached || err != nil {
		t.Errorf("empty login: got (%+v, %v, %v), want (nil, false, nil)", rec, cached, err)
	}
}

// TestFetchUserRepos_CacheHit covers the on-disk cache path: a
// second call within the TTL returns the cached record and reports
// cached=true.
func TestFetchUserRepos_CacheHit(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should be hit at most once (the first call). The second
		// call should be served from disk.
		_, _ = w.Write([]byte(`[{"fork": true, "pushed_at": "` +
			time.Now().UTC().Format(time.RFC3339) + `"}]`))
	}))
	defer srv.Close()

	c := newTestClientREST(t, srv)
	// First call: live fetch, cache write.
	rec1, cached1, err := c.FetchUserRepos(context.Background(), "alice", time.Hour)
	if err != nil {
		t.Fatalf("first FetchUserRepos: %v", err)
	}
	if cached1 {
		t.Error("first call: expected cached=false")
	}
	if rec1 == nil {
		t.Fatal("first call: expected non-nil record")
	}
	// Second call: cache hit, no HTTP traffic.
	rec2, cached2, err := c.FetchUserRepos(context.Background(), "alice", time.Hour)
	if err != nil {
		t.Fatalf("second FetchUserRepos: %v", err)
	}
	if !cached2 {
		t.Error("second call: expected cached=true")
	}
	if rec2 == nil {
		t.Fatal("second call: expected non-nil record")
	}
	if rec2.Login != rec1.Login || rec2.ForkCount != rec1.ForkCount {
		t.Errorf("cache mismatch: %+v vs %+v", rec1, rec2)
	}
}

// TestOwnerProfileCache_RoundTrip covers the disk cache: a written
// record is read back identically under the default TTL.
func TestOwnerProfileCache_RoundTrip(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rec := &ownerProfileRecord{
		Login:            "alice",
		TotalPublicRepos: 7,
		ForkCount:        4,
		SignalForkCount:  2,
		NonForkRepoCount: 3,
		FetchedAt:        time.Now().UTC(),
	}
	if err := saveOwnerProfile(rec); err != nil {
		t.Fatalf("saveOwnerProfile: %v", err)
	}
	got := loadOwnerProfile("alice", ownerProfileTTL)
	if got == nil {
		t.Fatal("expected non-nil record from cache")
	}
	if got.Login != rec.Login || got.TotalPublicRepos != rec.TotalPublicRepos ||
		got.ForkCount != rec.ForkCount || got.SignalForkCount != rec.SignalForkCount ||
		got.NonForkRepoCount != rec.NonForkRepoCount {
		t.Errorf("cache mismatch: got %+v, want %+v", got, rec)
	}
}

// TestOwnerProfileCache_Expired covers the TTL path: a record fetched
// more than 25h ago is treated as stale and loadOwnerProfile returns
// nil under the default 24h TTL.
func TestOwnerProfileCache_Expired(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rec := &ownerProfileRecord{
		Login:     "alice",
		FetchedAt: time.Now().Add(-25 * time.Hour).UTC(),
	}
	if err := saveOwnerProfile(rec); err != nil {
		t.Fatalf("saveOwnerProfile: %v", err)
	}
	if got := loadOwnerProfile("alice", ownerProfileTTL); got != nil {
		t.Errorf("expired record should be nil, got %+v", got)
	}
}

// TestOwnerProfileCache_TTLZeroDisablesCache covers the override path:
// ttl=0 forces a live fetch (or a no-op) without consulting the cache.
func TestOwnerProfileCache_TTLZeroDisablesCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rec := &ownerProfileRecord{
		Login:     "alice",
		FetchedAt: time.Now().UTC(),
	}
	if err := saveOwnerProfile(rec); err != nil {
		t.Fatalf("saveOwnerProfile: %v", err)
	}
	if got := loadOwnerProfile("alice", 0); got != nil {
		t.Errorf("ttl=0 should skip cache, got %+v", got)
	}
}

// TestOwnerProfileCache_CaseInsensitive covers the login lowercase
// invariant: saving as "Alice" and loading as "alice" hits the same
// file.
func TestOwnerProfileCache_CaseInsensitive(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rec := &ownerProfileRecord{
		Login:     "Alice",
		FetchedAt: time.Now().UTC(),
	}
	if err := saveOwnerProfile(rec); err != nil {
		t.Fatalf("saveOwnerProfile: %v", err)
	}
	if got := loadOwnerProfile("alice", ownerProfileTTL); got == nil {
		t.Error("case-insensitive lookup failed")
	}
}
