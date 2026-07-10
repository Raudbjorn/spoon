package forksops

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/svnbjrn/spoon/internal/heat"
)

func TestVisibilityPolicyFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/visibility_policy_cases.json")
	if err != nil {
		t.Fatalf("read visibility policy fixture: %v", err)
	}

	var fixture struct {
		Cases []struct {
			Name      string   `json:"name"`
			Penalties []string `json:"penalties"`
			Skips     struct {
				Budget       string `json:"budget"`
				Cluster      string `json:"cluster"`
				Contributors string `json:"contributors"`
				OwnerProfile string `json:"owner_profile"`
				SiblingSim   string `json:"sibling_sim"`
			} `json:"skips"`
			WantVisibility VisibilityDecision `json:"wantVisibility"`
			WantDegraded   []DegradedStage    `json:"wantDegraded"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode visibility policy fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("visibility policy fixture has no cases")
	}

	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			result := Result{Heat: heat.HeatResult{Penalties: tc.Penalties}}
			if tc.Skips.Budget != "" {
				result.BudgetSkip = &StageSkip{Stage: "compare", Reason: tc.Skips.Budget}
			}
			if tc.Skips.Contributors != "" {
				result.T3Skip = &StageSkip{Stage: "contributors", Reason: tc.Skips.Contributors}
			}
			if tc.Skips.OwnerProfile != "" {
				result.OwnerProfileSkip = &StageSkip{Stage: "owner_profile", Reason: tc.Skips.OwnerProfile}
			}
			if tc.Skips.SiblingSim != "" {
				result.SiblingSimSkip = &StageSkip{Stage: "sibling_sim", Reason: tc.Skips.SiblingSim}
			}
			if tc.Skips.Cluster != "" {
				result.ClusterSkip = &ClusterSkip{Message: tc.Skips.Cluster}
			}

			gotVisibility := DeriveVisibility(result)
			if gotVisibility.Status != tc.WantVisibility.Status ||
				!slices.Equal(gotVisibility.Reasons, tc.WantVisibility.Reasons) {
				t.Errorf("DeriveVisibility() = %#v, want %#v", gotVisibility, tc.WantVisibility)
			}

			gotDegraded := CollectDegradedStages(result)
			if !slices.Equal(gotDegraded, tc.WantDegraded) {
				t.Errorf("CollectDegradedStages() = %#v, want %#v", gotDegraded, tc.WantDegraded)
			}
		})
	}
}
