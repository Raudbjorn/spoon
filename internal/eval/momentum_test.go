package eval

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestEvaluateMomentumPolicies(t *testing.T) {
	data, err := os.ReadFile("testdata/momentum_policy_cases.json")
	if err != nil {
		t.Fatalf("read momentum policy fixture: %v", err)
	}

	var fixture struct {
		K    int               `json:"k"`
		Rows []MomentumEvalRow `json:"rows"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode momentum policy fixture: %v", err)
	}
	if len(fixture.Rows) == 0 {
		t.Fatal("momentum policy fixture has no rows")
	}

	results := EvaluateMomentumPolicies(fixture.Rows, fixture.K)
	wantPolicies := []MomentumPolicy{
		MomentumPolicyOutputOnly,
		MomentumPolicyTieBreaker,
		MomentumPolicyWeighted3,
		MomentumPolicyWeighted5,
	}
	gotPolicies := make([]MomentumPolicy, len(results))
	for i, result := range results {
		gotPolicies[i] = result.Policy
	}
	if !slices.Equal(gotPolicies, wantPolicies) {
		t.Fatalf("policy order = %v, want %v", gotPolicies, wantPolicies)
	}

	outputOnly := results[0]
	weighted5 := results[3]
	wantOutputOnly := []string{"steady-useful", "popular-stale-junk", "rising-useful"}
	if !slices.Equal(outputOnly.TopIDs, wantOutputOnly) {
		t.Errorf("output_only top IDs = %v, want %v", outputOnly.TopIDs, wantOutputOnly)
	}
	wantWeighted5 := []string{"rising-useful", "steady-useful", "subfork-rising-useful"}
	if !slices.Equal(weighted5.TopIDs, wantWeighted5) {
		t.Errorf("weighted_5 top IDs = %v, want %v", weighted5.TopIDs, wantWeighted5)
	}
	if weighted5.PrecisionAtK <= outputOnly.PrecisionAtK {
		t.Errorf("weighted_5 precision@k = %v, want > output_only %v", weighted5.PrecisionAtK, outputOnly.PrecisionAtK)
	}
	if weighted5.NDCGAtK <= outputOnly.NDCGAtK {
		t.Errorf("weighted_5 NDCG@k = %v, want > output_only %v", weighted5.NDCGAtK, outputOnly.NDCGAtK)
	}
	if slices.Contains(weighted5.TopIDs, "unknown-momentum") {
		t.Errorf("weighted_5 promoted unknown-momentum: %v", weighted5.TopIDs)
	}
	unknownFound := false
	for _, row := range fixture.Rows {
		if row.ID != "unknown-momentum" {
			continue
		}
		unknownFound = true
		if momentumSignal(row) != 0 {
			t.Errorf("unknown-momentum signal = %v, want 0", momentumSignal(row))
		}
	}
	if !unknownFound {
		t.Fatal("momentum policy fixture is missing unknown-momentum")
	}
}

func TestEvaluateMomentumPolicies_TieBreaker(t *testing.T) {
	rows := []MomentumEvalRow{
		{ID: "b-equal", Score: 50, StarsDelta30d: 1, ObservedDays: 30},
		{ID: "z-high", Score: 50, StarsDelta30d: 4, ObservedDays: 30},
		{ID: "a-equal", Score: 50, StarsDelta30d: 1, ObservedDays: 30},
	}

	results := EvaluateMomentumPolicies(rows, len(rows))
	want := []string{"z-high", "a-equal", "b-equal"}
	if !slices.Equal(results[1].TopIDs, want) {
		t.Errorf("tie_breaker top IDs = %v, want %v", results[1].TopIDs, want)
	}
}
