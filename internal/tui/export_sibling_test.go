package tui

import (
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func div(ahead, files, adds, dels int, head string) *ExportDiv {
	return &ExportDiv{
		Ahead:        ahead,
		FilesChanged: files,
		Additions:    adds,
		Deletions:    dels,
		HeadSHA:      head,
	}
}

func TestAssignSiblingGroups_identicalDiffShapeIsGrouped(t *testing.T) {
	// Observed in the qvr/nonraid export: emtee40, ghenry22 and jsebean all
	// reported (10 ahead, 7 files, +11210, -0) — the same pre-restructure work,
	// listed three times as if independent.
	forks := []ExportFork{
		{FullName: "emtee40/nonraid", Divergence: div(10, 7, 11210, 0, ""), Heat: ExportHeat{Score: 26.4}},
		{FullName: "ghenry22/nonraid", Divergence: div(10, 7, 11210, 0, ""), Heat: ExportHeat{Score: 26.3}},
		{FullName: "jsebean/nonraid", Divergence: div(10, 7, 11210, 0, ""), Heat: ExportHeat{Score: 26.3}},
		{FullName: "iiLaurens/nonraid", Divergence: div(1, 2, 188, 73, ""), Heat: ExportHeat{Score: 26.1}},
	}

	AssignSiblingGroups(forks)

	for _, i := range []int{0, 1, 2} {
		if forks[i].SiblingCount != 3 {
			t.Errorf("%s: SiblingCount = %d, want 3", forks[i].FullName, forks[i].SiblingCount)
		}
	}
	if forks[0].SiblingGroup != forks[1].SiblingGroup || forks[1].SiblingGroup != forks[2].SiblingGroup {
		t.Error("the three identical forks did not share a sibling group")
	}

	// Highest score wins the primary slot.
	if !forks[0].SiblingPrimary {
		t.Error("emtee40 (highest score) should be the primary")
	}
	if forks[1].SiblingPrimary || forks[2].SiblingPrimary {
		t.Error("only one member of a group may be primary")
	}

	// The genuinely distinct fork is untouched.
	if forks[3].SiblingGroup != "" || forks[3].SiblingCount != 0 || forks[3].SiblingPrimary {
		t.Errorf("iiLaurens should not be grouped, got %+v", forks[3])
	}
}

func TestAssignSiblingGroups_headSHABeatsDiffShape(t *testing.T) {
	// Same head commit is proof of identical work; the shapes here differ, so
	// only the SHA can group them. The key must be SHA-derived.
	forks := []ExportFork{
		{FullName: "a/x", Divergence: div(3, 1, 10, 2, "deadbeef"), Heat: ExportHeat{Score: 1}},
		{FullName: "b/x", Divergence: div(9, 4, 99, 9, "deadbeef"), Heat: ExportHeat{Score: 2}},
	}

	AssignSiblingGroups(forks)

	if forks[0].SiblingGroup != "h:deadbeef" {
		t.Errorf("SiblingGroup = %q, want h:deadbeef", forks[0].SiblingGroup)
	}
	if !forks[1].SiblingPrimary {
		t.Error("b/x has the higher score and should be primary")
	}
}

func TestAssignSiblingGroups_inertForksAreNeverGrouped(t *testing.T) {
	// 10 of the 22 nonraid forks are unmodified mirrors. Grouping them would
	// produce one enormous meaningless bucket.
	forks := []ExportFork{
		{FullName: "a/x", Divergence: div(0, 0, 0, 0, "")},
		{FullName: "b/x", Divergence: div(0, 0, 0, 0, "")},
		{FullName: "c/x"}, // no compare ran at all
	}

	AssignSiblingGroups(forks)

	for _, f := range forks {
		if f.SiblingGroup != "" || f.SiblingCount != 0 {
			t.Errorf("%s: inert fork was grouped (%+v)", f.FullName, f)
		}
	}
}

func TestForgeT2ToExportDiv_carriesTriageSignal(t *testing.T) {
	// These are computed during T2 and feed the heat score, but used to be
	// dropped before reaching the export. Upstreamed in particular is what tells
	// a consumer the work is already merged and not worth reviewing.
	t2 := &forge.T2Data{
		AheadCount:         4,
		BehindCount:        2,
		MNA:                120,
		FeatureCommitRatio: 0.75,
		IsBranchWork:       true,
		ActiveBranch:       "feature/foo",
		Upstreamed:         true,
		UpstreamedPR:       115,
		BaseSHA:            "base123",
		HeadSHA:            "head456",
		Diffs: []forge.FileDiff{
			{Additions: 10, Deletions: 3},
			{Additions: 5, Deletions: 1},
		},
	}

	got := forgeT2ToExportDiv(t2)
	if got == nil {
		t.Fatal("forgeT2ToExportDiv returned nil")
	}

	checks := []struct {
		name      string
		got, want any
	}{
		{"Ahead", got.Ahead, 4},
		{"Behind", got.Behind, 2},
		{"FilesChanged", got.FilesChanged, 2},
		{"Additions", got.Additions, 15},
		{"Deletions", got.Deletions, 4},
		{"Upstreamed", got.Upstreamed, true},
		{"UpstreamedPR", got.UpstreamedPR, 115},
		{"MNA", got.MNA, 120},
		{"FeatureRatio", got.FeatureRatio, 0.75},
		{"IsBranchWork", got.IsBranchWork, true},
		{"ActiveBranch", got.ActiveBranch, "feature/foo"},
		{"BaseSHA", got.BaseSHA, "base123"},
		{"HeadSHA", got.HeadSHA, "head456"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestFormatOptionalTime_zeroBecomesEmpty(t *testing.T) {
	// A zero CreatedAt previously serialised as "0001-01-01T00:00:00Z", which
	// reads as real data. Paired with omitempty, the field now disappears.
	if got := formatOptionalTime(time.Time{}); got != "" {
		t.Errorf("zero time = %q, want empty", got)
	}
	when := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	if got := formatOptionalTime(when); got != "2026-08-02T12:00:00Z" {
		t.Errorf("formatOptionalTime() = %q", got)
	}
}
