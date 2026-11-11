package store

import (
	"database/sql"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestOpenInvalidatesLegacyWebPatches(t *testing.T) {
	ctx := t.Context()
	// Start from the actual released schema so rollback also covers v6 DDL.
	s := historicalSchemaTestStore(t, 5)
	path := s.path
	var err error
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	})
	now := time.Now().UTC().Truncate(time.Second)
	repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now}
	for owner, source := range map[string]string{"legacy": "github_web", "verified": "github_web_v2", "rest": "compare_rest"} {
		patch := "@@ -1,2 +1 @@\n-old1\n-old2\n+new\n"
		if owner == "legacy" {
			patch = "+partial\n"
		}
		t1 := forge.T1Data{ID: owner + "/repo", Owner: owner, Name: "repo", PushedAt: now}
		t2 := &forge.T2Data{
			AheadCount: 1,
			Diffs: []forge.FileDiff{
				{Path: "new.go", PreviousPath: "old.go", Status: "renamed", Additions: 1, Deletions: 2, Patch: patch, PatchSource: source},
				{Path: "rest.go", Status: "added", Additions: 1, Patch: "+rest\n", PatchSource: "compare_rest"},
			},
			Commits: []forge.AheadCommit{{SHA: "sha", Message: "message", Timestamp: now}},
		}
		snap := SnapshotFromForge(repo, t1, t2, 1, 2, now)
		snap.Document = DocumentRecord{DocumentID: DocumentID(snap.ForkKey()), ContentHash: owner, Body: "derived " + owner, UpdatedAt: now}
		if err := s.UpsertSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertEmbeddings(ctx, []EmbeddingRecord{{DocumentID: snap.Document.DocumentID, Model: "m", Dim: 1, Vector: []byte{0, 0, 128, 63}, ContentHash: owner, CreatedAt: now}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_web_patch_invalidation BEFORE UPDATE OF patch ON compare_files BEGIN SELECT RAISE(ABORT,'invalidation blocked'); END`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err == nil {
		t.Fatal("invalidation unexpectedly succeeded despite failing trigger")
	}
	s, err = OpenForDowngrade(path)
	if err != nil {
		t.Fatal(err)
	}
	// A failed invalidation must leave the cache and all derived data together.
	for query, want := range map[string]int{
		`PRAGMA user_version`: 5,
		`SELECT COUNT(*) FROM sqlite_master WHERE name='cluster_runs'`: 0,
		`SELECT COUNT(*) FROM documents`:                               3,
		`SELECT COUNT(*) FROM embeddings`:                              3,
		`SELECT COUNT(*) FROM forks WHERE t2_json<>''`:                 3,
		`SELECT COUNT(*) FROM compare_files WHERE patch IS NOT NULL`:   6,
	} {
		var got int
		if err := s.db.QueryRowContext(ctx, query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("failed migration changed state: %s = %d, want %d", query, got, want)
		}
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER fail_web_patch_invalidation"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	legacyKey := ForkKey(RepoKey("github", "github.com", "up", "repo"), "legacy/repo")
	assertMigrated := func() {
		t.Helper()
		var patch sql.NullString
		var source, previousPath, status string
		var additions, deletions int
		if err := s.db.QueryRowContext(ctx, `SELECT patch,patch_source,previous_path,status,additions,deletions FROM compare_files WHERE fork_key=? AND path='new.go'`, legacyKey).Scan(&patch, &source, &previousPath, &status, &additions, &deletions); err != nil {
			t.Fatal(err)
		}
		if patch.Valid || source != "" {
			t.Errorf("legacy web patch still trusted: patch=%v source=%q", patch, source)
		}
		if previousPath != "old.go" || status != "renamed" || additions != 1 || deletions != 2 {
			t.Errorf("REST metadata changed: %q %q +%d/-%d", previousPath, status, additions, deletions)
		}
		for query, want := range map[string]int{
			`SELECT COUNT(*) FROM compare_files WHERE path='rest.go' AND patch_source='compare_rest'`: 3,
			`SELECT COUNT(*) FROM compare_files WHERE patch IS NOT NULL`:                              5,
			`SELECT COUNT(*) FROM commits`:                                                            3,
			`SELECT COUNT(*) FROM documents`:                                                          2,
			`SELECT COUNT(*) FROM embeddings`:                                                         2,
		} {
			var got int
			if err := s.db.QueryRowContext(ctx, query).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s = %d, want %d", query, got, want)
			}
		}
		snapshot, err := s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "repo")
		if err != nil {
			t.Fatal(err)
		}
		for _, owner := range []string{"legacy", "verified", "rest"} {
			fork := snapshot.Fork(owner + "/repo")
			if fork == nil || !fork.T1.PushedAt.Equal(now) {
				t.Fatalf("listing metadata lost for %s: %+v", owner, fork)
			}
			if (snapshot.ValidT2(fork.T1) == nil) != (owner == "legacy") {
				t.Errorf("cached T2 validity for %s: %+v", owner, fork.T2)
			}
		}
		rows, err := s.SearchRows(ctx, "m", "up", "repo")
		if err != nil || len(rows) != 2 {
			t.Fatalf("search rows=%v err=%v", rows, err)
		}
		for _, row := range rows {
			if row.ForkKey == legacyKey {
				t.Error("search still exposes legacy patch-derived embedding")
			}
		}
		pending, err := s.PendingDocuments(ctx, "new-model")
		if err != nil || len(pending) != 2 {
			t.Fatalf("pending documents=%v err=%v", pending, err)
		}
		for _, document := range pending {
			if document.ForkKey == legacyKey {
				t.Error("legacy patch-derived document can still be embedded")
			}
		}
	}
	assertMigrated()
	// A reset version stamp replays migrations; v2 provenance must survive it.
	if _, err := s.db.ExecContext(ctx, "PRAGMA user_version=0"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertMigrated()
}
