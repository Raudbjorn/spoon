package main

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/forksops"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// When LinearHistory is &true, all four fields ride into the JSON export
// (boolean + scalar + truncate + raw vector).
func TestForkToJSON_EmitsLinearHistory_True(t *testing.T) {
	linear := true
	r := forksops.Result{
		Fork: forge.T1Data{
			ID: "alice/repo", Owner: "alice", Name: "repo",
			LinearHistory:      &linear,
			MergeCommits:       0,
			MergeCommitHistory: []int{1, 1, 1, 1, 1},
		},
		Heat: heat.HeatResult{},
	}
	out := forkToJSON(r)
	if v, ok := out["linearHistory"].(bool); !ok || v != true {
		t.Fatalf("linearHistory = %v, want true", out["linearHistory"])
	}
	if v, ok := out["mergeCommits"].(int); !ok || v != 0 {
		t.Fatalf("mergeCommits = %v, want 0", out["mergeCommits"])
	}
	if v, ok := out["mergeCommitTruncated"].(bool); !ok || v != false {
		t.Fatalf("mergeCommitTruncated = %v, want false (default)", out["mergeCommitTruncated"])
	}
	vec, ok := out["mergeCommitHistory"].([]int)
	if !ok || len(vec) != 5 {
		t.Fatalf("mergeCommitHistory = %v, want 5-element vector", out["mergeCommitHistory"])
	}
}

// When LinearHistory is nil (unknown), the boolean + scalar + vector
// must be omitted, but the truncate flag must still emit its default
// false. Downstream consumers need a deterministic key on every row.
func TestForkToJSON_OmitsLinearHistory_WhenUnknown(t *testing.T) {
	r := forksops.Result{
		Fork: forge.T1Data{ID: "alice/repo", Owner: "alice", Name: "repo"},
		Heat: heat.HeatResult{},
	}
	out := forkToJSON(r)
	if _, ok := out["linearHistory"]; ok {
		t.Errorf("linearHistory should be omitted when nil, got %v", out["linearHistory"])
	}
	if _, ok := out["mergeCommits"]; ok {
		t.Errorf("mergeCommits should be omitted when no signal, got %v", out["mergeCommits"])
	}
	if v, ok := out["mergeCommitTruncated"].(bool); !ok || v != false {
		t.Errorf("mergeCommitTruncated = %v, want false default", out["mergeCommitTruncated"])
	}
}

// When LinearHistory is &false (computed, with merges), all four
// fields ride -- the boolean distinguishes the case from "unknown".
func TestForkToJSON_EmitsLinearHistory_False(t *testing.T) {
	linear := false
	r := forksops.Result{
		Fork: forge.T1Data{
			ID: "alice/repo", Owner: "alice", Name: "repo",
			LinearHistory:        &linear,
			MergeCommits:         3,
			MergeCommitHistory:   []int{2, 1, 2},
			MergeCommitTruncated: true,
		},
		Heat: heat.HeatResult{},
	}
	out := forkToJSON(r)
	if v, ok := out["linearHistory"].(bool); !ok || v != false {
		t.Errorf("linearHistory = %v, want false", out["linearHistory"])
	}
	if v, ok := out["mergeCommits"].(int); !ok || v != 3 {
		t.Errorf("mergeCommits = %v, want 3", out["mergeCommits"])
	}
	if v, ok := out["mergeCommitTruncated"].(bool); !ok || v != true {
		t.Errorf("mergeCommitTruncated = %v, want true", out["mergeCommitTruncated"])
	}
	if v, ok := out["mergeCommitHistory"].([]int); !ok || len(v) != 3 {
		t.Errorf("mergeCommitHistory = %v, want 3-element vector", out["mergeCommitHistory"])
	}
}
