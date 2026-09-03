package forksops

import (
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/pathmatch"
	"github.com/svnbjrn/spoon/internal/repo"
)

func mustMatcher(t *testing.T, pats ...string) pathmatch.Matcher {
	t.Helper()
	m, err := pathmatch.Compile(pats)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTouchOne(t *testing.T) {
	m := mustMatcher(t, "**/registry/antipatterns.mjs")
	diff := forge.FileDiff{Path: "cli/engine/registry/antipatterns.mjs", Status: "modified", Additions: 7}
	cases := []struct {
		name string
		r    Result
		want TouchStatus
		emit bool
	}{
		{"matched", Result{T2: &forge.T2Data{Performed: true, AheadCount: 2, Diffs: []forge.FileDiff{diff}}}, TouchMatched, true},
		{"unmatched", Result{T2: &forge.T2Data{Performed: true, AheadCount: 2, Diffs: []forge.FileDiff{{Path: "README.md"}}}}, TouchUnmatched, false},
		{"zero_ahead_is_unmatched_without_scanning", Result{T2: &forge.T2Data{Performed: true, AheadCount: 0, Diffs: []forge.FileDiff{diff}}}, TouchUnmatched, false},
		{"compare_404", Result{T2: &forge.T2Data{Performed: false}}, TouchUnknown, false},
		{"no_t2_reserve", Result{BudgetSkip: &StageSkip{Stage: "compare"}}, TouchUnknown, false},
		{"no_t2_never_pushed", Result{Fork: forge.T1Data{CreatedAt: time.Unix(10, 0), PushedAt: time.Unix(10, 0)}}, TouchNeverPushed, false},
		{"cache_rows_missing", Result{T2FromCache: true, T2: &forge.T2Data{Performed: true, AheadCount: 3, TotalAdditions: 9}}, TouchUnknown, false},
		{"cache_net_empty", Result{T2FromCache: true, T2: &forge.T2Data{Performed: true, AheadCount: 3}}, TouchUnmatched, false},
		{"renamed_from_matches_previous_path", Result{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "x/new.mjs", PreviousPath: "a/registry/antipatterns.mjs", Status: "renamed"}}}}, TouchMatched, true},
	}
	for _, tc := range cases {
		got := touchOne(m, nil, tc.r)
		if got.Status != tc.want {
			t.Errorf("%s: status=%s want %s (reason %q)", tc.name, got.Status, tc.want, got.Reason)
		}
		if got.Emit() != tc.emit {
			t.Errorf("%s: Emit=%v want %v", tc.name, got.Emit(), tc.emit)
		}
	}
}

func TestTouchOnePartial(t *testing.T) {
	m := mustMatcher(t, "never/matches")
	diffs := make([]forge.FileDiff, forge.CompareFilesCap)
	for i := range diffs {
		diffs[i].Path = "f" + string(rune('a'+i%26))
	}
	got := touchOne(m, nil, Result{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: diffs}})
	if got.Status != TouchUnmatched || !got.Partial || !got.Emit() {
		t.Fatalf("300-file unmatched must be partial and emittable: %+v", got)
	}
	got = touchOne(m, nil, Result{T2: &forge.T2Data{Performed: true, AheadCount: 1, FilesTruncated: true, Diffs: diffs[:5]}})
	if !got.Partial {
		t.Fatal("FilesTruncated flag must mark partial")
	}
}

func TestScoreTouchingSummary(t *testing.T) {
	m := mustMatcher(t, "a.go")
	rs := []Result{
		{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "a.go"}}}},
		{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "b.go"}}}},
		{BudgetSkip: &StageSkip{Stage: "compare"}},
	}
	s := scoreTouching(m, nil, rs)
	if s.Matched != 1 || s.Unmatched != 1 || s.Unknown != 1 {
		t.Fatalf("summary %+v", s)
	}
	if rs[0].Touching == nil || rs[0].Touching.Files[0].Pattern != "a.go" {
		t.Fatalf("pattern not recorded: %+v", rs[0].Touching)
	}
	if s.CentralityMethod != "" || rs[0].Touching.Impact != 0 {
		t.Fatalf("no centrality backend must yield zero impact: %+v", rs[0].Touching)
	}
}

func TestTouchOneCentrality(t *testing.T) {
	m := mustMatcher(t, "**/*.go")
	// repo.DirectoryCentrality keys directories with a trailing slash
	// (internal/repo/centrality.go:205-215) and ScoreFork averages them.
	c := repo.DirectoryCentrality{DirScore: map[string]float64{"core/": 1.0, "docs/": 0.1}}
	r := Result{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{
		{Path: "docs/x.go"}, {Path: "core/y.go"},
	}}}
	got := touchOne(m, c, r)
	if got.CentralityMethod != "directory" {
		t.Fatalf("method %q", got.CentralityMethod)
	}
	if got.Files[0].Centrality >= got.Files[1].Centrality {
		t.Fatalf("per-file centrality not applied: %+v", got.Files)
	}
	if got.Impact != got.Files[1].Centrality || got.Impact <= 0.9 {
		t.Fatalf("Impact must be the max file centrality: %+v", got)
	}
}

func TestSortTouchingLanesByImpact(t *testing.T) {
	rs := []Result{
		{Fork: forge.T1Data{ID: "rest"}},
		{Fork: forge.T1Data{ID: "partial"}, Touching: &TouchMatch{Status: TouchUnmatched, Partial: true}},
		{Fork: forge.T1Data{ID: "low"}, Touching: &TouchMatch{Status: TouchMatched, Impact: 0.2}},
		{Fork: forge.T1Data{ID: "high"}, Touching: &TouchMatch{Status: TouchMatched, Impact: 0.9}},
	}
	sortTouchingLanes(rs)
	want := []string{"high", "low", "partial", "rest"}
	for i, w := range want {
		if rs[i].Fork.ID != w {
			t.Fatalf("pos %d = %s want %s", i, rs[i].Fork.ID, w)
		}
	}
}
