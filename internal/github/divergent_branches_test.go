package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// graphQLStub answers the two phases of FetchDivergentBranchCounts, recording
// the query documents so the test can assert on how the work was batched.
func graphQLStub(t *testing.T, phaseA, phaseB string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var docs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)

		mu.Lock()
		docs = append(docs, req.Query)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "refPrefix") {
			_, _ = w.Write([]byte(phaseA))
			return
		}
		_, _ = w.Write([]byte(phaseB))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), docs...)
	}
}

const rl = `"rateLimit":{"limit":5000,"remaining":4999,"used":1,"resetAt":"2030-01-01T00:00:00Z","cost":1}`

func TestFetchDivergentBranchCounts_CountsOnlyAheadBranches(t *testing.T) {
	phaseA := `{"data":{
		"f0":{"refs":{"totalCount":3,"nodes":[{"name":"master"},{"name":"newbranch"},{"name":"stale"}]}},
		"f1":{"refs":{"totalCount":1,"nodes":[{"name":"main"}]}},
		` + rl + `}}`
	// f0: master ahead 0, newbranch ahead 1, stale ahead 0  -> 1
	// f1: main ahead 6                                      -> 1
	phaseB := `{"data":{"repository":{"ref":{
		"c0":{"aheadBy":0},
		"c1":{"aheadBy":1},
		"c2":{"aheadBy":0},
		"c3":{"aheadBy":6}
	}},` + rl + `}}`

	srv, docs := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "dustinrue", "proxmox-packer", "main",
		[]ForkTarget{
			{ID: "rsanchez-s/proxmox-packer", Owner: "rsanchez-s", Name: "proxmox-packer"},
			{ID: "gregums587/proxmox-packer", Owner: "gregums587", Name: "proxmox-packer"},
		})
	if err != nil {
		t.Fatalf("FetchDivergentBranchCounts: %v", err)
	}

	if n := got.Divergent["rsanchez-s/proxmox-packer"]; n != 1 {
		t.Errorf("rsanchez-s divergent = %d, want 1", n)
	}
	if n := got.Divergent["gregums587/proxmox-packer"]; n != 1 {
		t.Errorf("gregums587 divergent = %d, want 1", n)
	}

	// The whole sweep must be two calls, not one per fork or per branch.
	if n := len(docs()); n != 2 {
		t.Errorf("issued %d GraphQL queries, want 2 (one per phase)", n)
	}
}

// A fork whose branches all match upstream must read as a real 0, while a fork
// that could not be resolved at all must be absent — the caller renders those
// differently ("0" vs "-").
func TestFetchDivergentBranchCounts_ZeroIsNotUnknown(t *testing.T) {
	phaseA := `{"data":{
		"f0":{"refs":{"totalCount":1,"nodes":[{"name":"main"}]}},
		"f1":null,
		` + rl + `}}`
	phaseB := `{"data":{"repository":{"ref":{"c0":{"aheadBy":0}}},` + rl + `}}`

	srv, _ := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "main",
		[]ForkTarget{
			{ID: "alive/repo", Owner: "alive", Name: "repo"},
			{ID: "deleted/repo", Owner: "deleted", Name: "repo"},
		})
	if err != nil {
		t.Fatalf("FetchDivergentBranchCounts: %v", err)
	}

	if n, ok := got.Divergent["alive/repo"]; !ok || n != 0 {
		t.Errorf("alive/repo = (%d, %v), want (0, true) — checked and not divergent", n, ok)
	}
	if _, ok := got.Divergent["deleted/repo"]; ok {
		t.Error("deleted/repo present in results; an unresolvable fork must stay unknown")
	}
}

// A per-alias NOT_FOUND rides along with usable data on an HTTP 200. Losing the
// whole batch over one deleted branch would be a severe overreaction.
func TestFetchDivergentBranchCounts_ToleratesPartialNotFound(t *testing.T) {
	phaseA := `{"data":{"f0":{"refs":{"totalCount":2,"nodes":[{"name":"main"},{"name":"gone"}]}},` + rl + `},
		"errors":[]}`
	phaseB := `{"data":{"repository":{"ref":{"c0":{"aheadBy":3},"c1":null}},` + rl + `},
		"errors":[{"type":"NOT_FOUND","path":["repository","ref","c1"],"message":"Could not resolve head ref"}]}`

	srv, _ := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "main",
		[]ForkTarget{{ID: "o/repo", Owner: "o", Name: "repo"}})
	if err != nil {
		t.Fatalf("a partial NOT_FOUND must not fail the sweep: %v", err)
	}
	if n := got.Divergent["o/repo"]; n != 1 {
		t.Errorf("divergent = %d, want 1 (the surviving ahead branch)", n)
	}
}

// Any error class other than NOT_FOUND means the query itself is suspect, so
// the batch must not be silently reported as a set of zeros.
func TestFetchDivergentBranchCounts_RejectsNonLookupErrors(t *testing.T) {
	phaseA := `{"data":{"f0":null,` + rl + `},
		"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`

	srv, _ := graphQLStub(t, phaseA, "")
	c := newTestClientGQL(t, srv)

	_, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "main",
		[]ForkTarget{{ID: "o/repo", Owner: "o", Name: "repo"}})
	if err == nil {
		t.Fatal("a RATE_LIMITED error was swallowed; the sweep must fail loudly")
	}
}

// An unresolved baseline would compare against "repos///..." — the same defect
// that produced the all-zeros fork list.
func TestFetchDivergentBranchCounts_RefusesUnresolvedBaseline(t *testing.T) {
	srv, _ := graphQLStub(t, "", "")
	c := newTestClientGQL(t, srv)

	if _, err := c.FetchDivergentBranchCounts(context.Background(), "", "", "main",
		[]ForkTarget{{ID: "o/r", Owner: "o", Name: "r"}}); err == nil {
		t.Fatal("expected an error when the upstream baseline is unset")
	}
}

// Branch names and logins are interpolated into the query document, not bound
// as variables, so they must be escaped.
func TestGqlString_EscapesLiterals(t *testing.T) {
	cases := map[string]string{
		`main`:       `"main"`,
		`fix"quote`:  `"fix\"quote"`,
		`back\slash`: `"back\\slash"`,
		"tab\there":  `"tab\there"`,
		"new\nline":  `"new\nline"`,
	}
	for in, want := range cases {
		if got := gqlString(in); got != want {
			t.Errorf("gqlString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSlidingChunks(t *testing.T) {
	got := slidingChunks(7, 3)
	want := []chunkRange{{0, 3}, {3, 6}, {6, 7}}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d = %v, want %v", i, got[i], want[i])
		}
	}
	if slidingChunks(0, 10) != nil || slidingChunks(10, 0) != nil {
		t.Error("degenerate inputs must yield no chunks")
	}
}
