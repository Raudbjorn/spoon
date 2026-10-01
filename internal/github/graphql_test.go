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
	"os"
	"strings"
	"testing"
	"time"

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
	// Was "should return nil extras": see
	// TestFetchForksAuto_PartialGraphQL_RESTSuccess. The GraphQL-streamed
	// fork keeps its extras; the REST-only fork has none.
	if _, ok := extras[1]; !ok || len(extras) != 1 {
		t.Errorf("extras = %v, want only fork 1's (fetched by GraphQL)", extras)
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
		DatabaseID:     2001,
		NameWithOwner:  "alice/fork-of-root",
		Name:           "fork-of-root",
		StargazerCount: 5,
		PushedAt:       "2025-08-01T00:00:00Z",
		CreatedAt:      "2025-07-01T00:00:00Z",
		ForkCount:      3,
		Parent: &struct {
			NameWithOwner string `json:"nameWithOwner"`
			DatabaseID    int64  `json:"databaseId"`
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
		ForkCount:     7, // this node has 7 children
		Parent: &struct {
			NameWithOwner string `json:"nameWithOwner"`
			DatabaseID    int64  `json:"databaseId"`
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

// ── Fixture-driven decode tests ─────────────────────────────────────────────────
//
// The tests below load the JSON fixtures in testdata/inventory_contract/ and run
// them through the production decode pipeline (gqlResponse → gqlForkToForkInfo
// → annotateDepths). They are the load-bearing integration tests for Task 3:
// they catch drift between the GraphQL schema extension (parent { nameWithOwner
// databaseId }) and the T1Extra lineage fields, and between the GraphQL
// repository forkCount vs forks.totalCount and the Coverage fields.

// TestFixtureParentChain_DecodeAndAnnotate loads parent_chain.json and asserts
// that the production decode + annotateDepths pipeline produces T1Extra values
// matching the fixture:
//
//   - alice/level1-fork → depth 1, direct parent = octo/root-repo
//   - bob/level2-fork   → depth 2, parent = alice/level1-fork
//   - carol/level3-fork → depth 3, parent = bob/level2-fork
//
// The fixture is loaded from disk and decoded into the production gqlResponse
// type so this test guards against schema drift between the GraphQL query and
// the decoder.
func TestFixtureParentChain_DecodeAndAnnotate(t *testing.T) {
	const fixturePath = "testdata/inventory_contract/parent_chain.json"
	resp := loadGQLFixture(t, fixturePath)

	root := "octo/root-repo"
	nodes := resp.Repository.Forks.Nodes
	if len(nodes) != 3 {
		t.Fatalf("parent_chain.json: got %d fork nodes, want 3", len(nodes))
	}

	forks := make([]ForkInfo, 0, len(nodes))
	extras := make([]T1Extra, 0, len(nodes))
	for _, node := range nodes {
		fork, extra := gqlForkToForkInfo(
			node,
			resp.Repository.ForkCount,
			resp.Repository.Forks.TotalCount,
			"authenticated",
			"2022-11-28",
		)
		forks = append(forks, fork)
		extras = append(extras, extra)
	}

	// Decode-side assertions: each fork's parent must round-trip through the
	// GraphQL parent { nameWithOwner databaseId } payload.
	wantParents := map[string]struct {
		parentFullPath string
		parentDB       int64
	}{
		"alice/level1-fork": {"octo/root-repo", 4000},
		"bob/level2-fork":   {"alice/level1-fork", 4001},
		"carol/level3-fork": {"bob/level2-fork", 4002},
	}
	for i, f := range forks {
		want, ok := wantParents[f.FullName]
		if !ok {
			t.Errorf("unexpected fork %q in parent_chain.json", f.FullName)
			continue
		}
		if extras[i].ParentFullPath != want.parentFullPath {
			t.Errorf("%s: ParentFullPath=%q, want %q",
				f.FullName, extras[i].ParentFullPath, want.parentFullPath)
		}
		if extras[i].ParentDatabaseID != want.parentDB {
			t.Errorf("%s: ParentDatabaseID=%d, want %d",
				f.FullName, extras[i].ParentDatabaseID, want.parentDB)
		}
	}

	// Annotation-side assertions: depths propagate through the parent chain.
	annotated := annotateDepths(forks, extras, root)
	depth := map[string]int{}
	direct := map[string]int{}
	for i, f := range forks {
		depth[f.FullName] = annotated[i].DepthFromRoot
		direct[f.FullName] = annotated[i].DirectParent
	}

	if depth["alice/level1-fork"] != 1 {
		t.Errorf("alice/level1-fork: DepthFromRoot=%d, want 1", depth["alice/level1-fork"])
	}
	if depth["bob/level2-fork"] != 2 {
		t.Errorf("bob/level2-fork: DepthFromRoot=%d, want 2", depth["bob/level2-fork"])
	}
	if depth["carol/level3-fork"] != 3 {
		t.Errorf("carol/level3-fork: DepthFromRoot=%d, want 3", depth["carol/level3-fork"])
	}

	// DirectParent is 1 only for the direct child of the root; deeper levels
	// are 0 because the brief defines DirectParent as 0/1 and only direct
	// children qualify.
	if direct["alice/level1-fork"] != 1 {
		t.Errorf("alice/level1-fork: DirectParent=%d, want 1", direct["alice/level1-fork"])
	}
	if direct["bob/level2-fork"] != 0 {
		t.Errorf("bob/level2-fork: DirectParent=%d, want 0", direct["bob/level2-fork"])
	}
	if direct["carol/level3-fork"] != 0 {
		t.Errorf("carol/level3-fork: DirectParent=%d, want 0", direct["carol/level3-fork"])
	}

	// Whole-network forkCount (15) and direct totalCount (15) from the fixture
	// must round-trip onto T1Extra so Coverage.Unresolved can be derived later.
	for i, f := range forks {
		if annotated[i].WholeNetworkForkCount != 15 {
			t.Errorf("%s: WholeNetworkForkCount=%d, want 15 (from repository.forkCount)",
				f.FullName, annotated[i].WholeNetworkForkCount)
		}
		if annotated[i].DirectTotalCount != 15 {
			t.Errorf("%s: DirectTotalCount=%d, want 15 (from forks.totalCount)",
				f.FullName, annotated[i].DirectTotalCount)
		}
	}
}

// TestFixtureDirectWholeCountGap_Coverage loads direct_whole_count_gap.json and
// asserts that the production decode pipeline separates repository.forkCount
// (whole-network: 128) from forks.totalCount (direct: 118). This is the
// Coverage.Unresolved = whole - direct contract; the test asserts both values
// land on T1Extra so downstream code can derive the unresolved gap.
func TestFixtureDirectWholeCountGap_Coverage(t *testing.T) {
	const fixturePath = "testdata/inventory_contract/direct_whole_count_gap.json"
	resp := loadGQLFixture(t, fixturePath)

	if resp.Repository.ForkCount != 128 {
		t.Errorf("repository.forkCount=%d, want 128", resp.Repository.ForkCount)
	}
	if resp.Repository.Forks.TotalCount != 118 {
		t.Errorf("forks.totalCount=%d, want 118", resp.Repository.Forks.TotalCount)
	}

	for _, node := range resp.Repository.Forks.Nodes {
		_, extra := gqlForkToForkInfo(
			node,
			resp.Repository.ForkCount,
			resp.Repository.Forks.TotalCount,
			"authenticated",
			"2022-11-28",
		)
		if extra.WholeNetworkForkCount != 128 {
			t.Errorf("%s: WholeNetworkForkCount=%d, want 128",
				node.NameWithOwner, extra.WholeNetworkForkCount)
		}
		if extra.DirectTotalCount != 118 {
			t.Errorf("%s: DirectTotalCount=%d, want 118",
				node.NameWithOwner, extra.DirectTotalCount)
		}
		// Each fork in this fixture is a direct child of chunkhound/chunkhound,
		// so its parent payload must round-trip and produce DirectParent=1.
		if extra.ParentFullPath != "chunkhound/chunkhound" {
			t.Errorf("%s: ParentFullPath=%q, want %q",
				node.NameWithOwner, extra.ParentFullPath, "chunkhound/chunkhound")
		}
		if extra.ParentDatabaseID != 3000 {
			t.Errorf("%s: ParentDatabaseID=%d, want 3000",
				node.NameWithOwner, extra.ParentDatabaseID)
		}
	}
}

// TestFixtureDirectWholeCountGap_AnnotateDepths runs the direct_whole_count_gap
// fixture through annotateDepths and verifies the direct-child path: every fork
// in the fixture has ParentFullPath == root, so each gets DirectParent=1 and
// DepthFromRoot=1, regardless of slice order.
func TestFixtureDirectWholeCountGap_AnnotateDepths(t *testing.T) {
	const fixturePath = "testdata/inventory_contract/direct_whole_count_gap.json"
	resp := loadGQLFixture(t, fixturePath)

	root := "chunkhound/chunkhound"
	nodes := resp.Repository.Forks.Nodes
	forks := make([]ForkInfo, 0, len(nodes))
	extras := make([]T1Extra, 0, len(nodes))
	for _, node := range nodes {
		fork, extra := gqlForkToForkInfo(
			node,
			resp.Repository.ForkCount,
			resp.Repository.Forks.TotalCount,
			"authenticated",
			"2022-11-28",
		)
		forks = append(forks, fork)
		extras = append(extras, extra)
	}

	annotated := annotateDepths(forks, extras, root)
	for i, f := range forks {
		if annotated[i].DirectParent != 1 {
			t.Errorf("%s: DirectParent=%d, want 1 (direct child of %s)",
				f.FullName, annotated[i].DirectParent, root)
		}
		if annotated[i].DepthFromRoot != 1 {
			t.Errorf("%s: DepthFromRoot=%d, want 1 (direct child of %s)",
				f.FullName, annotated[i].DepthFromRoot, root)
		}
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
		{ParentFullPath: "bob/level2-fork", ParentDatabaseID: 4002},   // level3
		{ParentFullPath: "octo/root-repo", ParentDatabaseID: 4000},    // level1
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

// TestFetchForksGraphQL_FixtureDirectWholeCountGap exercises the production
// GraphQL decoder with the inventory fixture rather than locally-built nodes.
// It preserves the distinct direct-child (forks.totalCount) and whole-network
// (repository.forkCount) counts end to end.
func TestFetchForksGraphQL_FixtureDirectWholeCountGap(t *testing.T) {
	payload, err := os.ReadFile("testdata/inventory_contract/direct_whole_count_gap.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/graphql") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	forks, extras, _, err := newTestClientGQL(t, srv).FetchForksGraphQL(
		context.Background(), "chunkhound", "chunkhound", nil,
	)
	if err != nil {
		t.Fatalf("FetchForksGraphQL: %v", err)
	}
	if len(forks) != 2 || len(extras) != 2 {
		t.Fatalf("decoded fork/extras counts = %d/%d, want 2/2", len(forks), len(extras))
	}
	for i, fork := range forks {
		extra := extras[i]
		if extra.DirectTotalCount != 118 {
			t.Errorf("%s direct count = %d, want 118", fork.FullName, extra.DirectTotalCount)
		}
		if extra.WholeNetworkForkCount != 128 {
			t.Errorf("%s whole-network count = %d, want 128", fork.FullName, extra.WholeNetworkForkCount)
		}
		if unresolved := max(extra.WholeNetworkForkCount-extra.DirectTotalCount, 0); unresolved != 10 {
			t.Errorf("%s unresolved = %d, want 10", fork.FullName, unresolved)
		}
		if extra.ParentFullPath != "chunkhound/chunkhound" || extra.DepthFromRoot != 1 {
			t.Errorf("%s lineage = parent %q depth %d, want chunkhound/chunkhound depth 1",
				fork.FullName, extra.ParentFullPath, extra.DepthFromRoot)
		}
	}
}

// TestFetchForksGraphQL_FixtureParentChainOutOfOrder feeds the real
// parent_chain fixture through the production GraphQL path after reversing its
// nodes. The fixed-point lineage annotation must still derive depths 1, 2, 3.
func TestFetchForksGraphQL_FixtureParentChainOutOfOrder(t *testing.T) {
	payload, err := os.ReadFile("testdata/inventory_contract/parent_chain.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var fixture map[string]any
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatalf("decode fixture for reordering: %v", err)
	}
	data := fixture["data"].(map[string]any)
	repository := data["repository"].(map[string]any)
	forksPayload := repository["forks"].(map[string]any)
	nodes := forksPayload["nodes"].([]any)
	for left, right := 0, len(nodes)-1; left < right; left, right = left+1, right-1 {
		nodes[left], nodes[right] = nodes[right], nodes[left]
	}
	payload, err = json.Marshal(fixture)
	if err != nil {
		t.Fatalf("encode reordered fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/graphql") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	forks, extras, _, err := newTestClientGQL(t, srv).FetchForksGraphQL(
		context.Background(), "octo", "root-repo", nil,
	)
	if err != nil {
		t.Fatalf("FetchForksGraphQL: %v", err)
	}
	if len(forks) != 3 || len(extras) != 3 {
		t.Fatalf("decoded fork/extras counts = %d/%d, want 3/3", len(forks), len(extras))
	}

	depthByName := make(map[string]int, len(forks))
	for i, fork := range forks {
		depthByName[fork.FullName] = extras[i].DepthFromRoot
	}
	for name, want := range map[string]int{
		"alice/level1-fork": 1,
		"bob/level2-fork":   2,
		"carol/level3-fork": 3,
	} {
		if got := depthByName[name]; got != want {
			t.Errorf("%s depth = %d, want %d", name, got, want)
		}
	}
}

func loadGQLFixture(t *testing.T, path string) gqlResponse {
	t.Helper()
	var envelope struct {
		Data gqlResponse `json:"data"`
	}
	if err := loadFixtureJSON(path, &envelope); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	return envelope.Data
}

func TestFetchForksBounded_DecodesAliasedBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/graphql") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"r0":{"forkCount":1,"forks":{"totalCount":1,"nodes":[{"databaseId":4001,"nameWithOwner":"alice/level1-fork","name":"level1-fork","forkCount":0,"parent":{"nameWithOwner":"octo/root-repo","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"}]}},"rateLimit":{"limit":5000,"remaining":4999,"used":1,"cost":1}}}`))
	}))
	defer srv.Close()

	forks, extras, report, err := newTestClientGQL(t, srv).FetchForksBounded(
		context.Background(), "octo", "root-repo", nil, BoundedOptions{MaxDepth: 3, MaxNodes: 50, MaxPages: 20},
	)
	if err != nil {
		t.Fatalf("FetchForksBounded: %v", err)
	}
	if len(forks) != 1 || forks[0].FullName != "alice/level1-fork" {
		t.Fatalf("forks=%v, want alice/level1-fork", forks)
	}
	if extras[forks[0].ID].ParentFullPath != "octo/root-repo" {
		t.Fatalf("parent=%q", extras[forks[0].ID].ParentFullPath)
	}
	if report == nil || report.Scope != "all" || report.UniqueRows != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestFetchForksBounded_ReportsCompleteCoverage(t *testing.T) {
	tests := []struct {
		name           string
		opts           BoundedOptions
		failAfterRoot  bool
		wantForks      int
		wantVisited    int
		wantUnresolved int
		wantCap        string
		wantErr        bool
	}{
		{"max nodes", BoundedOptions{MaxNodes: 1, MaxDepth: 3, MaxPages: 20}, false, 1, 1, 2, "max_nodes", false},
		{"max pages", BoundedOptions{MaxNodes: 50, MaxDepth: 3, MaxPages: 1}, false, 2, 2, 1, "max_pages", false},
		{"max depth", BoundedOptions{MaxNodes: 50, MaxDepth: 1, MaxPages: 20}, false, 2, 2, 1, "max_depth", false},
		{"query failure", BoundedOptions{MaxNodes: 50, MaxDepth: 3, MaxPages: 20}, true, 2, 2, 1, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls > 1 && tt.failAfterRoot {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				if calls == 1 {
					_, _ = w.Write([]byte(`{"data":{"r0":{"forkCount":3,"forks":{"totalCount":2,"nodes":[{"databaseId":4001,"nameWithOwner":"alice/level1-fork","name":"level1-fork","forkCount":1,"parent":{"nameWithOwner":"octo/root-repo","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"},{"databaseId":4002,"nameWithOwner":"bob/level1-fork","name":"level1-fork","forkCount":0,"parent":{"nameWithOwner":"octo/root-repo","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"}]}}},"rateLimit":{"limit":5000,"remaining":4999,"used":1,"cost":1}}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":{"r0":{"forkCount":1,"forks":{"totalCount":0,"nodes":[]}}},"rateLimit":{"limit":5000,"remaining":4999,"used":1,"cost":1}}`))
			}))
			defer srv.Close()
			forks, _, report, err := newTestClientGQL(t, srv).FetchForksBounded(context.Background(), "octo", "root-repo", nil, tt.opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if len(forks) != tt.wantForks {
				t.Errorf("forks = %d, want %d", len(forks), tt.wantForks)
			}
			if report == nil {
				t.Fatal("report is nil")
			}
			if report.VisitedNodes != tt.wantVisited {
				t.Errorf("VisitedNodes = %d, want %d", report.VisitedNodes, tt.wantVisited)
			}
			if report.Unresolved != tt.wantUnresolved {
				t.Errorf("Unresolved = %d, want %d", report.Unresolved, tt.wantUnresolved)
			}
			if report.CapReason != tt.wantCap {
				t.Errorf("CapReason = %q, want %q", report.CapReason, tt.wantCap)
			}
			if tt.wantErr && report.Error != "graphql_failed" {
				t.Errorf("Error = %q, want graphql_failed", report.Error)
			}
		})
	}
}

func TestFetchForksBounded_ContinuesQueuedBranchesAfterMaxDepth(t *testing.T) {
	// Regression for the P1 review: setting cap = CapReasonMaxDepth inside
	// the fork-processing loop terminates the outer loop prematurely.
	// The fix records depthCap without breaking the queue drain.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/graphql") {
			http.NotFound(w, r)
			return
		}
		// Root has two depth-1 forks; alice/alpha has forkCount=1 so its
		// (would-be-depth-2) children would hit the boundary. bob/beta has
		// forkCount=0 and is a leaf. Both must be visited.
		_, _ = w.Write([]byte(`{"data":{"r0":{"forkCount":3,"forks":{"totalCount":2,"nodes":[` +
			`{"databaseId":4001,"nameWithOwner":"alice/alpha","name":"alpha","forkCount":1,"parent":{"nameWithOwner":"octo/root","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"},` +
			`{"databaseId":4002,"nameWithOwner":"bob/beta","name":"beta","forkCount":0,"parent":{"nameWithOwner":"octo/root","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"}` +
			`]}},"rateLimit":{"limit":5000,"remaining":4999,"used":1,"cost":1}}}`))
	}))
	defer srv.Close()
	forks, _, report, err := newTestClientGQL(t, srv).FetchForksBounded(
		context.Background(), "octo", "root", nil,
		BoundedOptions{MaxDepth: 1, MaxNodes: 50, MaxPages: 20},
	)
	if err != nil {
		t.Fatalf("FetchForksBounded: %v", err)
	}
	if len(forks) != 2 {
		t.Errorf("forks = %d, want 2 (alpha + beta)", len(forks))
	}
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.CapReason != "max_depth" {
		t.Errorf("CapReason = %q, want max_depth", report.CapReason)
	}
}


func TestFetchForksBounded_BoundedContextCancelsMidCall(t *testing.T) {
	// Regression for the P2 review: the elapsed limit was checked only
	// between batches; a slow request (with retries) could blow past
	// MaxElapsed. The fix derives a per-call deadline from MaxElapsed so
	// in-flight work is bounded too. This test sets MaxElapsed to 50ms
	// and makes the server slow (300ms); the deadline cancel should fire
	// inside doGraphQLWithRetry before the full HTTP timeout.
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":{"r0":{"forkCount":1,"forks":{"totalCount":1,"nodes":[{"databaseId":4001,"nameWithOwner":"a/b","name":"b","forkCount":0,"parent":{"nameWithOwner":"octo/r","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"}]}}},"rateLimit":{"limit":5000,"remaining":4999,"used":1,"cost":1}}`))
	}))
	defer srv.Close()
	forks, _, report, err := newTestClientGQL(t, srv).FetchForksBounded(
		context.Background(), "octo", "r", nil,
		BoundedOptions{MaxDepth: 3, MaxNodes: 50, MaxPages: 20, MaxElapsed: 50 * time.Millisecond},
	)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected deadline error, got nil (forks=%d)", len(forks))
	}
	if elapsed > 2*time.Second {
		t.Errorf("elapsed = %v, want < 2s", elapsed)
	}
	// The bounded context fires when MaxElapsed elapses mid-request. That is
	// a user-configured time limit, not an upstream GraphQL outage, so the
	// report's CapReason should be "max_elapsed" and Error should be empty.
	// The error itself is still surfaced so callers know the run was cut short.
	if report == nil {
		t.Fatalf("report is nil")
	}
	if report.CapReason != "max_elapsed" {
		t.Errorf("report.CapReason = %q, want max_elapsed", report.CapReason)
	}
	if report.Error != "" {
		t.Errorf("report.Error = %q, want empty (deadline is a cap, not an error)", report.Error)
	}
}

func TestFetchForksBounded_PropagatesPartialForksOnError(t *testing.T) {
	// Regression for the P1 review (adapter side): when an in-flight
	// GraphQL batch fails after earlier batches succeeded, the partial
	// fork list must not be discarded.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// First batch: alpha has forkCount=1 (queues a second batch),
			// beta has forkCount=0 (leaf).
			_, _ = w.Write([]byte(`{"data":{"r0":{"forkCount":3,"forks":{"totalCount":2,"nodes":[{"databaseId":4001,"nameWithOwner":"alice/alpha","name":"alpha","forkCount":1,"parent":{"nameWithOwner":"octo/r","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"},{"databaseId":4002,"nameWithOwner":"bob/beta","name":"beta","forkCount":0,"parent":{"nameWithOwner":"octo/r","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"}]}}},"rateLimit":{"limit":5000,"remaining":4999,"used":1,"cost":1}}`))
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	forks, _, report, err := newTestClientGQL(t, srv).FetchForksBounded(
		context.Background(), "octo", "r", nil,
		BoundedOptions{MaxDepth: 3, MaxNodes: 50, MaxPages: 20},
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(forks) != 2 {
		t.Errorf("forks = %d, want 2 (partial inventory must not be discarded)", len(forks))
	}
	if report == nil || report.Error != "graphql_failed" {
		t.Errorf("report.Error = %q, want graphql_failed", report.Error)
	}
}

func TestFetchForksBounded_DirectTotalCountIsRootForksTotalCount(t *testing.T) {
	// Regression for the P2 review: DirectTotalCount was len(seen), the
	// whole-network discovered count, instead of the depth-zero alias's
	// forks.totalCount. Capture forks.totalCount from the depth-0 alias
	// and use it as DirectTotalCount for every fork's T1Extra.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"r0":{"forkCount":128,"forks":{"totalCount":118,"nodes":[{"databaseId":5001,"nameWithOwner":"a/b","name":"b","forkCount":0,"parent":{"nameWithOwner":"octo/r","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"}]}}},"rateLimit":{"limit":5000,"remaining":4999,"used":1,"cost":1}}`))
	}))
	defer srv.Close()
	forks, extras, _, err := newTestClientGQL(t, srv).FetchForksBounded(
		context.Background(), "octo", "r", nil,
		BoundedOptions{MaxDepth: 3, MaxNodes: 50, MaxPages: 20},
	)
	if err != nil {
		t.Fatalf("FetchForksBounded: %v", err)
	}
	if len(forks) != 1 {
		t.Fatalf("forks = %d, want 1", len(forks))
	}
	extra := extras[forks[0].ID]
	if extra.DirectTotalCount != 118 {
		t.Errorf("DirectTotalCount = %d, want 118 (root forks.totalCount)", extra.DirectTotalCount)
	}
	if extra.WholeNetworkForkCount != 128 {
		t.Errorf("WholeNetworkForkCount = %d, want 128 (root forkCount)", extra.WholeNetworkForkCount)
	}
}

func TestFetchForksBounded_PartialForksRetainExtrasOnError(t *testing.T) {
	// Cursor follow-up: when the bounded call fails mid-walk, the partial
	// fork list must come with a populated extras map so callers can
	// recover lineage (parent paths, DirectTotalCount, WholeNetworkForkCount).
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":{"r0":{"forkCount":128,"forks":{"totalCount":118,"nodes":[{"databaseId":5001,"nameWithOwner":"alice/alpha","name":"alpha","forkCount":1,"parent":{"nameWithOwner":"octo/r","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"}]}}},"rateLimit":{"limit":5000,"remaining":4999,"used":1,"cost":1}}`))
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	forks, extras, _, err := newTestClientGQL(t, srv).FetchForksBounded(
		context.Background(), "octo", "r", nil,
		BoundedOptions{MaxDepth: 3, MaxNodes: 50, MaxPages: 20},
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(forks) != 1 {
		t.Fatalf("forks = %d, want 1", len(forks))
	}
	extra, ok := extras[forks[0].ID]
	if !ok {
		t.Fatalf("extras missing entry for fork %d (got %d entries)", forks[0].ID, len(extras))
	}
	if extra.ParentFullPath != "octo/r" {
		t.Errorf("ParentFullPath = %q, want octo/r (lineage lost on partial failure)", extra.ParentFullPath)
	}
	if extra.DirectTotalCount != 118 {
		t.Errorf("DirectTotalCount = %d, want 118 (root forks.totalCount)", extra.DirectTotalCount)
	}
	if extra.WholeNetworkForkCount != 128 {
		t.Errorf("WholeNetworkForkCount = %d, want 128 (root forkCount)", extra.WholeNetworkForkCount)
	}
}

func TestFetchForksBounded_DeadlineExpiryReportsMaxElapsed(t *testing.T) {
	// Cursor follow-up: when the bounded context fires because MaxElapsed
	// elapses mid-request, the report's CapReason must be "max_elapsed" and
	// Error must be empty. A user-configured time limit is a cap, not an
	// upstream GraphQL outage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":{"r0":{"forkCount":1,"forks":{"totalCount":1,"nodes":[{"databaseId":5001,"nameWithOwner":"a/b","name":"b","forkCount":0,"parent":{"nameWithOwner":"octo/r","databaseId":4000},"pushedAt":"2025-08-01T00:00:00Z"}]}}},"rateLimit":{"limit":5000,"remaining":4999,"used":1,"cost":1}}`))
	}))
	defer srv.Close()
	_, _, report, err := newTestClientGQL(t, srv).FetchForksBounded(
		context.Background(), "octo", "r", nil,
		BoundedOptions{MaxDepth: 3, MaxNodes: 50, MaxPages: 20, MaxElapsed: 50 * time.Millisecond},
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.CapReason != "max_elapsed" {
		t.Errorf("CapReason = %q, want max_elapsed", report.CapReason)
	}
	if report.Error != "" {
		t.Errorf("Error = %q, want empty (deadline is a cap, not an error)", report.Error)
	}
}

// A response body cut off mid-JSON is retried, not treated as fatal: it
// aborted a 65-minute GraphQL fork walk on llama.cpp (page 371).
func TestIsTransientServerError_TruncatedBody(t *testing.T) {
	for _, err := range []error{
		io.ErrUnexpectedEOF,
		io.EOF,
		fmt.Errorf("GraphQL query: %w", io.ErrUnexpectedEOF),
		fmt.Errorf("GraphQL query: %w", io.EOF),
		errors.New("unexpected end of JSON input"),
	} {
		if !isTransientServerError(err) {
			t.Errorf("isTransientServerError(%v) = false, want true", err)
		}
	}
}
