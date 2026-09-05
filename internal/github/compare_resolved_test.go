package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// requestCounter is an httptest server that serves a fixed compare response
// while counting requests by kind, so tests can assert CompareResolved makes
// exactly the REST calls the brief allows -- one compare, and never a
// commits/{sha}/pulls probe or a second (side-branch) compare.
type requestCounter struct {
	mu      sync.Mutex
	compare []string // path of each /compare/ request seen
	pulls   int      // count of .../commits/{sha}/pulls requests seen
}

func (rc *requestCounter) compareCount() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.compare)
}

func (rc *requestCounter) pullsCount() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.pulls
}

// newCompareResolvedServer serves one compare response for every /compare/
// request (regardless of branch) and a 200 empty array for any /pulls
// request, recording each so the test can assert on call counts.
func newCompareResolvedServer(t *testing.T, resp map[string]any) (*httptest.Server, *requestCounter) {
	t.Helper()
	rc := &requestCounter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/compare/"):
			rc.mu.Lock()
			rc.compare = append(rc.compare, path)
			rc.mu.Unlock()
			_ = json.NewEncoder(w).Encode(resp)
		case strings.HasSuffix(path, "/pulls"):
			rc.mu.Lock()
			rc.pulls++
			rc.mu.Unlock()
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("CompareResolved made an unexpected request: %s", path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rc
}

func resolvedTestFork() forge.T1Data {
	return forge.T1Data{
		ID:            "forkowner/repo",
		Owner:         "forkowner",
		Name:          "repo",
		DefaultBranch: "main",
	}
}

// CompareResolved must issue exactly one REST compare against the selected
// branch -- no commits/{sha}/pulls probe, no side-branch scan -- since the
// batch that produced the BranchSelection already resolved those questions.
// It must also set the branch-work flags when the selected branch is not
// the fork's default.
func TestCompareResolved_OneCompareNoProbeNoScan(t *testing.T) {
	srv, rc := newCompareResolvedServer(t, map[string]any{
		"ahead_by":      5,
		"behind_by":     2,
		"total_commits": 5,
		"commits": []map[string]any{
			{"sha": "sha_parent"},
			{"sha": "sha_tip"},
		},
		"files": []map[string]any{
			{"filename": "a.go", "status": "modified", "additions": 3, "deletions": 1, "patch": "@@ -1 +1 @@"},
		},
	})

	c := newTestClient(t, srv)
	p := NewGHProvider(c, AuthStatus{})
	p.SetCompareBaseline("up", "stream", "main")

	fork := resolvedTestFork()
	sel := forge.BranchSelection{
		Branch:    "feature-new",
		Ahead:     5,
		Behind:    2,
		IsSide:    true,
		NeedsREST: true,
	}

	t2, err := p.CompareResolved(context.Background(), fork, sel)
	if err != nil {
		t.Fatalf("CompareResolved: %v", err)
	}

	if got := rc.compareCount(); got != 1 {
		t.Errorf("compare requests = %d, want exactly 1", got)
	}
	if got := rc.pullsCount(); got != 0 {
		t.Errorf("pulls requests = %d, want 0 -- CompareResolved must not probe commits/{sha}/pulls, "+
			"the batch already carries the upstreamed verdict", got)
	}

	if !t2.Performed {
		t.Error("t2.Performed = false, want true for a successful compare")
	}
	if t2.AheadCount != 5 || t2.BehindCount != 2 {
		t.Errorf("ahead=%d behind=%d, want 5/2", t2.AheadCount, t2.BehindCount)
	}
	if !t2.IsBranchWork {
		t.Error("IsBranchWork = false, want true for a side-branch selection")
	}
	if t2.ActiveBranch != "feature-new" {
		t.Errorf("ActiveBranch = %q, want %q", t2.ActiveBranch, "feature-new")
	}
}

// The upstreamed verdict must come from the BranchSelection (already decided
// by the batch), not be re-derived by probing commits/{sha}/pulls.
func TestCompareResolved_UpstreamedFlagsCopiedFromSelection(t *testing.T) {
	srv, rc := newCompareResolvedServer(t, map[string]any{
		"ahead_by":      3,
		"behind_by":     0,
		"total_commits": 3,
		"commits":       []map[string]any{{"sha": "sha_tip"}},
	})

	c := newTestClient(t, srv)
	p := NewGHProvider(c, AuthStatus{})
	p.SetCompareBaseline("up", "stream", "main")

	fork := resolvedTestFork()
	sel := forge.BranchSelection{
		Branch:       "old-merged",
		Ahead:        3,
		IsSide:       true,
		Upstreamed:   true,
		UpstreamedPR: 42,
		NeedsREST:    true,
	}

	t2, err := p.CompareResolved(context.Background(), fork, sel)
	if err != nil {
		t.Fatalf("CompareResolved: %v", err)
	}

	if got := rc.pullsCount(); got != 0 {
		t.Errorf("pulls requests = %d, want 0 -- Upstreamed/UpstreamedPR must come from sel, not a probe", got)
	}
	if !t2.Upstreamed {
		t.Error("t2.Upstreamed = false, want true (copied from BranchSelection)")
	}
	if t2.UpstreamedPR != 42 {
		t.Errorf("t2.UpstreamedPR = %d, want 42", t2.UpstreamedPR)
	}
}

// A selection naming the fork's own default branch must not be flagged as
// branch work -- that flag exists to surface work living off the default
// branch, and false positives here would mislabel every ordinary fork.
func TestCompareResolved_DefaultBranchSelection_NotFlaggedAsBranchWork(t *testing.T) {
	srv, _ := newCompareResolvedServer(t, map[string]any{
		"ahead_by":      2,
		"behind_by":     0,
		"total_commits": 2,
		"commits":       []map[string]any{{"sha": "sha_tip"}},
	})

	c := newTestClient(t, srv)
	p := NewGHProvider(c, AuthStatus{})
	p.SetCompareBaseline("up", "stream", "main")

	fork := resolvedTestFork() // DefaultBranch: "main"
	sel := forge.BranchSelection{Branch: "main", Ahead: 2, IsSide: false, NeedsREST: true}

	t2, err := p.CompareResolved(context.Background(), fork, sel)
	if err != nil {
		t.Fatalf("CompareResolved: %v", err)
	}
	if t2.IsBranchWork {
		t.Errorf("IsBranchWork = true for the default branch, want false; ActiveBranch=%q", t2.ActiveBranch)
	}
}

// A 404 from the compare endpoint is "could not be compared", not an error
// and not a real zero-divergence result -- same contract FetchCompare
// documents and Compare relies on. CompareResolved must preserve it.
func TestCompareResolved_NotFoundIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	p := NewGHProvider(c, AuthStatus{})
	p.SetCompareBaseline("up", "stream", "main")

	fork := resolvedTestFork()
	sel := forge.BranchSelection{Branch: "gone", Ahead: 1, IsSide: true, NeedsREST: true}

	t2, err := p.CompareResolved(context.Background(), fork, sel)
	if err != nil {
		t.Fatalf("a 404 must stay a non-fatal condition, got error: %v", err)
	}
	if t2.Performed {
		t.Errorf("404 reported as a performed comparison: %+v", t2)
	}
}

// Compile-time capability check lives in adapter.go
// (var _ forge.ResolvedCompareProvider = (*GHProvider)(nil)); this test just
// exercises the interface value the way the divergence stream would.
func TestCompareResolved_ImplementsResolvedCompareProvider(t *testing.T) {
	var _ forge.ResolvedCompareProvider = (*GHProvider)(nil)
}
