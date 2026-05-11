package heat

import "testing"

func TestNoveltyComponent_Zero(t *testing.T) {
	c := NoveltyComponent(0.0)
	if c.Name != "novelty" {
		t.Errorf("Name = %q, want %q", c.Name, "novelty")
	}
	if c.Points != 0 {
		t.Errorf("Points = %v, want 0", c.Points)
	}
	if c.Max != 5 {
		t.Errorf("Max = %v, want 5", c.Max)
	}
	if c.Raw != 0 {
		t.Errorf("Raw = %v, want 0", c.Raw)
	}
}

func TestNoveltyComponent_One(t *testing.T) {
	c := NoveltyComponent(1.0)
	if c.Points != 5 {
		t.Errorf("Points = %v, want 5", c.Points)
	}
	if c.Max != 5 {
		t.Errorf("Max = %v, want 5", c.Max)
	}
	if c.Raw != 1 {
		t.Errorf("Raw = %v, want 1", c.Raw)
	}
}

func TestNoveltyComponent_Half(t *testing.T) {
	c := NoveltyComponent(0.5)
	if !approxEqual(c.Points, 2.5, 0.001) {
		t.Errorf("Points = %v, want 2.5", c.Points)
	}
	if c.Max != 5 {
		t.Errorf("Max = %v, want 5", c.Max)
	}
}

func TestNoveltyComponent_NegativeClamps(t *testing.T) {
	c := NoveltyComponent(-0.1)
	if c.Points != 0 {
		t.Errorf("Points = %v, want 0 (clamped from negative)", c.Points)
	}
	if !approxEqual(c.Raw, -0.1, 0.001) {
		t.Errorf("Raw = %v, want -0.1", c.Raw)
	}
}

func TestNoveltyComponent_AboveOneClamps(t *testing.T) {
	c := NoveltyComponent(1.5)
	if c.Points != 5 {
		t.Errorf("Points = %v, want 5 (clamped from 1.5)", c.Points)
	}
	if !approxEqual(c.Raw, 1.5, 0.001) {
		t.Errorf("Raw = %v, want 1.5", c.Raw)
	}
}

func TestApplyNoveltyToScore_NoNovelty(t *testing.T) {
	hr := &HeatResult{Score: 42, NoveltyScore: 0}
	ApplyNoveltyToScore(hr)
	if hr.Score != 42 {
		t.Errorf("Score = %v, want 42 (unchanged)", hr.Score)
	}
}

func TestApplyNoveltyToScore_FullNovelty(t *testing.T) {
	hr := &HeatResult{Score: 50, NoveltyScore: 1.0}
	ApplyNoveltyToScore(hr)
	if !approxEqual(hr.Score, 55, 0.001) {
		t.Errorf("Score = %v, want 55", hr.Score)
	}
}

func TestApplyNoveltyToScore_HalfNovelty(t *testing.T) {
	hr := &HeatResult{Score: 50, NoveltyScore: 0.5}
	ApplyNoveltyToScore(hr)
	if !approxEqual(hr.Score, 52.5, 0.001) {
		t.Errorf("Score = %v, want 52.5", hr.Score)
	}
}

func TestApplyNoveltyToScore_Caps100(t *testing.T) {
	hr := &HeatResult{Score: 98, NoveltyScore: 1.0}
	ApplyNoveltyToScore(hr)
	if !approxEqual(hr.Score, 100, 0.001) {
		t.Errorf("Score = %v, want 100 (capped, not 103)", hr.Score)
	}
}

func TestApplyNoveltyToScore_NoveltyAboveOneClamps(t *testing.T) {
	hr := &HeatResult{Score: 50, NoveltyScore: 1.5}
	ApplyNoveltyToScore(hr)
	if !approxEqual(hr.Score, 55, 0.001) {
		t.Errorf("Score = %v, want 55 (novelty clamped to 1)", hr.Score)
	}
}

func TestApplyNoveltyToScore_NegativeNoveltyIsNoOp(t *testing.T) {
	hr := &HeatResult{Score: 42, NoveltyScore: -0.5}
	ApplyNoveltyToScore(hr)
	if hr.Score != 42 {
		t.Errorf("Score = %v, want 42 (no-op when novelty <= 0)", hr.Score)
	}
}

func TestApplyNoveltyToScore_Nil(t *testing.T) {
	// Must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ApplyNoveltyToScore(nil) panicked: %v", r)
		}
	}()
	ApplyNoveltyToScore(nil)
}

// TestApplyNoveltyToScore_DoesNotRescaleNonClustered pins the documented
// asymmetry: novelty is layered on top of percentile-based heat as a
// separate axis. Forks that didn't go through clustering (NoveltyScore == 0)
// are left unchanged; forks that did get exactly novelty * 5 added. The two
// distributions are NOT rescaled together — clustered forks may end up to
// +5 ahead of unclustered ones for that reason, and this asymmetry is the
// intentional cost of treating novelty as an additive axis rather than a
// percentile-adjusted signal. See novelty.go's ApplyNoveltyToScore doc.
func TestApplyNoveltyToScore_DoesNotRescaleNonClustered(t *testing.T) {
	unclustered := &HeatResult{Score: 50, NoveltyScore: 0}
	clustered := &HeatResult{Score: 50, NoveltyScore: 0.5}

	ApplyNoveltyToScore(unclustered)
	ApplyNoveltyToScore(clustered)

	if unclustered.Score != 50 {
		t.Errorf("unclustered Score = %v, want 50 (unchanged)", unclustered.Score)
	}
	// clustered gets exactly 0.5 * 5 = 2.5 added.
	if !approxEqual(clustered.Score, 52.5, 0.001) {
		t.Errorf("clustered Score = %v, want 52.5", clustered.Score)
	}
}
