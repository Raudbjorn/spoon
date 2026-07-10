package forksops

import "sort"

type VisibilityStatus string

const (
	VisibilityVisible VisibilityStatus = "visible"
	VisibilityDemoted VisibilityStatus = "demoted"
	VisibilityHidden  VisibilityStatus = "hidden"
)

type VisibilityDecision struct {
	Status  VisibilityStatus
	Reasons []string
}

type DegradedStage struct {
	Stage  string
	Reason string
}

type RankBand string

const (
	RankBandTinySet     RankBand = "tiny_set"
	RankBandTop1Pct     RankBand = "top_1pct"
	RankBandTop5Pct     RankBand = "top_5pct"
	RankBandTop10Pct    RankBand = "top_10pct"
	RankBandTop25Pct    RankBand = "top_25pct"
	RankBandTop50Pct    RankBand = "top_50pct"
	RankBandBottom50Pct RankBand = "bottom_50pct"
)

type NetworkRank struct {
	Position   int
	Total      int
	Percentile float64
	Band       RankBand
}

type MomentumStatus string

const (
	MomentumUnknown MomentumStatus = "unknown"
	MomentumNew     MomentumStatus = "new"
	MomentumRising  MomentumStatus = "rising"
	MomentumFalling MomentumStatus = "falling"
	MomentumFlat    MomentumStatus = "flat"
)

func DeriveVisibility(r Result) VisibilityDecision {
	return deriveVisibility(r)
}

func CollectDegradedStages(r Result) []DegradedStage {
	return collectDegradedStages(r)
}

type MomentumInfo struct {
	Status           MomentumStatus
	StarsDelta30d    int
	SubForksDelta30d int
	ObservedDays     int
}

func deriveVisibility(r Result) VisibilityDecision {
	if len(r.Heat.Penalties) == 0 {
		return VisibilityDecision{Status: VisibilityVisible}
	}
	reasons := append([]string(nil), r.Heat.Penalties...)
	for _, penalty := range r.Heat.Penalties {
		if penalty == "no_ahead" || penalty == "upstreamed" {
			return VisibilityDecision{Status: VisibilityHidden, Reasons: reasons}
		}
	}
	return VisibilityDecision{Status: VisibilityDemoted, Reasons: reasons}
}

func collectDegradedStages(r Result) []DegradedStage {
	var degraded []DegradedStage
	if r.ClusterSkip != nil {
		degraded = append(degraded, DegradedStage{Stage: "cluster", Reason: r.ClusterSkip.Message})
	}
	if r.BudgetSkip != nil {
		degraded = append(degraded, DegradedStage{Stage: "compare", Reason: r.BudgetSkip.Reason})
	}
	if r.T3Skip != nil {
		degraded = append(degraded, DegradedStage{Stage: "contributors", Reason: r.T3Skip.Reason})
	}
	if r.OwnerProfileSkip != nil {
		degraded = append(degraded, DegradedStage{Stage: "owner_profile", Reason: r.OwnerProfileSkip.Reason})
	}
	if r.SiblingSimSkip != nil {
		degraded = append(degraded, DegradedStage{Stage: "sibling_sim", Reason: r.SiblingSimSkip.Reason})
	}
	return degraded
}

func assignNetworkRanks(results []Result) {
	total := len(results)
	if total == 0 {
		return
	}
	order := make([]int, total)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		left := results[order[i]]
		right := results[order[j]]
		if left.Heat.Score != right.Heat.Score {
			return left.Heat.Score > right.Heat.Score
		}
		return left.Fork.ID < right.Fork.ID
	})
	for pos, idx := range order {
		position := pos + 1
		percentile := float64(total-position+1) / float64(total)
		results[idx].NetworkRank = &NetworkRank{
			Position:   position,
			Total:      total,
			Percentile: percentile,
			Band:       rankBand(total, percentile),
		}
	}
}

func rankBand(total int, percentile float64) RankBand {
	if total < 10 {
		return RankBandTinySet
	}
	if percentile >= 0.99 {
		return RankBandTop1Pct
	}
	if percentile >= 0.95 {
		return RankBandTop5Pct
	}
	if percentile >= 0.90 {
		return RankBandTop10Pct
	}
	if percentile >= 0.75 {
		return RankBandTop25Pct
	}
	if percentile >= 0.50 {
		return RankBandTop50Pct
	}
	return RankBandBottom50Pct
}
