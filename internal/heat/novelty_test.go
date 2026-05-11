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
