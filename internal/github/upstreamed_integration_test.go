//go:build integration

package github

import (
	"context"
	"testing"
)

// go test -tags integration -run TestCheckUpstreamed_Live ./internal/github/
// Proves the detector end-to-end against a real OpenVINO maintainer fork:
// one branch whose tip heads a merged upstream PR, one dead scratch branch.
func TestCheckUpstreamed_Live(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	const upstream = "openvinotoolkit/openvino"

	cases := []struct {
		branch string
		want   bool
	}{
		{"arm_fc_reorder_weights_in_compile_model", true},       // PR #19634, merged
		{"arm_fc_reorder_weights_in_compile_model_demo", false}, // dead scratch, never PR'd
	}
	for _, tc := range cases {
		var br struct {
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		if err := c.Get(ctx, "repos/EgorDuplensky/openvino/branches/"+tc.branch, &br); err != nil {
			t.Fatalf("fetch branch %s: %v", tc.branch, err)
		}
		got, err := c.CheckUpstreamed(ctx, "EgorDuplensky", "openvino", br.Commit.SHA, upstream)
		if err != nil {
			t.Fatalf("CheckUpstreamed %s: %v", tc.branch, err)
		}
		t.Logf("branch=%-45s tip=%s upstreamed=%v pr=%d", tc.branch, br.Commit.SHA[:10], got.Upstreamed, got.PRNumber)
		if got.Upstreamed != tc.want {
			t.Errorf("branch %s: Upstreamed=%v, want %v", tc.branch, got.Upstreamed, tc.want)
		}
	}
}

// go test -tags integration -run TestBranchScan_Live ./internal/github/
// Exercises the full recency-first, upstreamed-aware selection against a real
// OpenVINO maintainer fork whose master is synced (ahead=0) and whose side
// branches are a mix of merged PRs and scratch work.
func TestBranchScan_Live(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()

	fork := ForkInfo{
		Owner:         OwnerInfo{Login: "EgorDuplensky"},
		Name:          "openvino",
		DefaultBranch: "master",
	}
	// A merged branch (older, big) and a never-PR'd scratch branch (also old).
	branches := []BranchInfo{
		{Name: "arm_fc_reorder_weights_in_compile_model_demo", LastCommitAt: "2023-10-01T00:00:00Z"},
		{Name: "arm_fc_reorder_weights_in_compile_model", LastCommitAt: "2023-09-01T00:00:00Z"},
	}
	scan, err := c.FetchCompareWithBranchScan(ctx, "openvinotoolkit", "openvino", "master", fork, branches)
	if err != nil {
		t.Fatalf("FetchCompareWithBranchScan: %v", err)
	}
	t.Logf("selected branch=%q ahead=%d upstreamed=%v pr=%d",
		scan.Branch, scan.Compare.AheadBy, scan.Upstreamed, scan.UpstreamedPR)
	// The _demo branch is most-recent and never merged → it must be selected as
	// genuine (not-upstreamed) work rather than the older merged sibling.
	if scan.Branch != "arm_fc_reorder_weights_in_compile_model_demo" {
		t.Errorf("selected %q, want the recent non-upstreamed _demo branch", scan.Branch)
	}
	if scan.Upstreamed {
		t.Errorf("recent scratch branch should not be flagged upstreamed")
	}
}
