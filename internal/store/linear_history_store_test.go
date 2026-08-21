package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// Persists a fork carrying every linear-history scalar plus the raw
// vector, then loads it back through the relational history loader. The
// "do not throw anything away" guarantee from the plan is checked here:
// every field the sweep produced must round-trip.
func TestPersistForkRoundtrip_PreservesLinearHistory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	repo := RepoRecord{
		Provider: "github", Host: "example.com",
		Owner: "upstream", Name: "repo",
		FirstSeen: time.Now(), LastSeen: time.Now(),
	}
	linear := true
	in := Snapshot{
		Repo: repo,
		Fork: ForkRecord{ForgeID: "fork1", Owner: "alice", Name: "fork1", URL: "https://example.com/alice/fork1",
			MergeCommits:         3,
			MergeCommitTruncated: true,
		},
		T1: &forge.T1Data{
			ID: "alice/fork1", Owner: "alice", Name: "fork1",
			LinearHistory:      &linear,
			MergeCommitHistory: []int{2, 1, 2},
		},
	}
	if err := s.UpsertSnapshot(t.Context(), in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	snap, err := s.LoadRepoSnapshot(t.Context(), "github", "example.com", "upstream", "repo")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(snap.Forks) != 1 {
		t.Fatalf("want 1 fork, got %d", len(snap.Forks))
	}
	got := snap.Forks[0]
	if got.MergeCommits != 3 {
		t.Errorf("MergeCommits = %d, want 3", got.MergeCommits)
	}
	if !got.MergeCommitTruncated {
		t.Error("MergeCommitTruncated = false, want true")
	}
	if got.T1.LinearHistory == nil || *got.T1.LinearHistory != true {
		t.Errorf("T1.LinearHistory = %v, want &true", got.T1.LinearHistory)
	}
	want := []int{2, 1, 2}
	if len(got.T1.MergeCommitHistory) != len(want) {
		t.Fatalf("MergeCommitHistory len = %d, want %d", len(got.T1.MergeCommitHistory), len(want))
	}
	for i, v := range want {
		if got.T1.MergeCommitHistory[i] != v {
			t.Errorf("MergeCommitHistory[%d] = %d, want %d", i, got.T1.MergeCommitHistory[i], v)
		}
	}
}
