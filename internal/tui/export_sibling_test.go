package tui

import (
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

func sfDiv(id string, score float64, ahead, files, adds, dels int, head, fingerprint string) ScoredFork {
	diffs := make([]forge.FileDiff, files)
	if files > 0 {
		diffs[0] = forge.FileDiff{Additions: adds, Deletions: dels}
	}
	return ScoredFork{
		Fork: forge.T1Data{ID: id, BranchFingerprint: fingerprint},
		Heat: heat.HeatResult{Score: score},
		T2:   &forge.T2Data{Performed: true, AheadCount: ahead, HeadSHA: head, Diffs: diffs},
	}
}

func TestAssignDuplicateGroups_identicalDiffShapeIsGrouped(t *testing.T) {
	// Observed in the qvr/nonraid export: emtee40, ghenry22 and jsebean all
	// reported (10 ahead, 7 files, +11210, -0) — the same pre-restructure work,
	// listed three times as if independent.
	m := &Model{forks: []ScoredFork{
		sfDiv("emtee40/nonraid", 26.4, 10, 7, 11210, 0, "", ""),
		sfDiv("ghenry22/nonraid", 26.3, 10, 7, 11210, 0, "", ""),
		sfDiv("jsebean/nonraid", 26.3, 10, 7, 11210, 0, "", ""),
		sfDiv("iiLaurens/nonraid", 26.1, 1, 2, 188, 73, "", ""),
	}}

	m.assignDuplicateGroups()

	for _, i := range []int{0, 1, 2} {
		if m.forks[i].SiblingCount != 3 {
			t.Errorf("%s: SiblingCount = %d, want 3", m.forks[i].Fork.ID, m.forks[i].SiblingCount)
		}
	}
	if m.forks[0].SiblingGroup != m.forks[1].SiblingGroup || m.forks[1].SiblingGroup != m.forks[2].SiblingGroup {
		t.Error("the three identical forks did not share a group")
	}
	if !m.forks[0].SiblingPrimary {
		t.Error("emtee40 (highest score) should be the primary")
	}
	if m.forks[1].SiblingPrimary || m.forks[2].SiblingPrimary {
		t.Error("only one member of a group may be primary")
	}
	if m.forks[3].SiblingGroup != "" || m.forks[3].SiblingCount != 0 {
		t.Errorf("iiLaurens should not be grouped, got %+v", m.forks[3])
	}
}

func TestAssignDuplicateGroups_keyPrecedence(t *testing.T) {
	// Fingerprint outranks head SHA, which outranks diff shape. Each key is
	// strictly more conclusive than the next.
	t.Run("fingerprint beats everything", func(t *testing.T) {
		m := &Model{forks: []ScoredFork{
			sfDiv("a/x", 1, 3, 1, 10, 2, "sha-a", "fp1"),
			sfDiv("b/x", 2, 9, 4, 99, 9, "sha-b", "fp1"),
		}}
		m.assignDuplicateGroups()
		if m.forks[0].SiblingGroup != "f:fp1" {
			t.Errorf("group = %q, want f:fp1", m.forks[0].SiblingGroup)
		}
		if !m.forks[1].SiblingPrimary {
			t.Error("b/x has the higher score and should be primary")
		}
	})

	t.Run("head SHA beats diff shape", func(t *testing.T) {
		// Shapes differ, so only the shared SHA can group these.
		m := &Model{forks: []ScoredFork{
			sfDiv("a/x", 1, 3, 1, 10, 2, "deadbeef", ""),
			sfDiv("b/x", 2, 9, 4, 99, 9, "deadbeef", ""),
		}}
		m.assignDuplicateGroups()
		if m.forks[0].SiblingGroup != "h:deadbeef" {
			t.Errorf("group = %q, want h:deadbeef", m.forks[0].SiblingGroup)
		}
	})

	t.Run("a fork with only a fingerprint still groups", func(t *testing.T) {
		// The sweep can land before T2 does.
		m := &Model{forks: []ScoredFork{
			{Fork: forge.T1Data{ID: "a/x", BranchFingerprint: "fp9"}},
			{Fork: forge.T1Data{ID: "b/x", BranchFingerprint: "fp9"}},
		}}
		m.assignDuplicateGroups()
		if m.forks[0].SiblingCount != 2 {
			t.Errorf("SiblingCount = %d, want 2 — fingerprint needs no T2", m.forks[0].SiblingCount)
		}
	})
}

func TestAssignDuplicateGroups_inertForksAreNeverGrouped(t *testing.T) {
	// 10 of the 22 nonraid forks are unmodified mirrors. Grouping them would
	// produce one enormous meaningless bucket.
	m := &Model{forks: []ScoredFork{
		{Fork: forge.T1Data{ID: "a/x"}, T2: &forge.T2Data{Performed: true, AheadCount: 0}},
		{Fork: forge.T1Data{ID: "b/x"}, T2: &forge.T2Data{Performed: true, AheadCount: 0}},
		{Fork: forge.T1Data{ID: "c/x"}}, // no compare ran at all
	}}

	m.assignDuplicateGroups()

	for _, f := range m.forks {
		if f.SiblingGroup != "" || f.SiblingCount != 0 {
			t.Errorf("%s: inert fork was grouped (%+v)", f.Fork.ID, f)
		}
	}
}

// Re-assignment must clear stale tags: a fork's key sharpens as the sweep and
// enrichment land, and a group formed under the weaker key must not persist.
func TestAssignDuplicateGroups_isIdempotentAndClearsStaleTags(t *testing.T) {
	m := &Model{forks: []ScoredFork{
		sfDiv("a/x", 2, 10, 7, 11210, 0, "", ""),
		sfDiv("b/x", 1, 10, 7, 11210, 0, "", ""),
	}}
	m.assignDuplicateGroups()
	if m.forks[0].SiblingCount != 2 {
		t.Fatalf("expected an initial group, got %d", m.forks[0].SiblingCount)
	}

	// b/x turns out to have distinct work once its fingerprint arrives.
	m.forks[1].Fork.BranchFingerprint = "unique"
	m.assignDuplicateGroups()

	for i, f := range m.forks {
		if f.SiblingGroup != "" || f.SiblingCount != 0 || f.SiblingPrimary {
			t.Errorf("fork %d kept a stale tag: %+v", i, f)
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
