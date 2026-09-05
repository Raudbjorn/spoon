package forksops

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// rankCase is one entry of testdata/rank_cases.json. The first cases are
// analytic (hand-derived); "recorded" cases were captured from the
// implementation and lock regressions the same way gemtc's validate tier
// does (distribution-aware tolerances, not exact equality).
type rankCase struct {
	Name  string    `json:"name"`
	Note  string    `json:"note"`
	Mu    []float64 `json:"mu"`
	Sigma []float64 `json:"sigma"`
	K     int       `json:"k"`
	Rule  string    `json:"rule"`
	Want  []struct {
		ExpectedRank float64 `json:"expectedRank"`
		PScore       float64 `json:"pScore"`
		PTopK        float64 `json:"pTopK"`
		PFirst       float64 `json:"pFirst"`
		Lo           int     `json:"lo"`
		Hi           int     `json:"hi"`
	} `json:"want"`
	WantOrder []int    `json:"wantOrder"`
	WantPOTH  *float64 `json:"wantPoth"` // nil = NaN expected (pool below the POTH minimum)
}

const (
	goldenProbTol = 1e-6
	goldenRankTol = 1e-6
)

func TestRankGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "rank_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []rankCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			p := winProbs(c.Mu, c.Sigma)
			got := computeRankStatsFrom(c.Mu, c.Sigma, p, c.K)
			if len(got) != len(c.Want) {
				t.Fatalf("len=%d want %d", len(got), len(c.Want))
			}
			for i, w := range c.Want {
				g := got[i]
				if math.Abs(g.ExpectedRank-w.ExpectedRank) > goldenRankTol {
					t.Errorf("[%d] expectedRank %v want %v", i, g.ExpectedRank, w.ExpectedRank)
				}
				for name, pair := range map[string][2]float64{
					"pScore": {g.PScore, w.PScore}, "pTopK": {g.PTopK, w.PTopK}, "pFirst": {g.PFirst, w.PFirst},
				} {
					if math.Abs(pair[0]-pair[1]) > goldenProbTol {
						t.Errorf("[%d] %s %v want %v", i, name, pair[0], pair[1])
					}
				}
				if g.Lo != w.Lo || g.Hi != w.Hi {
					t.Errorf("[%d] interval [%d,%d] want [%d,%d]", i, g.Lo, g.Hi, w.Lo, w.Hi)
				}
			}
			order := selectShortlist(got, c.Rule, c.K)
			if len(order) != len(c.WantOrder) {
				t.Fatalf("order len=%d want %d", len(order), len(c.WantOrder))
			}
			for i := range order {
				if order[i] != c.WantOrder[i] {
					t.Errorf("order %v want %v", order, c.WantOrder)
					break
				}
			}
			gotP := poth(p)
			switch {
			case c.WantPOTH == nil:
				if !math.IsNaN(gotP) {
					t.Errorf("poth %v want NaN", gotP)
				}
			case math.IsNaN(gotP) || math.Abs(gotP-*c.WantPOTH) > goldenProbTol:
				t.Errorf("poth %v want %v", gotP, *c.WantPOTH)
			}
		})
	}
}
