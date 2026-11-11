package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	gogithub "github.com/google/go-github/v90/github"
)

// searchPathQuery parses the query string of a built search path so tests
// assert on decoded parameters rather than on escaping details.
func searchPathQuery(t *testing.T, path string) url.Values {
	t.Helper()
	_, raw, ok := strings.Cut(path, "?")
	if !ok {
		t.Fatalf("path %q has no query string", path)
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return vals
}

func TestRepoSearchPath_Defaults(t *testing.T) {
	path, err := repoSearchPath("terminal language:Go", RepoSearchOptions{})
	if err != nil {
		t.Fatalf("repoSearchPath: %v", err)
	}
	if !strings.HasPrefix(path, "search/repositories?") {
		t.Fatalf("path %q is not a repository-search path", path)
	}
	vals := searchPathQuery(t, path)
	if got := vals.Get("q"); got != "terminal language:Go" {
		t.Errorf("q = %q, want the query verbatim", got)
	}
	if got := vals.Get("per_page"); got != "30" {
		t.Errorf("per_page = %q, want default 30", got)
	}
	if got := vals.Get("page"); got != "1" {
		t.Errorf("page = %q, want default 1", got)
	}
	// Best match is GitHub's default: sending sort or order would change it.
	if strings.Contains(path, "sort=") || strings.Contains(path, "order=") {
		t.Errorf("default path %q must omit sort and order", path)
	}
}

func TestRepoSearchPath_Sorts(t *testing.T) {
	for _, sort := range []string{"stars", "forks", "updated"} {
		path, err := repoSearchPath("terminal", RepoSearchOptions{Sort: sort})
		if err != nil {
			t.Fatalf("sort %q: %v", sort, err)
		}
		for _, want := range []string{"sort=" + sort, "order=desc"} {
			if !strings.Contains(path, want) {
				t.Errorf("path %q missing %q", path, want)
			}
		}
	}
}

func TestRepoSearchPath_RejectsUnknownSort(t *testing.T) {
	for _, sort := range []string{"readme", "best-match", "help-wanted-issues"} {
		if _, err := repoSearchPath("terminal", RepoSearchOptions{Sort: sort}); !errors.Is(err, ErrInvalidRepoSearch) {
			t.Errorf("sort %q: err = %v, want ErrInvalidRepoSearch", sort, err)
		}
	}
}

func TestRepoSearchPath_RejectsEmptyQuery(t *testing.T) {
	for _, q := range []string{"", "   ", "\t\n"} {
		if _, err := repoSearchPath(q, RepoSearchOptions{}); !errors.Is(err, ErrInvalidRepoSearch) {
			t.Errorf("query %q: err = %v, want ErrInvalidRepoSearch", q, err)
		}
	}
}

func TestRepoSearchPath_PerPageBounds(t *testing.T) {
	for _, tc := range []struct {
		perPage int
		ok      bool
	}{
		{0, true}, // unset: the default applies
		{1, true},
		{100, true},
		{-1, false},
		{101, false},
	} {
		_, err := repoSearchPath("terminal", RepoSearchOptions{PerPage: tc.perPage})
		if (err == nil) != tc.ok {
			t.Errorf("per_page %d: err = %v, want ok=%v", tc.perPage, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalidRepoSearch) {
			t.Errorf("per_page %d: err = %v, want ErrInvalidRepoSearch", tc.perPage, err)
		}
	}
}

// GitHub serves only the first 1,000 results of any search; a page past that
// is rejected with a 422, so the builder refuses to construct one.
func TestRepoSearchPath_PageBounds(t *testing.T) {
	for _, tc := range []struct {
		page, perPage int
		ok            bool
	}{
		{0, 30, true}, // unset: page 1
		{1, 30, true},
		{33, 30, true}, // 990 <= 1000
		{34, 30, false},
		{10, 100, true}, // exactly 1000
		{11, 100, false},
		{-1, 30, false},
	} {
		_, err := repoSearchPath("terminal", RepoSearchOptions{Page: tc.page, PerPage: tc.perPage})
		if (err == nil) != tc.ok {
			t.Errorf("page %d @ per_page %d: err = %v, want ok=%v", tc.page, tc.perPage, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalidRepoSearch) {
			t.Errorf("page %d @ per_page %d: err = %v, want ErrInvalidRepoSearch", tc.page, tc.perPage, err)
		}
	}
}

// The query is user-controlled: reserved characters must not leak into the
// URL structure, and nothing (notably fork:false) may be appended to it.
func TestRepoSearchPath_EscapesQueryAndAppendsNothing(t *testing.T) {
	for _, q := range []string{
		"a&sort=stars #frag 100% +plus",
		"topic:cli fork:true",
		"topic:cli fork:only",
		"stars:>100 pushed:>2026-01-01",
	} {
		path, err := repoSearchPath(q, RepoSearchOptions{PerPage: 5, Page: 2})
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		vals := searchPathQuery(t, path)
		if got := vals.Get("q"); got != q {
			t.Errorf("q round-trips as %q, want %q", got, q)
		}
		if vals.Get("sort") != "" {
			t.Errorf("query %q injected a sort parameter: %q", q, path)
		}
		if got := vals.Get("per_page"); got != "5" {
			t.Errorf("query %q: per_page = %q, want 5", q, got)
		}
		if strings.Contains(path, "fork%3Afalse") || strings.Contains(path, "fork:false") {
			t.Errorf("path %q must not append fork:false", path)
		}
	}
}

func TestSearchRepositories_DecodesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/repositories" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("q"); got != "terminal language:Go" {
			t.Errorf("q = %q", got)
		}
		if got := q.Get("per_page"); got != "2" {
			t.Errorf("per_page = %q", got)
		}
		if got := q.Get("page"); got != "3" {
			t.Errorf("page = %q", got)
		}
		if got := q.Get("sort"); got != "stars" {
			t.Errorf("sort = %q", got)
		}
		// total_count above GitHub's 1,000 cap and incomplete_results are the two
		// envelope fields the topic-search decoder drops.
		_, _ = w.Write([]byte(`{
			"total_count": 4321,
			"incomplete_results": true,
			"items": [
				{"full_name": "alice/term", "html_url": "https://github.com/alice/term",
				 "description": "a terminal", "language": "Go", "stargazers_count": 812,
				 "forks_count": 97, "fork": false, "archived": false,
				 "pushed_at": "2026-09-01T00:00:00Z", "topics": ["cli", "tui"]},
				{"full_name": "bob/term-fork", "html_url": "https://github.com/bob/term-fork",
				 "description": null, "language": null, "stargazers_count": 1,
				 "forks_count": 0, "fork": true, "archived": true,
				 "pushed_at": "2025-01-01T00:00:00Z", "topics": []}
			]
		}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	res, err := c.SearchRepositories(context.Background(), "terminal language:Go", RepoSearchOptions{Sort: "stars", PerPage: 2, Page: 3})
	if err != nil {
		t.Fatalf("SearchRepositories: %v", err)
	}
	if res.TotalCount != 4321 {
		t.Errorf("TotalCount = %d, want 4321 (not clamped to 1000)", res.TotalCount)
	}
	if !res.IncompleteResults {
		t.Error("IncompleteResults = false, want true")
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(res.Items))
	}
	want := RepoSearchItem{
		FullName: "alice/term", HTMLURL: "https://github.com/alice/term",
		Description: "a terminal", Language: "Go", Stars: 812, Forks: 97,
		PushedAt: "2026-09-01T00:00:00Z", Topics: []string{"cli", "tui"},
	}
	got := res.Items[0]
	if got.FullName != want.FullName || got.HTMLURL != want.HTMLURL || got.Description != want.Description ||
		got.Language != want.Language || got.Stars != want.Stars || got.Forks != want.Forks ||
		got.Fork || got.Archived || got.PushedAt != want.PushedAt ||
		strings.Join(got.Topics, ",") != "cli,tui" {
		t.Errorf("Items[0] = %+v, want %+v", got, want)
	}
	second := res.Items[1]
	if !second.Fork || !second.Archived || second.Description != "" || second.Language != "" {
		t.Errorf("Items[1] = %+v, want fork+archived with empty description/language", second)
	}
}

func TestSearchRepositories_DedupesByLowercasedFullName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_count": 4, "incomplete_results": false, "items": [
			{"full_name": "Alice/Term", "stargazers_count": 10},
			{"full_name": "alice/term", "stargazers_count": 99},
			{"full_name": "", "stargazers_count": 5},
			{"full_name": "bob/other", "stargazers_count": 3}
		]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	res, err := c.SearchRepositories(context.Background(), "term", RepoSearchOptions{})
	if err != nil {
		t.Fatalf("SearchRepositories: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2 (case-variant duplicate and nameless item dropped): %+v", len(res.Items), res.Items)
	}
	if res.Items[0].FullName != "Alice/Term" || res.Items[0].Stars != 10 {
		t.Errorf("Items[0] = %+v, want the first occurrence kept with its original casing", res.Items[0])
	}
	if res.Items[1].FullName != "bob/other" {
		t.Errorf("Items[1] = %+v, want bob/other", res.Items[1])
	}
	// total_count describes GitHub's result set, not the deduped page.
	if res.TotalCount != 4 {
		t.Errorf("TotalCount = %d, want 4", res.TotalCount)
	}
}

func TestSearchRepositories_EmptyResultHasNonNilItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_count": 0, "incomplete_results": false, "items": []}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	res, err := c.SearchRepositories(context.Background(), "nothing-matches-this", RepoSearchOptions{})
	if err != nil {
		t.Fatalf("SearchRepositories: %v", err)
	}
	if res.Items == nil || len(res.Items) != 0 {
		t.Errorf("Items = %#v, want a non-nil empty slice", res.Items)
	}
}

func TestSearchRepositories_UnprocessableIsQueryRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"The listed users cannot be searched","resource":"Search","field":"q","code":"invalid"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.SearchRepositories(context.Background(), "user:nobody", RepoSearchOptions{})
	if err == nil {
		t.Fatal("expected an error for a 422")
	}
	if !IsQueryRejected(err) {
		t.Errorf("IsQueryRejected(%v) = false, want true", err)
	}
	var rl *RateLimitError
	if errors.As(err, &rl) {
		t.Errorf("422 must not read as a rate limit: %v", err)
	}
}

func TestSearchRepositories_RateLimitSurvivesWrapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "9999999999") // far beyond maxRateWait: surfaced, not slept on
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.SearchRepositories(context.Background(), "terminal", RepoSearchOptions{})
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %T %v, want *RateLimitError reachable through errors.As", err, err)
	}
	if IsQueryRejected(err) {
		t.Error("a rate limit is not a rejected query")
	}
}

// A 500 whose message mentions "422" (the request URL echoes the query and
// page) is an upstream failure, not a rejected query.
func TestSearchRepositories_ServerErrorIsNotQueryRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.SearchRepositories(context.Background(), "issue 422", RepoSearchOptions{PerPage: 1, Page: 422})
	if err == nil {
		t.Fatal("expected an error for a 500")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Fatalf("precondition: error text %q should mention 422", err)
	}
	if IsQueryRejected(err) {
		t.Errorf("IsQueryRejected(%v) = true for a 500", err)
	}
}

func TestSearchRepositories_InvalidInputSkipsNetwork(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"total_count":0,"items":[]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	for _, tc := range []struct {
		name  string
		query string
		opts  RepoSearchOptions
	}{
		{"empty query", "", RepoSearchOptions{}},
		{"page past the 1000-result cap", "terminal", RepoSearchOptions{Page: 34, PerPage: 30}},
		{"unknown sort", "terminal", RepoSearchOptions{Sort: "readme"}},
	} {
		res, err := c.SearchRepositories(context.Background(), tc.query, tc.opts)
		if !errors.Is(err, ErrInvalidRepoSearch) {
			t.Errorf("%s: err = %v, want ErrInvalidRepoSearch", tc.name, err)
		}
		if res != nil {
			t.Errorf("%s: res = %+v, want nil", tc.name, res)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("server saw %d requests for invalid input, want 0", n)
	}
}

func TestValidateRepoSearch(t *testing.T) {
	if err := ValidateRepoSearch("terminal", RepoSearchOptions{Sort: "stars", PerPage: 100, Page: 10}); err != nil {
		t.Errorf("last reachable page rejected: %v", err)
	}
	for _, tc := range []struct {
		name  string
		query string
		opts  RepoSearchOptions
	}{
		{"empty query", " ", RepoSearchOptions{}},
		{"per_page above 100", "terminal", RepoSearchOptions{PerPage: 101}},
		{"page past the cap", "terminal", RepoSearchOptions{Page: 34, PerPage: 30}},
		{"unknown sort", "terminal", RepoSearchOptions{Sort: "best-match"}},
	} {
		if err := ValidateRepoSearch(tc.query, tc.opts); !errors.Is(err, ErrInvalidRepoSearch) {
			t.Errorf("%s: err = %v, want ErrInvalidRepoSearch", tc.name, err)
		}
	}
}

func TestIsQueryRejected(t *testing.T) {
	if IsQueryRejected(nil) {
		t.Error("nil is not a rejected query")
	}
	wrapped := fmt.Errorf("repo search: %w", &gogithub.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}})
	if !IsQueryRejected(wrapped) {
		t.Error("a wrapped 422 HTTPError must read as a rejected query")
	}
	if IsQueryRejected(&gogithub.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}}) {
		t.Error("a 404 is not a rejected query")
	}
	if IsQueryRejected(errors.New("HTTP 422 in prose only")) {
		t.Error("untyped text mentioning 422 must not read as a rejected query")
	}
	// A transport failure carries no HTTPError and its message embeds the
	// request URL. isUnprocessableEntity falls back to matching "422" in that
	// text, which would turn a retryable network error into a non-retryable
	// bad_input whenever the query or page contains those digits.
	transport := fmt.Errorf("repo search: %w",
		errors.New(`Get "https://api.github.com/search/repositories?q=issue&per_page=1&page=422": dial tcp: connection refused`))
	if IsQueryRejected(transport) {
		t.Error("a transport error whose URL contains 422 must not read as a rejected query")
	}
}
