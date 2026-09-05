package forksops

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// lastTouchFakeForge wraps *batchFakeForge to also implement
// forge.LastTouchProvider, so last-touch tests can drive both the
// pre-dispatch batch (for the BranchSelection decide() tests) and the
// last-touch gate itself from one fake.
type lastTouchFakeForge struct {
	*batchFakeForge

	upstream    map[string]forge.PathLastTouch
	upstreamErr error

	// forkEntries and forkOutcome are keyed by "<forkID>@<dir>". A key
	// absent from forkOutcome defaults to forge.LastTouchOK when present in
	// forkEntries, or forge.LastTouchNotFound otherwise.
	forkEntries map[string]map[string]string
	forkOutcome map[string]forge.LastTouchOutcome

	mu               sync.Mutex
	calls            []string // "<forkID>@<ref>@<dir>", one per ForkLastTouch call
	pathLastTouchHit int      // incremented once per PathLastTouch call
}

func (f *lastTouchFakeForge) PathLastTouch(_ context.Context, _ []string) (map[string]forge.PathLastTouch, error) {
	f.mu.Lock()
	f.pathLastTouchHit++
	f.mu.Unlock()
	if f.upstreamErr != nil {
		return nil, f.upstreamErr
	}
	return f.upstream, nil
}

func (f *lastTouchFakeForge) ForkLastTouch(_ context.Context, fork forge.T1Data, ref, dir string) (map[string]string, forge.LastTouchOutcome) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s@%s@%s", fork.ID, ref, dir))
	f.mu.Unlock()

	key := fork.ID + "@" + dir
	if out, ok := f.forkOutcome[key]; ok {
		return f.forkEntries[key], out
	}
	if entries, ok := f.forkEntries[key]; ok {
		return entries, forge.LastTouchOK
	}
	return nil, forge.LastTouchNotFound
}

func (f *lastTouchFakeForge) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *lastTouchFakeForge) pathLastTouchCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pathLastTouchHit
}

var _ forge.LastTouchProvider = (*lastTouchFakeForge)(nil)

// baseFork returns a single fork whose default branch is ahead of upstream,
// so the batch resolves it with NeedsREST == true and the worker reaches
// the last-touch gate.
func baseFork(now time.Time) forge.T1Data {
	return forge.T1Data{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now, CreatedAt: now.Add(-time.Hour)}
}

func TestLastTouchGate_equalOIDSkipsCompare(t *testing.T) {
	now := time.Now()
	var resolved []string
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{
			fakeForge: &fakeForge{
				parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
				forks:       []forge.T1Data{baseFork(now)},
				concurrency: 1,
			},
			batch: map[string]forge.ForkDivergence{
				"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "tip1", AheadBy: 2, BehindBy: 1}},
			},
			resolvedCalls: &resolved,
		},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go": {SHA: "C1", CommitsSince: 5},
		},
		forkEntries: map[string]map[string]string{
			"o/a@src": {"a.go": "C1"},
		},
	}
	var compareReport CompareSummary
	var touchReport TouchSummary
	var logger bytes.Buffer
	opts := Options{
		Tier: 2, Touching: []string{"src/a.go"},
		CompareReport: &compareReport, TouchReport: &touchReport,
		ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false},
		Logger: &logger,
	}
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	r := got[0]
	if len(resolved) != 0 {
		t.Errorf("CompareResolved should not be called on a last-touch skip, got %v", resolved)
	}
	if r.Touching == nil || r.Touching.Status != TouchUnmatched || r.Touching.Reason != "last_touch" {
		t.Fatalf("Touching = %+v, want unmatched/last_touch", r.Touching)
	}
	if !r.T2FilesUnfetched {
		t.Error("T2FilesUnfetched should be true on a last-touch skip")
	}
	if r.T2 == nil || !r.T2.Performed || r.T2.AheadCount != 2 || r.T2.BehindCount != 1 || r.T2.HeadSHA != "tip1" {
		t.Errorf("unexpected synthesised T2: %+v", r.T2)
	}
	if r.T2.CompareSource != "graphql_batch" {
		t.Errorf("CompareSource = %q, want graphql_batch", r.T2.CompareSource)
	}
	if compareReport.LastTouchSkipped != 1 {
		t.Errorf("CompareSummary.LastTouchSkipped = %d, want 1", compareReport.LastTouchSkipped)
	}
	if touchReport.LastTouchSkipped != 1 {
		t.Errorf("TouchSummary.LastTouchSkipped = %d, want 1", touchReport.LastTouchSkipped)
	}
	if touchReport.LastTouchLookedUp != 1 {
		t.Errorf("TouchSummary.LastTouchLookedUp = %d, want 1", touchReport.LastTouchLookedUp)
	}
	const wantSuffix = "last_touch: gated 0 · looked_up 1 · skipped 1 · mismatch 0 · unavailable 0"
	if !strings.Contains(logger.String(), wantSuffix) {
		t.Errorf("[touching] stderr line missing last-touch suffix %q, got log:\n%s", wantSuffix, logger.String())
	}
}

// TestOptionsNoLastTouchSkipsGateEntirely uses the exact fixture from
// TestLastTouchGate_equalOIDSkipsCompare -- upstream and fork OIDs equal, so
// the gate would definitely skip the REST compare if built -- but sets
// Options.NoLastTouch. The gate must never be built at all: no PathLastTouch
// round-trip (the once-per-run upstream lookup newLastTouchGate makes before
// any fork is examined), no ForkLastTouch call, and every TouchSummary
// last-touch counter stays zero. This is the fix for the CLI's
// --no-tree-commit-info: without NoLastTouch, the gate still built and spent
// the upstream lookup even though every fork's ForkLastTouch call was
// guaranteed to report the capability off, which the caller could not tell
// apart from a real lookup failure (task 8 fix round 1).
func TestOptionsNoLastTouchSkipsGateEntirely(t *testing.T) {
	now := time.Now()
	var resolved []string
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{
			fakeForge: &fakeForge{
				parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
				forks:       []forge.T1Data{baseFork(now)},
				concurrency: 1,
			},
			batch: map[string]forge.ForkDivergence{
				"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "tip1", AheadBy: 2, BehindBy: 1}},
			},
			resolvedCalls: &resolved,
		},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go": {SHA: "C1", CommitsSince: 5},
		},
		forkEntries: map[string]map[string]string{
			"o/a@src": {"a.go": "C1"},
		},
	}
	var compareReport CompareSummary
	var touchReport TouchSummary
	var logger bytes.Buffer
	opts := Options{
		Tier: 2, Touching: []string{"src/a.go"}, NoLastTouch: true,
		CompareReport: &compareReport, TouchReport: &touchReport,
		ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false},
		Logger: &logger,
	}
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if n := ff.pathLastTouchCallCount(); n != 0 {
		t.Errorf("PathLastTouch calls = %d, want 0 (NoLastTouch must stop the gate before the upstream lookup)", n)
	}
	if n := ff.callCount(); n != 0 {
		t.Errorf("ForkLastTouch calls = %d, want 0, got %v", n, ff.calls)
	}
	r := got[0]
	if r.T2FilesUnfetched {
		t.Error("T2FilesUnfetched should be false: the last-touch skip stage never ran")
	}
	if r.Touching != nil && r.Touching.Reason == "last_touch" {
		t.Errorf("Touching.Reason = %q, want anything but last_touch: the gate never ran to produce that verdict", r.Touching.Reason)
	}
	if compareReport.LastTouchSkipped != 0 {
		t.Errorf("CompareSummary.LastTouchSkipped = %d, want 0", compareReport.LastTouchSkipped)
	}
	if touchReport.LastTouchGated != 0 || touchReport.LastTouchLookedUp != 0 || touchReport.LastTouchSkipped != 0 ||
		touchReport.LastTouchMismatch != 0 || touchReport.LastTouchUnavailable != 0 {
		t.Errorf("TouchSummary last-touch counters not all zero: %+v", touchReport)
	}
}

func TestLastTouchGate_behindExceedsCommitsSinceNeverLooksUp(t *testing.T) {
	now := time.Now()
	var resolved []string
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{
			fakeForge: &fakeForge{
				parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
				forks:       []forge.T1Data{baseFork(now)},
				concurrency: 1,
				t2:          map[string]forge.T2Data{"o/a": {Performed: true, AheadCount: 2}},
			},
			batch: map[string]forge.ForkDivergence{
				// Behind(10) > CommitsSince(5): the selected branch cannot
				// be certain to contain C.
				"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "tip1", AheadBy: 2, BehindBy: 10}},
			},
			resolvedCalls: &resolved,
		},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go": {SHA: "C1", CommitsSince: 5},
		},
		forkEntries: map[string]map[string]string{
			"o/a@src": {"a.go": "C1"}, // would match if looked up -- must not be reached
		},
	}
	var touchReport TouchSummary
	opts := Options{
		Tier: 2, Touching: []string{"src/a.go"}, TouchReport: &touchReport,
		ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if ff.callCount() != 0 {
		t.Errorf("ForkLastTouch should not be called when behind > CommitsSince, got calls: %v", ff.calls)
	}
	if len(resolved) != 1 {
		t.Errorf("CompareResolved should still be called once, got %v", resolved)
	}
	if touchReport.LastTouchGated != 1 {
		t.Errorf("TouchSummary.LastTouchGated = %d, want 1", touchReport.LastTouchGated)
	}
	if touchReport.LastTouchLookedUp != 0 {
		t.Errorf("TouchSummary.LastTouchLookedUp = %d, want 0", touchReport.LastTouchLookedUp)
	}
}

func TestLastTouchGate_unavailableFallsThroughToCompare(t *testing.T) {
	for _, outcome := range []forge.LastTouchOutcome{forge.LastTouchNotFound, forge.LastTouchDisabled} {
		t.Run(outcome.String(), func(t *testing.T) {
			now := time.Now()
			var resolved []string
			ff := &lastTouchFakeForge{
				batchFakeForge: &batchFakeForge{
					fakeForge: &fakeForge{
						parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
						forks:       []forge.T1Data{baseFork(now)},
						concurrency: 1,
						t2:          map[string]forge.T2Data{"o/a": {Performed: true, AheadCount: 2}},
					},
					batch: map[string]forge.ForkDivergence{
						"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "tip1", AheadBy: 2, BehindBy: 1}},
					},
					resolvedCalls: &resolved,
				},
				upstream: map[string]forge.PathLastTouch{
					"src/a.go": {SHA: "C1", CommitsSince: 5},
				},
				forkOutcome: map[string]forge.LastTouchOutcome{"o/a@src": outcome},
			}
			var touchReport TouchSummary
			opts := Options{
				Tier: 2, Touching: []string{"src/a.go"}, TouchReport: &touchReport,
				ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false},
			}
			ch, err := Stream(context.Background(), ff, "o", "r", opts)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			for range ch {
			}
			if len(resolved) != 1 {
				t.Errorf("CompareResolved should be called once on an unavailable last-touch lookup, got %v", resolved)
			}
			if touchReport.LastTouchUnavailable != 1 {
				t.Errorf("TouchSummary.LastTouchUnavailable = %d, want 1", touchReport.LastTouchUnavailable)
			}
			if touchReport.LastTouchLookedUp != 1 {
				t.Errorf("TouchSummary.LastTouchLookedUp = %d, want 1 (the lookup was attempted, just unusable)", touchReport.LastTouchLookedUp)
			}
		})
	}
}

func TestLastTouchGate_wildcardPatternDisablesStage(t *testing.T) {
	now := time.Now()
	var resolved []string
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{
			fakeForge: &fakeForge{
				parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
				forks:       []forge.T1Data{baseFork(now)},
				concurrency: 1,
				t2:          map[string]forge.T2Data{"o/a": {Performed: true, AheadCount: 2}},
			},
			batch: map[string]forge.ForkDivergence{
				"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "tip1", AheadBy: 2, BehindBy: 1}},
			},
			resolvedCalls: &resolved,
		},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go": {SHA: "C1", CommitsSince: 5},
		},
		// Equal OIDs -- if the gate ran despite the wildcard pattern, this
		// would (wrongly) skip the compare.
		forkEntries: map[string]map[string]string{
			"o/a@src": {"a.go": "C1"},
		},
	}
	var touchReport TouchSummary
	opts := Options{
		Tier: 2, Touching: []string{"src/*.go"}, TouchReport: &touchReport,
		ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if ff.callCount() != 0 {
		t.Errorf("ForkLastTouch must never be called when a --touching pattern is a wildcard, got calls: %v", ff.calls)
	}
	if len(resolved) != 1 {
		t.Errorf("CompareResolved should still be called once, got %v", resolved)
	}
	if touchReport.LastTouchGated+touchReport.LastTouchLookedUp+touchReport.LastTouchSkipped+touchReport.LastTouchMismatch+touchReport.LastTouchUnavailable != 0 {
		t.Errorf("last-touch tallies should all be zero when the stage is disabled: %+v", touchReport)
	}
}

func TestLastTouchGate_mismatchCallsCompare(t *testing.T) {
	now := time.Now()
	var resolved []string
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{
			fakeForge: &fakeForge{
				parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
				forks:       []forge.T1Data{baseFork(now)},
				concurrency: 1,
				t2:          map[string]forge.T2Data{"o/a": {Performed: true, AheadCount: 2}},
			},
			batch: map[string]forge.ForkDivergence{
				"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "tip1", AheadBy: 2, BehindBy: 1}},
			},
			resolvedCalls: &resolved,
		},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go": {SHA: "C1", CommitsSince: 5},
		},
		forkEntries: map[string]map[string]string{
			"o/a@src": {"a.go": "C2"}, // differs from upstream's C1
		},
	}
	var touchReport TouchSummary
	opts := Options{
		Tier: 2, Touching: []string{"src/a.go"}, TouchReport: &touchReport,
		ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if len(resolved) != 1 {
		t.Errorf("CompareResolved should be called once on a mismatched OID, got %v", resolved)
	}
	if touchReport.LastTouchMismatch != 1 {
		t.Errorf("TouchSummary.LastTouchMismatch = %d, want 1", touchReport.LastTouchMismatch)
	}
	if touchReport.LastTouchLookedUp != 1 {
		t.Errorf("TouchSummary.LastTouchLookedUp = %d, want 1", touchReport.LastTouchLookedUp)
	}
}

func TestLastTouchGate_groupsTargetsByDirectory(t *testing.T) {
	now := time.Now()
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{
			fakeForge: &fakeForge{
				parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
				forks:       []forge.T1Data{baseFork(now)},
				concurrency: 1,
			},
			batch: map[string]forge.ForkDivergence{
				"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "tip1", AheadBy: 2, BehindBy: 1}},
			},
		},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go":  {SHA: "C1", CommitsSince: 5},
			"docs/b.md": {SHA: "C2", CommitsSince: 5},
		},
		forkEntries: map[string]map[string]string{
			"o/a@src":  {"a.go": "C1"},
			"o/a@docs": {"b.md": "C2"},
		},
	}
	opts := Options{
		Tier: 2, Touching: []string{"src/a.go", "docs/b.md"},
		ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if got := ff.callCount(); got != 2 {
		t.Errorf("ForkLastTouch calls = %d, want 2 (one per directory), got %v", got, ff.calls)
	}

	// Two targets sharing a directory: exactly one call for that directory.
	ff2 := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{
			fakeForge: &fakeForge{
				parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
				forks:       []forge.T1Data{baseFork(now)},
				concurrency: 1,
			},
			batch: map[string]forge.ForkDivergence{
				"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "tip1", AheadBy: 2, BehindBy: 1}},
			},
		},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go": {SHA: "C1", CommitsSince: 5},
			"src/b.go": {SHA: "C2", CommitsSince: 5},
		},
		forkEntries: map[string]map[string]string{
			"o/a@src": {"a.go": "C1", "b.go": "C2"},
		},
	}
	opts2 := Options{
		Tier: 2, Touching: []string{"src/a.go", "src/b.go"},
		ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false},
	}
	ch2, err := Stream(context.Background(), ff2, "o", "r", opts2)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch2 {
	}
	if got := ff2.callCount(); got != 1 {
		t.Errorf("ForkLastTouch calls = %d, want 1 (same directory), got %v", got, ff2.calls)
	}
}

// TestLastTouchGate_unresolvedTargetNeverSkipsAndLogsOnce covers a target
// with no upstream history at all (absent from PathLastTouch's result map,
// distinct from CommitsSince == 0): the gate must never license a skip on
// it -- a path upstream never touched is exactly the case where fork-side
// content is most likely the fork's own work -- and the construction-time
// log line must name it so an operator can tell "no baseline" apart from
// "lookups failed" in the unavailable tally.
func TestLastTouchGate_unresolvedTargetNeverSkipsAndLogsOnce(t *testing.T) {
	now := time.Now()
	var resolved []string
	var logger bytes.Buffer
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{
			fakeForge: &fakeForge{
				parent:      forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
				forks:       []forge.T1Data{baseFork(now)},
				concurrency: 1,
				t2:          map[string]forge.T2Data{"o/a": {Performed: true, AheadCount: 2}},
			},
			batch: map[string]forge.ForkDivergence{
				"o/a": {Resolved: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "tip1", AheadBy: 2, BehindBy: 1}},
			},
			resolvedCalls: &resolved,
		},
		// "src/new.go" deliberately absent from upstream: no history there.
		upstream: map[string]forge.PathLastTouch{},
	}
	var touchReport TouchSummary
	opts := Options{
		Tier: 2, Touching: []string{"src/new.go"}, TouchReport: &touchReport,
		ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false},
		Logger: &logger,
	}
	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if ff.callCount() != 0 {
		t.Errorf("ForkLastTouch should not be called for an unresolved target, got calls: %v", ff.calls)
	}
	if len(resolved) != 1 {
		t.Errorf("CompareResolved should be called once, got %v", resolved)
	}
	if touchReport.LastTouchUnavailable != 1 {
		t.Errorf("TouchSummary.LastTouchUnavailable = %d, want 1", touchReport.LastTouchUnavailable)
	}
	if touchReport.LastTouchLookedUp != 0 {
		t.Errorf("TouchSummary.LastTouchLookedUp = %d, want 0 (no upstream baseline to check against)", touchReport.LastTouchLookedUp)
	}
	if !strings.Contains(logger.String(), "no upstream history for [src/new.go]") {
		t.Errorf("expected a one-time log naming the unresolved path, got log:\n%s", logger.String())
	}
}

// TestLastTouchGate_emptyUpstreamSHATreatedAsUnresolved (M1) covers an
// upstream PathLastTouch entry that is present but carries SHA == "" --
// distinct from a wholly absent key (already covered by
// TestLastTouchGate_unresolvedTargetNeverSkipsAndLogsOnce), and just as
// unusable: the gate must treat it as unresolved, never license a skip on
// it, and log it the same way.
func TestLastTouchGate_emptyUpstreamSHATreatedAsUnresolved(t *testing.T) {
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{fakeForge: &fakeForge{}},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go": {SHA: "", CommitsSince: 5}, // present, but no resolvable SHA
		},
		forkEntries: map[string]map[string]string{
			// Would (wrongly) match if the gate ever compared "" == "".
			"o/x@src": {"a.go": ""},
		},
	}
	var logger bytes.Buffer
	g := newLastTouchGate(context.Background(), ff, []string{"src/a.go"}, &logger)
	if g == nil {
		t.Fatal("gate should still build (PathLastTouch itself did not error)")
	}
	sel := forge.BranchSelection{Branch: "main", Behind: 1}
	ok, outcome := g.decide(context.Background(), forge.T1Data{ID: "o/x"}, sel)
	if ok || outcome != lastTouchUnavailableOutcome {
		t.Fatalf("decide = (%v, %v), want (false, unavailable): an empty upstream SHA must never license a skip", ok, outcome)
	}
	if ff.callCount() != 0 {
		t.Errorf("ForkLastTouch should not be called for a target with no resolvable upstream SHA, got calls: %v", ff.calls)
	}
	if !strings.Contains(logger.String(), "no upstream history for [src/a.go]") {
		t.Errorf("expected the unresolved-target log line, got:\n%s", logger.String())
	}
}

// TestLastTouchGate_emptyForkOIDNeverMatches (M1) covers decide()'s other
// half: upstream resolves a real SHA, but the fork's own ForkLastTouch
// entry comes back with an empty OID for the target (present in the map,
// but no resolvable commit fork-side). That must classify as unavailable,
// never as a match (which an unguarded oid != t.c.SHA check would already
// avoid, since t.c.SHA is guaranteed non-empty by the gate-build guard --
// but the outcome must specifically be "unavailable", not "mismatch",
// since nothing was actually disproved).
func TestLastTouchGate_emptyForkOIDNeverMatches(t *testing.T) {
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{fakeForge: &fakeForge{}},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go": {SHA: "C1", CommitsSince: 5},
		},
		forkEntries: map[string]map[string]string{
			"o/x@src": {"a.go": ""}, // present entry, empty OID
		},
	}
	g := newLastTouchGate(context.Background(), ff, []string{"src/a.go"}, io.Discard)
	if g == nil {
		t.Fatal("gate should build")
	}
	sel := forge.BranchSelection{Branch: "main", Behind: 1}
	ok, outcome := g.decide(context.Background(), forge.T1Data{ID: "o/x"}, sel)
	if ok || outcome != lastTouchUnavailableOutcome {
		t.Fatalf("decide = (%v, %v), want (false, unavailable): an empty fork OID must never be treated as equal to upstream's SHA", ok, outcome)
	}
	if ff.callCount() != 1 {
		t.Errorf("ForkLastTouch calls = %d, want 1 (the lookup was attempted, just unusable)", ff.callCount())
	}
}

// TestLastTouchGate_consecutiveMissBreaker (I2) drives the gate directly
// (newLastTouchGate + decide), bypassing Stream: lastTouchMaxConsecutiveMisses
// consecutive real lookups that never resolve to a skip must disable the
// stage for the rest of the run -- the next decision tallies straight as
// unavailable without ever calling ForkLastTouch again, and the disable is
// logged exactly once. Table-driven over both non-skip outcomes
// (mismatch and unavailable) so both call sites of recordLookupOutcome(false)
// -- the oid != t.c.SHA branch and the outcome != forge.LastTouchOK/missing-
// entry branch -- are actually exercised, not just one of them.
func TestLastTouchGate_consecutiveMissBreaker(t *testing.T) {
	cases := []struct {
		name        string
		forkEntries map[string]map[string]string
		wantOutcome lastTouchOutcome
	}{
		{
			name: "mismatch",
			forkEntries: map[string]map[string]string{
				"o/x@src": {"a.go": "C2"}, // differs from upstream's C1
			},
			wantOutcome: lastTouchMismatchOutcome,
		},
		{
			name:        "unavailable",
			forkEntries: nil, // "o/x" absent from forkEntries -> ForkLastTouch reports NotFound
			wantOutcome: lastTouchUnavailableOutcome,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ff := &lastTouchFakeForge{
				batchFakeForge: &batchFakeForge{fakeForge: &fakeForge{}},
				upstream: map[string]forge.PathLastTouch{
					"src/a.go": {SHA: "C1", CommitsSince: 5},
				},
				forkEntries: tc.forkEntries,
			}
			var logger bytes.Buffer
			g := newLastTouchGate(context.Background(), ff, []string{"src/a.go"}, &logger)
			if g == nil {
				t.Fatal("gate should build: upstream resolved a baseline for src/a.go")
			}
			sel := forge.BranchSelection{Branch: "main", Behind: 1}
			fork := forge.T1Data{ID: "o/x"}

			// Same fork ID every call: decide() carries no per-fork memo, so
			// this exercises the breaker's own counter, not incidental
			// per-fork caching.
			for i := 0; i < lastTouchMaxConsecutiveMisses; i++ {
				ok, outcome := g.decide(context.Background(), fork, sel)
				if ok || outcome != tc.wantOutcome {
					t.Fatalf("miss %d: decide = (%v, %v), want (false, %v)", i, ok, outcome, tc.wantOutcome)
				}
			}
			if got := ff.callCount(); got != lastTouchMaxConsecutiveMisses {
				t.Fatalf("ForkLastTouch calls = %d, want %d (one real lookup per miss so far)", got, lastTouchMaxConsecutiveMisses)
			}

			// The next decision: the breaker has tripped, so ForkLastTouch
			// must not be called again, regardless of which failure kind
			// tripped it.
			ok, outcome := g.decide(context.Background(), forge.T1Data{ID: "o/next"}, sel)
			if ok || outcome != lastTouchUnavailableOutcome {
				t.Fatalf("post-trip decide = (%v, %v), want (false, unavailable)", ok, outcome)
			}
			if got := ff.callCount(); got != lastTouchMaxConsecutiveMisses {
				t.Fatalf("ForkLastTouch calls after breaker trips = %d, want unchanged %d", got, lastTouchMaxConsecutiveMisses)
			}
			wantLog := fmt.Sprintf("last-touch lookups disabled after %d consecutive non-skips (mismatch/unavailable); remaining forks go straight to compare", lastTouchMaxConsecutiveMisses)
			if strings.Count(logger.String(), wantLog) != 1 {
				t.Errorf("breaker log line must appear exactly once, got log:\n%s", logger.String())
			}
			_, lookedUp, _, mismatch, unavailable := g.counts()
			if lookedUp != lastTouchMaxConsecutiveMisses {
				t.Errorf("LastTouchLookedUp = %d, want %d (the post-trip decision never reached the provider)", lookedUp, lastTouchMaxConsecutiveMisses)
			}
			if tc.wantOutcome == lastTouchMismatchOutcome {
				if mismatch != lastTouchMaxConsecutiveMisses {
					t.Errorf("LastTouchMismatch = %d, want %d", mismatch, lastTouchMaxConsecutiveMisses)
				}
				if unavailable != 1 {
					t.Errorf("LastTouchUnavailable = %d, want 1 (only the post-trip decision)", unavailable)
				}
			} else if unavailable != lastTouchMaxConsecutiveMisses+1 {
				t.Errorf("LastTouchUnavailable = %d, want %d", unavailable, lastTouchMaxConsecutiveMisses+1)
			}
		})
	}
}

// TestLastTouchGate_skipResetsConsecutiveMissCounter (I2) interleaves a
// skip among near-breaker-threshold mismatches: the skip must reset the
// counter, so the breaker never trips and every subsequent fork still gets
// a real lookup. Uses genuine mismatches (not the "unavailable" outcome)
// so the reset is proven against the same failure kind the brief's spec
// names ("a skip resets the counter").
func TestLastTouchGate_skipResetsConsecutiveMissCounter(t *testing.T) {
	ff := &lastTouchFakeForge{
		batchFakeForge: &batchFakeForge{fakeForge: &fakeForge{}},
		upstream: map[string]forge.PathLastTouch{
			"src/a.go": {SHA: "C1", CommitsSince: 5},
		},
		forkEntries: map[string]map[string]string{
			"o/skip@src": {"a.go": "C1"}, // equal OID -> skip
			"o/miss@src": {"a.go": "C2"}, // differs from upstream's C1 -> mismatch
		},
	}
	var logger bytes.Buffer
	g := newLastTouchGate(context.Background(), ff, []string{"src/a.go"}, &logger)
	if g == nil {
		t.Fatal("gate should build")
	}
	sel := forge.BranchSelection{Branch: "main", Behind: 1}
	missFork := forge.T1Data{ID: "o/miss"}
	skipFork := forge.T1Data{ID: "o/skip"}

	// lastTouchMaxConsecutiveMisses-1 mismatches, then a skip, twice: the
	// breaker must never trip, since the skip resets the counter back to
	// zero each round.
	for round := 0; round < 2; round++ {
		for i := 0; i < lastTouchMaxConsecutiveMisses-1; i++ {
			ok, outcome := g.decide(context.Background(), missFork, sel)
			if ok || outcome != lastTouchMismatchOutcome {
				t.Fatalf("round %d miss %d: decide = (%v, %v), want (false, mismatch)", round, i, ok, outcome)
			}
		}
		ok, outcome := g.decide(context.Background(), skipFork, sel)
		if !ok || outcome != lastTouchSkippedOutcome {
			t.Fatalf("round %d: skip decide = (%v, %v), want (true, skipped)", round, ok, outcome)
		}
	}
	if strings.Contains(logger.String(), "last-touch lookups disabled") {
		t.Errorf("breaker must never trip when a skip resets the counter each round, got log:\n%s", logger.String())
	}
	// One more decision after the last skip: still a real lookup (the
	// breaker never engaged).
	before := ff.callCount()
	g.decide(context.Background(), missFork, sel)
	if got := ff.callCount(); got != before+1 {
		t.Errorf("ForkLastTouch calls = %d, want %d (breaker inactive, real lookup still made)", got, before+1)
	}
}
