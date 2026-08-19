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

// ── Lineage and coverage tests ─────────────────────────────────────────────────

// TestGqlForkToForkInfo_LineageFields verifies that a fork node with parent
// data produces T1Extra fields for lineage tracking.
func TestGqlForkToForkInfo_LineageFields(t *testing.T) {
	parentName := "octo/root-repo"
	parentDB := int64(1001)
	node := gqlForkNode{
		DatabaseID:    2001,
		NameWithOwner: "alice/fork-of-root",
		Name:          "fork-of-root",
		StargazerCount: 5,
		PushedAt:     "2025-08-01T00:00:00Z",
		CreatedAt:    "2025-07-01T00:00:00Z",
		ForkCount:    3,
		Parent: &struct {
			NameWithOwner string `json:"nameWithOwner"`
			DatabaseID   int64  `json:"databaseId"`
		}{NameWithOwner: parentName, DatabaseID: parentDB},
	}

	_, extra := gqlForkToForkInfo(node, 15, 12, "authenticated", "2022-11-28")

	if extra.ParentFullPath != parentName {
		t.Errorf("ParentFullPath: got %q, want %q", extra.ParentFullPath, parentName)
	}
	if extra.ParentDatabaseID != parentDB {
		t.Errorf("ParentDatabaseID: got %d, want %d", extra.ParentDatabaseID, parentDB)
	}
	if extra.WholeNetworkForkCount != 15 {
		t.Errorf("WholeNetworkForkCount: got %d, want 15", extra.WholeNetworkForkCount)
	}
	if extra.DirectTotalCount != 12 {
		t.Errorf("DirectTotalCount: got %d, want 12", extra.DirectTotalCount)
	}
}

// TestGqlForkToForkInfo_WholeNetworkForkCount verifies that repoForkCount (root forkCount)
// populates WholeNetworkForkCount, distinct from node.ForkCount (own child count).
func TestGqlForkToForkInfo_WholeNetworkForkCount(t *testing.T) {
	node := gqlForkNode{
		DatabaseID:    3001,
		NameWithOwner: "bob/my-fork",
		Name:          "my-fork",
		ForkCount:    7, // this node has 7 children
		Parent: &struct {
			NameWithOwner string `json:"nameWithOwner"`
			DatabaseID   int64  `json:"databaseId"`
		}{NameWithOwner: "octo/root", DatabaseID: 3000},
	}

	_, extra := gqlForkToForkInfo(node, 128, 118, "authenticated", "2022-11-28")

	// Node's own fork count (7) goes to ForkCount.
	if extra.ForkCount != 7 {
		t.Errorf("ForkCount (node's own): got %d, want 7", extra.ForkCount)
	}
	// Root forkCount (128) goes to WholeNetworkForkCount.
	if extra.WholeNetworkForkCount != 128 {
		t.Errorf("WholeNetworkForkCount (root): got %d, want 128", extra.WholeNetworkForkCount)
	}
	// Direct total (118) goes to DirectTotalCount.
	if extra.DirectTotalCount != 118 {
		t.Errorf("DirectTotalCount: got %d, want 118", extra.DirectTotalCount)
	}
}

// TestAnnotateDepths_ParentChain verifies that depths 1, 2, 3 are assigned correctly
// from the parent_chain.json fixture (root=octo/root-repo).
func TestAnnotateDepths_ParentChain(t *testing.T) {
	// Simulate the parent_chain.json structure:
	// level1: parent=octo/root-repo (depth 1)
	// level2: parent=alice/level1-fork (depth 2)
	// level3: parent=bob/level2-fork (depth 3)
	forks := []ForkInfo{
		{ID: 4003, FullName: "carol/level3-fork"},
		{ID: 4001, FullName: "alice/level1-fork"},
		{ID: 4002, FullName: "bob/level2-fork"},
	}
	// Deliberately out of order to test algorithm.
	extras := []T1Extra{
		{ParentFullPath: "bob/level2-fork", ParentDatabaseID: 4002}, // level3
		{ParentFullPath: "octo/root-repo", ParentDatabaseID: 4000}, // level1
		{ParentFullPath: "alice/level1-fork", ParentDatabaseID: 4001}, // level2
	}

	result := annotateDepths(forks, extras, "octo/root-repo")

	// Collect by fork ID.
	depth := map[int64]int{}
	for i, f := range forks {
		depth[f.ID] = result[i].DepthFromRoot
	}

	if depth[4001] != 1 {
		t.Errorf("alice/level1-fork: got depth %d, want 1", depth[4001])
	}
	if depth[4002] != 2 {
		t.Errorf("bob/level2-fork: got depth %d, want 2", depth[4002])
	}
	if depth[4003] != 3 {
		t.Errorf("carol/level3-fork: got depth %d, want 3", depth[4003])
	}

	// DirectParent should be 1 for level1, 0 for others.
	if result[0].DirectParent != 0 {
		t.Errorf("carol/level3-fork: DirectParent=%d, want 0", result[0].DirectParent)
	}
	if result[1].DirectParent != 1 {
		t.Errorf("alice/level1-fork: DirectParent=%d, want 1", result[1].DirectParent)
	}
	if result[2].DirectParent != 0 {
		t.Errorf("bob/level2-fork: DirectParent=%d, want 0", result[2].DirectParent)
	}
}

// TestAnnotateDepths_CycleDetection verifies that a cycle (A->B->A) does not loop.
func TestAnnotateDepths_CycleDetection(t *testing.T) {
	forks := []ForkInfo{
		{ID: 5001, FullName: "alice/fork-a"},
		{ID: 5002, FullName: "bob/fork-b"},
	}
	// Cycle: A's parent is B, B's parent is A.
	extras := []T1Extra{
		{ParentFullPath: "bob/fork-b", ParentDatabaseID: 5002},
		{ParentFullPath: "alice/fork-a", ParentDatabaseID: 5001},
	}

	// Should terminate without looping. Depth stays 0 (unknown) for both.
	result := annotateDepths(forks, extras, "octo/root-repo")

	if result[0].DepthFromRoot != 0 {
		t.Errorf("alice/fork-a in cycle: got depth %d, want 0 (unknown)", result[0].DepthFromRoot)
	}
	if result[1].DepthFromRoot != 0 {
		t.Errorf("bob/fork-b in cycle: got depth %d, want 0 (unknown)", result[1].DepthFromRoot)
	}
}

// TestAnnotateDepths_MissingParentUnknown verifies that a fork with no parent
// (or a parent not in the result set) gets depth 0.
func TestAnnotateDepths_MissingParentUnknown(t *testing.T) {
	forks := []ForkInfo{
		{ID: 6001, FullName: "alice/orphan-fork"},
	}
	extras := []T1Extra{
		{ParentFullPath: "ghost/unknown-parent", ParentDatabaseID: 9999}, // not in result set
	}

	result := annotateDepths(forks, extras, "octo/root-repo")

	if result[0].DepthFromRoot != 0 {
		t.Errorf("orphan-fork: got depth %d, want 0 (unknown)", result[0].DepthFromRoot)
	}
	if result[0].DirectParent != 0 {
		t.Errorf("orphan-fork: DirectParent=%d, want 0 (per brief: 0/1; 0=not direct/unknown)", result[0].DirectParent)
	}
}

// TestAnnotateDepths_DirectChild verifies that a fork whose parent equals the
// requested root gets DirectParent=1 and DepthFromRoot=1.
func TestAnnotateDepths_DirectChild(t *testing.T) {
	forks := []ForkInfo{
		{ID: 7001, FullName: "alice/direct-fork"},
	}
	extras := []T1Extra{
		{ParentFullPath: "octo/root-repo", ParentDatabaseID: 7000},
	}

	result := annotateDepths(forks, extras, "octo/root-repo")

	if result[0].DirectParent != 1 {
		t.Errorf("DirectParent: got %d, want 1", result[0].DirectParent)
	}
	if result[0].DepthFromRoot != 1 {
		t.Errorf("DepthFromRoot: got %d, want 1", result[0].DepthFromRoot)
	}
}

// TestAnnotateDepths_EmptyForks verifies that annotateDepths is safe on empty input.
func TestAnnotateDepths_EmptyForks(t *testing.T) {
	result := annotateDepths(nil, nil, "octo/root-repo")
	if result != nil {
		t.Errorf("nil input: got %v, want nil", result)
	}
	result2 := annotateDepths([]ForkInfo{}, []T1Extra{}, "octo/root-repo")
	if result2 != nil {
		t.Errorf("empty slices: got %v, want nil", result2)
	}
}

// TestAnnotateDepths_RootNotInForkList verifies that annotateDepths handles the
// case where the requested root is not itself a fork in the result list (the
// common case for the root repo being non-fork or having been filtered).
func TestAnnotateDepths_RootNotInForkList(t *testing.T) {
	// root is octo/root-repo; the result list only contains forks.
	forks := []ForkInfo{
		{ID: 8001, FullName: "alice/fork-a"},
	}
	extras := []T1Extra{
		{ParentFullPath: "octo/root-repo", ParentDatabaseID: 8000},
	}

	result := annotateDepths(forks, extras, "octo/root-repo")

	if result[0].DirectParent != 1 {
		t.Errorf("DirectParent: got %d, want 1", result[0].DirectParent)
	}
	if result[0].DepthFromRoot != 1 {
		t.Errorf("DepthFromRoot: got %d, want 1", result[0].DepthFromRoot)
	}
}
