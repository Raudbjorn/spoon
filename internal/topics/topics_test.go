package topics

import (
	"context"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func candidates(now time.Time) []forge.TopicRepo {
	return []forge.TopicRepo{
		{FullName: "big/canonical", Stars: 40000, ForkCount: 3000, PushedAt: now.Add(-24 * time.Hour)},
		{FullName: "mid/active", Stars: 2000, ForkCount: 400, PushedAt: now.Add(-7 * 24 * time.Hour)},
		{FullName: "starred/no-forks", Stars: 9000, ForkCount: 0, PushedAt: now},
		{FullName: "old/archived", Stars: 30000, ForkCount: 2500, PushedAt: now.Add(-3 * 365 * 24 * time.Hour), Archived: true},
		{FullName: "tiny/fresh", Stars: 5, ForkCount: 2, PushedAt: now},
	}
}

func TestSelectBest_RanksAndFilters(t *testing.T) {
	now := time.Now()
	got := SelectBest(candidates(now), 3, now)
	if len(got) != 3 {
		t.Fatalf("expected 3 selections, got %d", len(got))
	}
	if got[0].FullName != "big/canonical" {
		t.Errorf("top pick = %s, want big/canonical", got[0].FullName)
	}
	for _, s := range got {
		if s.FullName == "starred/no-forks" {
			t.Error("a repo with zero forks must never be selected — nothing to prospect")
		}
		if s.Score <= 0 || s.Score > 100 {
			t.Errorf("%s: score %v out of range", s.FullName, s.Score)
		}
		if len(s.Components) != 3 {
			t.Errorf("%s: expected 3 score components, got %v", s.FullName, s.Components)
		}
	}
}

func TestSelectBest_ArchivedDampedNotExcluded(t *testing.T) {
	now := time.Now()
	got := SelectBest(candidates(now), 5, now)
	foundArchived := false
	for _, s := range got {
		if s.FullName == "old/archived" {
			foundArchived = true
			// Un-archived twin would score roughly stars(~38)+network(~37): the
			// halving must show.
			if s.Score > 45 {
				t.Errorf("archived repo score %v looks undamped", s.Score)
			}
		}
	}
	if !foundArchived {
		t.Error("archived repos must remain selectable — their forks are the point")
	}
}

type stubSearcher struct {
	forge.Forge
	repos []forge.TopicRepo
	err   error
}

func (s stubSearcher) SearchTopicRepos(_ context.Context, _ string, _ int) ([]forge.TopicRepo, error) {
	return s.repos, s.err
}

func TestResolve_UnsupportedProvider(t *testing.T) {
	var plain forge.Forge // nil interface won't assert to TopicSearcher
	if _, err := Resolve(context.Background(), plain, "zig", 3); err != ErrUnsupported {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestResolve_NoProspectableRepos(t *testing.T) {
	s := stubSearcher{repos: []forge.TopicRepo{{FullName: "a/b", Stars: 100, ForkCount: 0}}}
	_, err := Resolve(context.Background(), s, "zig", 3)
	var noRepos *NoReposError
	if err == nil {
		t.Fatal("expected NoReposError")
	}
	if ok := errorAs(err, &noRepos); !ok || noRepos.Topic != "zig" {
		t.Fatalf("expected NoReposError for zig, got %v", err)
	}
}

func errorAs(err error, target **NoReposError) bool {
	e, ok := err.(*NoReposError)
	if ok {
		*target = e
	}
	return ok
}

func TestParseLanes_DedupesAndRejectsUnknown(t *testing.T) {
	got, err := ParseLanes("stars, updated, stars, forks")
	if err != nil {
		t.Fatalf("ParseLanes returned error: %v", err)
	}
	want := []forge.TopicLane{forge.TopicLaneStars, forge.TopicLaneUpdated, forge.TopicLaneForks}
	if len(got) != len(want) {
		t.Fatalf("lanes length=%d want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lane[%d]=%q want %q", i, got[i], want[i])
		}
	}
	if _, err := ParseLanes("stars,nope"); err == nil {
		t.Fatal("expected unknown lane error")
	}
}

func TestResolve_DefaultLaneMatchesResolve(t *testing.T) {
	s := stubSearcher{repos: candidates(time.Now())}
	gotResolve, err := Resolve(context.Background(), s, "zig", 0)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	gotOptions, err := ResolveWithOptions(context.Background(), s, "zig", ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveWithOptions: %v", err)
	}
	if len(gotResolve) != len(gotOptions) {
		t.Fatalf("selection length mismatch: Resolve=%d ResolveWithOptions=%d", len(gotResolve), len(gotOptions))
	}
	for i := range gotResolve {
		if gotResolve[i].FullName != gotOptions[i].FullName {
			t.Fatalf("selection[%d]=%s want %s", i, gotOptions[i].FullName, gotResolve[i].FullName)
		}
	}
}

func TestSelectBestFromLanes_DedupesAndPreservesLanes(t *testing.T) {
	now := time.Now()
	got := SelectBestFromLanes([]LaneCandidate{
		{Repo: forge.TopicRepo{FullName: "dup/repo", Stars: 100, ForkCount: 50, PushedAt: now}, Lane: forge.TopicLaneStars},
		{Repo: forge.TopicRepo{FullName: "zero/forks", Stars: 500, ForkCount: 0, PushedAt: now}, Lane: forge.TopicLaneStars},
		{Repo: forge.TopicRepo{FullName: "dup/repo", Stars: 9999, ForkCount: 9999, PushedAt: now}, Lane: forge.TopicLaneUpdated},
	}, 5, now)
	if len(got) != 1 {
		t.Fatalf("selection length=%d want 1: %#v", len(got), got)
	}
	if got[0].FullName != "dup/repo" {
		t.Fatalf("selected repo=%s want dup/repo", got[0].FullName)
	}
	wantLanes := []forge.TopicLane{forge.TopicLaneStars, forge.TopicLaneUpdated}
	if len(got[0].Lanes) != len(wantLanes) {
		t.Fatalf("lanes=%#v want %#v", got[0].Lanes, wantLanes)
	}
	for i := range wantLanes {
		if got[0].Lanes[i] != wantLanes[i] {
			t.Fatalf("lane[%d]=%q want %q", i, got[0].Lanes[i], wantLanes[i])
		}
	}
}
