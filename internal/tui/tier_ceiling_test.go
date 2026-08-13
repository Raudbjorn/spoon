package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/store"
)

// tierFakeForge is the minimal Forge the enrichment path touches. compareCalls
// counts real spends so a test can assert the ceiling prevented one.
type tierFakeForge struct {
	t2           map[string]forge.T2Data
	headroom     float64
	compareCalls int
	failIfCalled *testing.T
}

func (f *tierFakeForge) Auth(context.Context) (forge.AuthInfo, error) { return forge.AuthInfo{}, nil }
func (f *tierFakeForge) Parent(context.Context, string, string) (forge.ParentData, error) {
	return forge.ParentData{}, nil
}
func (f *tierFakeForge) ListForks(context.Context, string, string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg)
	close(ch)
	return ch, nil
}
func (f *tierFakeForge) Branches(context.Context, forge.T1Data, int) ([]forge.BranchRef, error) {
	return nil, nil
}
func (f *tierFakeForge) Compare(_ context.Context, fk forge.T1Data, _ string) (forge.T2Data, error) {
	f.compareCalls++
	if f.failIfCalled != nil {
		f.failIfCalled.Errorf("Compare was called for %s despite the ceiling forbidding it", fk.ID)
	}
	return f.t2[fk.ID], nil
}
func (f *tierFakeForge) Contributors(context.Context, forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}
func (f *tierFakeForge) Headroom() float64 { return f.headroom }

// A bare Model literal must not panic on a ceiling read -- most tests in this
// package build one, and tierCeiling is nil in all of them.
func TestMaxTier_NilCeilingDefaultsToThree(t *testing.T) {
	var m Model
	if got := m.maxTier(); got != defaultMaxTier {
		t.Errorf("maxTier() on a zero Model = %d, want %d", got, defaultMaxTier)
	}
	m2 := movementModel(3)
	if got := m2.maxTier(); got != 3 {
		t.Errorf("maxTier() = %d, want 3", got)
	}
	_ = m2.viewTable() // must not panic
}

func TestCycleMaxTier_WrapsThreeTwoOne(t *testing.T) {
	m := movementModel(3)
	for _, want := range []int{2, 1, 3, 2} {
		_, _ = m.handleTableKey("c")
		if got := m.maxTier(); got != want {
			t.Fatalf("after c: maxTier() = %d, want %d", got, want)
		}
	}
}

func TestUpdate_CKeyReachesTheTableHandler(t *testing.T) {
	m := movementModel(3)
	m.view = viewTable
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if got := updated.(*Model).maxTier(); got != 2 {
		t.Errorf("maxTier() after 'c' through Update = %d, want 2", got)
	}
}

func tierTestFork() forge.T1Data {
	return forge.T1Data{ID: "o/r", Owner: "o", Name: "r", DefaultBranch: "main"}
}

// At ceiling 1 the compare must be skipped, and the skip must be reported --
// a nil return would freeze enrichDone and block the cluster pipeline.
func TestCompareCmd_CeilingOneSkipsWithoutSpending(t *testing.T) {
	fake := &tierFakeForge{headroom: 1.0, failIfCalled: t}
	m := movementModel(1)
	m.provider = fake
	m.refresh = true // bypass the store fast path
	m.enrichCtx = context.Background()
	m.enrichSem = make(chan struct{}, 1)
	m.setMaxTier(1)

	msg := m.compareCmd(tierTestFork())().(tier2ResultMsg)
	if !msg.tierSkipped {
		t.Errorf("msg = %+v, want tierSkipped", msg)
	}
	if msg.budgetSkipped {
		t.Error("a ceiling skip must not be reported as a budget skip; they have different remedies")
	}
	if fake.compareCalls != 0 {
		t.Errorf("Compare called %d times at ceiling 1, want 0", fake.compareCalls)
	}
}

// Gate ordering: the store is consulted before the ceiling, so a cached
// compare still serves at T1. The ceiling limits spending, not display.
func TestCompareCmd_CachedCompareSurvivesCeilingOne(t *testing.T) {
	fake := &tierFakeForge{headroom: 1.0, failIfCalled: t}
	m := movementModel(1)
	m.provider = fake
	m.refresh = false
	m.enrichCtx = context.Background()
	m.enrichSem = make(chan struct{}, 1)
	m.setMaxTier(1)
	m.cached = tierCachedSnapshot(t, tierTestFork())

	msg := m.compareCmd(tierTestFork())().(tier2ResultMsg)
	if !msg.fromCache {
		t.Errorf("msg = %+v, want fromCache (a stored compare costs no budget and must serve at any ceiling)", msg)
	}
	if msg.tierSkipped {
		t.Error("cached compare was reported as tier-skipped")
	}
}

// Gate ordering the other way: at an allowed ceiling, the rate-reserve floor
// still applies and must report a budget skip, not a tier skip.
func TestCompareCmd_ReserveFloorStillAppliesAtCeilingTwo(t *testing.T) {
	fake := &tierFakeForge{headroom: 0.05, failIfCalled: t}
	m := movementModel(1)
	m.provider = fake
	m.refresh = true
	m.enrichCtx = context.Background()
	m.enrichSem = make(chan struct{}, 1)
	m.setMaxTier(2)

	msg := m.compareCmd(tierTestFork())().(tier2ResultMsg)
	if !msg.budgetSkipped {
		t.Errorf("msg = %+v, want budgetSkipped", msg)
	}
	if msg.tierSkipped {
		t.Error("a reserve-floor skip must not be reported as a ceiling skip")
	}
}

// The contract that keeps clustering reachable: a skip still advances the
// progress counter and clears the enriching flag.
func TestProcessPendingUpdates_TierSkipAdvancesProgress(t *testing.T) {
	m := movementModel(1)
	m.forks[0].Fork.ID = "o/r"
	m.forks[0].Enriching = true
	m.enriching = true
	m.enrichTotal = 1
	m.pendingUpdates = []tier2ResultMsg{{forkID: "o/r", tierSkipped: true}}

	m.processPendingUpdates()

	if m.enrichDone != 1 {
		t.Errorf("enrichDone = %d, want 1", m.enrichDone)
	}
	if m.forks[0].Enriching {
		t.Error("fork still marked Enriching after a tier skip")
	}
	if !m.forks[0].TierSkipped {
		t.Error("fork not marked TierSkipped")
	}
	if m.enriching {
		t.Error("enriching still true after the last update; the cluster pipeline would never start")
	}
}

// Raising the ceiling must re-dispatch exactly the forks that were skipped --
// not the enriched ones, and not the errored ones.
func TestReenrichPending_SelectsOnlySkippedForks(t *testing.T) {
	m := movementModel(4)
	m.provider = &tierFakeForge{headroom: 1.0}
	m.forks[0].Enriched = true      // done: skip
	m.forks[1].TierSkipped = true   // ceiling skip: retry
	m.forks[2].BudgetSkipped = true // budget skip: retry
	m.forks[3].Enriching = true     // in flight: skip
	m.setMaxTier(3)

	if cmd := m.reenrichPending(); cmd == nil {
		t.Fatal("reenrichPending returned nil with two retryable forks")
	}
	if m.enrichTotal != 2 {
		t.Errorf("enrichTotal = %d, want 2", m.enrichTotal)
	}
	if !m.forks[1].Enriching || !m.forks[2].Enriching {
		t.Error("skipped forks were not marked Enriching on re-dispatch")
	}
	if m.forks[1].TierSkipped || m.forks[2].BudgetSkipped {
		t.Error("skip flags must be cleared when the fork is re-dispatched")
	}
}

// Lowering to T2 must drop the lone-wolf component; raising back must restore
// it. Neither needs a network call.
func TestCycleMaxTier_TogglesLoneWolfScoring(t *testing.T) {
	m := tierScoredModel(t)
	if m.forks[0].Heat.LoneWolfV2 == nil {
		t.Fatal("fixture must start with lone-wolf scoring wired at T3")
	}
	t3Score := m.forks[0].Heat.Score

	_, _ = m.handleTableKey("c") // → T2
	if m.forks[0].Heat.LoneWolfV2 != nil {
		t.Error("lone-wolf still wired at ceiling T2")
	}
	if m.forks[0].Heat.Tier != 2 {
		t.Errorf("Heat.Tier = %d at ceiling T2, want 2", m.forks[0].Heat.Tier)
	}
	if m.forks[0].Heat.Score >= t3Score {
		t.Errorf("score at T2 (%v) should be below the T3 score (%v)", m.forks[0].Heat.Score, t3Score)
	}

	_, _ = m.handleTableKey("c") // → T1
	_, _ = m.handleTableKey("c") // → T3
	if m.forks[0].Heat.LoneWolfV2 == nil {
		t.Error("lone-wolf not restored at ceiling T3")
	}
}

// The regression that made this fix necessary: rescoring assigns a fresh
// HeatResult wholesale, which erased everything the cluster pipeline wrote in
// place -- so `g` reported "no clusters" on a repo that had them.
func TestCycleMaxTier_PreservesClusterFields(t *testing.T) {
	m := tierScoredModel(t)
	m.forks[0].Heat.ClusterID = "c0"
	m.forks[0].Heat.ClusterLabel = "label-a"
	m.forks[0].Heat.ClusterMemberCount = 3
	m.forks[0].Heat.NoveltyScore = 0.7
	m.forks[0].Heat.Category = "feature"

	_, _ = m.handleTableKey("c")
	_, _ = m.handleTableKey("c")

	h := m.forks[0].Heat
	if h.ClusterID != "c0" || h.ClusterLabel != "label-a" || h.ClusterMemberCount != 3 {
		t.Errorf("cluster identity lost across rescore: %+v", h)
	}
	if h.NoveltyScore != 0.7 {
		t.Errorf("NoveltyScore = %v, want 0.7", h.NoveltyScore)
	}
	if h.Category != "feature" {
		t.Errorf("Category = %q, want \"feature\"", h.Category)
	}
	if !m.hasClusterData() {
		t.Error("hasClusterData() false after rescore; the cluster view would report no clusters")
	}
}

func TestStatusBar_ShowsCeilingAndSkipCount(t *testing.T) {
	m := movementModel(3)
	if strings.Contains(m.renderStatusBar(), "T<=") {
		t.Error("ceiling shown at the default T3; it should be omitted when nothing is capped")
	}

	m.setMaxTier(1)
	m.forks[0].TierSkipped = true
	m.forks[1].TierSkipped = true
	bar := m.renderStatusBar()
	if !strings.Contains(bar, "T<=1") {
		t.Errorf("status bar missing the ceiling: %q", bar)
	}
	if !strings.Contains(bar, "2 skipped") {
		t.Errorf("status bar missing the skip count: %q", bar)
	}
}

// tierScoredModel builds a model whose single fork has real T2 commits, scored
// through the live scorer, so ceiling changes produce real score movement.
func tierScoredModel(t *testing.T) *Model {
	t.Helper()
	m := movementModel(1)
	m.scorer = heat.NewScorer(nil)
	m.parent = &forge.ParentData{FullName: "up/stream", DefaultBranch: "main",
		PushedAt: time.Now().Add(-24 * time.Hour)}

	now := time.Now()
	m.forks[0].T2 = &forge.T2Data{
		AheadCount:  6,
		BehindCount: 1,
		Performed:   true,
		Commits: []forge.AheadCommit{
			{SHA: "a1", AuthorLogin: "solo", AuthorEmail: "solo@example.com", Timestamp: now.Add(-72 * time.Hour), Message: "feat: one"},
			{SHA: "a2", AuthorLogin: "solo", AuthorEmail: "solo@example.com", Timestamp: now.Add(-48 * time.Hour), Message: "feat: two"},
			{SHA: "a3", AuthorLogin: "solo", AuthorEmail: "solo@example.com", Timestamp: now.Add(-24 * time.Hour), Message: "feat: three"},
		},
	}
	m.forks[0].Enriched = true
	m.recomputeT2Score(0)
	return m
}

// tierCachedSnapshot returns a RepoSnapshot whose ValidT2 hits for fk: the
// stored pushed_at must equal the live one, since compare validity is
// content-addressed rather than TTL-based.
func tierCachedSnapshot(t *testing.T, fk forge.T1Data) *store.RepoSnapshot {
	t.Helper()
	return store.NewRepoSnapshot([]store.CachedFork{{
		T1: fk,
		T2: &forge.T2Data{Performed: true, AheadCount: 4},
	}})
}
