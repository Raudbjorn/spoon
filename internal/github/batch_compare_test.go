package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
	"github.com/svnbjrn/spoon/internal/forge"
)

// isBatchServerFailure decides whether adaptive halving applies at all, so
// its three-way split (server failure vs. tolerated partial vs. fatal
// GraphQL error vs. caller cancellation) gets direct, table-driven coverage
// here rather than only the indirect coverage the integration tests below
// give it.
func TestIsBatchServerFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed 502", &ghAPI.HTTPError{StatusCode: 502}, true},
		{"typed 503", &ghAPI.HTTPError{StatusCode: 503}, true},
		{"typed 504", &ghAPI.HTTPError{StatusCode: 504}, true},
		{"typed 500 is not this class", &ghAPI.HTTPError{StatusCode: 500}, false},
		{"typed 404 is not this class", &ghAPI.HTTPError{StatusCode: 404}, false},
		{"context canceled", context.Canceled, false},
		{"context deadline exceeded", context.DeadlineExceeded, false},
		{"wrapped context canceled", fmt.Errorf("request: %w", context.Canceled), false},
		{"NOT_FOUND graphql error is not this class", &ghAPI.GraphQLError{Errors: []ghAPI.GraphQLErrorItem{{Type: "NOT_FOUND"}}}, false},
		{"RATE_LIMITED graphql error is not this class", &ghAPI.GraphQLError{Errors: []ghAPI.GraphQLErrorItem{{Type: "RATE_LIMITED"}}}, false},
		{"unclassified transport/decode error", errors.New("unexpected end of JSON input"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBatchServerFailure(tt.err); got != tt.want {
				t.Errorf("isBatchServerFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

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

// A GraphQL-level error that is not NOT_FOUND-only, and not a server-side
// failure either, must still fail the whole call: adaptive halving cannot
// fix a rate limit, so this class of error keeps the pre-halving behavior
// of aborting the sweep rather than silently under-reporting it.
func TestFetchBatchDivergence_RejectsNonLookupGraphQLError(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":null}},
		"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`

	srv, _ := batchStub(t, phaseA, "")
	c := newTestClientGQL(t, srv)

	_, _, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main",
		[]BatchTarget{{ID: "o/repo", Owner: "o", Name: "repo", DefaultBranch: "main"}})
	if err == nil {
		t.Fatal("a RATE_LIMITED error was swallowed; the sweep must fail loudly")
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

// phaseAOKBody builds a successful Phase A response with n aliases, each
// resolving to aheadBy=0 behindBy=0 -- the shape aliasesOverThreshold's stub
// serves for a document it accepts.
func phaseAOKBody(n int) string {
	var sb strings.Builder
	sb.WriteString(`{"data":{"repository":{"ref":{`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"c%d":{"aheadBy":0,"behindBy":0}`, i)
	}
	sb.WriteString(`}},` + rl + `}}`)
	return sb.String()
}

// aliasCount counts compare(headRef: aliases in a query document, used by
// the stubs below to decide how many branches a given request is asking
// about without depending on chunk boundaries.
func aliasCount(query string) int {
	return strings.Count(query, "compare(headRef:")
}

// An oversized Phase A document (GitHub's "malformed/empty response" or
// HTTP 502 failure mode, observed independent of GraphQL's own reported
// query cost -- see the batchCompareSize doc comment) must not fail the
// whole call: adaptive halving retries it as smaller documents until they
// fit, and every branch ends up resolved.
func TestFetchBatchDivergence_PhaseAHalvingRecoversFromOversizedDocument(t *testing.T) {
	const threshold = 30 // aliases; a document larger than this 502s

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
		n := aliasCount(req.Query)
		if n > threshold {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(phaseAOKBody(n)))
	}))
	t.Cleanup(srv.Close)
	c := newTestClientGQL(t, srv)

	const numTargets = 35 // one top-level chunk (batchCompareSize=50), over threshold
	targets := make([]BatchTarget, numTargets)
	for i := range targets {
		targets[i] = BatchTarget{
			ID: fmt.Sprintf("o/repo%d", i), Owner: "o", Name: fmt.Sprintf("repo%d", i),
			DefaultBranch: "main",
		}
	}

	got, stats, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main", targets)
	if err != nil {
		t.Fatalf("adaptive halving must recover from an oversized-document server failure: %v", err)
	}
	for _, tg := range targets {
		fd, ok := got[tg.ID]
		if !ok || !fd.Resolved {
			t.Errorf("%s = (%+v, %v), want Resolved=true after halving", tg.ID, fd, ok)
		}
	}
	// stats.Queries counts one per document actually sent (a retried
	// document is still one document, physically resent up to
	// gqlMaxAttempts times) -- assert that directly against the set of
	// distinct query strings the stub actually received, rather than a
	// hand-derived number, so the logical-vs-physical distinction is
	// explicit: 35 fails once (over threshold, retried 3x physically but
	// one document), then splits into 17+18, both under threshold and
	// succeed on the first try -- 3 distinct documents, 5 physical requests.
	mu.Lock()
	unique := make(map[string]struct{}, len(docs))
	for _, d := range docs {
		unique[d] = struct{}{}
	}
	physical := len(docs)
	mu.Unlock()
	if stats.Queries != len(unique) {
		t.Errorf("stats.Queries = %d, want %d (the number of distinct documents sent)", stats.Queries, len(unique))
	}
	if physical <= len(unique) {
		t.Errorf("physical requests = %d, want more than %d distinct documents (the oversized one should have been retried)", physical, len(unique))
	}
}

// A Phase A document that fails no matter how small it gets (a persistent
// 5xx, not merely an oversized one) must still not fail the whole call:
// adaptive halving gives up at batchMinChunk and leaves those branches
// unresolved, and the number of documents sent stays bounded rather than
// recursing forever.
func TestFetchBatchDivergence_PhaseAPersistentFailureDropsWithoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	c := newTestClientGQL(t, srv)

	// 11 is the smallest size that still forces exactly one split (over
	// batchMinChunk=10); both halves (5 and 6) then fail at or below the
	// floor and are dropped without splitting further.
	const numTargets = 11
	targets := make([]BatchTarget, numTargets)
	for i := range targets {
		targets[i] = BatchTarget{
			ID: fmt.Sprintf("o/repo%d", i), Owner: "o", Name: fmt.Sprintf("repo%d", i),
			DefaultBranch: "main",
		}
	}

	got, stats, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main", targets)
	if err != nil {
		t.Fatalf("a persistent server failure must not fail the whole batch: %v", err)
	}
	for _, tg := range targets {
		if fd := got[tg.ID]; fd.Resolved {
			t.Errorf("%s Resolved = true, want false (every Phase A document failed)", tg.ID)
		}
	}
	// 11 fails, splits into 5+6, both fail at/below the floor and are
	// dropped without further splitting: exactly 3 documents, not an
	// unbounded recursion.
	if stats.Queries != 3 {
		t.Errorf("stats.Queries = %d, want 3 (bounded: no infinite halving)", stats.Queries)
	}
}

// Halving splits one target's attempts (its default branch plus its side
// branches) across chunks independently of which target they belong to, so
// a single fork's default branch can be dropped at the floor while a
// sibling side branch of the *same* fork resolves cleanly in a different
// chunk. The fork must still come back Resolved=false as a whole -- a
// resolved side must not stand in for a clean answer about the dropped
// default, which would otherwise synthesize a fabricated "0 ahead" default
// and skip the REST fallback for a fork that was never actually checked.
func TestFetchBatchDivergence_PartialDropWithinTargetForcesUnresolved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		// Any document asking about the default branch ("o:main") 502s;
		// documents asking only about side branches succeed. This forces
		// the default branch's chunk (and only that chunk) to fail all the
		// way down to the floor.
		if strings.Contains(req.Query, `"o:main"`) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(phaseAOKBody(aliasCount(req.Query))))
	}))
	t.Cleanup(srv.Close)
	c := newTestClientGQL(t, srv)

	// One target: default branch + 10 side branches = 11 attempts, forcing
	// exactly one split (over batchMinChunk=10). The split puts the default
	// branch in the first half (size 5, which includes it) and the last 6
	// side branches in the second half (no default) -- see batchAttempt
	// construction order in FetchBatchDivergence (default first, then
	// sides in order).
	target := BatchTarget{ID: "o/repo", Owner: "o", Name: "repo", DefaultBranch: "main", DefaultTipSHA: "mainSeed"}
	for i := 0; i < 10; i++ {
		target.Sides = append(target.Sides, BatchBranch{Name: fmt.Sprintf("side%d", i), TipSHA: fmt.Sprintf("sideSeed%d", i)})
	}

	got, stats, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main", []BatchTarget{target})
	if err != nil {
		t.Fatalf("a partial drop within one target must not fail the whole batch: %v", err)
	}
	fd, ok := got["o/repo"]
	if !ok {
		t.Fatal("o/repo missing from result map, want a present Resolved=false entry")
	}
	if fd.Resolved {
		t.Errorf("o/repo = %+v, want Resolved=false (its default branch was never actually answered)", fd)
	}
	if fd.Default.Name != "" || fd.Default.AheadBy != 0 || len(fd.Sides) != 0 {
		t.Errorf("o/repo = %+v, want zero-value Default/Sides (must not synthesize a fabricated answer from the resolved sides)", fd)
	}
	// The size-11 attempt sees "o:main" -> 502; it splits into 5 (still
	// containing "o:main") + 6 (no default, side branches only). The size-5
	// half also sees "o:main" -> 502, and since 5 <= batchMinChunk it is
	// dropped without splitting further. The size-6 half has no default
	// branch alias, so it succeeds immediately. 3 documents total.
	if stats.Queries != 3 {
		t.Errorf("stats.Queries = %d, want 3 (size-11 fail, size-5 fail-and-drop, size-6 succeed)", stats.Queries)
	}
}

// The Phase B analogue: a chunk that fails no matter how small it gets
// (here, via a response body that never decodes -- GitHub's other observed
// oversized-document failure mode, and explicitly the same isBatchServerFailure
// class as an HTTP 5xx) halves down to the floor and is dropped the same
// way Phase A drops one, leaving those branches on their listing-seeded tip
// with UpstreamedPR left at 0 rather than failing the call.
func TestFetchBatchDivergence_PhaseBHalvingDropsAtFloorWithoutError(t *testing.T) {
	const numBranches = 11 // smallest size forcing exactly one split, as above

	phaseA := phaseAAllAheadBody(numBranches)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "commits(last:") {
			// A body that never decodes as JSON -- go-gh surfaces this as a
			// plain decode error, not retried by doGraphQLWithRetry (it
			// isn't an HTTP 5xx), which is exactly what keeps this test fast
			// despite exercising the same isBatchServerFailure classification.
			_, _ = w.Write([]byte("{not valid json"))
			return
		}
		_, _ = w.Write([]byte(phaseA))
	}))
	t.Cleanup(srv.Close)
	c := newTestClientGQL(t, srv)

	seedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	targets := make([]BatchTarget, numBranches)
	for i := range targets {
		targets[i] = BatchTarget{
			ID: fmt.Sprintf("o/repo%d", i), Owner: "o", Name: fmt.Sprintf("repo%d", i),
			DefaultBranch: "main", DefaultTipSHA: fmt.Sprintf("seed%d", i), DefaultCommittedAt: seedTime,
		}
	}

	got, stats, err := c.FetchBatchDivergence(context.Background(), "up", "stream", "main", targets)
	if err != nil {
		t.Fatalf("a persistent Phase B failure must not fail the whole batch: %v", err)
	}
	for _, tg := range targets {
		fd := got[tg.ID]
		if !fd.Resolved || fd.Default.AheadBy != 3 {
			t.Fatalf("%s = %+v, want Resolved=true AheadBy=3 (Phase A succeeded)", tg.ID, fd)
		}
		if fd.Default.TipSHA != tg.DefaultTipSHA || !fd.Default.TipCommittedAt.Equal(seedTime) {
			t.Errorf("%s tip = (%q, %v), want listing fallback (%q, %v)", tg.ID, fd.Default.TipSHA, fd.Default.TipCommittedAt, tg.DefaultTipSHA, seedTime)
		}
		if fd.Default.UpstreamedPR != 0 {
			t.Errorf("%s UpstreamedPR = %d, want 0 (Phase B never answered)", tg.ID, fd.Default.UpstreamedPR)
		}
	}
	// 1 phase A document + phase B: 11 fails, splits into 5+6, both fail
	// at/below the floor and are dropped: 3 more documents, 4 total.
	if stats.Queries != 4 {
		t.Errorf("stats.Queries = %d, want 4 (1 phase A + 3 phase B: bounded, no infinite halving)", stats.Queries)
	}
}

// phaseAAllAheadBody builds a successful Phase A response with n aliases,
// each ahead by 3 (so every branch qualifies for Phase B).
func phaseAAllAheadBody(n int) string {
	var sb strings.Builder
	sb.WriteString(`{"data":{"repository":{"ref":{`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"c%d":{"aheadBy":3,"behindBy":0}`, i)
	}
	sb.WriteString(`}},` + rl + `}}`)
	return sb.String()
}
