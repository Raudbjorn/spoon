package forge

import (
	"testing"
	"time"
)

// TestSelectDivergentBranch covers the five branch-choice policy outcomes
// documented on SelectDivergentBranch: default genuine work wins; else the
// newest genuine side branch; else default upstreamed work; else the newest
// upstreamed side branch; else the default branch with nothing ahead.
func TestSelectDivergentBranch(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		d    ForkDivergence
		want BranchSelection
	}{
		{
			name: "default genuine work wins over a more recent genuine side",
			d: ForkDivergence{
				Default: BranchDivergence{Name: "main", TipSHA: "d1", TipCommittedAt: base, AheadBy: 5, BehindBy: 2, UpstreamedPR: 0},
				Sides: []BranchDivergence{
					{Name: "feat", TipSHA: "s1", TipCommittedAt: base.Add(time.Hour), AheadBy: 3, BehindBy: 1, UpstreamedPR: 0},
				},
			},
			want: BranchSelection{Branch: "main", Ahead: 5, Behind: 2, TipSHA: "d1", Upstreamed: false, UpstreamedPR: 0, IsSide: false, NeedsREST: true},
		},
		{
			name: "newest genuine side wins, skipping a more recent but upstreamed side and an older genuine one",
			d: ForkDivergence{
				Default: BranchDivergence{Name: "main", TipSHA: "d1", TipCommittedAt: base, AheadBy: 0, BehindBy: 0, UpstreamedPR: 0},
				Sides: []BranchDivergence{
					{Name: "old-genuine", TipSHA: "s0", TipCommittedAt: base.Add(-time.Hour), AheadBy: 2, BehindBy: 0, UpstreamedPR: 0},
					{Name: "new-genuine", TipSHA: "s1", TipCommittedAt: base.Add(2 * time.Hour), AheadBy: 4, BehindBy: 1, UpstreamedPR: 0},
					{Name: "newer-upstreamed", TipSHA: "s2", TipCommittedAt: base.Add(3 * time.Hour), AheadBy: 6, BehindBy: 0, UpstreamedPR: 99},
				},
			},
			want: BranchSelection{Branch: "new-genuine", Ahead: 4, Behind: 1, TipSHA: "s1", Upstreamed: false, UpstreamedPR: 0, IsSide: true, NeedsREST: true},
		},
		{
			name: "default upstreamed work wins over a more recent upstreamed side",
			d: ForkDivergence{
				Default: BranchDivergence{Name: "main", TipSHA: "d1", TipCommittedAt: base, AheadBy: 5, BehindBy: 0, UpstreamedPR: 42},
				Sides: []BranchDivergence{
					{Name: "not-ahead", TipSHA: "s0", TipCommittedAt: base.Add(-time.Hour), AheadBy: 0, BehindBy: 0, UpstreamedPR: 0},
					{Name: "also-upstreamed", TipSHA: "s1", TipCommittedAt: base.Add(time.Hour), AheadBy: 3, BehindBy: 0, UpstreamedPR: 7},
				},
			},
			want: BranchSelection{Branch: "main", Ahead: 5, Behind: 0, TipSHA: "d1", Upstreamed: true, UpstreamedPR: 42, IsSide: false, NeedsREST: true},
		},
		{
			name: "newest upstreamed side wins when default has no work at all",
			d: ForkDivergence{
				Default: BranchDivergence{Name: "main", TipSHA: "d1", TipCommittedAt: base, AheadBy: 0, BehindBy: 0, UpstreamedPR: 0},
				Sides: []BranchDivergence{
					{Name: "old-upstreamed", TipSHA: "s0", TipCommittedAt: base.Add(-time.Hour), AheadBy: 2, BehindBy: 0, UpstreamedPR: 11},
					{Name: "new-upstreamed", TipSHA: "s1", TipCommittedAt: base.Add(time.Hour), AheadBy: 4, BehindBy: 1, UpstreamedPR: 22},
					{Name: "not-ahead", TipSHA: "s2", TipCommittedAt: base.Add(2 * time.Hour), AheadBy: 0, BehindBy: 0, UpstreamedPR: 0},
				},
			},
			want: BranchSelection{Branch: "new-upstreamed", Ahead: 4, Behind: 1, TipSHA: "s1", Upstreamed: true, UpstreamedPR: 22, IsSide: true, NeedsREST: true},
		},
		{
			name: "nothing ahead anywhere falls back to the default branch",
			d: ForkDivergence{
				Default: BranchDivergence{Name: "main", TipSHA: "d1", TipCommittedAt: base, AheadBy: 0, BehindBy: 3, UpstreamedPR: 0},
				Sides: []BranchDivergence{
					{Name: "idle", TipSHA: "s0", TipCommittedAt: base.Add(time.Hour), AheadBy: 0, BehindBy: 0, UpstreamedPR: 0},
				},
			},
			want: BranchSelection{Branch: "main", Ahead: 0, Behind: 3, TipSHA: "d1", Upstreamed: false, UpstreamedPR: 0, IsSide: false, NeedsREST: false},
		},
		{
			name: "nothing ahead anywhere with no side branches at all",
			d: ForkDivergence{
				Default: BranchDivergence{Name: "main", TipSHA: "d1", TipCommittedAt: base, AheadBy: 0, BehindBy: 0, UpstreamedPR: 0},
			},
			want: BranchSelection{Branch: "main", Ahead: 0, Behind: 0, TipSHA: "d1", Upstreamed: false, UpstreamedPR: 0, IsSide: false, NeedsREST: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectDivergentBranch(tt.d)
			if got != tt.want {
				t.Fatalf("SelectDivergentBranch() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestSelectDivergentBranch_TieStability verifies that when two side
// branches tie on TipCommittedAt, the one listed first in Sides wins --
// SelectDivergentBranch must sort stably rather than, say, alphabetically
// or by AheadBy.
func TestSelectDivergentBranch_TieStability(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	older := BranchDivergence{Name: "older", TipSHA: "s-older", TipCommittedAt: base.Add(-time.Hour), AheadBy: 1, UpstreamedPR: 0}
	tiedB := BranchDivergence{Name: "b", TipSHA: "s-b", TipCommittedAt: base, AheadBy: 2, UpstreamedPR: 0}
	tiedA := BranchDivergence{Name: "a", TipSHA: "s-a", TipCommittedAt: base, AheadBy: 3, UpstreamedPR: 0}
	def := BranchDivergence{Name: "main", TipSHA: "d1", TipCommittedAt: base, AheadBy: 0, BehindBy: 0, UpstreamedPR: 0}

	t.Run("b listed before a", func(t *testing.T) {
		d := ForkDivergence{Default: def, Sides: []BranchDivergence{older, tiedB, tiedA}}
		got := SelectDivergentBranch(d)
		if got.Branch != "b" || got.Ahead != 2 {
			t.Fatalf("SelectDivergentBranch() = %+v, want branch %q (tied first in input order)", got, "b")
		}
	})

	t.Run("a listed before b", func(t *testing.T) {
		d := ForkDivergence{Default: def, Sides: []BranchDivergence{older, tiedA, tiedB}}
		got := SelectDivergentBranch(d)
		if got.Branch != "a" || got.Ahead != 3 {
			t.Fatalf("SelectDivergentBranch() = %+v, want branch %q (tied first in input order)", got, "a")
		}
	})

	t.Run("input slice is not mutated", func(t *testing.T) {
		sides := []BranchDivergence{older, tiedB, tiedA}
		orig := append([]BranchDivergence(nil), sides...)
		_ = SelectDivergentBranch(ForkDivergence{Default: def, Sides: sides})
		for i := range sides {
			if sides[i] != orig[i] {
				t.Fatalf("SelectDivergentBranch mutated input Sides at index %d: got %+v, want %+v", i, sides[i], orig[i])
			}
		}
	})
}

// TestT2Data_IsFilesTruncated is a truth table over FilesTruncated,
// len(Diffs) vs CompareFilesCap, and FilesComplete -- including the
// pre-flag-rows fallback case: an old row (FilesComplete defaults to false
// because the field didn't exist when it was written) whose Diffs still
// sits at the cap must read as truncated.
func TestT2Data_IsFilesTruncated(t *testing.T) {
	tests := []struct {
		name           string
		filesTruncated bool
		diffCount      int
		filesComplete  bool
		want           bool
	}{
		{
			name:           "explicit flag set, under cap, not marked complete",
			filesTruncated: true,
			diffCount:      5,
			filesComplete:  false,
			want:           true,
		},
		{
			name:           "under cap, not truncated, not marked complete",
			filesTruncated: false,
			diffCount:      CompareFilesCap - 1,
			filesComplete:  false,
			want:           false,
		},
		{
			name:           "pre-flag-rows fallback: at cap, no flag, FilesComplete false",
			filesTruncated: false,
			diffCount:      CompareFilesCap,
			filesComplete:  false,
			want:           true,
		},
		{
			name:           "diff fallback confirmed complete despite sitting at cap",
			filesTruncated: false,
			diffCount:      CompareFilesCap,
			filesComplete:  true,
			want:           false,
		},
		{
			name:           "explicit flag dominates even when FilesComplete is true",
			filesTruncated: true,
			diffCount:      CompareFilesCap,
			filesComplete:  true,
			want:           true,
		},
		{
			name:           "under cap and marked complete",
			filesTruncated: false,
			diffCount:      CompareFilesCap - 1,
			filesComplete:  true,
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tw := T2Data{
				FilesTruncated: tt.filesTruncated,
				Diffs:          make([]FileDiff, tt.diffCount),
				FilesComplete:  tt.filesComplete,
			}
			if got := tw.IsFilesTruncated(); got != tt.want {
				t.Fatalf("IsFilesTruncated() = %v, want %v (filesTruncated=%v diffCount=%d filesComplete=%v)",
					got, tt.want, tt.filesTruncated, tt.diffCount, tt.filesComplete)
			}
		})
	}
}
