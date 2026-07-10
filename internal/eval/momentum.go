package eval

import (
	"math"
	"sort"
)

type MomentumPolicy string

const (
	MomentumPolicyOutputOnly MomentumPolicy = "output_only"
	MomentumPolicyTieBreaker MomentumPolicy = "tie_breaker"
	MomentumPolicyWeighted3  MomentumPolicy = "weighted_3"
	MomentumPolicyWeighted5  MomentumPolicy = "weighted_5"
)

type MomentumEvalRow struct {
	ID               string  `json:"id"`
	Score            float64 `json:"score"`
	StarsDelta30d    int     `json:"starsDelta30d"`
	SubForksDelta30d int     `json:"subForksDelta30d"`
	ObservedDays     int     `json:"observedDays"`
	Useful           bool    `json:"useful"`
}

type MomentumVariantResult struct {
	Policy       MomentumPolicy `json:"policy"`
	PrecisionAtK float64        `json:"precisionAtK"`
	NDCGAtK      float64        `json:"ndcgAtK"`
	TopIDs       []string       `json:"topIds"`
}

type momentumRankedRow struct {
	row       MomentumEvalRow
	signal    float64
	rankScore float64
}

func EvaluateMomentumPolicies(rows []MomentumEvalRow, k int) []MomentumVariantResult {
	if k <= 0 {
		k = len(rows)
	}
	if k > len(rows) {
		k = len(rows)
	}

	base := make([]momentumRankedRow, len(rows))
	usefulRows := 0
	for i, row := range rows {
		base[i] = momentumRankedRow{row: row, signal: momentumSignal(row)}
		if row.Useful {
			usefulRows++
		}
	}

	idealUseful := usefulRows
	if idealUseful > k {
		idealUseful = k
	}
	idealDCG := 0.0
	for i := range idealUseful {
		idealDCG += 1 / math.Log2(float64(i)+2)
	}

	policies := []MomentumPolicy{
		MomentumPolicyOutputOnly,
		MomentumPolicyTieBreaker,
		MomentumPolicyWeighted3,
		MomentumPolicyWeighted5,
	}
	results := make([]MomentumVariantResult, 0, len(policies))
	for _, policy := range policies {
		ranked := append([]momentumRankedRow(nil), base...)
		for i := range ranked {
			ranked[i].rankScore = ranked[i].row.Score
			switch policy {
			case MomentumPolicyWeighted3:
				ranked[i].rankScore += 3 * ranked[i].signal
			case MomentumPolicyWeighted5:
				ranked[i].rankScore += 5 * ranked[i].signal
			}
		}

		sort.Slice(ranked, func(i, j int) bool {
			if policy == MomentumPolicyTieBreaker {
				if ranked[i].row.Score != ranked[j].row.Score {
					return ranked[i].row.Score > ranked[j].row.Score
				}
				if ranked[i].signal != ranked[j].signal {
					return ranked[i].signal > ranked[j].signal
				}
				return ranked[i].row.ID < ranked[j].row.ID
			}
			if ranked[i].rankScore != ranked[j].rankScore {
				return ranked[i].rankScore > ranked[j].rankScore
			}
			return ranked[i].row.ID < ranked[j].row.ID
		})

		topIDs := make([]string, 0, k)
		usefulAtK := 0
		dcg := 0.0
		for i := range k {
			row := ranked[i].row
			topIDs = append(topIDs, row.ID)
			if row.Useful {
				usefulAtK++
				dcg += 1 / math.Log2(float64(i)+2)
			}
		}

		precisionAtK := 0.0
		if k > 0 {
			precisionAtK = float64(usefulAtK) / float64(k)
		}
		ndcgAtK := 0.0
		if idealDCG > 0 {
			ndcgAtK = dcg / idealDCG
		}
		results = append(results, MomentumVariantResult{
			Policy:       policy,
			PrecisionAtK: precisionAtK,
			NDCGAtK:      ndcgAtK,
			TopIDs:       topIDs,
		})
	}
	return results
}

func momentumSignal(row MomentumEvalRow) float64 {
	if row.ObservedDays <= 0 {
		return 0
	}
	growth := maxInt(0, row.StarsDelta30d) + 2*maxInt(0, row.SubForksDelta30d)
	signal := math.Log1p(float64(growth)) / math.Log1p(20)
	if signal > 1 {
		signal = 1
	}
	return signal
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
