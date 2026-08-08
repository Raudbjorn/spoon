package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func div(ahead, files, adds, dels int) *ExportDiv {
	return &ExportDiv{
		Ahead:        ahead,
		FilesChanged: files,
		Additions:    adds,
		Deletions:    dels,
	}
}

func TestAssignDuplicateGroups_identicalDiffShapeIsGrouped(t *testing.T) {
	// Observed in the qvr/nonraid export: emtee40, ghenry22 and jsebean all
	// carried the same pre-restructure commits (10 ahead, 7 files, +11210, -0),
	// listed three times as if independent. Shared commit identity confirms the
	// group; the equal diff shape alone would only make them candidates.
	sameWork := "c:aaaaaaaaaaaa"
	forks := []ExportFork{
		{FullName: "emtee40/nonraid", Divergence: div(10, 7, 11210, 0), Heat: ExportHeat{Score: 26.4}, workKey: sameWork},
		{FullName: "ghenry22/nonraid", Divergence: div(10, 7, 11210, 0), Heat: ExportHeat{Score: 26.3}, workKey: sameWork},
		{FullName: "jsebean/nonraid", Divergence: div(10, 7, 11210, 0), Heat: ExportHeat{Score: 26.3}, workKey: sameWork},
		{FullName: "iiLaurens/nonraid", Divergence: div(1, 2, 188, 73), Heat: ExportHeat{Score: 26.1}, workKey: "c:bbbbbbbbbbbb"},
	}

	AssignDuplicateGroups(forks)

	for _, i := range []int{0, 1, 2} {
		if forks[i].DuplicateCount != 3 {
			t.Errorf("%s: DuplicateCount = %d, want 3", forks[i].FullName, forks[i].DuplicateCount)
		}
	}
	if forks[0].DuplicateGroup != forks[1].DuplicateGroup || forks[1].DuplicateGroup != forks[2].DuplicateGroup {
		t.Error("the three identical forks did not share a duplicate group")
	}

	// Highest score wins the primary slot.
	if !forks[0].DuplicatePrimary {
		t.Error("emtee40 (highest score) should be the primary")
	}
	if forks[1].DuplicatePrimary || forks[2].DuplicatePrimary {
		t.Error("only one member of a group may be primary")
	}

	// The genuinely distinct fork is untouched.
	if forks[3].DuplicateGroup != "" || forks[3].DuplicateCount != 0 || forks[3].DuplicatePrimary {
		t.Errorf("iiLaurens should not be grouped, got %+v", forks[3])
	}
	if forks[3].DuplicateCandidate != "" {
		t.Errorf("iiLaurens has a different diff shape, DuplicateCandidate = %q, want empty", forks[3].DuplicateCandidate)
	}
}

func TestAssignDuplicateGroups_equalTotalsDifferentWorkNotGrouped(t *testing.T) {
	// CodeRabbit's false-positive scenario: two forks whose diffs total the same
	// (ahead, files, adds, dels) but whose commit sets differ. They must not be
	// confirmed duplicates — no group, no count, no primary that a consumer
	// could fold on. The shape match survives only as a candidate signal.
	forks := []ExportFork{
		{FullName: "alice/x", Divergence: div(3, 5, 200, 40), Heat: ExportHeat{Score: 10}, workKey: "c:aaaaaaaaaaaa"},
		{FullName: "bob/x", Divergence: div(3, 5, 200, 40), Heat: ExportHeat{Score: 9}, workKey: "c:bbbbbbbbbbbb"},
	}

	AssignDuplicateGroups(forks)

	for _, f := range forks {
		if f.DuplicateGroup != "" || f.DuplicateCount != 0 || f.DuplicatePrimary {
			t.Errorf("%s: equal totals with different work was treated as a duplicate (%+v)", f.FullName, f)
		}
	}
	if forks[0].DuplicateCandidate == "" || forks[0].DuplicateCandidate != forks[1].DuplicateCandidate {
		t.Errorf("shape match should set a shared candidate key, got %q / %q",
			forks[0].DuplicateCandidate, forks[1].DuplicateCandidate)
	}
}

func TestAssignDuplicateGroups_noIdentityIsCandidateOnly(t *testing.T) {
	// Without commit identity (empty workKey — commits missing or truncated),
	// an identical diff shape is a signal, never proof: candidate only.
	forks := []ExportFork{
		{FullName: "carol/x", Divergence: div(10, 7, 11210, 0), Heat: ExportHeat{Score: 20}},
		{FullName: "dave/x", Divergence: div(10, 7, 11210, 0), Heat: ExportHeat{Score: 19}},
	}

	AssignDuplicateGroups(forks)

	for _, f := range forks {
		if f.DuplicateGroup != "" || f.DuplicateCount != 0 || f.DuplicatePrimary {
			t.Errorf("%s: shape-only match set confirmed-duplicate fields (%+v)", f.FullName, f)
		}
		if f.DuplicateCandidate == "" {
			t.Errorf("%s: shape-only match should set DuplicateCandidate", f.FullName)
		}
	}
}

func TestWorkIdentityKey(t *testing.T) {
	commits := func(shas ...string) []forge.AheadCommit {
		cs := make([]forge.AheadCommit, len(shas))
		for i, s := range shas {
			cs[i] = forge.AheadCommit{SHA: s}
		}
		return cs
	}

	if got := workIdentityKey(nil); got != "" {
		t.Errorf("nil T2 = %q, want empty", got)
	}
	if got := workIdentityKey(&forge.T2Data{AheadCount: 3}); got != "" {
		t.Errorf("no commits = %q, want empty", got)
	}
	// Truncated list (e.g. GitHub caps compare commits at 250): last commit is
	// not the head and the set is incomplete — no identity.
	if got := workIdentityKey(&forge.T2Data{AheadCount: 300, Commits: commits("a", "b")}); got != "" {
		t.Errorf("truncated commit list = %q, want empty", got)
	}

	k1 := workIdentityKey(&forge.T2Data{AheadCount: 2, Commits: commits("a1", "b2")})
	k2 := workIdentityKey(&forge.T2Data{AheadCount: 2, Commits: commits("b2", "a1")})
	k3 := workIdentityKey(&forge.T2Data{AheadCount: 2, Commits: commits("a1", "c3")})

	if k1 == "" || !strings.HasPrefix(k1, "c:") {
		t.Errorf("complete list should yield a \"c:\"-prefixed key, got %q", k1)
	}
	if k1 != k2 {
		t.Errorf("same SHA set in different order: %q != %q", k1, k2)
	}
	if k1 == k3 {
		t.Errorf("different SHA sets produced the same key %q", k1)
	}
}

func TestAssignDuplicateGroups_inertForksAreNeverGrouped(t *testing.T) {
	// 10 of the 22 nonraid forks are unmodified mirrors. Grouping them would
	// produce one enormous meaningless bucket.
	forks := []ExportFork{
		{FullName: "a/x", Divergence: div(0, 0, 0, 0)},
		{FullName: "b/x", Divergence: div(0, 0, 0, 0)},
		{FullName: "c/x"}, // no compare ran at all
	}

	AssignDuplicateGroups(forks)

	for _, f := range forks {
		if f.DuplicateGroup != "" || f.DuplicateCount != 0 || f.DuplicateCandidate != "" {
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
