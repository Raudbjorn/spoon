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

func TestAssignDuplicateGroups_identicalDiffShapeIsCandidateOnly(t *testing.T) {
	// Observed in the qvr/nonraid export: emtee40, ghenry22 and jsebean all
	// reported (10 ahead, 7 files, +11210, -0) — the same pre-restructure work,
	// listed three times as if independent. Without commit identity (no head
	// SHA, no fingerprint) the equal shape is a signal, never proof: candidate
	// tag only, no group, no primary a consumer could fold on.
	m := &Model{forks: []ScoredFork{
		sfDiv("emtee40/nonraid", 26.4, 10, 7, 11210, 0, "", ""),
		sfDiv("ghenry22/nonraid", 26.3, 10, 7, 11210, 0, "", ""),
		sfDiv("jsebean/nonraid", 26.3, 10, 7, 11210, 0, "", ""),
		sfDiv("iiLaurens/nonraid", 26.1, 1, 2, 188, 73, "", ""),
	}}

	m.assignDuplicateGroups()

	for _, i := range []int{0, 1, 2} {
		f := m.forks[i]
		if f.SiblingGroup != "" || f.SiblingCount != 0 || f.SiblingPrimary {
			t.Errorf("%s: shape-only match set confirmed-group fields (%+v)", f.Fork.ID, f)
		}
		if f.SiblingCandidate == "" || f.SiblingCandidate != m.forks[0].SiblingCandidate {
			t.Errorf("%s: SiblingCandidate = %q, want shared non-empty key", f.Fork.ID, f.SiblingCandidate)
		}
	}
	if m.forks[3].SiblingCandidate != "" {
		t.Errorf("iiLaurens has a different shape, SiblingCandidate = %q, want empty", m.forks[3].SiblingCandidate)
	}
}

func TestAssignDuplicateGroups_equalTotalsDifferentWorkNotGrouped(t *testing.T) {
	// The false-positive scenario: two forks whose diffs total the same
	// (ahead, files, adds, dels) but whose head SHAs differ. They must not be
	// confirmed duplicates — no group, no count, no primary — and the shape
	// match survives only as a candidate signal.
	m := &Model{forks: []ScoredFork{
		sfDiv("alice/x", 10, 3, 1, 200, 40, "sha-alice", ""),
		sfDiv("bob/x", 9, 3, 1, 200, 40, "sha-bob", ""),
	}}

	m.assignDuplicateGroups()

	for _, f := range m.forks {
		if f.SiblingGroup != "" || f.SiblingCount != 0 || f.SiblingPrimary {
			t.Errorf("%s: equal totals with different work was treated as a duplicate (%+v)", f.Fork.ID, f)
		}
	}
	if m.forks[0].SiblingCandidate == "" || m.forks[0].SiblingCandidate != m.forks[1].SiblingCandidate {
		t.Errorf("shape match should set a shared candidate key, got %q / %q",
			m.forks[0].SiblingCandidate, m.forks[1].SiblingCandidate)
	}
}

func TestAssignDuplicateGroups_sharedHeadSHAIsConfirmed(t *testing.T) {
	// Commit identity present and equal: the full trio applies, and the
	// highest-scoring member is the primary.
	m := &Model{forks: []ScoredFork{
		sfDiv("emtee40/nonraid", 26.4, 10, 7, 11210, 0, "shared-head", ""),
		sfDiv("ghenry22/nonraid", 26.3, 10, 7, 11210, 0, "shared-head", ""),
		sfDiv("jsebean/nonraid", 26.3, 10, 7, 11210, 0, "shared-head", ""),
	}}

	m.assignDuplicateGroups()

	for _, f := range m.forks {
		if f.SiblingCount != 3 || f.SiblingGroup != "h:shared-head" {
			t.Errorf("%s: group/count = %q/%d, want h:shared-head/3", f.Fork.ID, f.SiblingGroup, f.SiblingCount)
		}
	}
	if !m.forks[0].SiblingPrimary {
		t.Error("emtee40 (highest score) should be the primary")
	}
	if m.forks[1].SiblingPrimary || m.forks[2].SiblingPrimary {
		t.Error("only one member of a group may be primary")
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
		sfDiv("a/x", 2, 10, 7, 11210, 0, "shared-head", ""),
		sfDiv("b/x", 1, 10, 7, 11210, 0, "shared-head", ""),
	}}
	m.assignDuplicateGroups()
	if m.forks[0].SiblingCount != 2 {
		t.Fatalf("expected an initial group, got %d", m.forks[0].SiblingCount)
	}

	// b/x turns out to have distinct work once its fingerprint arrives (the
	// fingerprint outranks the compared branch's head SHA).
	m.forks[1].Fork.BranchFingerprint = "unique"
	m.assignDuplicateGroups()

	for i, f := range m.forks {
		if f.SiblingGroup != "" || f.SiblingCount != 0 || f.SiblingPrimary {
			t.Errorf("fork %d kept a stale tag: %+v", i, f)
		}
	}
	// The equal diff shape still stands as a candidate signal — it is
	// independent of the identity tier and was never proof to begin with.
	if m.forks[0].SiblingCandidate == "" || m.forks[0].SiblingCandidate != m.forks[1].SiblingCandidate {
		t.Errorf("candidate signal lost on re-assignment: %q / %q",
			m.forks[0].SiblingCandidate, m.forks[1].SiblingCandidate)
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
