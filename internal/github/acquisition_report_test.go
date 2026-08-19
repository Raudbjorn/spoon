package github

import (
	"context"
	"encoding/json"
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
	if len(forks) != 3 {
		t.Errorf("len(forks) = %d, want 3 (raw count)", len(forks))
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
