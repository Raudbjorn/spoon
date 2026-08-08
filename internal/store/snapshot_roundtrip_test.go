package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// The store is the single global cache, so a snapshot written with full T1/T2
// data must come back field-for-field — in particular the triage scalars
// (Upstreamed, MNA, ActiveBranch …) that the old JSON cache silently dropped
// on its round trip.
func TestRepoSnapshotRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	parent := &forge.ParentData{
		FullName: "up/stream", Description: "d", DefaultBranch: "main", HeadSHA: "head123",
		Stars: 42, Forks: 7, Size: 1024, PushedAt: now, URL: "https://example.com/up/stream",
		Language: "Go", Topics: []string{"a", "b"},
	}
	t1 := &forge.T1Data{
		ID: "alice/stream", Owner: "alice", Name: "stream", URL: "https://example.com/alice/stream",
		DefaultBranch: "dev", Stars: 3, PushedAt: now.Add(-time.Hour), SubForkCount: 1,
		OpenPRCount: 2, ReleaseCount: 1, Description: "fork", Size: 55, Language: "Go",
		OpenIssues: 4, CreatedAt: now.Add(-24 * time.Hour),
		Branches: []forge.BranchRef{{Name: "dev", CommittedDate: now.Add(-time.Hour)}},
		Topics:   []string{"x"},
	}
	patch := "@@ -1 +1 @@"
	t2 := &forge.T2Data{
		AheadCount: 2, BehindCount: 5, MNA: 120, TotalAdditions: 30, TotalDeletions: 4,
		FeatureCommitRatio: 0.5, IsBranchWork: true, ActiveBranch: "dev",
		Upstreamed: true, UpstreamedPR: 99, PatchSkipReason: "",
		Diffs: []forge.FileDiff{{Path: "a.go", Status: "modified", Additions: 30, Deletions: 4, Patch: patch, PatchSource: "compare_rest"}},
		Commits: []forge.AheadCommit{
			{SHA: "sha1", Message: "one", AuthorEmail: "a@x", AuthorLogin: "alice", Timestamp: now.Add(-2 * time.Hour),
				Files: []forge.FileDiff{{Path: "a.go", Status: "modified", Additions: 10, Deletions: 1, PatchSource: "compare_rest"}}},
			{SHA: "sha2", Message: "two", AuthorEmail: "a@x", AuthorLogin: "alice", Timestamp: now.Add(-time.Hour)},
		},
	}

	snap := Snapshot{
		Repo: RepoRecord{
			Provider: "github", Host: "github.com", Owner: "up", Name: "stream",
			FirstSeen: now, LastSeen: now, Parent: parent, ForksSyncedAt: now,
		},
		Fork: ForkRecord{
			ForgeID: t1.ID, Owner: t1.Owner, Name: t1.Name, URL: t1.URL,
			PushedAt: t1.PushedAt, Heat: 12.5, Tier: 2, UpdatedAt: now,
		},
		T1: t1, T2: t2, T2Present: true,
		CompareFiles: []FileRecord{{Path: "a.go", Status: "modified", Additions: 30, Deletions: 4, Patch: &patch, PatchSource: "compare_rest"}},
		Commits: []CommitRecord{
			{SHA: "sha1", Message: "one", AuthorLogin: "alice", AuthorEmail: "a@x", CommittedAt: now.Add(-2 * time.Hour),
				Files: []FileRecord{{Path: "a.go", Status: "modified", Additions: 10, Deletions: 1, PatchSource: "compare_rest"}}},
			{SHA: "sha2", Message: "two", AuthorLogin: "alice", AuthorEmail: "a@x", CommittedAt: now.Add(-time.Hour)},
		},
	}
	if err := s.UpsertSnapshot(ctx, snap); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load returned nil for a persisted repo")
	}
	if !reflect.DeepEqual(got.Parent, parent) {
		t.Errorf("parent mismatch:\n got %+v\nwant %+v", got.Parent, parent)
	}
	if !got.ForksSyncedAt.Equal(now) {
		t.Errorf("ForksSyncedAt = %v, want %v", got.ForksSyncedAt, now)
	}
	if len(got.Forks) != 1 {
		t.Fatalf("forks = %d, want 1", len(got.Forks))
	}
	cf := got.Forks[0]
	if !reflect.DeepEqual(&cf.T1, t1) {
		t.Errorf("t1 mismatch:\n got %+v\nwant %+v", cf.T1, t1)
	}
	if cf.T2 == nil {
		t.Fatal("t2 not reconstructed")
	}
	// Patch text is deliberately not hydrated (it can run to megabytes per
	// fork and the snapshot stays pinned for a session); everything else must
	// round-trip exactly.
	wantT2 := *t2
	wantT2.Diffs = append([]forge.FileDiff(nil), t2.Diffs...)
	for i := range wantT2.Diffs {
		wantT2.Diffs[i].Patch = ""
	}
	if !reflect.DeepEqual(cf.T2, &wantT2) {
		t.Errorf("t2 mismatch:\n got %+v\nwant %+v", cf.T2, &wantT2)
	}
	// The lookup index and validity check work off the hydrated snapshot.
	if got.ValidT2(*t1) == nil {
		t.Error("ValidT2 missed a fork with matching pushed_at")
	}
	stale := *t1
	stale.PushedAt = stale.PushedAt.Add(time.Hour)
	if got.ValidT2(stale) != nil {
		t.Error("ValidT2 served a compare for a fork pushed since it was recorded")
	}
	if cf.Heat != 12.5 || cf.Tier != 2 {
		t.Errorf("heat/tier = %v/%v, want 12.5/2", cf.Heat, cf.Tier)
	}

	// head_sha column: complete commit list → last commit.
	var head string
	forkKey := ForkKey(RepoKey("github", "github.com", "up", "stream"), t1.ID)
	if err := s.db.QueryRowContext(ctx, `SELECT head_sha FROM forks WHERE fork_key=?`, forkKey).Scan(&head); err != nil {
		t.Fatalf("read head_sha: %v", err)
	}
	if head != "sha2" {
		t.Errorf("head_sha = %q, want sha2", head)
	}
}

// A snapshot without T2 must not disturb previously stored compare data, and a
// new T2 write must fully replace it (self-eviction: one compare per fork).
func TestRepoSnapshotT2ReplaceAndPreserve(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	base := Snapshot{
		Repo: RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "stream", FirstSeen: now, LastSeen: now},
		Fork: ForkRecord{ForgeID: "a/s", Owner: "a", Name: "s", URL: "u", PushedAt: now, UpdatedAt: now},
		T1:   &forge.T1Data{ID: "a/s", Owner: "a", Name: "s", PushedAt: now},
	}
	withT2 := base
	withT2.T2Present = true
	withT2.T2 = &forge.T2Data{AheadCount: 1, Commits: []forge.AheadCommit{{SHA: "old"}}}
	withT2.Commits = []CommitRecord{{SHA: "old", CommittedAt: now}}
	if err := s.UpsertSnapshot(ctx, withT2); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Degraded pass (no T2): compare survives.
	if err := s.UpsertSnapshot(ctx, base); err != nil {
		t.Fatalf("degraded upsert: %v", err)
	}
	got, err := s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
	if err != nil || got == nil || len(got.Forks) != 1 {
		t.Fatalf("load after degraded: %+v, %v", got, err)
	}
	if got.Forks[0].T2 == nil || len(got.Forks[0].T2.Commits) != 1 || got.Forks[0].T2.Commits[0].SHA != "old" {
		t.Fatalf("degraded scan erased enrichment: %+v", got.Forks[0].T2)
	}

	// Fork progressed: new T2 replaces the old rows entirely.
	newT2 := base
	newT2.T2Present = true
	newT2.T2 = &forge.T2Data{AheadCount: 1, Commits: []forge.AheadCommit{{SHA: "new"}}}
	newT2.Commits = []CommitRecord{{SHA: "new", CommittedAt: now}}
	if err := s.UpsertSnapshot(ctx, newT2); err != nil {
		t.Fatalf("replace upsert: %v", err)
	}
	got, err = s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
	if err != nil || got == nil {
		t.Fatalf("load after replace: %v", err)
	}
	shas := []string{}
	for _, c := range got.Forks[0].T2.Commits {
		shas = append(shas, c.SHA)
	}
	if len(shas) != 1 || shas[0] != "new" {
		t.Fatalf("old compare rows survived replacement: %v", shas)
	}
}
