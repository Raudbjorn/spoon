// Package github: lineage/coverage tests for the GraphQL adapter.
//
// These tests cover Task 3 of the spoon-endpoint-networking-plan:
//   - parent { nameWithOwner databaseId } decoding into gqlForkNode and T1Extra
//   - annotateDepths: deterministic, cycle-safe, cross-page, out-of-order
//   - forkInfoToT1 lineage mapping (REST leaves parent unknown; GraphQL uses extra)
//
// See /home/svnbjrn/.superpowers/sdd/spoon-endpoint-networking-plan/task-3-brief.md
// for the full contract.
package github

import "testing"

// TestGqlForkToForkInfo_ParentDecoded covers the GraphQL parent-field
// decoding contract: when the GraphQL node has a parent { nameWithOwner
// databaseId } payload, gqlForkToForkInfo must populate
// T1Extra.ParentFullPath and T1Extra.ParentDatabaseID.
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

	got := annotateDepths(forks, extras, "octo/root-repo")

	for i, e := range got {
		if e.DepthFromRoot != 0 {
			t.Errorf("%s DepthFromRoot: got %d, want 0 (cycle)", forks[i].FullName, e.DepthFromRoot)
		}
	}
}

// TestSourceFullPath_ForkWithSource covers a fork whose REST response
// includes `source`. sourceFullPath should return Source.FullName.
func TestSourceFullPath_ForkWithSource(t *testing.T) {
	info := RepoInfo{
		FullName: "alice/fork-of-fork",
		Fork:     true,
		Source:   &RepoRef{ID: 1, FullName: "chunkhound/chunkhound"},
	}
	got := sourceFullPath(info)
	want := "chunkhound/chunkhound"
	if got != want {
		t.Errorf("sourceFullPath: got %q, want %q", got, want)
	}
}

// TestSourceFullPath_NonForkRoot covers a non-fork root (no Source
// payload). sourceFullPath should return the repo's own FullName.
func TestSourceFullPath_NonForkRoot(t *testing.T) {
	info := RepoInfo{
		FullName: "chunkhound/chunkhound",
		Fork:     false,
	}
	got := sourceFullPath(info)
	want := "chunkhound/chunkhound"
	if got != want {
		t.Errorf("sourceFullPath: got %q, want %q", got, want)
	}
}

// TestSourceFullPath_ForkNoSource covers a fork missing the Source
// payload. sourceFullPath should fall back to the repo's own FullName.
func TestSourceFullPath_ForkNoSource(t *testing.T) {
	info := RepoInfo{
		FullName: "alice/fork-without-source",
		Fork:     true,
		// Source is nil — REST may omit the field.
	}
	got := sourceFullPath(info)
	want := "alice/fork-without-source"
	if got != want {
		t.Errorf("sourceFullPath fallback: got %q, want %q", got, want)
	}
}

// TestDirectParentFullPath_ForkWithParent covers a fork whose REST
// response includes `parent`. directParentFullPath should return
// Parent.FullName.
func TestDirectParentFullPath_ForkWithParent(t *testing.T) {
	info := RepoInfo{
		FullName: "alice/fork-of-fork",
		Fork:     true,
		Parent:   &RepoRef{ID: 2, FullName: "bob/intermediate-fork"},
	}
	got := directParentFullPath(info)
	want := "bob/intermediate-fork"
	if got != want {
		t.Errorf("directParentFullPath: got %q, want %q", got, want)
	}
}

// TestDirectParentFullPath_NonForkRoot covers a non-fork root. The
// direct parent is conceptually empty (the repo is its own root).
func TestDirectParentFullPath_NonForkRoot(t *testing.T) {
	info := RepoInfo{
		FullName: "chunkhound/chunkhound",
		Fork:     false,
	}
	got := directParentFullPath(info)
	if got != "" {
		t.Errorf("directParentFullPath for non-fork: got %q, want empty", got)
	}
}

// TestDirectParentFullPath_ForkNoParent covers a fork missing the
// Parent payload. directParentFullPath should return empty.
func TestDirectParentFullPath_ForkNoParent(t *testing.T) {
	info := RepoInfo{
		FullName: "alice/fork-without-parent",
		Fork:     true,
		// Parent is nil.
	}
	got := directParentFullPath(info)
	if got != "" {
		t.Errorf("directParentFullPath for fork-without-parent: got %q, want empty", got)
	}
}

// TestForkInfoToT1_RESTNoParentFabrication covers the REST-fallback
// path: when extra is nil, forkInfoToT1 must NOT fabricate the
// requested network root as ParentFullPath. Per brief:
// "REST list-forks has no parent fields: keep direct parent and
// depth unknown instead of fabricating the requested root."
func TestForkInfoToT1_RESTNoParentFabrication(t *testing.T) {
	f := ForkInfo{
		ID:       9001,
		FullName: "alice/fork-from-rest",
		Name:     "fork-from-rest",
	}
	got := forkInfoToT1(f, nil, "octo/root-repo")

	// SourceFullPath is the network root (always known).
	if got.SourceFullPath != "octo/root-repo" {
		t.Errorf("SourceFullPath: got %q, want %q", got.SourceFullPath, "octo/root-repo")
	}
	// ParentFullPath must remain empty (unknown) — REST has no parent data.
	if got.ParentFullPath != "" {
		t.Errorf("ParentFullPath: got %q, want empty (REST path)", got.ParentFullPath)
	}
	// DepthFromRoot must remain 0 (unknown).
	if got.DepthFromRoot != 0 {
		t.Errorf("DepthFromRoot: got %d, want 0 (REST path)", got.DepthFromRoot)
	}
	// IsForkOfFork must remain false (depth unknown).
	if got.IsForkOfFork {
		t.Errorf("IsForkOfFork: got true, want false (REST path)")
	}
}

// TestForkInfoToT1_GraphQLDepthFromExtra covers the GraphQL path:
// when extra is non-nil with lineage fields, forkInfoToT1 must
// populate ParentFullPath, DepthFromRoot, IsForkOfFork, and the
// coverage counts from extra.
func TestForkInfoToT1_GraphQLDepthFromExtra(t *testing.T) {
	f := ForkInfo{
		ID:       9002,
		FullName: "alice/fork-from-graphql",
		Name:     "fork-from-graphql",
	}
	extra := &T1Extra{
		ParentFullPath:       "octo/root-repo",
		ParentDatabaseID:     8000,
		DepthFromRoot:        2,
		DirectTotalCount:     118,
		WholeNetworkForkCount: 128,
	}
	got := forkInfoToT1(f, extra, "octo/root-repo")

	if got.SourceFullPath != "octo/root-repo" {
		t.Errorf("SourceFullPath: got %q, want %q", got.SourceFullPath, "octo/root-repo")
	}
	if got.ParentFullPath != "octo/root-repo" {
		t.Errorf("ParentFullPath: got %q, want %q", got.ParentFullPath, "octo/root-repo")
	}
	if got.DepthFromRoot != 2 {
		t.Errorf("DepthFromRoot: got %d, want 2", got.DepthFromRoot)
	}
	if !got.IsForkOfFork {
		t.Errorf("IsForkOfFork: got false, want true (depth=2 > 1)")
	}
	if got.DirectTotalCount != 118 {
		t.Errorf("DirectTotalCount: got %d, want 118", got.DirectTotalCount)
	}
	if got.WholeNetworkForkCount != 128 {
		t.Errorf("WholeNetworkForkCount: got %d, want 128", got.WholeNetworkForkCount)
	}
}
