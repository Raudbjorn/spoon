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
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// batchStub answers FetchBatchDivergence's two phases, routing on the
// "commits(last:" substring that only Phase B's nested tip+PR selection set
// carries (both phases otherwise share the same repository{ref{compare...}}
// document shape, unlike divergent_branches.go's phase A/B which differ at
// the top level).
func batchStub(t *testing.T, phaseA, phaseB string) (*httptest.Server, func() []string) {
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
		if strings.Contains(req.Query, "commits(last:") {
			_, _ = w.Write([]byte(phaseB))
			return
		}
		_, _ = w.Write([]byte(phaseA))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), docs...)
	}
}

// A fork with no divergence anywhere must not trigger Phase B at all, and its
// tip must come from the listing rather than a compare that never ran.
func TestFetchBatchDivergence_ZeroAheadSkipsPhaseB(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":{
		"c0":{"aheadBy":0,"behindBy":2}
	}},` + rl + `}}`

	srv, docs := batchStub(t, phaseA, "")
	c := newTestClientGQL(t, srv)

	seedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, stats, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main",
		[]BatchTarget{{
			ID: "alive/repo", Owner: "alive", Name: "repo",
			DefaultBranch: "main", DefaultTipSHA: "abc123", DefaultCommittedAt: seedTime,
		}})
	if err != nil {
		t.Fatalf("FetchBatchDivergence: %v", err)
	}

	fd, ok := got["alive/repo"]
	if !ok || !fd.Resolved {
		t.Fatalf("alive/repo = (%+v, %v), want resolved", fd, ok)
	}
	if fd.Default.AheadBy != 0 || fd.Default.BehindBy != 2 {
		t.Errorf("Default = %+v, want AheadBy=0 BehindBy=2", fd.Default)
	}
	if fd.Default.TipSHA != "abc123" || !fd.Default.TipCommittedAt.Equal(seedTime) {
		t.Errorf("Default tip = (%q, %v), want listing seed (\"abc123\", %v)", fd.Default.TipSHA, fd.Default.TipCommittedAt, seedTime)
	}
	if fd.Default.UpstreamedPR != 0 {
		t.Errorf("Default.UpstreamedPR = %d, want 0 (no Phase B ran)", fd.Default.UpstreamedPR)
	}

	all := docs()
	if len(all) != 1 {
		t.Errorf("issued %d queries, want 1 (zero-ahead: no Phase B)", len(all))
	}
	for _, d := range all {
		if strings.Contains(d, "commits(last:") {
			t.Error("a Phase B document was sent despite no ahead branch")
		}
	}
	if stats.Queries != 1 {
		t.Errorf("stats.Queries = %d, want 1", stats.Queries)
	}
}

// When the default branch shows no divergence but a side branch does, the
// side branch must carry the Phase B tip and SelectDivergentBranch must pick
// it over the quiet default.
func TestFetchBatchDivergence_SideBranchWinner(t *testing.T) {
	// c0 = default (main, ahead 0), c1 = side (feature, ahead 4).
	phaseA := `{"data":{"repository":{"ref":{
		"c0":{"aheadBy":0,"behindBy":0},
		"c1":{"aheadBy":4,"behindBy":1}
	}},` + rl + `}}`
	// Phase B only queries the ahead branch; it gets a fresh c0 alias.
	phaseB := `{"data":{"repository":{"ref":{
		"c0":{"commits":{"nodes":[{"oid":"sidetip123","committedDate":"2026-02-02T00:00:00Z","associatedPullRequests":{"nodes":[]}}]}}
	}},` + rl + `}}`

	srv, docs := batchStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	seedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, _, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main",
		[]BatchTarget{{
			ID: "o/repo", Owner: "o", Name: "repo",
			DefaultBranch: "main", DefaultTipSHA: "mainSHA", DefaultCommittedAt: seedTime,
			Sides: []BatchBranch{{Name: "feature", TipSHA: "featureSeed", CommittedAt: seedTime}},
		}})
	if err != nil {
		t.Fatalf("FetchBatchDivergence: %v", err)
	}

	fd := got["o/repo"]
	if !fd.Resolved {
		t.Fatal("o/repo not resolved")
	}
	if len(fd.Sides) != 1 {
		t.Fatalf("Sides = %+v, want 1 entry", fd.Sides)
	}
	side := fd.Sides[0]
	if side.Name != "feature" || side.AheadBy != 4 || side.BehindBy != 1 {
		t.Errorf("side = %+v, want feature/4/1", side)
	}
	if side.TipSHA != "sidetip123" {
		t.Errorf("side.TipSHA = %q, want Phase B tip \"sidetip123\"", side.TipSHA)
	}

	sel := forge.SelectDivergentBranch(fd)
	if sel.Branch != "feature" || !sel.IsSide || sel.Ahead != 4 {
		t.Errorf("SelectDivergentBranch = %+v, want feature/side/ahead=4", sel)
	}

	all := docs()
	if len(all) != 2 {
		t.Errorf("issued %d queries, want 2 (phase A + phase B for the one ahead branch)", len(all))
	}
}

// A tip commit heading a merged PR into the upstream repo marks the branch
// upstreamed; an unrelated merged PR (wrong base repo) must not.
func TestFetchBatchDivergence_MergedPRMarksUpstreamed(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":{
		"c0":{"aheadBy":3,"behindBy":0}
	}},` + rl + `}}`
	phaseB := `{"data":{"repository":{"ref":{
		"c0":{"commits":{"nodes":[{"oid":"tipSHA","committedDate":"2026-03-03T00:00:00Z","associatedPullRequests":{"nodes":[
			{"number":10,"merged":true,"baseRepository":{"nameWithOwner":"other/repo"}},
			{"number":42,"merged":true,"baseRepository":{"nameWithOwner":"UP/STREAM"}}
		]}}]}}
	}},` + rl + `}}`

	srv, _ := batchStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, _, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main",
		[]BatchTarget{{ID: "o/repo", Owner: "o", Name: "repo", DefaultBranch: "main"}})
	if err != nil {
		t.Fatalf("FetchBatchDivergence: %v", err)
	}

	fd := got["o/repo"]
	if fd.Default.UpstreamedPR != 42 {
		t.Errorf("Default.UpstreamedPR = %d, want 42 (only the upstream-based merged PR counts, case-insensitively)", fd.Default.UpstreamedPR)
	}

	sel := forge.SelectDivergentBranch(fd)
	if !sel.Upstreamed || sel.UpstreamedPR != 42 || !sel.NeedsREST {
		t.Errorf("SelectDivergentBranch = %+v, want Upstreamed=true UpstreamedPR=42 NeedsREST=true", sel)
	}
}

// A fork whose every Phase A alias comes back null must be reported
// Resolved=false, distinct from a fork that resolved to real zero divergence.
func TestFetchBatchDivergence_PartialNotFoundLeavesForkUnresolved(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":{
		"c0":{"aheadBy":0,"behindBy":0},
		"c1":null
	}},` + rl + `},
		"errors":[{"type":"NOT_FOUND","path":["repository","ref","c1"],"message":"Could not resolve head ref"}]}`

	srv, docs := batchStub(t, phaseA, "")
	c := newTestClientGQL(t, srv)

	got, _, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main",
		[]BatchTarget{
			{ID: "alive/repo", Owner: "alive", Name: "repo", DefaultBranch: "main"},
			{ID: "deleted/repo", Owner: "deleted", Name: "repo", DefaultBranch: "main"},
		})
	if err != nil {
		t.Fatalf("a partial NOT_FOUND must not fail the batch: %v", err)
	}

	if fd := got["alive/repo"]; !fd.Resolved {
		t.Errorf("alive/repo = %+v, want Resolved=true", fd)
	}
	fd, ok := got["deleted/repo"]
	if !ok {
		t.Fatal("deleted/repo missing from result map, want a present Resolved=false entry")
	}
	if fd.Resolved {
		t.Errorf("deleted/repo = %+v, want Resolved=false", fd)
	}

	// Every branch was ahead-0 or unresolved, so Phase B must not have run.
	for _, d := range docs() {
		if strings.Contains(d, "commits(last:") {
			t.Error("a Phase B document was sent despite no ahead branch")
		}
	}
}

// An upstream ref that fails to resolve at all (as opposed to one individual
// alias) must fail the whole call loudly, not silently report zeros.
func TestFetchBatchDivergence_UnresolvedUpstreamRefErrors(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":null},` + rl + `}}`

	srv, _ := batchStub(t, phaseA, "")
	c := newTestClientGQL(t, srv)

	_, _, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main",
		[]BatchTarget{{ID: "o/repo", Owner: "o", Name: "repo", DefaultBranch: "main"}})
	if err == nil {
		t.Fatal("expected an error when the upstream ref does not resolve")
	}
}

// A Phase B query that fails outright (retries exhausted on a persistent
// 5xx) must not fail the whole call or discard Phase A's ahead/behind data:
// the branch falls back to its listing tip with UpstreamedPR left at 0,
// mirroring tipUpstreamed's fail-open behavior on the REST path.
func TestFetchBatchDivergence_PhaseBFailureFallsBackToListingTip(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":{
		"c0":{"aheadBy":5,"behindBy":1}
	}},` + rl + `}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		if strings.Contains(req.Query, "commits(last:") {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(phaseA))
	}))
	t.Cleanup(srv.Close)
	c := newTestClientGQL(t, srv)

	seedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, stats, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main",
		[]BatchTarget{{
			ID: "o/repo", Owner: "o", Name: "repo",
			DefaultBranch: "main", DefaultTipSHA: "listingSHA", DefaultCommittedAt: seedTime,
		}})
	if err != nil {
		t.Fatalf("a Phase B failure must not fail the whole batch: %v", err)
	}

	fd := got["o/repo"]
	if !fd.Resolved {
		t.Fatal("o/repo not resolved (Phase A succeeded and must stand)")
	}
	if fd.Default.AheadBy != 5 || fd.Default.BehindBy != 1 {
		t.Errorf("Default = %+v, want Phase A's AheadBy=5 BehindBy=1 preserved", fd.Default)
	}
	if fd.Default.TipSHA != "listingSHA" || !fd.Default.TipCommittedAt.Equal(seedTime) {
		t.Errorf("Default tip = (%q, %v), want listing fallback (\"listingSHA\", %v)", fd.Default.TipSHA, fd.Default.TipCommittedAt, seedTime)
	}
	if fd.Default.UpstreamedPR != 0 {
		t.Errorf("Default.UpstreamedPR = %d, want 0 (Phase B never answered)", fd.Default.UpstreamedPR)
	}
	if stats.Queries != 2 {
		t.Errorf("stats.Queries = %d, want 2 (phase A + the failed phase B document)", stats.Queries)
	}
}
