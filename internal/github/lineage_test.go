// Package github: lineage/coverage tests for the GraphQL adapter.
//
// These tests cover Task 3 of the spoon-endpoint-networking-plan:
//   - parent { nameWithOwner databaseId } decoding into gqlForkNode and T1Extra
//   - annotateDepths: deterministic, cycle-safe, cross-page, out-of-order
//   - forkInfoToT1 lineage mapping (REST leaves parent unknown; GraphQL uses extra)
//
// See /home/svnbjrn/projects/spooon/.superpowers/sdd/spoon-endpoint-networking-plan/task-3-brief.md
// for the full contract.
package github

import "testing"

// TestGqlForkToForkInfo_ParentDecoded covers the GraphQL parent-field
// decoding contract: when the GraphQL node has a parent { nameWithOwner
// databaseId } payload, gqlForkToForkInfo must populate
// T1Extra.ParentFullPath and T1Extra.ParentDatabaseID.
//
// RED: this test fails to compile because gqlForkNode.Parent and the
// corresponding T1Extra fields do not exist yet.
func TestGqlForkToForkInfo_ParentDecoded(t *testing.T) {
	node := gqlForkNode{
		DatabaseID:    3001,
		NameWithOwner: "chunkhound/chunkhound-fork-1",
		Name:          "chunkhound-fork-1",
		ForkCount:     5,
		Parent: &struct {
			NameWithOwner string `json:"nameWithOwner"`
			DatabaseID    int64  `json:"databaseId"`
		}{
			NameWithOwner: "chunkhound/chunkhound",
			DatabaseID:    3000,
		},
	}

	_, extra := gqlForkToForkInfo(node, 128, 118, "authenticated", "2022-11-28")

	if extra.ParentFullPath != "chunkhound/chunkhound" {
		t.Errorf("ParentFullPath: got %q, want %q", extra.ParentFullPath, "chunkhound/chunkhound")
	}
	if extra.ParentDatabaseID != 3000 {
		t.Errorf("ParentDatabaseID: got %d, want %d", extra.ParentDatabaseID, int64(3000))
	}
	if extra.WholeNetworkForkCount != 128 {
		t.Errorf("WholeNetworkForkCount: got %d, want %d", extra.WholeNetworkForkCount, 128)
	}
	if extra.DirectTotalCount != 118 {
		t.Errorf("DirectTotalCount: got %d, want %d", extra.DirectTotalCount, 118)
	}
}

// TestGqlForkToForkInfo_NoParentLeavesZero covers the case where the
// GraphQL node has no parent payload (legacy response or root fork).
// ParentFullPath and ParentDatabaseID must remain at their zero values.
func TestGqlForkToForkInfo_NoParentLeavesZero(t *testing.T) {
	node := gqlForkNode{
		DatabaseID:    3001,
		NameWithOwner: "chunkhound/chunkhound",
		Name:          "chunkhound",
	}

	_, extra := gqlForkToForkInfo(node, 128, 118, "authenticated", "2022-11-28")

	if extra.ParentFullPath != "" {
		t.Errorf("ParentFullPath: got %q, want empty (no parent payload)", extra.ParentFullPath)
	}
	if extra.ParentDatabaseID != 0 {
		t.Errorf("ParentDatabaseID: got %d, want 0 (no parent payload)", extra.ParentDatabaseID)
	}
}

// TestAnnotateDepths_DirectChild covers the simplest case: a fork whose
// parent path equals the requested root gets DirectParent=1, DepthFromRoot=1.
func TestAnnotateDepths_DirectChild(t *testing.T) {
	forks := []ForkInfo{
		{ID: 3001, FullName: "alice/fork-a"},
	}
	extras := []T1Extra{
		{ParentFullPath: "octo/root", ParentDatabaseID: 0},
	}

	got := annotateDepths(forks, extras, "octo/root", nil)

	if got[0].DirectParent != 1 {
		t.Errorf("DirectParent: got %d, want 1", got[0].DirectParent)
	}
	if got[0].DepthFromRoot != 1 {
		t.Errorf("DepthFromRoot: got %d, want 1", got[0].DepthFromRoot)
	}
}

// TestAnnotateDepths_ThreeLevelChain covers the parent_chain fixture:
// level1 → root, level2 → level1, level3 → level2. After annotation:
// level1 has depth 1, level2 has depth 2, level3 has depth 3.
func TestAnnotateDepths_ThreeLevelChain(t *testing.T) {
	forks := []ForkInfo{
		{ID: 4001, FullName: "alice/level1-fork"},
		{ID: 4002, FullName: "bob/level2-fork"},
		{ID: 4003, FullName: "carol/level3-fork"},
	}
	extras := []T1Extra{
		{ParentFullPath: "octo/root-repo", ParentDatabaseID: 4000},
		{ParentFullPath: "alice/level1-fork", ParentDatabaseID: 4001},
		{ParentFullPath: "bob/level2-fork", ParentDatabaseID: 4002},
	}

	got := annotateDepths(forks, extras, "octo/root-repo", nil)

	wantDepths := map[int64]int{4001: 1, 4002: 2, 4003: 3}
	for i, e := range got {
		if want := wantDepths[forks[i].ID]; e.DepthFromRoot != want {
			t.Errorf("%s DepthFromRoot: got %d, want %d", forks[i].FullName, e.DepthFromRoot, want)
		}
	}
}

// TestAnnotateDepths_OutOfOrder covers the brief's "independent of node
// order" requirement: even when children arrive before their parents in
// the input slice, depths are still computed correctly.
func TestAnnotateDepths_OutOfOrder(t *testing.T) {
	forks := []ForkInfo{
		{ID: 4003, FullName: "carol/level3-fork"}, // child first
		{ID: 4001, FullName: "alice/level1-fork"}, // parent later
		{ID: 4002, FullName: "bob/level2-fork"},   // middle
	}
	extras := []T1Extra{
		{ParentFullPath: "bob/level2-fork", ParentDatabaseID: 4002},
		{ParentFullPath: "octo/root-repo", ParentDatabaseID: 4000},
		{ParentFullPath: "alice/level1-fork", ParentDatabaseID: 4001},
	}

	got := annotateDepths(forks, extras, "octo/root-repo", nil)

	if got[2].DepthFromRoot != 2 {
		t.Errorf("bob/level2-fork DepthFromRoot: got %d, want 2", got[2].DepthFromRoot)
	}
	if got[0].DepthFromRoot != 3 {
		t.Errorf("carol/level3-fork DepthFromRoot: got %d, want 3", got[0].DepthFromRoot)
	}
}

// TestAnnotateDepths_CycleSafe covers a cycle: a→b→a. Both must stay at
// depth 0 because the algorithm cannot assign a finite depth without
// looping. The brief forbids guessing or fabricating.
func TestAnnotateDepths_CycleSafe(t *testing.T) {
	forks := []ForkInfo{
		{ID: 5001, FullName: "alice/cycle-a"},
		{ID: 5002, FullName: "bob/cycle-b"},
	}
	extras := []T1Extra{
		{ParentFullPath: "bob/cycle-b", ParentDatabaseID: 5002},
		{ParentFullPath: "alice/cycle-a", ParentDatabaseID: 5001},
	}

	got := annotateDepths(forks, extras, "octo/root-repo", nil)

	for i, e := range got {
		if e.DepthFromRoot != 0 {
			t.Errorf("%s DepthFromRoot: got %d, want 0 (cycle)", forks[i].FullName, e.DepthFromRoot)
		}
	}
}

// TestAnnotateDepths_MissingParent covers a node whose parent is not in
// the input set (and not the root). DepthFromRoot must remain 0.
func TestAnnotateDepths_MissingParent(t *testing.T) {
	forks := []ForkInfo{
		{ID: 6001, FullName: "alice/orphan"},
	}
	extras := []T1Extra{
		{ParentFullPath: "ghost/unknown", ParentDatabaseID: 9999},
	}

	got := annotateDepths(forks, extras, "octo/root-repo", nil)

	if got[0].DepthFromRoot != 0 {
		t.Errorf("DepthFromRoot: got %d, want 0 (parent missing)", got[0].DepthFromRoot)
	}
	if got[0].DirectParent != 0 {
		t.Errorf("DirectParent: got %d, want 0 (parent missing)", got[0].DirectParent)
	}
}
