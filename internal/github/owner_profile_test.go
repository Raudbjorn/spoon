package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	rest, err := newRESTClient("x", tr)
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
	// FetchUserRepos writes the cache even for a live fetch; keep it out
	// of the real ~/.cache/spoon.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	recent := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	ancient := time.Now().Add(-3 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/alice/repos" {
			t.Errorf("unexpected path: %q, want /users/alice/repos (no leading //)", r.URL.Path)
		}
		switch r.URL.Query().Get("page") {
		case "", "1":
			next := *r.URL
			q := next.Query()
			q.Set("page", "2")
			next.RawQuery = q.Encode()
			w.Header().Set("Link", "<https://api.github.com"+next.String()+">; rel=\"next\"")
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
	if !rec.Complete {
		t.Error("Complete: got false, want true for a two-page account")
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

// ownerReposPath is the only shape the owner-repos request may take.
var ownerReposPath = regexp.MustCompile(`^/users/[^/]+/repos$`)

// ownerPageJSON renders n repos sharing one fork flag and pushed_at.
func ownerPageJSON(n int, fork bool, pushedAt string) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"fork": %t, "pushed_at": %q}`, fork, pushedAt)
	}
	b.WriteString("]")
	return b.String()
}

// newPagedOwnerServer serves pages[i] for page i+1 and advertises
// rel="next" on every page except the last, counting each request.
// Link headers are absolute, as GitHub sends them; a relative one would be
// re-joined onto the API root by the client and gain a second slash.
// It also asserts the request asks for newest-push-first ordering:
// without an explicit sort GitHub returns full_name ascending, which
// would make the retained window alphabetical rather than recent.
func newPagedOwnerServer(t *testing.T, pages []string, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		// Exact match, not HasSuffix: a leading slash in the client path
		// becomes "//users/..." on the wire, which GitHub answers with 404
		// (read by the caller as "no signal"), and a suffix check hides it.
		if !ownerReposPath.MatchString(r.URL.Path) {
			t.Errorf("owner repos request path = %q, want /users/<login>/repos", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("sort") != "pushed" || q.Get("direction") != "desc" {
			t.Errorf("owner repos must be requested newest-push first; got query %q", r.URL.RawQuery)
		}
		page := 1
		if p := q.Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		if page < 1 || page > len(pages) {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if page < len(pages) {
			next := *r.URL
			nq := next.Query()
			nq.Set("page", strconv.Itoa(page+1))
			next.RawQuery = nq.Encode()
			w.Header().Set("Link", "<https://api.github.com"+next.String()+">; rel=\"next\"")
		}
		_, _ = w.Write([]byte(pages[page-1]))
	}))
}

// TestFetchUserRepos_StopsAtPageCapWithoutExtraRequest reproduces the
// report's fixture: 500 stale forks fill the first five pages and two
// original repos sit on page six. The walker must stop after page five
// without downloading page six.
func TestFetchUserRepos_StopsAtPageCapWithoutExtraRequest(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ancient := time.Now().Add(-3 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	pages := make([]string, 0, ownerProfilePaginationCap+1)
	for i := 0; i < ownerProfilePaginationCap; i++ {
		pages = append(pages, ownerPageJSON(100, true, ancient))
	}
	pages = append(pages, ownerPageJSON(2, false, recent))

	var requests atomic.Int32
	srv := newPagedOwnerServer(t, pages, &requests)
	defer srv.Close()

	c := newTestClientREST(t, srv)
	rec, _, err := c.FetchUserRepos(context.Background(), "farmerish", -1)
	if err != nil {
		t.Fatalf("FetchUserRepos: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if got := requests.Load(); got != ownerProfilePaginationCap {
		t.Errorf("requests: got %d, want %d (page %d must not be fetched)", got, ownerProfilePaginationCap, ownerProfilePaginationCap+1)
	}
	if rec.TotalPublicRepos != 500 || rec.ForkCount != 500 || rec.NonForkRepoCount != 0 {
		t.Errorf("sample: got total=%d forks=%d nonfork=%d, want 500/500/0", rec.TotalPublicRepos, rec.ForkCount, rec.NonForkRepoCount)
	}
	// The two originals were never seen, so the record must say the
	// sample is partial; otherwise "0 non-fork repos" reads as fact.
	if rec.Complete {
		t.Error("Complete: got true, want false for an account with a sixth page")
	}
	if rec.SampleOrder != ownerProfileSampleOrder {
		t.Errorf("SampleOrder: got %q, want %q", rec.SampleOrder, ownerProfileSampleOrder)
	}
}

// TestOwnerProfileRecord_ProfileCarriesEveryField pins the conversion
// the pipeline uses for both cached and live records. A dropped field
// here would silently turn a partial sample back into a "complete" one
// (Complete defaults to false, but the counts and order would be lost).
func TestOwnerProfileRecord_ProfileCarriesEveryField(t *testing.T) {
	fetched := time.Now().UTC().Truncate(time.Second)
	rec := &ownerProfileRecord{
		Login:            "alice",
		TotalPublicRepos: 9,
		ForkCount:        6,
		SignalForkCount:  2,
		NonForkRepoCount: 3,
		Complete:         true,
		SampleOrder:      ownerProfileSampleOrder,
		FetchedAt:        fetched,
	}
	got := rec.Profile()
	if got == nil {
		t.Fatal("Profile() returned nil")
	}
	if got.Login != "alice" || got.TotalPublicRepos != 9 || got.ForkCount != 6 ||
		got.SignalForkCount != 2 || got.NonForkRepoCount != 3 ||
		!got.Complete || got.SampleOrder != ownerProfileSampleOrder || !got.FetchedAt.Equal(fetched) {
		t.Errorf("Profile() dropped or changed a field: %+v", got)
	}
	if (*ownerProfileRecord)(nil).Profile() != nil {
		t.Error("nil record must convert to a nil profile")
	}
}

// TestOwnerProfileCache_PreCompletenessFileIsMiss covers invalidation:
// a cache file written before records carried a schema version cannot
// say whether its sample was complete, so it must be refetched rather
// than trusted.
func TestOwnerProfileCache_PreCompletenessFileIsMiss(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path, err := ownerProfileCachePath("alice")
	if err != nil {
		t.Fatalf("ownerProfileCachePath: %v", err)
	}
	legacy := `{"login":"alice","total_public_repos":500,"fork_count":500,` +
		`"signal_fork_count":0,"non_fork_repo_count":0,"fetched_at":"` +
		time.Now().UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}
	if got := loadOwnerProfile("alice", ownerProfileTTL); got != nil {
		t.Errorf("pre-completeness record must be a cache miss, got %+v", got)
	}
}

// TestFetchUserRepos_ExactlyCapPagesIssuesCapRequests pins the other
// side of the boundary: an account of exactly 500 repos has no next
// link on page five and needs exactly five requests.
func TestFetchUserRepos_ExactlyCapPagesIssuesCapRequests(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ancient := time.Now().Add(-3 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	pages := make([]string, 0, ownerProfilePaginationCap)
	for i := 0; i < ownerProfilePaginationCap; i++ {
		pages = append(pages, ownerPageJSON(100, true, ancient))
	}

	var requests atomic.Int32
	srv := newPagedOwnerServer(t, pages, &requests)
	defer srv.Close()

	c := newTestClientREST(t, srv)
	rec, _, err := c.FetchUserRepos(context.Background(), "exactly500", -1)
	if err != nil {
		t.Fatalf("FetchUserRepos: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if got := requests.Load(); got != ownerProfilePaginationCap {
		t.Errorf("requests: got %d, want %d", got, ownerProfilePaginationCap)
	}
	if rec.TotalPublicRepos != 500 {
		t.Errorf("TotalPublicRepos: got %d, want 500", rec.TotalPublicRepos)
	}
	if !rec.Complete {
		t.Error("Complete: got false, want true when page five has no next link")
	}
}

// TestOwnerProfileCache_RoundTripKeepsCompleteness covers both flag
// values: a partial sample must not come back from disk looking
// complete, and a complete one must not be downgraded.
func TestOwnerProfileCache_RoundTripKeepsCompleteness(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		rec := &ownerProfileRecord{
			Login:       "alice",
			Complete:    complete,
			SampleOrder: ownerProfileSampleOrder,
			FetchedAt:   time.Now().UTC(),
		}
		if err := saveOwnerProfile(rec); err != nil {
			t.Fatalf("complete=%v: saveOwnerProfile: %v", complete, err)
		}
		got := LoadCachedOwnerProfile("alice", ownerProfileTTL)
		if got == nil {
			t.Fatalf("complete=%v: expected a cache hit", complete)
		}
		if got.Complete != complete || got.SampleOrder != ownerProfileSampleOrder {
			t.Errorf("complete=%v: got Complete=%v SampleOrder=%q", complete, got.Complete, got.SampleOrder)
		}
		if got.SchemaVersion != ownerProfileSchemaVersion {
			t.Errorf("complete=%v: SchemaVersion: got %d, want %d", complete, got.SchemaVersion, ownerProfileSchemaVersion)
		}
	}
}
