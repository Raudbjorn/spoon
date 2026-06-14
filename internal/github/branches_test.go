package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// branchMeta describes a fake branch's divergence and upstreamed state for the
// compare/pulls test server.
type branchMeta struct {
	ahead    int
	tipSHA   string
	mergedPR int // >0 → tip heads a merged upstream PR
}

// scanTestServer routes the two endpoints branch selection touches:
//
//	GET /repos/{up}/compare/{base}...{forkOwner}:{branch}  → divergence
//	GET /repos/{fork}/commits/{sha}/pulls                  → upstreamed probe
//
// upstream is the full_name a merged PR must target to count as upstreamed.
func scanTestServer(t *testing.T, upstream string, byBranch map[string]branchMeta) *httptest.Server {
	t.Helper()
	// tip SHA → branch, for the pulls handler.
	bySHA := map[string]branchMeta{}
	for _, m := range byBranch {
		if m.tipSHA != "" {
			bySHA[m.tipSHA] = m
		}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/compare/"):
			// branch is the segment after the last ":".
			branch := path[strings.LastIndex(path, ":")+1:]
			m, ok := byBranch[branch]
			if !ok {
				t.Errorf("compare for unknown branch %q", branch)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			commits := make([]map[string]any, 0, m.ahead)
			for i := 0; i < m.ahead; i++ {
				sha := m.tipSHA
				if i < m.ahead-1 { // only the LAST commit is the tip
					sha = m.tipSHA + "_parent"
				}
				commits = append(commits, map[string]any{"sha": sha})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ahead_by":  m.ahead,
				"behind_by": 100,
				"commits":   commits,
			})
		case strings.HasSuffix(path, "/pulls"):
			// .../commits/{sha}/pulls
			rest := strings.TrimSuffix(path, "/pulls")
			sha := rest[strings.LastIndex(rest, "/")+1:]
			m, ok := bySHA[sha]
			if !ok || m.mergedPR == 0 {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			merged := "2024-01-01T00:00:00Z"
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"number":    m.mergedPR,
				"state":     "closed",
				"merged_at": merged,
				"base":      map[string]any{"repo": map[string]any{"full_name": upstream}},
			}})
		default:
			t.Errorf("unexpected path: %s", path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func testFork() ForkInfo {
	return ForkInfo{Owner: OwnerInfo{Login: "maint"}, Name: "proj", DefaultBranch: "main"}
}

// The core behavioral change: the most-RECENT branch with genuine work is
// chosen over an older branch with MORE commits whose work is already merged.
func TestFetchCompareWithBranchScan_PrefersRecentGenuineOverMostAheadMerged(t *testing.T) {
	srv := scanTestServer(t, "up/stream", map[string]branchMeta{
		"main":        {ahead: 0},
		"feature-new": {ahead: 5, tipSHA: "sha_new", mergedPR: 0},  // recent, genuine
		"old-merged":  {ahead: 40, tipSHA: "sha_old", mergedPR: 7}, // older, bigger, merged
	})
	defer srv.Close()

	c := newTestClient(t, srv)
	branches := []BranchInfo{
		{Name: "feature-new", LastCommitAt: "2026-06-01T00:00:00Z"},
		{Name: "old-merged", LastCommitAt: "2023-01-01T00:00:00Z"},
	}
	scan, err := c.FetchCompareWithBranchScan(context.Background(), "up", "stream", "main", testFork(), branches)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Branch != "feature-new" {
		t.Errorf("selected branch = %q, want feature-new (old behavior would pick old-merged for +40)", scan.Branch)
	}
	if scan.Upstreamed {
		t.Errorf("feature-new is genuine work, want Upstreamed=false")
	}
	if scan.Compare.AheadBy != 5 {
		t.Errorf("AheadBy = %d, want 5", scan.Compare.AheadBy)
	}
}

// When every divergent branch is already merged, return the most-recent one
// flagged Upstreamed so scoring zeroes it as "merged" rather than "no work".
func TestFetchCompareWithBranchScan_AllUpstreamed_FlagsMostRecent(t *testing.T) {
	srv := scanTestServer(t, "up/stream", map[string]branchMeta{
		"main":        {ahead: 0},
		"feature-new": {ahead: 5, tipSHA: "sha_new", mergedPR: 100},
		"old-merged":  {ahead: 40, tipSHA: "sha_old", mergedPR: 50},
	})
	defer srv.Close()

	c := newTestClient(t, srv)
	branches := []BranchInfo{
		{Name: "feature-new", LastCommitAt: "2026-06-01T00:00:00Z"},
		{Name: "old-merged", LastCommitAt: "2023-01-01T00:00:00Z"},
	}
	scan, err := c.FetchCompareWithBranchScan(context.Background(), "up", "stream", "main", testFork(), branches)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Branch != "feature-new" {
		t.Errorf("selected branch = %q, want feature-new (most recent)", scan.Branch)
	}
	if !scan.Upstreamed || scan.UpstreamedPR != 100 {
		t.Errorf("want Upstreamed=true PR=100, got %v PR=%d", scan.Upstreamed, scan.UpstreamedPR)
	}
}

// Default branch with genuine work wins outright — no side-branch scan.
func TestFetchCompareWithBranchScan_DefaultGenuineWins(t *testing.T) {
	srv := scanTestServer(t, "up/stream", map[string]branchMeta{
		"main": {ahead: 3, tipSHA: "sha_main", mergedPR: 0},
	})
	defer srv.Close()

	c := newTestClient(t, srv)
	branches := []BranchInfo{{Name: "side", LastCommitAt: "2026-06-01T00:00:00Z"}}
	scan, err := c.FetchCompareWithBranchScan(context.Background(), "up", "stream", "main", testFork(), branches)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Branch != "main" || scan.Upstreamed {
		t.Errorf("want main/not-upstreamed, got %q/%v", scan.Branch, scan.Upstreamed)
	}
}

// Default branch's own work is already merged, but a side branch carries
// genuine work → the side branch is preferred over the merged default.
func TestFetchCompareWithBranchScan_DefaultMerged_PrefersGenuineSide(t *testing.T) {
	srv := scanTestServer(t, "up/stream", map[string]branchMeta{
		"main":        {ahead: 2, tipSHA: "sha_main", mergedPR: 88}, // merged
		"feature-new": {ahead: 9, tipSHA: "sha_new", mergedPR: 0},   // genuine
	})
	defer srv.Close()

	c := newTestClient(t, srv)
	branches := []BranchInfo{{Name: "feature-new", LastCommitAt: "2026-06-01T00:00:00Z"}}
	scan, err := c.FetchCompareWithBranchScan(context.Background(), "up", "stream", "main", testFork(), branches)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Branch != "feature-new" || scan.Upstreamed {
		t.Errorf("want feature-new/not-upstreamed, got %q/%v", scan.Branch, scan.Upstreamed)
	}
}
