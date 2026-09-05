package forksops

import (
	"context"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestStreamTouchingOrdersAndAnnotates(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	fk := func(id string, pushedAfter bool) forge.T1Data {
		f := forge.T1Data{ID: id, Owner: "o", Name: id, DefaultBranch: "main", CreatedAt: now.Add(-48 * time.Hour), PushedAt: now.Add(-48 * time.Hour)}
		if pushedAfter {
			f.PushedAt = now.Add(-time.Hour)
		}
		return f
	}
	prov := &fakeForge{
		parent: forge.ParentData{FullName: "up/repo", DefaultBranch: "main", PushedAt: now},
		forks:  []forge.T1Data{fk("hit", true), fk("miss", true), fk("stale", false)},
		t2: map[string]forge.T2Data{
			"hit":   {Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "src/a.go", Status: "modified", Additions: 1}}},
			"miss":  {Performed: true, AheadCount: 9, Diffs: []forge.FileDiff{{Path: "README.md"}}},
			"stale": {Performed: true, AheadCount: 0},
		},
	}
	var report TouchSummary
	opts := Options{Touching: []string{"src/**"}, TouchReport: &report, ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false}}
	ch, err := Stream(context.Background(), prov, "up", "repo", opts)
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("Stream must never drop results, got %d", len(got))
	}
	if got[0].Fork.ID != "hit" || got[0].Touching.Status != TouchMatched || got[0].Visibility.Status != VisibilityPinned {
		t.Fatalf("first result must be the pinned match: %+v", got[0])
	}
	if report.Matched != 1 || report.Unmatched != 2 {
		t.Fatalf("report %+v", report)
	}
}

func TestStreamTouchingRejectsBadPattern(t *testing.T) {
	prov := &fakeForge{parent: forge.ParentData{FullName: "up/repo", DefaultBranch: "main"}}
	if _, err := Stream(context.Background(), prov, "up", "repo", Options{Touching: []string{"["}}); err == nil {
		t.Fatal("expected pattern error before any fetch")
	}
}

func TestTouchingDispatchDemotesNeverPushed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	var order []string
	prov := &fakeForge{
		parent: forge.ParentData{FullName: "up/repo", DefaultBranch: "main", PushedAt: now},
		forks: []forge.T1Data{
			{ID: "never", Owner: "o", Name: "never", DefaultBranch: "main", Stars: 500, CreatedAt: now.Add(-time.Hour), PushedAt: now.Add(-time.Hour)},
			{ID: "pushed", Owner: "o", Name: "pushed", DefaultBranch: "main", CreatedAt: now.Add(-48 * time.Hour), PushedAt: now.Add(-time.Minute)},
		},
		t2:           map[string]forge.T2Data{"never": {Performed: true}, "pushed": {Performed: true}},
		concurrency:  1,
		compareOrder: &order,
	}
	ch, err := Stream(context.Background(), prov, "up", "repo", Options{Touching: []string{"x"}, ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if len(order) != 2 || order[0] != "pushed" {
		t.Fatalf("pushed-after-fork fork must be compared first, got %v", order)
	}
}
