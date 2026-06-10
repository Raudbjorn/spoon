package forksops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/github"
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
func (f *fakeForge) Headroom() float64 { return 1.0 }

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

func TestStream_clusterPipelineEnabled(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	now := time.Now()
	parentPushed := now.Add(-7 * 24 * time.Hour)
	ids := []string{"o/a1", "o/a2", "o/a3", "o/z1", "o/z2", "o/z3"}
	paths := map[string]string{
		"o/a1": "Alpha/x.go", "o/a2": "Alpha/y.go", "o/a3": "Alpha/z.go",
		"o/z1": "Zeta/p.go", "o/z2": "Zeta/q.go", "o/z3": "Zeta/r.go",
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
		NonInteractive: true,
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

func TestStream_clusterEmbedderUnreachable_emitsSkip(t *testing.T) {
	// Isolate the on-disk cluster cache, or a prior run's cached result would
	// short-circuit the embed step and no ClusterSkip would be surfaced.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: now, DefaultBranch: "main"},
			{ID: "o/b", Owner: "o", Name: "b", PushedAt: now, DefaultBranch: "main"},
			{ID: "o/c", Owner: "o", Name: "c", PushedAt: now, DefaultBranch: "main"},
		},
		t2: map[string]forge.T2Data{
			"o/a": {AheadCount: 1, Diffs: []forge.FileDiff{{Path: "a.go", Additions: 1}}},
			"o/b": {AheadCount: 1, Diffs: []forge.FileDiff{{Path: "b.go", Additions: 1}}},
			"o/c": {AheadCount: 1, Diffs: []forge.FileDiff{{Path: "c.go", Additions: 1}}},
		},
	}
	opts := Options{Tier: 2}
	opts.Cluster = ClusterOptions{
		Enabled:        true,
		TopN:           10,
		Epsilon:        0.6,
		MinClusterSize: 3,
		NonInteractive: true,
		Endpoint:       "http://127.0.0.1:1", // unreachable
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", opts)
	var skipSeen *ClusterSkip
	var anyClusterID bool
	for r := range ch {
		if r.ClusterSkip != nil {
			skipSeen = r.ClusterSkip
		}
		if r.Heat.ClusterID != "" {
			anyClusterID = true
		}
	}
	if skipSeen == nil {
		t.Fatalf("expected ClusterSkip to be surfaced when embedder unreachable")
	}
	if skipSeen.Code == "" {
		t.Errorf("expected non-empty skip code")
	}
	if anyClusterID {
		t.Errorf("expected no clusterID populated when embedder unreachable")
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
