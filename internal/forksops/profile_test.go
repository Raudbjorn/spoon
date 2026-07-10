package forksops

import (
	"slices"
	"testing"

	"github.com/svnbjrn/spoon/internal/heat"
)

func TestDeriveProfile(t *testing.T) {
	cases := []struct {
		name        string
		result      Result
		wantLabel   string
		wantReasons []string
	}{
		{
			name:        "hidden_upstreamed",
			result:      Result{Visibility: VisibilityDecision{Status: VisibilityHidden, Reasons: []string{"upstreamed"}}},
			wantLabel:   "hidden",
			wantReasons: []string{"upstreamed"},
		},
		{
			name:        "focused_change_sniper",
			result:      Result{Heat: heat.HeatResult{LoneWolfV2: &heat.LoneWolfResult{Detected: true, Archetype: heat.ArchetypeSniper, Label: "Sniper"}}},
			wantLabel:   "focused_change",
			wantReasons: []string{"archetype:Sniper"},
		},
		{
			name:        "focused_feature_builder",
			result:      Result{Heat: heat.HeatResult{LoneWolfV2: &heat.LoneWolfResult{Detected: true, Archetype: heat.ArchetypeFeatureBuilder, Label: "Feature Builder"}}},
			wantLabel:   "focused_feature",
			wantReasons: []string{"archetype:Feature Builder"},
		},
		{
			name:        "broad_maintenance_drifter",
			result:      Result{Heat: heat.HeatResult{LoneWolfV2: &heat.LoneWolfResult{Detected: true, Archetype: heat.ArchetypeDrifter, Label: "Drifter"}}},
			wantLabel:   "broad_maintenance",
			wantReasons: []string{"archetype:Drifter"},
		},
		{
			name:        "emerging_active_rising",
			result:      Result{Momentum: MomentumInfo{Status: MomentumRising}},
			wantLabel:   "emerging_active",
			wantReasons: []string{"momentum:rising"},
		},
		{
			name:        "emerging_active_new",
			result:      Result{Momentum: MomentumInfo{Status: MomentumNew}},
			wantLabel:   "emerging_active",
			wantReasons: []string{"momentum:new"},
		},
		{
			name:        "stale_archived_low_recency_sorted",
			result:      Result{Heat: heat.HeatResult{Penalties: []string{"low_recency", "archived"}}},
			wantLabel:   "stale",
			wantReasons: []string{"archived", "low_recency"},
		},
		{
			name:        "matches_priors",
			result:      Result{PriorScore: 1, PriorReasons: []string{"path:internal/auth"}},
			wantLabel:   "matches_priors",
			wantReasons: []string{"path:internal/auth"},
		},
		{
			name:      "standard_floor",
			result:    Result{},
			wantLabel: "standard",
		},
		{
			name: "hidden_precedence_over_archetype",
			result: Result{
				Visibility: VisibilityDecision{Status: VisibilityHidden, Reasons: []string{"no_ahead"}},
				Heat:       heat.HeatResult{LoneWolfV2: &heat.LoneWolfResult{Detected: true, Archetype: heat.ArchetypeSniper, Label: "Sniper"}},
			},
			wantLabel:   "hidden",
			wantReasons: []string{"no_ahead"},
		},
		{
			name: "archetype_precedence_over_momentum",
			result: Result{
				Heat:     heat.HeatResult{LoneWolfV2: &heat.LoneWolfResult{Detected: true, Archetype: heat.ArchetypeSniper, Label: "Sniper"}},
				Momentum: MomentumInfo{Status: MomentumRising},
			},
			wantLabel:   "focused_change",
			wantReasons: []string{"archetype:Sniper"},
		},
		{
			name: "stale_precedence_over_priors",
			result: Result{
				Heat:         heat.HeatResult{Penalties: []string{"archived"}},
				PriorScore:   1,
				PriorReasons: []string{"path:internal/auth"},
			},
			wantLabel:   "stale",
			wantReasons: []string{"archived"},
		},
		{
			name: "detected_but_archetype_none_falls_to_momentum",
			result: Result{
				Heat:     heat.HeatResult{LoneWolfV2: &heat.LoneWolfResult{Detected: true, Archetype: heat.ArchetypeNone}},
				Momentum: MomentumInfo{Status: MomentumRising},
			},
			wantLabel:   "emerging_active",
			wantReasons: []string{"momentum:rising"},
		},
		{
			name:      "undetected_lonewolf_falls_through",
			result:    Result{Heat: heat.HeatResult{LoneWolfV2: &heat.LoneWolfResult{Detected: false, Archetype: heat.ArchetypeSniper, Label: "Sniper"}}},
			wantLabel: "standard",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, reasons := DeriveProfile(tc.result)
			if label != tc.wantLabel {
				t.Errorf("label = %q, want %q", label, tc.wantLabel)
			}
			if !slices.Equal(reasons, tc.wantReasons) {
				t.Errorf("reasons = %#v, want %#v", reasons, tc.wantReasons)
			}
		})
	}
}
