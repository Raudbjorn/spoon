// Tests for cmd/spn JSON emission of lineage and coverage on each fork
// record. See /home/svnbjrn/projects/spooon/.superpowers/sdd/
// spoon-endpoint-networking-plan/task-3-brief.md for the contract:
//   - lineage.networkRoot/directParent/depthFromRoot (omit when no field known)
//   - coverage.directTotalCount/wholeNetworkForkCount/unresolved
//     (emit when either count is known, including known zero unresolved)
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forksops"
)

// TestForkToJSON_EmitsLineageAndCoverage_DirectChild covers the
// direct_whole_count_gap fixture scenario: a direct child of the
// chunkhound root with depth 1, coverage unresolved=10.
func TestForkToJSON_EmitsLineageAndCoverage_DirectChild(t *testing.T) {
	r := forksops.Result{
		Lineage: forksops.Lineage{
			NetworkRoot:   "chunkhound/chunkhound",
			DirectParent:  "chunkhound/chunkhound",
			DepthFromRoot: 1,
		},
		Coverage: forksops.Coverage{
			DirectTotalCount:       118,
			WholeNetworkForkCount: 128,
			Unresolved:             10,
		},
	}
	out := forkToJSON(r)

	lin, ok := out["lineage"].(map[string]any)
	if !ok {
		t.Fatalf("expected lineage map in JSON output, got: %#v", out["lineage"])
	}
	if lin["networkRoot"] != "chunkhound/chunkhound" {
		t.Errorf("lineage.networkRoot: got %v, want chunkhound/chunkhound", lin["networkRoot"])
	}
	if lin["directParent"] != "chunkhound/chunkhound" {
		t.Errorf("lineage.directParent: got %v, want chunkhound/chunkhound", lin["directParent"])
	}
	if d := lin["depthFromRoot"]; d != int(1) {
		t.Errorf("lineage.depthFromRoot: got %v, want 1", d)
	}

	cov, ok := out["coverage"].(map[string]any)
	if !ok {
		t.Fatalf("expected coverage map in JSON output, got: %#v", out["coverage"])
	}
	if u := cov["unresolved"]; u != int(10) {
		t.Errorf("coverage.unresolved: got %v, want 10", u)
	}
	if d := cov["directTotalCount"]; d != int(118) {
		t.Errorf("coverage.directTotalCount: got %v, want 118", d)
	}
	if w := cov["wholeNetworkForkCount"]; w != int(128) {
		t.Errorf("coverage.wholeNetworkForkCount: got %v, want 128", w)
	}

	// JSON round-trip: the JSON shape must match exactly.
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"unresolved":10`) {
		t.Errorf("JSON missing unresolved:10: %s", b)
	}
	if !strings.Contains(string(b), `"directParent":"chunkhound/chunkhound"`) {
		t.Errorf("JSON missing directParent: %s", b)
	}
}

// TestForkToJSON_EmitsLineage_ParentChainDepth2 covers the parent_chain
// fixture: a level-2 fork (depth 2) under octo/root-repo.
func TestForkToJSON_EmitsLineage_ParentChainDepth2(t *testing.T) {
	r := forksops.Result{
		Lineage: forksops.Lineage{
			NetworkRoot:   "octo/root-repo",
			DirectParent:  "alice/level1-fork",
			DepthFromRoot: 2,
		},
	}
	out := forkToJSON(r)
	lin, ok := out["lineage"].(map[string]any)
	if !ok {
		t.Fatalf("expected lineage map in JSON output, got: %#v", out["lineage"])
	}
	if lin["directParent"] != "alice/level1-fork" {
		t.Errorf("lineage.directParent: got %v, want alice/level1-fork", lin["directParent"])
	}
	if d := lin["depthFromRoot"]; d != int(2) {
		t.Errorf("lineage.depthFromRoot: got %v, want 2", d)
	}
}

// TestForkToJSON_OmitsLineageAndCoverage_WhenUnknown covers the
// "omit when no field is known" contract: a REST record with no
// lineage/coverage data must not emit either key.
func TestForkToJSON_OmitsLineageAndCoverage_WhenUnknown(t *testing.T) {
	r := forksops.Result{} // all zero
	out := forkToJSON(r)
	if _, ok := out["lineage"]; ok {
		t.Errorf("lineage should be omitted when no field is known: %#v", out["lineage"])
	}
	if _, ok := out["coverage"]; ok {
		t.Errorf("coverage should be omitted when no counts are known: %#v", out["coverage"])
	}
}

// TestForkToJSON_EmitsCoverage_KnownZeroUnresolved covers the brief's
// rule: "A known zero unresolved gap is still emitted when counts are
// known."
func TestForkToJSON_EmitsCoverage_KnownZeroUnresolved(t *testing.T) {
	r := forksops.Result{
		Coverage: forksops.Coverage{
			DirectTotalCount:       5,
			WholeNetworkForkCount: 5,
			Unresolved:             0,
		},
	}
	out := forkToJSON(r)
	cov, ok := out["coverage"].(map[string]any)
	if !ok {
		t.Fatalf("expected coverage map in JSON output, got: %#v", out["coverage"])
	}
	if u := cov["unresolved"]; u != int(0) {
		t.Errorf("coverage.unresolved: got %v, want 0", u)
	}
}

// TestForkToJSON_EmitsLineage_WhenOnlyDepthKnown covers a partial
// lineage: depth known (depth>0 means at least one hop from root) but
// parent path not present.
func TestForkToJSON_EmitsLineage_WhenOnlyDepthKnown(t *testing.T) {
	r := forksops.Result{
		Lineage: forksops.Lineage{
			DepthFromRoot: 3,
		},
	}
	out := forkToJSON(r)
	lin, ok := out["lineage"].(map[string]any)
	if !ok {
		t.Fatalf("expected lineage map, got: %#v", out["lineage"])
	}
	if d := lin["depthFromRoot"]; d != int(3) {
		t.Errorf("lineage.depthFromRoot: got %v, want 3", d)
	}
}
