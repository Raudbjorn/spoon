package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

// newRESTOnlyTestClient builds a *Client with only a REST client and no GraphQL
// backend — for the anonymous REST path.
func newRESTOnlyTestClient(t *testing.T, srv *httptest.Server) *Client {
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
	return &Client{rest: rest, authenticated: false, authScopeID: computeAuthScopeID("github", "github.com", nil)}
}

// TestFetchForksAuto_GraphQLSuccess_Report populates the acquisition report on a
// successful GraphQL run with two pages and dedupes repeated IDs.
func TestFetchForksAuto_GraphQLSuccess_Report(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/graphql") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var got struct {
			Variables struct {
				Cursor string `json:"cursor"`
			} `json:"variables"`
		}
		_ = json.Unmarshal(body, &got)
		// Replay body for the next reader.
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		switch got.Variables.Cursor {
		case "":
			_, _ = io.WriteString(w, `{"data":{"repository":{"forkCount":3,"forks":{`+
				`"totalCount":3,`+
				`"pageInfo":{"hasNextPage":true,"endCursor":"c1"},`+
				`"nodes":[{"databaseId":1,"nameWithOwner":"a/r","name":"r"}]}}}}`)
		case "c1":
			// Second page contains a duplicate (id 1) and a fresh row (id 2).
			_, _ = io.WriteString(w, `{"data":{"repository":{"forkCount":3,"forks":{`+
				`"totalCount":3,`+
				`"pageInfo":{"hasNextPage":false,"endCursor":"c2"},`+
				`"nodes":[`+
				`{"databaseId":1,"nameWithOwner":"a/r","name":"r"},`+
				`{"databaseId":2,"nameWithOwner":"b/r","name":"r"}`+
				`]}}}}`)
		}
	}))
	defer srv.Close()

	c := newTestClientGQL(t, srv)
	forks, extrasMap, report, err := c.FetchForksAuto(context.Background(), "foo", "bar", nil)
	if err != nil {
		t.Fatalf("FetchForksAuto: %v", err)
	}
	// Was "want 3 (raw count)": repeats used to be returned and only counted.
	// Every consumer downstream (TUI scoring, rank pool, export, enrichment
	// dispatch) assumes one row per fork, and on ggml-org/llama.cpp 9,187
	// repeats split forks into an enriched copy and stub copies. The raw
	// count now lives only in report.RawRows.
	if len(forks) != 2 {
		t.Errorf("len(forks) = %d, want 2 (repeats dropped)", len(forks))
	}
	if report != nil && report.ExpectedRows != 3 {
		t.Errorf("report.ExpectedRows = %d, want 3 (forks.totalCount)", report.ExpectedRows)
	}
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.Method != "graphql" {
		t.Errorf("report.Method = %q, want %q", report.Method, "graphql")
	}
	if report.Scope != "direct" {
		t.Errorf("report.Scope = %q, want %q", report.Scope, "direct")
	}
	if report.APIVersion != "2022-11-28" {
		t.Errorf("report.APIVersion = %q, want %q", report.APIVersion, "2022-11-28")
	}
	if report.AuthMode != "authenticated" {
		t.Errorf("report.AuthMode = %q, want %q", report.AuthMode, "authenticated")
	}
	if got := report.FallbackChain; len(got) != 1 || got[0] != "graphql" {
		t.Errorf("report.FallbackChain = %v, want [graphql]", got)
	}
	if report.Pages != 2 {
		t.Errorf("report.Pages = %d, want 2", report.Pages)
	}
	if report.RawRows != 3 {
		t.Errorf("report.RawRows = %d, want 3", report.RawRows)
	}
	if report.UniqueRows != 2 {
		t.Errorf("report.UniqueRows = %d, want 2", report.UniqueRows)
	}
	if report.DuplicateRows != 1 {
		t.Errorf("report.DuplicateRows = %d, want 1", report.DuplicateRows)
	}
	if report.AuthScopeID == "" {
		t.Error("report.AuthScopeID is empty")
	}
	if len(report.AuthScopeID) != 16 {
		t.Errorf("report.AuthScopeID = %q (len=%d), want 16 chars", report.AuthScopeID, len(report.AuthScopeID))
	}
	if len(extrasMap) != 2 {
		t.Errorf("len(extrasMap) = %d, want 2", len(extrasMap))
	}
	for _, e := range extrasMap {
		if e.ForkCount == 0 {
			// node.forkCount is missing in this minimal fixture; that is fine.
		}
	}
}

// TestFetchForksAuto_ZeroResults covers the empty-repo case where every
// page count is zero. The report must still be populated.
func TestFetchForksAuto_ZeroResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"repository":{"forkCount":0,"forks":{`+
			`"totalCount":0,`+
			`"pageInfo":{"hasNextPage":false,"endCursor":""},`+
			`"nodes":[]}}}}`)
	}))
	defer srv.Close()

	c := newTestClientGQL(t, srv)
	forks, extrasMap, report, err := c.FetchForksAuto(context.Background(), "foo", "bar", nil)
	if err != nil {
		t.Fatalf("FetchForksAuto: %v", err)
	}
	if len(forks) != 0 {
		t.Errorf("len(forks) = %d, want 0", len(forks))
	}
	if len(extrasMap) != 0 {
		t.Errorf("len(extrasMap) = %d, want 0", len(extrasMap))
	}
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.Method != "graphql" {
		t.Errorf("report.Method = %q, want %q", report.Method, "graphql")
	}
	if report.RawRows != 0 || report.UniqueRows != 0 || report.DuplicateRows != 0 {
		t.Errorf("report counts not zero: %+v", report)
	}
	if report.Pages != 0 {
		t.Errorf("zero-results Pages = %d, want 0", report.Pages)
	}
}

// TestFetchForksAuto_AnonymousREST covers a client with no GraphQL backend
// (anonymous). The report must report Method="rest", AuthMode="anonymous".
func TestFetchForksAuto_AnonymousREST(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/foo/bar/forks") {
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]ForkInfo{
			{ID: 1, FullName: "alice/repo"},
			{ID: 2, FullName: "bob/repo"},
		})
	}))
	defer srv.Close()

	c := newRESTOnlyTestClient(t, srv)
	forks, extrasMap, report, err := c.FetchForksAuto(context.Background(), "foo", "bar", nil)
	if err != nil {
		t.Fatalf("FetchForksAuto: %v", err)
	}
	if len(forks) != 2 {
		t.Errorf("len(forks) = %d, want 2", len(forks))
	}
	if extrasMap != nil {
		t.Errorf("extras = %v, want nil for REST path", extrasMap)
	}
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.Method != "rest" {
		t.Errorf("report.Method = %q, want %q", report.Method, "rest")
	}
	if report.AuthMode != "anonymous" {
		t.Errorf("report.AuthMode = %q, want %q", report.AuthMode, "anonymous")
	}
	if got := report.FallbackChain; len(got) != 1 || got[0] != "rest" {
		t.Errorf("report.FallbackChain = %v, want [rest]", got)
	}
	if report.Error != "" {
		t.Errorf("report.Error = %q, want empty", report.Error)
	}
	if report.Pages != 1 {
		t.Errorf("report.Pages = %d, want 1 non-empty REST callback", report.Pages)
	}
	if report.RawRows != 2 {
		t.Errorf("report.RawRows = %d, want 2", report.RawRows)
	}
}

// TestFetchForksAuto_RESTFailed covers the pure-REST-failure case. The report
// must carry Method="rest", Error="rest_failed", and the error is non-nil.
func TestFetchForksAuto_RESTFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newRESTOnlyTestClient(t, srv)
	_, _, report, err := c.FetchForksAuto(context.Background(), "foo", "bar", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.Method != "rest" {
		t.Errorf("report.Method = %q, want %q", report.Method, "rest")
	}
	if report.Error != "rest_failed" {
		t.Errorf("report.Error = %q, want %q", report.Error, "rest_failed")
	}
}

// TestFetchForksAuto_PartialGraphQL_RESTSuccess covers the GraphQL→REST
// partial-fallback path. The first GraphQL call streams one fork then fails;
// REST succeeds. Report.Method must be "graphql+rest", FallbackChain
// ["graphql","rest"], Error empty (because REST succeeded).
func TestFetchForksAuto_PartialGraphQL_RESTSuccess(t *testing.T) {
	var gqlCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/graphql"):
			gqlCalls++
			if gqlCalls == 1 {
				_, _ = io.WriteString(w, `{"data":{"repository":{"forkCount":3,"forks":{`+
					`"totalCount":3,`+
					`"pageInfo":{"hasNextPage":true,"endCursor":"c1"},`+
					`"nodes":[{"databaseId":1,"nameWithOwner":"a/r","name":"r"}]}}}}`)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/repos/foo/bar/forks"):
			_ = json.NewEncoder(w).Encode([]ForkInfo{
				{ID: 1, FullName: "a/r"}, // duplicate of GraphQL row
				{ID: 2, FullName: "b/r"},
			})
		}
	}))
	defer srv.Close()

	c := newTestClientGQL(t, srv)
	forks, extrasMap, report, err := c.FetchForksAuto(context.Background(), "foo", "bar", nil)
	if err != nil {
		t.Fatalf("FetchForksAuto: %v", err)
	}
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.Method != "graphql+rest" {
		t.Errorf("report.Method = %q, want %q", report.Method, "graphql+rest")
	}
	if got := report.FallbackChain; len(got) != 2 || got[0] != "graphql" || got[1] != "rest" {
		t.Errorf("report.FallbackChain = %v, want [graphql rest]", got)
	}
	if report.Error != "" {
		t.Errorf("report.Error = %q, want empty (REST succeeded)", report.Error)
	}
	if len(forks) != 2 {
		t.Errorf("len(forks) = %d, want 2 (dedup merged)", len(forks))
	}
	if extrasMap != nil {
		t.Errorf("extras = %v, want nil on REST fallback", extrasMap)
	}
	if report.UniqueRows != 2 {
		t.Errorf("report.UniqueRows = %d, want 2", report.UniqueRows)
	}
	if report.DuplicateRows != 1 {
		t.Errorf("report.DuplicateRows = %d, want 1", report.DuplicateRows)
	}
	if report.Pages != 2 {
		t.Errorf("report.Pages = %d, want 2 (one GraphQL + one REST page)", report.Pages)
	}
	if report.RawRows != 3 {
		t.Errorf("report.RawRows = %d, want 3 (one GraphQL + two REST rows)", report.RawRows)
	}
}

// TestFetchForksAuto_PartialGraphQL_RESTFailed covers the case where GraphQL
// fails AND the REST fallback also fails. The report remains the partial
// GraphQL snapshot: Method="graphql", FallbackChain=["graphql"], and
// Error="rest_fallback_failed".
func TestFetchForksAuto_PartialGraphQL_RESTFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClientGQL(t, srv)
	_, _, report, err := c.FetchForksAuto(context.Background(), "foo", "bar", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.Method != "graphql" {
		t.Errorf("report.Method = %q, want %q", report.Method, "graphql")
	}
	if report.Error != "rest_fallback_failed" {
		t.Errorf("report.Error = %q, want %q", report.Error, "rest_fallback_failed")
	}
	if got := report.FallbackChain; len(got) != 1 || got[0] != "graphql" {
		t.Errorf("report.FallbackChain = %v, want [graphql]", got)
	}
}

// TestAuthScopeIDDeterministic verifies the scope ID is the same string for
// the same (provider, host, token set), regardless of token order.
func TestAuthScopeIDDeterministic(t *testing.T) {
	id1 := computeAuthScopeID("github", "github.com", []string{"b", "a", "c"})
	id2 := computeAuthScopeID("github", "github.com", []string{"c", "a", "b"})
	if id1 != id2 {
		t.Errorf("AuthScopeID not deterministic: %q vs %q", id1, id2)
	}
	if len(id1) != 16 {
		t.Errorf("len(AuthScopeID) = %d, want 16", len(id1))
	}
	// Anonymous still has a deterministic scope ID for empty tokens.
	idAnon := computeAuthScopeID("github", "github.com", nil)
	if len(idAnon) != 16 {
		t.Errorf("anonymous AuthScopeID length = %d, want 16", len(idAnon))
	}
	if idAnon == id1 {
		t.Error("anonymous and authenticated scope IDs must differ")
	}
	// Host matters: changing the host changes the ID.
	idHost := computeAuthScopeID("github", "ghe.example.com", []string{"a"})
	if idHost == id1 {
		t.Error("different hosts must produce different scope IDs")
	}
}

// TestAuthScopeIDSafeWithTokens ensures the scope ID does NOT contain any
// token bytes (secret-leak regression).
func TestAuthScopeIDSafeWithTokens(t *testing.T) {
	sentinel := "SECRET_TOKEN_DO_NOT_LEAK_12345"
	id := computeAuthScopeID("github", "github.com", []string{sentinel})
	if strings.Contains(id, sentinel) {
		t.Errorf("AuthScopeID %q contains sentinel token; secret leak!", id)
	}
}

// The fork listing must page by an immutable key. Paging by stars returned
// ~20k rows covering only 11,171 of 19,981 forks on ggml-org/llama.cpp (most
// forks tie at 0 stars, so cursor pages overlap and skip); CREATED_AT returned
// all 19,981 exactly once (live, 2026-09-26).
func TestForksGraphQLQuery_PagesByCreationTime(t *testing.T) {
	if !strings.Contains(forksGraphQLQuery, "orderBy: {field: CREATED_AT, direction: ASC}") {
		t.Fatalf("forks query must page by CREATED_AT ASC; got:\n%s", forksGraphQLQuery)
	}
	if strings.Contains(forksGraphQLQuery, "field: STARGAZERS") {
		t.Fatal("forks query pages by STARGAZERS, a heavily tied key")
	}
}

// REST fallback: pages requested oldest-first, repeats across pages dropped
// (and from onPage), raw count kept in the report, result in stars order.
func TestFetchForksAuto_RESTDedupAndOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("sort"); got != "oldest" {
			t.Errorf("REST forks sort = %q, want oldest", got)
		}
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode([]ForkInfo{
				{ID: 2, FullName: "b/r"}, // repeat from page 1
				{ID: 3, FullName: "c/r", Stars: 7},
			})
			return
		}
		w.Header().Set("Link", `<`+"http://"+r.Host+r.URL.Path+`?sort=oldest&per_page=100&page=2>; rel="next"`)
		_ = json.NewEncoder(w).Encode([]ForkInfo{
			{ID: 1, FullName: "a/r"},
			{ID: 2, FullName: "b/r", Stars: 7},
		})
	}))
	defer srv.Close()

	var streamed []int64
	c := newRESTOnlyTestClient(t, srv)
	forks, _, report, err := c.FetchForksAuto(context.Background(), "foo", "bar", func(fs []ForkInfo, _ int) {
		for _, f := range fs {
			streamed = append(streamed, f.ID)
		}
	})
	if err != nil {
		t.Fatalf("FetchForksAuto: %v", err)
	}
	got := make([]int64, len(forks))
	for i, f := range forks {
		got[i] = f.ID
	}
	// Stars desc, ID asc on ties: 2 (7★), 3 (7★), 1 (0★).
	if want := []int64{2, 3, 1}; !equalInt64s(got, want) {
		t.Errorf("fork IDs = %v, want %v", got, want)
	}
	if want := []int64{1, 2, 3}; !equalInt64s(streamed, want) {
		t.Errorf("onPage streamed %v, want %v (repeat must not be re-emitted)", streamed, want)
	}
	if report.RawRows != 4 || report.UniqueRows != 3 || report.DuplicateRows != 1 {
		t.Errorf("report raw/unique/dup = %d/%d/%d, want 4/3/1", report.RawRows, report.UniqueRows, report.DuplicateRows)
	}
}

func TestSortForksByStars_KeepsExtrasParallel(t *testing.T) {
	forks := []ForkInfo{{ID: 5, Stars: 0}, {ID: 9, Stars: 3}, {ID: 1, Stars: 0}}
	extras := []T1Extra{{ForkCount: 50}, {ForkCount: 90}, {ForkCount: 10}}
	sortForksByStars(forks, extras)
	for i, want := range []int64{9, 1, 5} {
		if forks[i].ID != want {
			t.Fatalf("forks[%d].ID = %d, want %d", i, forks[i].ID, want)
		}
		if extras[i].ForkCount != int(want)*10 {
			t.Fatalf("extras[%d] = %d, not aligned with fork %d", i, extras[i].ForkCount, want)
		}
	}
}

// A page whose rows all repeat earlier pages is still an upstream page:
// Pages counts it (RawRows already includes its rows), and onPage is not
// called with an empty batch. Review finding on #135.
func TestFetchForksAuto_DuplicateOnlyPagesStillCounted(t *testing.T) {
	t.Run("graphql", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var got struct {
				Variables struct {
					Cursor string `json:"cursor"`
				} `json:"variables"`
			}
			_ = json.Unmarshal(body, &got)
			node := func(id int) string {
				return fmt.Sprintf(`{"databaseId":%d,"nameWithOwner":"o%d/r","name":"r"}`, id, id)
			}
			page := func(next, cursor string, nodes ...string) string {
				return `{"data":{"repository":{"forkCount":2,"forks":{"totalCount":2,` +
					`"pageInfo":{"hasNextPage":` + next + `,"endCursor":"` + cursor + `"},` +
					`"nodes":[` + strings.Join(nodes, ",") + `]}}}}`
			}
			switch got.Variables.Cursor {
			case "":
				_, _ = io.WriteString(w, page("true", "c1", node(1)))
			case "c1":
				_, _ = io.WriteString(w, page("true", "c2", node(1))) // repeats only
			case "c2":
				_, _ = io.WriteString(w, page("false", "c3", node(2)))
			}
		}))
		defer srv.Close()

		var calls int
		c := newTestClientGQL(t, srv)
		forks, _, report, err := c.FetchForksAuto(context.Background(), "foo", "bar", func([]ForkInfo, int) { calls++ })
		if err != nil {
			t.Fatalf("FetchForksAuto: %v", err)
		}
		if len(forks) != 2 || report.Pages != 3 || report.RawRows != 3 || calls != 2 {
			t.Errorf("forks=%d pages=%d raw=%d onPage calls=%d; want 2/3/3/2", len(forks), report.Pages, report.RawRows, calls)
		}
	})
	t.Run("rest", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "2" {
				_ = json.NewEncoder(w).Encode([]ForkInfo{{ID: 1, FullName: "a/r"}}) // repeats only
				return
			}
			w.Header().Set("Link", `<`+"http://"+r.Host+r.URL.Path+`?sort=oldest&per_page=100&page=2>; rel="next"`)
			_ = json.NewEncoder(w).Encode([]ForkInfo{{ID: 1, FullName: "a/r"}})
		}))
		defer srv.Close()

		var calls int
		c := newRESTOnlyTestClient(t, srv)
		_, _, report, err := c.FetchForksAuto(context.Background(), "foo", "bar", func([]ForkInfo, int) { calls++ })
		if err != nil {
			t.Fatalf("FetchForksAuto: %v", err)
		}
		if report.Pages != 2 || report.RawRows != 2 || report.UniqueRows != 1 || calls != 1 {
			t.Errorf("pages=%d raw=%d unique=%d onPage calls=%d; want 2/2/1/1", report.Pages, report.RawRows, report.UniqueRows, calls)
		}
	})
}
