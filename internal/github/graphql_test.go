package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

func TestIsTransientServerError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed 502", &ghAPI.HTTPError{StatusCode: 502}, true},
		{"typed 503", &ghAPI.HTTPError{StatusCode: 503}, true},
		{"typed 504", &ghAPI.HTTPError{StatusCode: 504}, true},
		{"typed 500 not retried", &ghAPI.HTTPError{StatusCode: 500}, false},
		{"typed 404 not retried", &ghAPI.HTTPError{StatusCode: 404}, false},
		{"typed 403 not retried", &ghAPI.HTTPError{StatusCode: 403}, false},
		{"wrapped 502", fmt.Errorf("GraphQL query: %w", &ghAPI.HTTPError{StatusCode: 502}), true},
		{"string 502", errors.New("HTTP 502: 502 Bad Gateway"), true},
		{"string 504", errors.New("HTTP 504: Gateway Timeout"), true},
		{"plain error", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientServerError(tt.err); got != tt.want {
				t.Errorf("isTransientServerError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// newTestClientGQL builds a *Client whose REST and GraphQL calls both land on
// srv, so FetchForksAuto's GraphQL→REST fallback can be exercised end to end.
func newTestClientGQL(t *testing.T, srv *httptest.Server) *Client {
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
	gql, err := ghAPI.NewGraphQLClient(ghAPI.ClientOptions{
		AuthToken: "x",
		Host:      "github.com",
		Transport: tr,
	})
	if err != nil {
		t.Fatalf("NewGraphQLClient: %v", err)
	}
	return &Client{rest: rest, gql: gql, authenticated: true, authScopeID: computeAuthScopeID("github", "github.com", []string{"test-token"})}
}

// TestFetchForksAuto_FallbackDedup covers the GraphQL→REST fallback path: the
// GraphQL query streams one page of forks via onPage and then fails, so we fall
// back to REST. REST restarts from page 1 and returns an overlapping fork, which
// must be filtered out of the fallback's onPage so callers see each fork once —
// while the final returned slice still holds the complete REST result.
func TestFetchForksAuto_FallbackDedup(t *testing.T) {
	var gqlCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/graphql"):
			gqlCalls++
			if gqlCalls == 1 {
				// First page succeeds and streams fork ID 1, with more pages to come.
				_, _ = io.WriteString(w, `{"data":{"repository":{"forks":{`+
					`"pageInfo":{"hasNextPage":true,"endCursor":"c1"},`+
					`"nodes":[{"databaseId":1,"nameWithOwner":"alice/repo","name":"repo"}]}}}}`)
				return
			}
			// Second page fails non-transiently (500 is not retried), triggering the
			// REST fallback after fork ID 1 was already streamed.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"boom"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/repos/foo/bar/forks"):
			// REST returns the full fork list, overlapping the streamed ID 1.
			_ = json.NewEncoder(w).Encode([]ForkInfo{
				{ID: 1, FullName: "alice/repo"},
				{ID: 2, FullName: "bob/repo"},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClientGQL(t, srv)

	var streamed []int64
	onPage := func(forks []ForkInfo, page int) {
		for _, f := range forks {
			streamed = append(streamed, f.ID)
		}
	}

	forks, extras, _, err := c.FetchForksAuto(context.Background(), "foo", "bar", onPage)
	if err != nil {
		t.Fatalf("FetchForksAuto: %v", err)
	}
	if gqlCalls != 2 {
		t.Fatalf("expected 2 GraphQL calls (success then failure), got %d", gqlCalls)
	}
	if extras != nil {
		t.Errorf("REST fallback should return nil extras, got %v", extras)
	}

	// Final slice is the complete REST result, including the overlapping fork.
	gotIDs := make([]int64, len(forks))
	for i, f := range forks {
		gotIDs[i] = f.ID
	}
	wantIDs := []int64{1, 2}
	if !equalInt64s(gotIDs, wantIDs) {
		t.Errorf("returned fork IDs = %v, want %v", gotIDs, wantIDs)
	}

	// onPage saw ID 1 once (GraphQL) and ID 2 once (REST) — the fallback filtered
	// out the re-streamed ID 1. Without dedup, streamed would be {1, 1, 2}.
	wantStreamed := []int64{1, 2}
	if !equalInt64s(streamed, wantStreamed) {
		t.Errorf("onPage streamed IDs = %v, want %v (fallback should not re-emit ID 1)", streamed, wantStreamed)
	}
}

func equalInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGqlForkToForkInfo_TopicsAndForkFlag covers the P1 plumbing path:
// a GraphQL `repositoryTopics` payload is flattened into ForkInfo.Topics,
// and the Fork boolean is set on every row of the forks query (since
// /repos/{o}/{r}/forks only returns forks, `fork: true` is implicit).
func TestGqlForkToForkInfo_TopicsAndForkFlag(t *testing.T) {
	node := gqlForkNode{
		DatabaseID:    42,
		NameWithOwner: "alice/foo",
		Name:          "foo",
		RepositoryTopics: struct {
			Nodes []struct {
				Topic struct {
					Name string `json:"name"`
				} `json:"topic"`
			} `json:"nodes"`
		}{
			Nodes: []struct {
				Topic struct {
					Name string `json:"name"`
				} `json:"topic"`
			}{
				{Topic: struct {
					Name string `json:"name"`
				}{Name: "kubernetes"}},
				{Topic: struct {
					Name string `json:"name"`
				}{Name: "kustomize"}},
			},
		},
	}

	fork, _ := gqlForkToForkInfo(node, 0, 0, "authenticated", "2022-11-28")

	if !fork.Fork {
		t.Errorf("Fork flag: got %v, want true (forks query rows are forks)", fork.Fork)
	}
	want := []string{"kubernetes", "kustomize"}
	if !equalStrings(fork.Topics, want) {
		t.Errorf("Topics: got %v, want %v", fork.Topics, want)
	}
}

// TestGqlForkToForkInfo_NoTopicsNilSlice covers the legacy/empty case
// where the GraphQL payload omits repositoryTopics. Fork.Topics must
// be nil (not []string{}) so JSON output stays idiomatic and P1's
// "no signal" guard fires.
func TestGqlForkToForkInfo_NoTopicsNilSlice(t *testing.T) {
	node := gqlForkNode{
		DatabaseID:    7,
		NameWithOwner: "bob/bar",
		Name:          "bar",
	}
	fork, _ := gqlForkToForkInfo(node, 0, 0, "authenticated", "2022-11-28")
	if fork.Topics != nil {
		t.Errorf("Topics: got %v, want nil (no repositoryTopics payload)", fork.Topics)
	}
	if !fork.Fork {
		t.Errorf("Fork flag: got %v, want true", fork.Fork)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
