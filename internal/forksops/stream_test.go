package forksops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/priors"
)

type fakeForge struct {
	parent        forge.ParentData
	parentErr     error
	forks         []forge.T1Data
	forkErrors    map[string]error
	t2            map[string]forge.T2Data
	t3            map[string]forge.T3Data
	contribErrors map[string]error

	// Test seams (optional; zero values preserve prior behavior):
	concurrency  int       // 0 → default 2
	compareOrder *[]string // when non-nil, Compare appends fk.ID (dispatch order)
	headroom     *float64  // nil → 1.0 (full); set below ReserveHeadroom to trip the floor
}

func (f *fakeForge) Auth(_ context.Context) (forge.AuthInfo, error) {
	conc := 2
	if f.concurrency > 0 {
		conc = f.concurrency
	}
	return forge.AuthInfo{Tier: forge.AuthCLI, Concurrency: conc}, nil
}
func (f *fakeForge) Parent(_ context.Context, _, _ string) (forge.ParentData, error) {
	return f.parent, f.parentErr
}
func (f *fakeForge) ListForks(_ context.Context, _, _ string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, len(f.forks))
	for _, fk := range f.forks {
		ch <- forge.ForkMsg{Fork: fk}
	}
	close(ch)
	return ch, nil
}

func (f *fakeForge) Branches(_ context.Context, fk forge.T1Data, _ int) ([]forge.BranchRef, error) {
	return []forge.BranchRef{{Name: fk.DefaultBranch}}, nil
}
func (f *fakeForge) Compare(_ context.Context, fk forge.T1Data, _ string) (forge.T2Data, error) {
	if f.compareOrder != nil {
		*f.compareOrder = append(*f.compareOrder, fk.ID)
	}
	if err, ok := f.forkErrors[fk.ID]; ok {
		return forge.T2Data{}, err
	}
	return f.t2[fk.ID], nil
}
func (f *fakeForge) Contributors(_ context.Context, fk forge.T1Data) (forge.T3Data, error) {
	if err, ok := f.contribErrors[fk.ID]; ok {
		return forge.T3Data{}, err
	}
	return f.t3[fk.ID], nil
}
func (f *fakeForge) Headroom() float64 {
	if f.headroom != nil {
		return *f.headroom
	}
	return 1.0
}

// batchFakeForge wraps *fakeForge to optionally implement
// forge.BatchCompareProvider and forge.ResolvedCompareProvider. A plain
// *fakeForge deliberately does NOT implement either interface -- several
// existing tests above rely on Stream falling back to the no-batch path
// when the provider lacks the capability. Wrapping (rather than adding the
// methods to fakeForge directly) keeps that behavior intact while letting
// batch-specific tests opt in by constructing a batchFakeForge instead.
type batchFakeForge struct {
	*fakeForge

	batch      map[string]forge.ForkDivergence
	batchErr   error
	batchStats forge.BatchStats
	batchCalls int // count of BatchCompare invocations

	// resolvedCalls, when non-nil, has "<forkID>@<branch>" appended for
	// every CompareResolved call, so a test can assert both which forks
	// were resolved via the batch-chosen-branch path and which branch was
	// selected for each.
	resolvedCalls *[]string
	// resolvedErr, keyed by fork ID, makes CompareResolved fail without
	// also making the fallback fakeForge.Compare fail (fakeForge.Compare
	// has its own, separate forkErrors map).
	resolvedErr map[string]error
}

func (f *batchFakeForge) BatchCompare(_ context.Context, _ []forge.T1Data) (map[string]forge.ForkDivergence, forge.BatchStats, error) {
	f.batchCalls++
	if f.batchErr != nil {
		return f.batch, f.batchStats, f.batchErr
	}
	return f.batch, f.batchStats, nil
}

func (f *batchFakeForge) CompareResolved(_ context.Context, fk forge.T1Data, sel forge.BranchSelection) (forge.T2Data, error) {
	if f.resolvedCalls != nil {
		*f.resolvedCalls = append(*f.resolvedCalls, fmt.Sprintf("%s@%s", fk.ID, sel.Branch))
	}
	if err, ok := f.resolvedErr[fk.ID]; ok {
		return forge.T2Data{}, err
	}
	return f.t2[fk.ID], nil
}

var (
	_ forge.Forge                   = (*batchFakeForge)(nil)
	_ forge.BatchCompareProvider    = (*batchFakeForge)(nil)
	_ forge.ResolvedCompareProvider = (*batchFakeForge)(nil)
)

func TestStream_dispatchesByPriorityNotSurfaceScore(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	parentPushed := now.Add(-30 * 24 * time.Hour)
	var order []string
	ff := &fakeForge{
		parent:       forge.ParentData{DefaultBranch: "main", PushedAt: parentPushed},
		concurrency:  1, // serialize so compareOrder == dispatch order
		compareOrder: &order,
		// Listed popular-stale first to prove dispatch reorders by promise:
		forks: []forge.T1Data{
			// many stars, but newest branch predates upstream → low promise
			{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", Stars: 9000, PushedAt: now.Add(-24 * time.Hour),
				Branches: []forge.BranchRef{{Name: "main", CommittedDate: parentPushed.Add(-10 * 24 * time.Hour)}}},
			// no stars, but a branch with commits 29d after upstream → highest promise
			{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", Stars: 0, PushedAt: now.Add(-24 * time.Hour),
				Branches: []forge.BranchRef{{Name: "feat", CommittedDate: now.Add(-1 * 24 * time.Hour)}}},
			// no stars, branch 25d after upstream → second
			{ID: "o/c", Owner: "o", Name: "c", DefaultBranch: "main", Stars: 0, PushedAt: now.Add(-24 * time.Hour),
				Branches: []forge.BranchRef{{Name: "feat", CommittedDate: now.Add(-5 * 24 * time.Hour)}}},
		},
		t2: map[string]forge.T2Data{"o/a": {AheadCount: 1}, "o/b": {AheadCount: 1}, "o/c": {AheadCount: 1}},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 compares, got %v", order)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	// The divergent (post-upstream-branch) forks must be compared before the
	// popular-but-stale one — best-first by promise, not by stars/surface score.
	if pos["o/a"] > pos["o/b"] || pos["o/c"] > pos["o/b"] {
		t.Errorf("popular-stale fork o/b should be dispatched after the divergent ones; order=%v", order)
	}
	if order[0] != "o/a" {
		t.Errorf("highest-promise fork o/a should be compared first; order=%v", order)
	}
}

func TestStream_reserveFloor_marksBudgetSkipNotZero(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	low := 0.05 // below ReserveHeadroom (0.10) → floor trips
	var order []string
	ff := &fakeForge{
		parent:       forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		concurrency:  1,
		compareOrder: &order,
		headroom:     &low,
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
			{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
		},
		t2: map[string]forge.T2Data{"o/a": {AheadCount: 7}, "o/b": {AheadCount: 3}},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for r := range ch {
		n++
		if r.T2 != nil {
			t.Errorf("%s enriched despite reserve floor", r.Fork.ID)
		}
		if r.BudgetSkip == nil {
			t.Errorf("%s should be marked BudgetSkip when below the reserve", r.Fork.ID)
		}
	}
	if n != 2 {
		t.Fatalf("expected 2 forks emitted, got %d", n)
	}
	if len(order) != 0 {
		t.Errorf("no Compare call should happen below the reserve; got %v", order)
	}
}

func TestStream_reserveDisabled_drainsBelowFloor(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	low := 0.02
	ff := &fakeForge{
		parent:   forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		headroom: &low,
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
		},
		t2: map[string]forge.T2Data{"o/a": {AheadCount: 7}},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, ReserveDisabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for r := range ch {
		if r.BudgetSkip != nil {
			t.Errorf("ReserveDisabled must bypass the floor; %s was skipped", r.Fork.ID)
		}
		if r.T2 == nil {
			t.Errorf("%s should be enriched when the reserve is disabled", r.Fork.ID)
		}
	}
}

func TestEstimateRequests(t *testing.T) {
	if got := EstimateRequests(10, 2); got != 30 {
		t.Errorf("tier2: got %d want 30", got)
	}
	if got := EstimateRequests(10, 3); got != 50 {
		t.Errorf("tier3: got %d want 50", got)
	}
	if got := EstimateRequests(10, 1); got != 0 {
		t.Errorf("tier1 needs no enrichment requests: got %d", got)
	}
}

// TestBatchEstimate_ContributorsCostAtTier3 pins batchEstimate's request
// count for a fixed fork mix at tier 2 and tier 3. A batch-resolved fork
// (zero-ahead or divergent) skips or shrinks its T2 compare cost, but the
// worker's tier-3 Contributors call still runs for it -- `enrich` does not
// depend on how T2 was obtained -- so the tier-3 estimate must add that
// share for every resolved fork, not just the ones the batch left
// unresolved.
func TestBatchEstimate_ContributorsCostAtTier3(t *testing.T) {
	pending := make([]forge.T1Data, 0, 105)
	divergence := make(map[string]forge.ForkDivergence, 100)
	for i := 0; i < 90; i++ {
		id := fmt.Sprintf("o/zero%d", i)
		pending = append(pending, forge.T1Data{ID: id})
		divergence[id] = forge.ForkDivergence{Resolved: true, Default: forge.BranchDivergence{AheadBy: 0}}
	}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("o/div%d", i)
		pending = append(pending, forge.T1Data{ID: id})
		divergence[id] = forge.ForkDivergence{Resolved: true, Default: forge.BranchDivergence{AheadBy: 2}}
	}
	for i := 0; i < 5; i++ {
		// Deliberately absent from divergence: unresolved by the batch.
		pending = append(pending, forge.T1Data{ID: fmt.Sprintf("o/unresolved%d", i)})
	}

	divergent, resolved, tier2Estimate := batchEstimate(pending, divergence, 2)
	if divergent != 10 {
		t.Errorf("divergent = %d, want 10", divergent)
	}
	if resolved != 100 {
		t.Errorf("resolved = %d, want 100", resolved)
	}
	// tier 2: no Contributors call at all, so a batch-resolved fork
	// (zero-ahead or divergent) never pays the T3 share. 10 divergent * 1
	// (CompareResolved) + 5 unresolved * perRequestCost(2)=3.
	if tier2Estimate != 25 {
		t.Errorf("tier2Estimate = %d, want 25 (10 divergent + 5*3 unresolved)", tier2Estimate)
	}

	_, _, tier3Estimate := batchEstimate(pending, divergence, 3)
	// tier 3: every resolved fork (all 100) still pays a Contributors call
	// -- perRequestCost(3)-perRequestCost(2) = 2 -- on top of the 10
	// divergent compares; unresolved forks pay the full perRequestCost(3)=5
	// each (already includes their own T3 share). 10 + 100*2 + 5*5 = 235.
	if tier3Estimate != 235 {
		t.Errorf("tier3Estimate = %d, want 235 (10 divergent + 100*2 contributors + 5*5 unresolved)", tier3Estimate)
	}
}

func TestStream_shortlistTruncatesAndRanks(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
			{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
			{ID: "o/c", Owner: "o", Name: "c", DefaultBranch: "main", PushedAt: now},
			{ID: "o/d", Owner: "o", Name: "d", DefaultBranch: "main", PushedAt: now},
		},
		t2: map[string]forge.T2Data{
			"o/a": {AheadCount: 1, MNA: 500, Diffs: []forge.FileDiff{{Path: "a.go", Additions: 500}}},
			"o/b": {AheadCount: 1, MNA: 5},
			"o/c": {AheadCount: 1, MNA: 50},
			"o/d": {AheadCount: 1, MNA: 1},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, ShortlistN: 2})
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("shortlist should emit 2, got %d", len(got))
	}
	for i, r := range got {
		if r.ExpectedRank <= 0 {
			t.Errorf("result %d missing expectedRank", i)
		}
	}
	if got[0].ExpectedRank > got[1].ExpectedRank {
		t.Errorf("shortlist not sorted by expected rank ascending: %v, %v", got[0].ExpectedRank, got[1].ExpectedRank)
	}
	for i, r := range got {
		if r.Rank == nil {
			t.Fatalf("result %d missing Rank stats", i)
		}
		if r.Rank.ExpectedRank != r.ExpectedRank {
			t.Errorf("result %d Rank.ExpectedRank %v != ExpectedRank %v", i, r.Rank.ExpectedRank, r.ExpectedRank)
		}
		// Pool was 4 forks: PScore = (4 − E)/3 by the identity.
		if want := (4 - r.ExpectedRank) / 3; math.Abs(r.Rank.PScore-want) > 1e-12 {
			t.Errorf("result %d PScore %v want %v", i, r.Rank.PScore, want)
		}
		if r.Rank.Lo < 1 || r.Rank.Hi > 4 || r.Rank.Lo > r.Rank.Hi {
			t.Errorf("result %d bad rank interval [%d,%d]", i, r.Rank.Lo, r.Rank.Hi)
		}
		if r.Rank.PTopK <= 0 || r.Rank.PTopK > 1 {
			t.Errorf("result %d PTopK %v out of (0,1]", i, r.Rank.PTopK)
		}
	}
}

func TestStream_noShortlistLeavesRankNil(t *testing.T) {
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 1})
	if err != nil {
		t.Fatal(err)
	}
	for r := range ch {
		if r.Rank != nil {
			t.Errorf("Rank should be nil without ShortlistN, got %+v", r.Rank)
		}
	}
}

func TestStream_emitsAllForks(t *testing.T) {
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
			{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now()},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 1})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	count := 0
	for r := range ch {
		if r.Err != nil {
			t.Errorf("per-fork err: %+v", r.Err)
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 results, got %d", count)
	}
}

func TestStream_perForkError_continuesStream(t *testing.T) {
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now(), DefaultBranch: "main"},
			{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now(), DefaultBranch: "main"},
		},
		forkErrors: map[string]error{"o/a": errors.New("compare failed")},
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	var errs, ok int
	for r := range ch {
		if r.Err != nil {
			errs++
		} else {
			ok++
		}
	}
	if errs != 1 || ok != 1 {
		t.Errorf("expected 1 err + 1 ok, got %d/%d", errs, ok)
	}
}

func TestStream_parentErr_isFatal(t *testing.T) {
	ff := &fakeForge{parentErr: errors.New("nope")}
	_, err := Stream(context.Background(), ff, "o", "r", Options{})
	if err == nil {
		t.Error("expected fatal error when Parent fails")
	}
}

func TestStream_ghostForksFiltered(t *testing.T) {
	pushedAt := time.Now().Add(-1 * time.Hour)
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: pushedAt},
		forks: []forge.T1Data{
			{ID: "o/live", Owner: "o", Name: "live", PushedAt: time.Now()},
			{ID: "o/ghost", Owner: "o", Name: "ghost", PushedAt: pushedAt},
		},
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", Options{Tier: 1})
	var ids []string
	for r := range ch {
		ids = append(ids, r.Fork.ID)
	}
	if len(ids) != 1 || ids[0] != "o/live" {
		t.Errorf("ghost fork not filtered: got %v", ids)
	}
}

// stubEmbedder gives a deterministic per-input vector. Used by the cluster
// integration test below.
type stubEmbedder struct{ dim int }

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	dim := s.dim
	if dim == 0 {
		dim = 8
	}
	out := make([]embed.Vector, len(texts))
	for i, t := range texts {
		v := make(embed.Vector, dim)
		if t == "" {
			out[i] = v
			continue
		}
		axis := int(t[0]) % dim
		v[axis] = 1.0
		out[i] = v
	}
	return out, nil
}

func (s *stubEmbedder) Dim() int {
	if s.dim == 0 {
		return 8
	}
	return s.dim
}

type fakeQueryScorer struct{}

func (fakeQueryScorer) Method() string { return "fake" }

func (fakeQueryScorer) Rerank(_ context.Context, _ string, docs []string) ([]float64, error) {
	scores := make([]float64, len(docs))
	for i, doc := range docs {
		if strings.Contains(doc, "cold") {
			scores[i] = 1
		}
	}
	return scores, nil
}

func TestStream_clusterPipelineEnabled(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	now := time.Now()
	parentPushed := now.Add(-7 * 24 * time.Hour)
	ids := []string{
		"o/a1", "o/a2", "o/a3", "o/a4", "o/a5",
		"o/z1", "o/z2", "o/z3", "o/z4", "o/z5",
	}
	paths := map[string]string{
		"o/a1": "Alpha/a.go", "o/a2": "Alpha/b.go", "o/a3": "Alpha/c.go", "o/a4": "Alpha/d.go", "o/a5": "Alpha/e.go",
		"o/z1": "Zeta/a.go", "o/z2": "Zeta/b.go", "o/z3": "Zeta/c.go", "o/z4": "Zeta/d.go", "o/z5": "Zeta/e.go",
	}
	var forks []forge.T1Data
	t2map := map[string]forge.T2Data{}
	for _, id := range ids {
		parts := strings.SplitN(id, "/", 2)
		forks = append(forks, forge.T1Data{
			ID: id, Owner: parts[0], Name: parts[1],
			PushedAt: now, DefaultBranch: "main", Language: "Go",
		})
		t2map[id] = forge.T2Data{
			AheadCount: 3,
			Diffs:      []forge.FileDiff{{Path: paths[id], Additions: 5, Deletions: 1}},
		}
	}
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: parentPushed},
		forks:  forks,
		t2:     t2map,
	}

	opts := Options{Tier: 2}
	opts.Cluster = ClusterOptions{
		Enabled:        true,
		TopN:           10,
		Epsilon:        0.6,
		MinClusterSize: 3,
	}
	opts.Cluster.SetEmbedderForTest(&stubEmbedder{dim: 8})
	var logBuf bytes.Buffer
	opts.Logger = &logBuf

	ch, err := Stream(context.Background(), ff, "up", "stream", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var results []Result
	for r := range ch {
		results = append(results, r)
	}
	if len(results) != len(ids) {
		t.Fatalf("expected %d results, got %d", len(ids), len(results))
	}
	gotCluster := 0
	for _, r := range results {
		if r.Heat.ClusterID != "" && r.Heat.ClusterID != "noise" {
			gotCluster++
			if r.Heat.ClusterMemberCount < 1 {
				t.Errorf("fork %s: ClusterMemberCount=0", r.Fork.ID)
			}
		}
	}
	if gotCluster == 0 {
		t.Errorf("expected at least one fork in a non-noise cluster; log:\n%s", logBuf.String())
	}
}

func TestStream_clusterDisabled_emitsImmediately(t *testing.T) {
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
		},
	}
	opts := Options{Tier: 1}
	opts.Cluster.Enabled = false
	ch, _ := Stream(context.Background(), ff, "o", "r", opts)
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Heat.ClusterID != "" {
		t.Errorf("expected empty ClusterID when disabled, got %q", got[0].Heat.ClusterID)
	}
	if got[0].ClusterSkip != nil {
		t.Errorf("expected nil ClusterSkip when disabled, got %+v", got[0].ClusterSkip)
	}
}

func TestStream_clusterBuiltinEmbedder_runsWithoutSkip(t *testing.T) {
	// No embedder stub: the stream must cluster with the built-in in-process
	// embedder and never surface a ClusterSkip for embedder availability.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	forks := make([]forge.T1Data, 10)
	t2 := make(map[string]forge.T2Data, len(forks))
	for i := range forks {
		name := fmt.Sprintf("fork-%d", i)
		id := "o/" + name
		forks[i] = forge.T1Data{
			ID: id, Owner: "o", Name: name, PushedAt: now, DefaultBranch: "main",
		}
		t2[id] = forge.T2Data{
			AheadCount: 1,
			Diffs:      []forge.FileDiff{{Path: fmt.Sprintf("auth/%d.go", i), Additions: 1}},
		}
	}
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks:  forks,
		t2:     t2,
	}
	opts := Options{Tier: 2}
	opts.Cluster = ClusterOptions{
		Enabled:        true,
		TopN:           10,
		Epsilon:        0.6,
		MinClusterSize: 3,
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", opts)
	var skipSeen *ClusterSkip
	assigned := 0
	for r := range ch {
		if r.ClusterSkip != nil {
			skipSeen = r.ClusterSkip
		}
		if r.Heat.ClusterID != "" {
			assigned++
		}
	}
	if skipSeen != nil {
		t.Fatalf("builtin embedder must not skip: %+v", skipSeen)
	}
	if assigned == 0 {
		t.Errorf("expected cluster assignments (cluster or noise) on every fork")
	}
}
func TestStream_heatRecomputedWithT2(t *testing.T) {
	pushedAt := time.Now()
	// Need >=10 forks so the scorer uses the full percentile path (not TinySetScore).
	// Only "o/a" gets T2 data; the others are padding.
	forks := make([]forge.T1Data, 10)
	for i := range forks {
		forks[i] = forge.T1Data{ID: fmt.Sprintf("o/pad%d", i), Owner: "o", Name: fmt.Sprintf("pad%d", i), PushedAt: pushedAt, DefaultBranch: "main"}
	}
	forks[0] = forge.T1Data{ID: "o/a", Owner: "o", Name: "a", PushedAt: pushedAt, DefaultBranch: "main"}
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: pushedAt.Add(-1 * time.Hour)},
		forks:  forks,
		t2:     map[string]forge.T2Data{"o/a": {AheadCount: 50, BehindCount: 0, MNA: 1000}},
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, TopN: 1})
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	// Find the enriched fork
	var enriched *Result
	for i := range got {
		if got[i].Fork.ID == "o/a" {
			enriched = &got[i]
			break
		}
	}
	if enriched == nil {
		t.Fatal("o/a not found in results")
	}
	if enriched.Heat.Tier != 2 {
		t.Errorf("expected tier 2 after T2 enrichment, got tier %d (score=%v)", enriched.Heat.Tier, enriched.Heat.Score)
	}
}

func TestStream_loneWolfV2Wired(t *testing.T) {
	pushedAt := time.Now()
	commit := forge.AheadCommit{
		SHA:         "abc123",
		Message:     "Implement feature X end-to-end",
		AuthorLogin: "solo-dev",
		AuthorEmail: "solo@example.com",
		Timestamp:   pushedAt,
		Files:       []forge.FileDiff{{Path: "internal/feature/x.go", Additions: 250, Deletions: 5}},
	}
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: pushedAt.Add(-30 * 24 * time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: pushedAt, DefaultBranch: "main"},
		},
		t2: map[string]forge.T2Data{
			"o/a": {
				AheadCount: 3,
				MNA:        245,
				Commits:    []forge.AheadCommit{commit, commit, commit},
				Diffs:      []forge.FileDiff{{Path: "internal/feature/x.go", Additions: 750, Deletions: 15}},
			},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 3, TopN: 1})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	r := <-ch
	if r.Err != nil {
		t.Fatalf("per-fork err: %+v", r.Err)
	}
	if r.Heat.LoneWolfV2 == nil {
		t.Fatalf("expected Heat.LoneWolfV2 to be populated; got nil")
	}
	if r.Heat.LoneWolfV2.EffectiveContribs != 1 {
		t.Errorf("expected EffectiveContribs=1 (single author), got %d", r.Heat.LoneWolfV2.EffectiveContribs)
	}
	if !r.Heat.LoneWolfV2.Detected {
		t.Errorf("expected LoneWolfV2.Detected=true for single-author high-MNA fork, got Detected=%v Strength=%v", r.Heat.LoneWolfV2.Detected, r.Heat.LoneWolfV2.Strength)
	}
}

func TestStream_contributorsTimeout_gracefulSkip(t *testing.T) {
	// A 202 timeout on the optional contributors stage must NOT drop the fork
	// or set Err — it should skip T3, flag it, and keep the fork in output.
	parentPushedAt := time.Now().Add(-1 * time.Hour)
	forkPushedAt := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: parentPushedAt},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: forkPushedAt, DefaultBranch: "main"},
		},
		t2: map[string]forge.T2Data{"o/a": {AheadCount: 2, MNA: 100}},
		contribErrors: map[string]error{
			"o/a": fmt.Errorf("contributors o/a: %w", github.ErrContributorsTimeout),
		},
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", Options{Tier: 3, TopN: 1})
	r := <-ch
	if r.Err != nil {
		t.Fatalf("contributors timeout must not set Err, got %+v", r.Err)
	}
	if r.T3 != nil {
		t.Errorf("expected T3 nil after skip, got %+v", r.T3)
	}
	if r.T3Skip == nil {
		t.Fatal("expected T3Skip to be set")
	}
	if r.T3Skip.Stage != "contributors" || r.T3Skip.ForkID != "o/a" {
		t.Errorf("unexpected T3Skip: %+v", r.T3Skip)
	}
	// The fork's T2 data must survive — it was not dropped.
	if r.T2 == nil || r.T2.AheadCount != 2 {
		t.Errorf("expected T2 preserved, got %+v", r.T2)
	}
}

func TestRescore_SpanFromT2(t *testing.T) {
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-60 * 24 * time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: now, DefaultBranch: "main"},
		},
		t2: map[string]forge.T2Data{
			"o/a": {
				AheadCount: 2,
				MNA:        100,
				Commits: []forge.AheadCommit{
					{SHA: "old", Timestamp: now.Add(-30 * 24 * time.Hour)},
					{SHA: "new", Timestamp: now},
				},
			},
		},
		contribErrors: map[string]error{
			"o/a": fmt.Errorf("contributors o/a: %w", github.ErrContributorsTimeout),
		},
	}

	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 3, TopN: 1})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	r := <-ch
	if r.Err != nil {
		t.Fatalf("per-fork err: %+v", r.Err)
	}
	if r.T3 != nil {
		t.Fatalf("expected contributor enrichment to be skipped, got T3=%+v", r.T3)
	}

	var span heat.Component
	for _, component := range r.Heat.Components {
		if component.Name == "span" {
			span = component
			break
		}
	}
	if span.Raw != 30 {
		t.Errorf("span raw value = %.1f, want 30", span.Raw)
	}
	if span.Points <= 0 {
		t.Errorf("span points = %.1f, want non-zero", span.Points)
	}
}

func TestStream_perForkRateLimit(t *testing.T) {
	// Parent must be older than the fork so IsGhostFork does not filter it out.
	parentPushedAt := time.Now().Add(-1 * time.Hour)
	forkPushedAt := time.Now()
	reset := time.Now().Add(60 * time.Second)
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: parentPushedAt},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: forkPushedAt, DefaultBranch: "main"},
		},
		forkErrors: map[string]error{
			"o/a": &github.RateLimitError{ResetAt: reset},
		},
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, TopN: 1})
	r := <-ch
	if r.Err == nil || r.Err.Code != "rate_limited" {
		t.Fatalf("expected per-fork rate_limited, got %+v", r.Err)
	}
	if _, ok := r.Err.Details["reset_at"].(string); !ok {
		t.Error("missing details.reset_at")
	}
}

func TestStream_fatalRateLimit_parent(t *testing.T) {
	ff := &fakeForge{parentErr: &github.RateLimitError{ResetAt: time.Now().Add(time.Minute)}}
	_, err := Stream(context.Background(), ff, "o", "r", Options{})
	if err == nil {
		t.Fatal("expected fatal error")
	}
	if !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("expected error to mention rate_limited; got %v", err)
	}
}

func TestStream_querySortsByRelevance(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/wayland", Owner: "o", Name: "wayland", PushedAt: now, DefaultBranch: "main"},
			{ID: "o/docs", Owner: "o", Name: "docs", PushedAt: now, DefaultBranch: "main"},
		},
		t2: map[string]forge.T2Data{
			"o/wayland": {AheadCount: 2,
				Diffs:   []forge.FileDiff{{Path: "compositor/wayland.go", Additions: 100}},
				Commits: []forge.AheadCommit{{Message: "add wayland protocol support to compositor"}}},
			"o/docs": {AheadCount: 1,
				Diffs:   []forge.FileDiff{{Path: "README.md", Additions: 2}},
				Commits: []forge.AheadCommit{{Message: "fix typo in readme"}}},
		},
	}
	opts := Options{Tier: 2, Query: "wayland compositor support"}
	opts.Cluster.Enabled = false
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].Fork.ID != "o/wayland" {
		t.Errorf("query-relevant fork should be first, got %s (scores %v / %v)",
			got[0].Fork.ID, got[0].QueryScore, got[1].QueryScore)
	}
	for _, r := range got {
		if r.QueryMethod != "lexical" {
			t.Errorf("fork %s: QueryMethod = %q, want lexical (no scorer injected)", r.Fork.ID, r.QueryMethod)
		}
	}
	if got[0].QueryScore <= got[1].QueryScore {
		t.Errorf("relevance ordering wrong: %v <= %v", got[0].QueryScore, got[1].QueryScore)
	}
}

func TestStream_batchModeAssignsNetworkRank(t *testing.T) {
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/hot", Owner: "o", Name: "hot", Stars: 100, PushedAt: now, DefaultBranch: "main"},
			{ID: "o/warm", Owner: "o", Name: "warm", Stars: 10, PushedAt: now, DefaultBranch: "main"},
			{ID: "o/cold", Owner: "o", Name: "cold", Stars: 1, PushedAt: now, DefaultBranch: "main"},
		},
	}
	opts := Options{Tier: 1}
	opts.Cluster.Enabled = true
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatal(err)
	}
	var results []Result
	for r := range ch {
		results = append(results, r)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if r.NetworkRank == nil {
			t.Fatalf("%s missing NetworkRank", r.Fork.ID)
		}
		if r.NetworkRank.Position < 1 || r.NetworkRank.Total != len(results) {
			t.Fatalf("%s invalid NetworkRank: %+v", r.Fork.ID, r.NetworkRank)
		}
	}
	byID := map[string]Result{}
	for _, r := range results {
		byID[r.Fork.ID] = r
	}
	if byID["o/hot"].Heat.Score > byID["o/cold"].Heat.Score &&
		byID["o/hot"].NetworkRank.Position >= byID["o/cold"].NetworkRank.Position {
		t.Fatalf("higher heat should have better rank: hot=%+v cold=%+v", byID["o/hot"].NetworkRank, byID["o/cold"].NetworkRank)
	}
}

func TestStream_queryModePreservesHeatBasedNetworkRank(t *testing.T) {
	now := time.Now()
	ff := &fakeForge{
		parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		concurrency: 1,
		forks: []forge.T1Data{
			{ID: "o/hot", Owner: "o", Name: "hot", Stars: 100, PushedAt: now, DefaultBranch: "main"},
			{ID: "o/cold", Owner: "o", Name: "cold", Stars: 1, PushedAt: now, DefaultBranch: "main"},
		},
		t2: map[string]forge.T2Data{
			"o/hot":  {AheadCount: 1, Commits: []forge.AheadCommit{{Message: "hot feature"}}},
			"o/cold": {AheadCount: 1, Commits: []forge.AheadCommit{{Message: "cold feature"}}},
		},
	}
	opts := Options{Tier: 2, Query: "prefer cold", QueryScorer: fakeQueryScorer{}}
	opts.Cluster.Enabled = false
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].Fork.ID != "o/cold" {
		t.Fatalf("query order should put cold first, got %s", got[0].Fork.ID)
	}
	byID := map[string]Result{got[0].Fork.ID: got[0], got[1].Fork.ID: got[1]}
	if byID["o/hot"].NetworkRank == nil || byID["o/cold"].NetworkRank == nil {
		t.Fatalf("missing network ranks: hot=%+v cold=%+v", byID["o/hot"].NetworkRank, byID["o/cold"].NetworkRank)
	}
	if byID["o/hot"].NetworkRank.Position >= byID["o/cold"].NetworkRank.Position {
		t.Fatalf("network rank should preserve heat order despite query output: hot=%+v cold=%+v", byID["o/hot"].NetworkRank, byID["o/cold"].NetworkRank)
	}
}

func TestStream_momentumDisabledIsUnknownAndSideEffectFree(t *testing.T) {
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks:  []forge.T1Data{{ID: "o/a", Owner: "o", Name: "a", Stars: 1, PushedAt: now, DefaultBranch: "main"}},
	}
	opts := Options{Tier: 1}
	opts.Cluster.Enabled = false
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatal(err)
	}
	for r := range ch {
		if r.Momentum.Status != MomentumUnknown {
			t.Fatalf("Momentum.Status = %q, want unknown", r.Momentum.Status)
		}
	}
}

func TestStream_momentumFirstRunUnknown(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks:  []forge.T1Data{{ID: "o/a", Owner: "o", Name: "a", Stars: 1, PushedAt: now, DefaultBranch: "main"}},
	}
	opts := Options{Tier: 1, MomentumSnapshots: true}
	opts.Cluster.Enabled = false
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.Momentum.Status != MomentumUnknown {
		t.Fatalf("Momentum.Status = %q, want unknown", r.Momentum.Status)
	}
}

func TestStream_momentumSecondRunComputesDelta(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	if err := saveSnapshotHistory("github", "o", "r", &snapshotHistory{
		Days: []snapshotDay{{
			Date: yesterday,
			Forks: map[string]snapshotFork{
				"o/a": {FullName: "o/a", Stars: 1, SubForks: 1, PushedAt: now.AddDate(0, 0, -1)},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", Stars: 5, SubForkCount: 2, PushedAt: now, DefaultBranch: "main"},
			{ID: "o/b", Owner: "o", Name: "b", Stars: 3, SubForkCount: 1, PushedAt: now, DefaultBranch: "main"},
		},
	}
	opts := Options{Tier: 1, MomentumSnapshots: true}
	opts.Cluster.Enabled = false
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]MomentumInfo{}
	for r := range ch {
		got[r.Fork.ID] = r.Momentum
	}
	if got["o/a"].Status != MomentumRising || got["o/a"].StarsDelta30d != 4 || got["o/a"].SubForksDelta30d != 1 {
		t.Fatalf("o/a momentum = %+v, want rising +4/+1", got["o/a"])
	}
	if got["o/b"].Status != MomentumNew || got["o/b"].StarsDelta30d != 3 || got["o/b"].SubForksDelta30d != 1 {
		t.Fatalf("o/b momentum = %+v, want new +3/+1", got["o/b"])
	}
	if got["o/a"].ObservedDays != 1 || got["o/b"].ObservedDays != 1 {
		t.Fatalf("ObservedDays = a:%d b:%d, want 1", got["o/a"].ObservedDays, got["o/b"].ObservedDays)
	}
}

func TestStream_degradedStagesFollowSkipFields(t *testing.T) {
	now := time.Now()
	low := 0.05
	ff := &fakeForge{
		parent:   forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		headroom: &low,
		forks:    []forge.T1Data{{ID: "o/a", Owner: "o", Name: "a", PushedAt: now, DefaultBranch: "main"}},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.BudgetSkip == nil {
		t.Fatal("expected BudgetSkip")
	}
	if len(r.Degraded) == 0 {
		t.Fatal("expected Degraded metadata")
	}
	if r.Degraded[0].Stage != "compare" || r.Degraded[0].Reason != r.BudgetSkip.Reason {
		t.Fatalf("first degraded stage = %+v, want compare reason %q", r.Degraded[0], r.BudgetSkip.Reason)
	}
}

func TestStream_priorsLaneSplit_matchedBeforeUnmatched(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	newForge := func() *fakeForge {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/docs", Owner: "o", Name: "docs", Stars: 500, SubForkCount: 20, PushedAt: now, DefaultBranch: "main"},
				{ID: "o/auth", Owner: "o", Name: "auth", Stars: 1, PushedAt: now, DefaultBranch: "main"},
			},
			t2: map[string]forge.T2Data{
				"o/docs": {AheadCount: 1, MNA: 10, Diffs: []forge.FileDiff{{Path: "README.md", Additions: 10}}},
				"o/auth": {AheadCount: 1, MNA: 10, Diffs: []forge.FileDiff{{Path: "internal/auth/token.go", Additions: 10}}},
			},
		}
	}
	collect := func(opts Options) []Result {
		opts.Cluster.Enabled = false
		ch, err := Stream(context.Background(), newForge(), "o", "r", opts)
		if err != nil {
			t.Fatal(err)
		}
		var got []Result
		for r := range ch {
			got = append(got, r)
		}
		return got
	}

	// Control: no priors → heat order, so high-heat docs leads.
	ctrl := collect(Options{Tier: 2})
	if len(ctrl) != 2 || ctrl[0].Fork.ID != "o/docs" {
		t.Fatalf("control (no priors) should sort high-heat docs first, got %d results, first=%q", len(ctrl), firstID(ctrl))
	}

	// With priors: the low-heat auth fork (matches the path) leads; docs still emitted.
	got := collect(Options{Tier: 2, Priors: &priors.Spec{Paths: []string{"internal/auth"}}})
	if len(got) != 2 {
		t.Fatalf("expected 2 results (nothing hidden), got %d", len(got))
	}
	if got[0].Fork.ID != "o/auth" {
		t.Fatalf("matched low-heat fork should lead, got first=%q second=%q", got[0].Fork.ID, got[1].Fork.ID)
	}
	if got[0].PriorScore != 1 {
		t.Errorf("auth PriorScore = %v, want 1", got[0].PriorScore)
	}
	if !slices.Contains(got[0].PriorReasons, "path:internal/auth") {
		t.Errorf("auth PriorReasons = %v, want to contain path:internal/auth", got[0].PriorReasons)
	}
	if got[1].Fork.ID != "o/docs" || got[1].PriorScore != 0 {
		t.Errorf("docs should be the unmatched lane with PriorScore 0, got %q score=%v", got[1].Fork.ID, got[1].PriorScore)
	}
	// Guard: the split really overrode heat — docs's heat is strictly higher.
	if got[1].Heat.Score <= got[0].Heat.Score {
		t.Errorf("expected docs heat (%v) > auth heat (%v); the priors lane split must reorder against heat", got[1].Heat.Score, got[0].Heat.Score)
	}
	// networkRank stays heat-relative regardless of prior lane: docs (higher
	// heat) outranks auth even though auth leads the matched lane.
	if got[0].NetworkRank == nil || got[1].NetworkRank == nil {
		t.Fatalf("both forks should carry a network rank: auth=%+v docs=%+v", got[0].NetworkRank, got[1].NetworkRank)
	}
	if got[1].NetworkRank.Position >= got[0].NetworkRank.Position {
		t.Errorf("networkRank must stay heat-relative: docs (higher heat) should outrank auth, got docs=%d auth=%d", got[1].NetworkRank.Position, got[0].NetworkRank.Position)
	}
}

func TestStream_priorsDoesNotReorderUnderQuery(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/auth", Owner: "o", Name: "auth", Stars: 1, PushedAt: now, DefaultBranch: "main"},
			{ID: "o/docs", Owner: "o", Name: "docs", Stars: 1, PushedAt: now, DefaultBranch: "main"},
		},
		t2: map[string]forge.T2Data{
			"o/auth": {AheadCount: 1, Diffs: []forge.FileDiff{{Path: "internal/auth/token.go"}}, Commits: []forge.AheadCommit{{Message: "refactor auth internals"}}},
			"o/docs": {AheadCount: 1, Diffs: []forge.FileDiff{{Path: "README.md"}}, Commits: []forge.AheadCommit{{Message: "expand readme documentation guide"}}},
		},
	}
	opts := Options{Tier: 2, Query: "readme documentation", Priors: &priors.Spec{Paths: []string{"internal/auth"}}}
	opts.Cluster.Enabled = false
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	// Query relevance owns ordering; priors only annotate, never reorder.
	if got[0].Fork.ID != "o/docs" {
		t.Fatalf("query relevance should lead with docs, got first=%q", got[0].Fork.ID)
	}
	byID := map[string]Result{got[0].Fork.ID: got[0], got[1].Fork.ID: got[1]}
	if byID["o/auth"].PriorScore != 1 || !slices.Contains(byID["o/auth"].PriorReasons, "path:internal/auth") {
		t.Errorf("auth should still be annotated by priors: score=%v reasons=%v", byID["o/auth"].PriorScore, byID["o/auth"].PriorReasons)
	}
}

// TestScorePriorsNeverMutatesHeat exercises the scorer directly (no Stream,
// no wall clock) so the "priors never touch heat" invariant is verified
// byte-for-byte, free of the recency drift two end-to-end runs would incur.
func TestScorePriorsNeverMutatesHeat(t *testing.T) {
	collected := []Result{
		{
			Fork: forge.T1Data{ID: "o/auth", Owner: "o", Language: "go"},
			Heat: heat.HeatResult{Score: 42.5},
			T2:   &forge.T2Data{Diffs: []forge.FileDiff{{Path: "internal/auth/token.go"}}},
		},
		{
			Fork: forge.T1Data{ID: "o/docs", Owner: "o"},
			Heat: heat.HeatResult{Score: 88.125},
			T2:   &forge.T2Data{Diffs: []forge.FileDiff{{Path: "README.md"}}},
		},
	}
	before := []float64{collected[0].Heat.Score, collected[1].Heat.Score}

	scorePriors(Options{Priors: &priors.Spec{Paths: []string{"internal/auth"}}}, collected)

	if collected[0].Heat.Score != before[0] || collected[1].Heat.Score != before[1] {
		t.Errorf("scorePriors mutated heat: before=%v after=[%v %v]",
			before, collected[0].Heat.Score, collected[1].Heat.Score)
	}
	// ...but it did run: the matching fork scores, the other does not.
	if collected[0].PriorScore != 1 || !slices.Contains(collected[0].PriorReasons, "path:internal/auth") {
		t.Errorf("auth fork not scored: score=%v reasons=%v", collected[0].PriorScore, collected[0].PriorReasons)
	}
	if collected[1].PriorScore != 0 || len(collected[1].PriorReasons) != 0 {
		t.Errorf("docs fork should have no prior signal: score=%v reasons=%v", collected[1].PriorScore, collected[1].PriorReasons)
	}
}

func firstID(rs []Result) string {
	if len(rs) == 0 {
		return ""
	}
	return rs[0].Fork.ID
}

// TestStreamEmitsLineageAndCoverage verifies that Result.Lineage and
// Result.Coverage are populated from T1Data fields at the Result construction
// site in Stream(). Uses fixture values: direct=118, whole=128, unresolved=10,
// and parent="chunkhound/chunkhound" (depth=1, direct child).
func TestStreamEmitsLineageAndCoverage(t *testing.T) {
	// fakeForge already implements ListForks; populate T1Data with fixture values
	// so Stream() populates Result.Lineage/Coverage from them.
	fake := &fakeForge{
		parent: forge.ParentData{
			FullName:      "chunkhound/chunkhound",
			DefaultBranch: "main",
			PushedAt:      time.Now(),
		},
		forks: []forge.T1Data{
			{
				ID:                    "alice/chunkhound-fork-1",
				Owner:                 "alice",
				Name:                  "chunkhound-fork-1",
				URL:                   "https://github.com/alice/chunkhound-fork-1",
				DefaultBranch:         "main",
				Stars:                 10,
				PushedAt:              time.Now(),
				SourceFullPath:        "chunkhound/chunkhound",
				ParentFullPath:        "chunkhound/chunkhound",
				DepthFromRoot:         1,
				DirectTotalCount:      118,
				WholeNetworkForkCount: 128,
				SubForkCount:          5,
			},
			{
				ID:                    "bob/chunkhound-fork-2",
				Owner:                 "bob",
				Name:                  "chunkhound-fork-2",
				URL:                   "https://github.com/bob/chunkhound-fork-2",
				DefaultBranch:         "main",
				Stars:                 5,
				PushedAt:              time.Now(),
				SourceFullPath:        "chunkhound/chunkhound",
				ParentFullPath:        "chunkhound/chunkhound",
				DepthFromRoot:         1,
				DirectTotalCount:      118,
				WholeNetworkForkCount: 128,
				SubForkCount:          0,
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := Stream(ctx, fake, "chunkhound", "chunkhound", Options{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var results []Result
	for r := range ch {
		results = append(results, r)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Lineage.NetworkRoot != "chunkhound/chunkhound" {
			t.Errorf("Lineage.NetworkRoot: got %q, want %q", r.Lineage.NetworkRoot, "chunkhound/chunkhound")
		}
		if r.Lineage.DirectParent != "chunkhound/chunkhound" {
			t.Errorf("Lineage.DirectParent: got %q, want %q", r.Lineage.DirectParent, "chunkhound/chunkhound")
		}
		if r.Lineage.DepthFromRoot != 1 {
			t.Errorf("Lineage.DepthFromRoot: got %d, want 1 (direct child)", r.Lineage.DepthFromRoot)
		}
		if r.Coverage.DirectTotalCount != 118 {
			t.Errorf("Coverage.DirectTotalCount: got %d, want 118", r.Coverage.DirectTotalCount)
		}
		if r.Coverage.WholeNetworkForkCount != 128 {
			t.Errorf("Coverage.WholeNetworkForkCount: got %d, want 128", r.Coverage.WholeNetworkForkCount)
		}
		if r.Coverage.Unresolved != 10 {
			t.Errorf("Coverage.Unresolved: got %d, want 10", r.Coverage.Unresolved)
		}
	}
}

func TestStream_shortlistRuleMembership_selectsByPTopK(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
			{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
			{ID: "o/c", Owner: "o", Name: "c", DefaultBranch: "main", PushedAt: now},
			{ID: "o/d", Owner: "o", Name: "d", DefaultBranch: "main", PushedAt: now},
		},
		t2: map[string]forge.T2Data{
			"o/a": {AheadCount: 1, MNA: 500, Diffs: []forge.FileDiff{{Path: "a.go", Additions: 500}}},
			"o/b": {AheadCount: 1, MNA: 5},
			"o/c": {AheadCount: 1, MNA: 50},
			"o/d": {AheadCount: 1, MNA: 1},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, ShortlistN: 2, ShortlistRule: ShortlistRuleMembership})
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("shortlist should emit 2, got %d", len(got))
	}
	// Selected forks are the two highest P(rank<=2) in the pool, and are
	// ordered by expected rank within the shortlist.
	if got[0].Rank == nil || got[1].Rank == nil {
		t.Fatal("missing Rank stats")
	}
	if got[0].ExpectedRank > got[1].ExpectedRank {
		t.Errorf("shortlist not ordered by expected rank: %v, %v", got[0].ExpectedRank, got[1].ExpectedRank)
	}
	for _, r := range got {
		if r.Rank.PTopK < 0.5 {
			t.Errorf("membership-selected fork %s has PTopK %v < 0.5", r.Fork.ID, r.Rank.PTopK)
		}
	}
}

func TestStream_shortlistFillsRankReportAndDiagnostics(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
			{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
			{ID: "o/c", Owner: "o", Name: "c", DefaultBranch: "main", PushedAt: now},
			{ID: "o/d", Owner: "o", Name: "d", DefaultBranch: "main", PushedAt: now},
		},
		t2: map[string]forge.T2Data{
			"o/a": {AheadCount: 1, MNA: 500, Diffs: []forge.FileDiff{{Path: "a.go", Additions: 500}}},
			"o/b": {AheadCount: 1, MNA: 5},
			"o/c": {AheadCount: 1, MNA: 50},
			"o/d": {AheadCount: 1, MNA: 1},
		},
	}
	var report RankReport
	ch, err := Stream(context.Background(), ff, "o", "r", Options{
		Tier: 2, ShortlistN: 3, RankReport: &report, RankDiagnostics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("shortlist should emit 3, got %d", len(got))
	}
	if report.PoolSize != 4 || report.ShortlistN != 3 || report.ShortlistRule != ShortlistRuleExpected {
		t.Errorf("report header fields: %+v", report)
	}
	if report.POTH < 0 || report.POTH > 1 || math.IsNaN(report.POTH) {
		t.Errorf("POTH %v not in [0,1]", report.POTH)
	}
	if report.CPOTHk < 0 || report.CPOTHk > 1 || math.IsNaN(report.CPOTHk) {
		t.Errorf("cPOTH_k %v not in [0,1] for k=3", report.CPOTHk)
	}
	for i, r := range got {
		if r.Rank == nil || r.Rank.PothResidual == nil {
			t.Errorf("result %d missing pothResidual under RankDiagnostics", i)
		}
	}
}

func TestStream_shortlistWithoutDiagnosticsOmitsResidual(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
			{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
			{ID: "o/c", Owner: "o", Name: "c", DefaultBranch: "main", PushedAt: now},
		},
		t2: map[string]forge.T2Data{
			"o/a": {AheadCount: 1, MNA: 500},
			"o/b": {AheadCount: 1, MNA: 5},
			"o/c": {AheadCount: 1, MNA: 50},
		},
	}
	var report RankReport
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, ShortlistN: 2, RankReport: &report})
	if err != nil {
		t.Fatal(err)
	}
	for r := range ch {
		if r.Rank != nil && r.Rank.PothResidual != nil {
			t.Errorf("pothResidual should be nil without RankDiagnostics")
		}
	}
	// k=2 is below the POTH pool minimum: cPOTH_k must be NaN, POTH (n=3) finite.
	if !math.IsNaN(report.CPOTHk) {
		t.Errorf("cPOTH_2 should be NaN, got %v", report.CPOTHk)
	}
	if math.IsNaN(report.POTH) {
		t.Errorf("POTH for n=3 should be finite")
	}
}

// ebFakeForge builds a pool big enough for the EB fit: a few strong tier-2
// forks and one with zero heat (no ahead commits → zeroed) that must bypass
// shrinkage and stay out of τ̂.
func ebFakeForge(now time.Time) *fakeForge {
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		t2:     map[string]forge.T2Data{},
	}
	mnas := []int{500, 5, 50, 1, 120, 30}
	for i, mna := range mnas {
		id := "o/f" + string(rune('a'+i))
		ff.forks = append(ff.forks, forge.T1Data{ID: id, Owner: "o", Name: id[2:], DefaultBranch: "main", PushedAt: now})
		ff.t2[id] = forge.T2Data{AheadCount: 1, MNA: mna}
	}
	ff.forks = append(ff.forks, forge.T1Data{ID: "o/zero", Owner: "o", Name: "zero", DefaultBranch: "main", PushedAt: now})
	ff.t2["o/zero"] = forge.T2Data{AheadCount: 0}
	return ff
}

func TestStream_ebShrinksScoresAndReportsTau(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ff := ebFakeForge(time.Now())
	var report RankReport
	ch, err := Stream(context.Background(), ff, "o", "r", Options{
		Tier: 2, ShortlistN: 7, RankReport: &report, EB: true, PriorScale: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if report.EBRegime != EBRegimeHeterogeneous && report.EBRegime != EBRegimeClamped {
		t.Fatalf("EB regime %q, report %+v", report.EBRegime, report)
	}
	if !(report.TauHat > 0) || report.EBPool != report.NonzeroPool {
		t.Errorf("tauHat=%v ebPool=%d nonzero=%d", report.TauHat, report.EBPool, report.NonzeroPool)
	}
	for _, r := range got {
		if r.Heat.Score == 0 {
			if r.EB != nil {
				t.Errorf("zero-heat fork %s must not carry EB stats", r.Fork.ID)
			}
			continue
		}
		if r.EB == nil {
			t.Fatalf("fork %s missing EB stats", r.Fork.ID)
		}
		if !(r.EB.PostSigma < rankSigma(r.Heat.Confidence)) {
			t.Errorf("fork %s posterior sigma %v not below tier sigma", r.Fork.ID, r.EB.PostSigma)
		}
		// Shrunken score lies between the raw score and the pooled mean.
		lo, hi := math.Min(r.Heat.Score, report.EBMean), math.Max(r.Heat.Score, report.EBMean)
		if r.EB.Theta < lo-1e-9 || r.EB.Theta > hi+1e-9 {
			t.Errorf("fork %s θ=%v outside [%v,%v]", r.Fork.ID, r.EB.Theta, lo, hi)
		}
		if r.EB.Leverage <= 0 || r.EB.Leverage >= 1 {
			t.Errorf("fork %s leverage %v outside (0,1)", r.Fork.ID, r.EB.Leverage)
		}
	}
}

func TestStream_ebOffLeavesNoEBStats(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ff := ebFakeForge(time.Now())
	var report RankReport
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, ShortlistN: 3, RankReport: &report})
	if err != nil {
		t.Fatal(err)
	}
	for r := range ch {
		if r.EB != nil {
			t.Errorf("EB stats present without Options.EB")
		}
	}
	if report.EBRegime != "" {
		t.Errorf("EB regime should be empty when EB is off, got %q", report.EBRegime)
	}
}

func TestStream_shortlistMarksTieBands(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
			{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
			{ID: "o/c", Owner: "o", Name: "c", DefaultBranch: "main", PushedAt: now},
		},
		t2: map[string]forge.T2Data{
			"o/a": {AheadCount: 1, MNA: 500, Diffs: []forge.FileDiff{{Path: "a.go", Additions: 500}}},
			"o/b": {AheadCount: 1, MNA: 5}, // identical evidence → identical heat → band
			"o/c": {AheadCount: 1, MNA: 5},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, ShortlistN: 3})
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results", len(got))
	}
	if got[0].Rank.TieBand {
		t.Errorf("clear leader %s should not be banded", got[0].Fork.ID)
	}
	if !got[1].Rank.TieBand || !got[2].Rank.TieBand {
		t.Errorf("identical forks should be banded: %v %v", got[1].Rank.TieBand, got[2].Rank.TieBand)
	}
}

// --- Task 5: batch integration ---

func TestStream_batchZeroAhead_neverHitsCompareOrResolved(t *testing.T) {
	now := time.Now()
	var order []string
	var resolved []string
	ff := &batchFakeForge{
		fakeForge: &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
			},
			concurrency:  1,
			compareOrder: &order,
		},
		batch: map[string]forge.ForkDivergence{
			"o/a": {
				Resolved: true,
				Default:  forge.BranchDivergence{Name: "main", TipSHA: "maintip", AheadBy: 0, BehindBy: 3},
			},
		},
		resolvedCalls: &resolved,
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	r := <-ch
	if r.Err != nil {
		t.Fatalf("unexpected error: %+v", r.Err)
	}
	if len(order) != 0 {
		t.Errorf("Compare should not be called for a zero-ahead batch resolution, got calls: %v", order)
	}
	if len(resolved) != 0 {
		t.Errorf("CompareResolved should not be called for a zero-ahead batch resolution, got calls: %v", resolved)
	}
	if r.T2 == nil {
		t.Fatal("expected synthesised T2")
	}
	if !r.T2.Performed || r.T2.AheadCount != 0 || r.T2.BehindCount != 3 || r.T2.HeadSHA != "maintip" {
		t.Errorf("unexpected synthesised T2: %+v", r.T2)
	}
	if r.T2.CompareSource != "graphql_batch" {
		t.Errorf("CompareSource = %q, want graphql_batch", r.T2.CompareSource)
	}
	if r.T2FromCache {
		t.Error("T2FromCache should be false for a batch-synthesised T2 so it persists")
	}
}

func TestStream_batchDivergent_hitsCompareResolvedOnceWithSideBranch(t *testing.T) {
	now := time.Now()
	var order []string
	var resolved []string
	ff := &batchFakeForge{
		fakeForge: &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
			},
			concurrency:  1,
			compareOrder: &order,
			t2: map[string]forge.T2Data{
				"o/b": {Performed: true, AheadCount: 5, BehindCount: 1, IsBranchWork: true, ActiveBranch: "feature", HeadSHA: "sidetip"},
			},
		},
		batch: map[string]forge.ForkDivergence{
			"o/b": {
				Resolved: true,
				Default:  forge.BranchDivergence{Name: "main", TipSHA: "maintip", AheadBy: 0, BehindBy: 1},
				Sides: []forge.BranchDivergence{
					{Name: "feature", TipSHA: "sidetip", TipCommittedAt: now, AheadBy: 5, BehindBy: 1},
				},
			},
		},
		resolvedCalls: &resolved,
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	r := <-ch
	if r.Err != nil {
		t.Fatalf("unexpected error: %+v", r.Err)
	}
	if len(order) != 0 {
		t.Errorf("plain Compare should not be called when the batch chose a branch, got calls: %v", order)
	}
	if want := []string{"o/b@feature"}; len(resolved) != 1 || resolved[0] != want[0] {
		t.Errorf("CompareResolved calls = %v, want exactly one call on the side branch %v", resolved, want)
	}
	if r.T2 == nil {
		t.Fatal("expected T2 from CompareResolved")
	}
	if !r.T2.IsBranchWork || r.T2.ActiveBranch != "feature" {
		t.Errorf("expected T2 to carry the fake's branch-work annotations, got %+v", r.T2)
	}
}

func TestStream_batchError_fallsBackToCompareForEveryFork(t *testing.T) {
	now := time.Now()
	var order []string
	summary := &CompareSummary{}
	ff := &batchFakeForge{
		fakeForge: &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
				{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
			},
			concurrency:  1,
			compareOrder: &order,
			t2: map[string]forge.T2Data{
				"o/a": {Performed: true, AheadCount: 1},
				"o/b": {Performed: true, AheadCount: 2},
			},
		},
		batchErr: errors.New("graphql: rate limited"),
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, CompareReport: summary})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	count := 0
	for r := range ch {
		if r.Err != nil {
			t.Errorf("unexpected per-fork error: %+v", r.Err)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("expected 2 results, got %d", count)
	}
	if len(order) != 2 {
		t.Errorf("expected both forks to go through Compare, got calls: %v", order)
	}
	if summary.BatchError == "" {
		t.Error("expected CompareReport.BatchError to be set on batch error")
	}
}

func TestStream_noBatchCompare_neverCallsBatchCompare(t *testing.T) {
	now := time.Now()
	var order []string
	ff := &batchFakeForge{
		fakeForge: &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
			},
			concurrency:  1,
			compareOrder: &order,
			t2: map[string]forge.T2Data{
				"o/a": {Performed: true, AheadCount: 0},
			},
		},
		batch: map[string]forge.ForkDivergence{
			"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", AheadBy: 0, BehindBy: 0}},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, NoBatchCompare: true})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	<-ch
	if ff.batchCalls != 0 {
		t.Errorf("BatchCompare should never be called when NoBatchCompare is set, got %d calls", ff.batchCalls)
	}
	if len(order) != 1 {
		t.Errorf("expected the fork to go through plain Compare, got calls: %v", order)
	}
}

func TestStream_synthesisedZeroAheadMatchesRESTZeroAnnotations(t *testing.T) {
	now := time.Now()
	ff := &batchFakeForge{
		fakeForge: &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/batch", Owner: "o", Name: "batch", DefaultBranch: "main", PushedAt: now},
				{ID: "o/rest", Owner: "o", Name: "rest", DefaultBranch: "main", PushedAt: now},
			},
			t2: map[string]forge.T2Data{
				// A REST-path zero-ahead fork: same shape a live Compare would
				// return for "nothing ahead."
				"o/rest": {Performed: true, AheadCount: 0, BehindCount: 4, HeadSHA: "resttip"},
			},
		},
		batch: map[string]forge.ForkDivergence{
			"o/batch": {
				Resolved: true,
				Default:  forge.BranchDivergence{Name: "main", TipSHA: "batchtip", AheadBy: 0, BehindBy: 4},
			},
			// "o/rest" deliberately absent from the batch result, so it falls
			// back to the REST path and Compare serves it from f.t2.
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	results := map[string]Result{}
	for r := range ch {
		results[r.Fork.ID] = r
	}
	batchRes, restRes := results["o/batch"], results["o/rest"]
	if batchRes.T2 == nil || restRes.T2 == nil {
		t.Fatalf("expected both forks to carry T2, got batch=%v rest=%v", batchRes.T2, restRes.T2)
	}
	if batchRes.T2.CompareSource != "graphql_batch" {
		t.Errorf("expected the batch fork's T2 to be marked graphql_batch, got %q", batchRes.T2.CompareSource)
	}
	if restRes.T2.CompareSource != "" {
		t.Errorf("expected the REST fork's T2 to carry no CompareSource, got %q", restRes.T2.CompareSource)
	}
	// The annotations that matter to consumers (rescore's heat penalties and
	// deriveVisibility's status) must agree between the two zero-ahead
	// paths: a synthesised zero should read exactly like a REST zero.
	if !slices.Equal(batchRes.Heat.Penalties, restRes.Heat.Penalties) {
		t.Errorf("heat penalties differ: batch=%v rest=%v", batchRes.Heat.Penalties, restRes.Heat.Penalties)
	}
	if batchRes.Visibility.Status != restRes.Visibility.Status {
		t.Errorf("visibility status differs: batch=%q rest=%q", batchRes.Visibility.Status, restRes.Visibility.Status)
	}
}

func TestStream_compareReportCounts(t *testing.T) {
	now := time.Now()
	cachedT2 := forge.T2Data{Performed: true, AheadCount: 9, CompareSource: ""}
	var resolved []string
	ff := &batchFakeForge{
		fakeForge: &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/cached", Owner: "o", Name: "cached", DefaultBranch: "main", PushedAt: now},
				{ID: "o/zero", Owner: "o", Name: "zero", DefaultBranch: "main", PushedAt: now},
				{ID: "o/side", Owner: "o", Name: "side", DefaultBranch: "main", PushedAt: now},
				{ID: "o/plain", Owner: "o", Name: "plain", DefaultBranch: "main", PushedAt: now},
				{ID: "o/diff", Owner: "o", Name: "diff", DefaultBranch: "main", PushedAt: now},
			},
			t2: map[string]forge.T2Data{
				"o/side":  {Performed: true, AheadCount: 3, IsBranchWork: true, ActiveBranch: "feature"},
				"o/plain": {Performed: true, AheadCount: 1},
				"o/diff":  {Performed: true, AheadCount: 1, FilesComplete: true},
			},
		},
		batch: map[string]forge.ForkDivergence{
			"o/zero": {Resolved: true, Default: forge.BranchDivergence{Name: "main", AheadBy: 0, BehindBy: 0}},
			"o/side": {
				Resolved: true,
				Default:  forge.BranchDivergence{Name: "main", AheadBy: 0, BehindBy: 0},
				Sides:    []forge.BranchDivergence{{Name: "feature", AheadBy: 3, BehindBy: 0, TipCommittedAt: now}},
			},
			// "o/plain" and "o/diff" absent: not resolved by the batch, fall
			// through to plain Compare.
		},
		batchStats:    forge.BatchStats{Queries: 2, Cost: 7},
		resolvedCalls: &resolved,
	}
	summary := &CompareSummary{}
	opts := Options{
		Tier:          2,
		CompareReport: summary,
		CachedT2: func(f forge.T1Data) *forge.T2Data {
			if f.ID == "o/cached" {
				t2 := cachedT2
				return &t2
			}
			return nil
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if summary.Cached != 1 {
		t.Errorf("Cached = %d, want 1", summary.Cached)
	}
	if summary.Batch != 1 {
		t.Errorf("Batch = %d, want 1", summary.Batch)
	}
	if summary.REST != 3 {
		t.Errorf("REST = %d, want 3 (side + plain + diff)", summary.REST)
	}
	if summary.DiffFallback != 1 {
		t.Errorf("DiffFallback = %d, want 1", summary.DiffFallback)
	}
	if summary.LastTouchSkipped != 0 {
		t.Errorf("LastTouchSkipped = %d, want 0 (not wired until task 7)", summary.LastTouchSkipped)
	}
	if summary.BatchQueries != 2 || summary.BatchCost != 7 {
		t.Errorf("BatchQueries/BatchCost = %d/%d, want 2/7", summary.BatchQueries, summary.BatchCost)
	}
	if summary.BatchError != "" {
		t.Errorf("BatchError = %q, want empty", summary.BatchError)
	}
}

func TestStream_compareResolvedError_fallsBackToCompareOnce(t *testing.T) {
	now := time.Now()
	var order []string
	var resolved []string
	ff := &batchFakeForge{
		fakeForge: &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
			},
			concurrency:  1,
			compareOrder: &order,
			t2: map[string]forge.T2Data{
				"o/b": {Performed: true, AheadCount: 5},
			},
		},
		batch: map[string]forge.ForkDivergence{
			"o/b": {
				Resolved: true,
				Default:  forge.BranchDivergence{Name: "main", AheadBy: 0, BehindBy: 0},
				Sides:    []forge.BranchDivergence{{Name: "feature", AheadBy: 5, BehindBy: 0, TipCommittedAt: now}},
			},
		},
		resolvedCalls: &resolved,
		resolvedErr:   map[string]error{"o/b": errors.New("compare resolved failed")},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	r := <-ch
	if len(resolved) != 1 {
		t.Errorf("expected exactly one CompareResolved attempt, got %v", resolved)
	}
	if len(order) != 1 {
		t.Errorf("expected exactly one Compare fallback attempt, got %v", order)
	}
	if r.T2 == nil || r.T2.AheadCount != 5 {
		t.Errorf("expected the fallback Compare's T2 to be used, got %+v", r.T2)
	}
	if r.Err != nil {
		t.Errorf("a CompareResolved failure followed by a successful Compare should not be a fork error, got %+v", r.Err)
	}
}
