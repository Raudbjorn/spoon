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
		SourceFullPath: "root/repo", SourceDefaultBranch: "master",
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

func TestCachedBehindRefreshPreservesStoredComparison(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "stream", FirstSeen: now, LastSeen: now}
	t1 := forge.T1Data{ID: "a/s", Owner: "a", Name: "s", DefaultBranch: "main", PushedAt: now}
	t2 := forge.T2Data{
		Performed: true, HeadSHA: "head", BaseSHA: "base", ComparedBranch: "main", AheadCount: 1, BehindCount: 2, MNA: 7,
		Diffs: []forge.FileDiff{{Path: "a.go", Patch: "compare patch", Additions: 7}},
		Commits: []forge.AheadCommit{{SHA: "head", Message: "keep commit", Timestamp: now,
			Files: []forge.FileDiff{{Path: "a.go", Patch: "commit patch", Additions: 7}}}},
	}
	base := SnapshotFromForge(repo, t1, &t2, 10, 2, now)
	base.Document = DocumentRecord{DocumentID: "doc", ContentHash: "hash", Body: "keep document", UpdatedAt: now}
	if err := s.UpsertSnapshot(ctx, base); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
	if err != nil {
		t.Fatal(err)
	}
	refresh := loaded.ValidT2(t1)
	refresh.BehindCount = 23
	refresh.MNA = 999 // A refresh may update only BehindCount, never other cached scalars.
	snap := SnapshotFromForge(repo, t1, nil, 12, 2, now.Add(time.Hour))
	snap.T2Refresh = refresh
	if err := s.UpsertSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.ValidT2(t1)
	if got == nil || got.BehindCount != 23 || got.AheadCount != 1 || got.MNA != 7 || len(got.Commits) != 1 || got.Commits[0].Message != "keep commit" {
		t.Fatalf("refreshed comparison = %+v", got)
	}
	var comparePatch, commitPatch, document, fetchedAt, head string
	for _, check := range []struct {
		query string
		into  *string
		want  string
	}{
		{`SELECT patch FROM compare_files WHERE fork_key=?`, &comparePatch, "compare patch"},
		{`SELECT patch FROM commit_files WHERE fork_key=?`, &commitPatch, "commit patch"},
		{`SELECT body FROM documents WHERE fork_key=?`, &document, "keep document"},
		{`SELECT t2_fetched_at FROM forks WHERE fork_key=?`, &fetchedAt, ts(now)},
		{`SELECT head_sha FROM forks WHERE fork_key=?`, &head, "head"},
	} {
		if err := s.db.QueryRowContext(ctx, check.query, snap.ForkKey()).Scan(check.into); err != nil {
			t.Fatal(err)
		}
		if *check.into != check.want {
			t.Errorf("%s = %q, want %q", check.query, *check.into, check.want)
		}
	}
}

func TestCachedBehindRefreshRejectsDifferentComparison(t *testing.T) {
	for _, field := range []string{"head", "branch", "base", "ahead", "pushed", "unknown head"} {
		t.Run(field, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "stream"}
			t1 := forge.T1Data{ID: "a/s", DefaultBranch: "main", PushedAt: now}
			t2 := forge.T2Data{HeadSHA: "head", BaseSHA: "base", ComparedBranch: "main", AheadCount: 1, BehindCount: 2}
			if err := s.UpsertSnapshot(ctx, SnapshotFromForge(repo, t1, &t2, 0, 2, now)); err != nil {
				t.Fatal(err)
			}
			refresh := t2
			refresh.BehindCount = 23
			switch field {
			case "head":
				refresh.HeadSHA = "stale"
			case "branch":
				refresh.ComparedBranch = "develop"
			case "base":
				refresh.BaseSHA = "stale"
			case "ahead":
				refresh.AheadCount++
			case "pushed":
				t1.PushedAt = now.Add(-time.Hour)
			case "unknown head":
				refresh.HeadSHA = ""
			}
			snap := SnapshotFromForge(repo, t1, nil, 0, 2, now)
			snap.T2Refresh = &refresh
			if err := s.UpsertSnapshot(ctx, snap); err != nil {
				t.Fatal(err)
			}
			loaded, err := s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
			if err != nil {
				t.Fatal(err)
			}
			if field == "pushed" {
				if loaded.Fork(t1.ID).T2 != nil {
					t.Fatal("changed pushed_at retained a reusable comparison")
				}
				return
			}
			if got := loaded.Fork(t1.ID).T2.BehindCount; got != 2 {
				t.Fatalf("stale refresh changed BehindCount to %d", got)
			}
		})
	}
}

func TestLegacyComparisonInvalidatedAfterDefaultBranchChange(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "stream"}
	t1 := forge.T1Data{ID: "a/s", DefaultBranch: "main", PushedAt: now}
	t2 := forge.T2Data{HeadSHA: "head", AheadCount: 1, BehindCount: 2}
	if err := s.UpsertSnapshot(ctx, SnapshotFromForge(repo, t1, &t2, 0, 2, now)); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.ValidT2(t1)
	if got == nil || got.ComparedBranch != "main" || loaded.Fork(t1.ID).T2.ComparedBranch != "" {
		t.Fatalf("legacy identity was not copied from its listing: %+v", got)
	}
	t1.DefaultBranch = "develop"
	if loaded.ValidT2(t1) != nil {
		t.Fatal("default branch change reused the old comparison")
	}
	if err := s.UpsertSnapshot(ctx, SnapshotFromForge(repo, t1, nil, 0, 1, now)); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fork(t1.ID).T2 != nil || loaded.ValidT2(t1) != nil {
		t.Fatal("T1-only save reassigned the old comparison to the new default branch")
	}
}

func TestCachedComparisonInvalidationPreservesEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, head           string
		staleHead, stalePush bool
	}{
		{name: "matching", head: "head"},
		{name: "replacement", head: "head", staleHead: true},
		{name: "legacy_empty_head"},
		{name: "legacy_replacement", staleHead: true},
		{name: "legacy_new_push", stalePush: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "stream"}
			t1 := forge.T1Data{ID: "a/s", DefaultBranch: "main", PushedAt: now}
			t2 := forge.T2Data{HeadSHA: tc.head, BaseSHA: "base", ComparedBranch: "main", AheadCount: 1, BehindCount: 2,
				Diffs:   []forge.FileDiff{{Path: "a.go", Patch: "keep patch"}},
				Commits: []forge.AheadCommit{{SHA: "head", Message: "keep commit", Timestamp: now}}}
			base := SnapshotFromForge(repo, t1, &t2, 0, 2, now)
			if err := s.UpsertSnapshot(ctx, base); err != nil {
				t.Fatal(err)
			}
			invalidated := t2
			if tc.staleHead {
				invalidated.HeadSHA = "old-head"
			}
			snap := SnapshotFromForge(repo, t1, nil, 0, 1, now)
			snap.T2Invalidate = &invalidated
			if tc.stalePush {
				// Isolate the invalidation guard from the separate listing refresh path.
				snap.T1 = nil
				snap.Fork.PushedAt = now.Add(-time.Hour)
			}
			if err := s.UpsertSnapshot(ctx, snap); err != nil {
				t.Fatal(err)
			}
			loaded, err := s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
			if err != nil {
				t.Fatal(err)
			}
			// Empty-head legacy caches must be evicted, while stale writes preserve newer evidence.
			if (loaded.ValidT2(t1) != nil) != (tc.staleHead || tc.stalePush) {
				t.Fatalf("cache validity after invalidation: %+v", loaded.ValidT2(t1))
			}
			var patch, message string
			if err := s.db.QueryRowContext(ctx, `SELECT patch FROM compare_files WHERE fork_key=?`, snap.ForkKey()).Scan(&patch); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT message FROM commits WHERE fork_key=?`, snap.ForkKey()).Scan(&message); err != nil {
				t.Fatal(err)
			}
			if patch != "keep patch" || message != "keep commit" {
				t.Fatalf("invalidation lost evidence: %q, %q", patch, message)
			}
		})
	}
}
