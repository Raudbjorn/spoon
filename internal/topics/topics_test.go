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
