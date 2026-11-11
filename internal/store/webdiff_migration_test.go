package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// Build the real released schema rather than opening today's schema and only
// resetting its version stamp; this exercises every v6 -> v7 upgrade statement.
func schemaV6TestStore(t *testing.T) *Store {
	t.Helper()
	return historicalSchemaTestStore(t, 6)
}

func historicalSchemaTestStore(t *testing.T, version int) *Store {
	t.Helper()
	t.Setenv("TURSO_DATABASE_URL", "")
	t.Setenv("TURSO_AUTH_TOKEN", "")
	path := filepath.Join(t.TempDir(), "spoon.db")
	base, err := baseConnector(path)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(&pragmaConnector{base: base})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, migration := range migrations {
		if migration.version > version {
			break
		}
		for _, stmt := range migration.stmts {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("create v%d schema: %v", migration.version, err)
			}
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
		t.Fatal(err)
	}
	return &Store{db: db, path: path}
}

func TestWebDiffMigrationInvalidatesLegacyEvidence(t *testing.T) {
	ctx := context.Background()
	legacy := schemaV6TestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	fixtures := []struct {
		name, repo            string
		webCompare, webCommit bool
	}{
		{name: "mixed", repo: "affected", webCompare: true},
		{name: "rest-sibling", repo: "affected"},
		{name: "commit-web", repo: "commit-affected", webCommit: true},
		{name: "rest-only", repo: "unaffected"},
	}
	var snapshots []Snapshot
	for _, fixture := range fixtures {
		repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: fixture.repo}
		t1 := forge.T1Data{ID: fixture.name, Owner: "owner", Name: fixture.name, DefaultBranch: "main", PushedAt: now,
			Description: "preserved T1", MergeCommits: 1, MergeCommitHistory: []int{1, 2}}
		compareSource, commitSource := "compare_rest", "commit_rest"
		if fixture.webCompare {
			compareSource = "github_web"
		}
		if fixture.webCommit {
			commitSource = "github_web"
		}
		t2 := forge.T2Data{Performed: true, ComparedBranch: "main", HeadSHA: "head", BaseSHA: "base", AheadCount: 1, FilesComplete: true,
			Diffs: []forge.FileDiff{
				{Path: "maybe-web.go", Additions: 2, Patch: "+old payload", PatchSource: compareSource},
				{Path: "rest.go", Additions: 1, Patch: "+keep REST", PatchSource: "compare_rest"},
			},
			Commits: []forge.AheadCommit{{SHA: "head", Message: "preserved commit", Timestamp: now,
				Files: []forge.FileDiff{{Path: "commit.go", Additions: 2, Patch: "+commit payload", PatchSource: commitSource}}}},
		}
		snap := SnapshotFromForge(repo, t1, &t2, 8, 2, now)
		snap.Document = DocumentRecord{DocumentID: DocumentID(snap.ForkKey()), ContentHash: fixture.name, Body: "old evidence " + fixture.name, UpdatedAt: now}
		if err := legacy.UpsertSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
		if err := legacy.UpsertEmbeddings(ctx, []EmbeddingRecord{{DocumentID: snap.Document.DocumentID, Model: "test-model", Dim: 1,
			Vector: []byte{0, 0, 128, 63}, ContentHash: snap.Document.ContentHash, CreatedAt: now}}); err != nil {
			t.Fatal(err)
		}
		repoKey := RepoKey(repo.Provider, repo.Host, repo.Owner, repo.Name)
		if err := legacy.UpsertClusterRun(ctx, ClusterRunRecord{RepoKey: repoKey, EmbedderFP: "old", ComputedAt: now, LastReassignedAt: now}); err != nil {
			t.Fatal(err)
		}
		lineageID := "lineage:" + repo.Name
		if err := legacy.SaveClusters(ctx, repoKey, []ClusterRecord{{RepoKey: repoKey, ClusterID: "cluster", Centroid: []byte{1}, AnchorCentroid: []byte{1}, LineageID: lineageID}},
			[]ClusterMemberRecord{{RepoKey: repoKey, ForkKey: snap.ForkKey(), HeadSHA: "head", ClusterID: "cluster", Embedding: []byte{1}}}); err != nil {
			t.Fatal(err)
		}
		if err := legacy.UpsertGlobalLineage(ctx, GlobalLineageRecord{LineageID: lineageID, OriginRepo: repoKey, Centroid: []byte{1}},
			[]LineageOccurrenceRecord{{LineageID: lineageID, RepoKey: repoKey, ClusterID: "cluster"}}); err != nil {
			t.Fatal(err)
		}
		if err := legacy.UpsertTouchedUnits(ctx, []TouchedUnitRecord{{ForkKey: snap.ForkKey(), Path: "maybe-web.go", UnitsJSON: `[]`}}); err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snap)
	}
	// A prototype originating elsewhere may still have incorporated affected
	// evidence. Remove that shared prototype, but retain the other repo's cluster.
	unaffectedRepo := RepoKey("github", "github.com", "up", "unaffected")
	affectedRepo := RepoKey("github", "github.com", "up", "affected")
	if err := legacy.UpsertGlobalLineage(ctx, GlobalLineageRecord{LineageID: "shared", OriginRepo: unaffectedRepo, Centroid: []byte{1}},
		[]LineageOccurrenceRecord{{LineageID: "shared", RepoKey: affectedRepo, ClusterID: "cluster"}, {LineageID: "shared", RepoKey: unaffectedRepo, ClusterID: "cluster"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec("UPDATE clusters SET lineage_id='shared' WHERE repo_key=?", unaffectedRepo); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(legacy.path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	var version int
	if err := current.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("upgrade version=%d err=%v", version, err)
	}
	for i, fixture := range fixtures {
		snap := snapshots[i]
		affected := fixture.webCompare || fixture.webCommit
		loaded, err := current.LoadRepoSnapshot(ctx, "github", "github.com", "up", fixture.repo)
		if err != nil {
			t.Fatal(err)
		}
		cached := loaded.Fork(fixture.name)
		if cached == nil || !reflect.DeepEqual(cached.T1, *snap.T1) {
			t.Fatalf("%s lost T1 metadata: %+v", fixture.name, cached)
		}
		if (loaded.ValidT2(*snap.T1) == nil) != affected {
			t.Fatalf("%s T2 reuse after upgrade: %+v", fixture.name, cached.T2)
		}
		for _, check := range []struct {
			table, path, wantPatch, wantSource string
			cleared                            bool
		}{
			{"compare_files", "maybe-web.go", "+old payload", "compare_rest", fixture.webCompare},
			{"compare_files", "rest.go", "+keep REST", "compare_rest", false},
			{"commit_files", "commit.go", "+commit payload", "commit_rest", fixture.webCommit},
		} {
			var patch sql.NullString
			var source string
			if err := current.db.QueryRow("SELECT patch, patch_source FROM "+check.table+" WHERE fork_key=? AND path=?", snap.ForkKey(), check.path).Scan(&patch, &source); err != nil {
				t.Fatal(err)
			}
			if check.cleared && (patch.Valid || source != "") || !check.cleared && (!patch.Valid || patch.String != check.wantPatch || source != check.wantSource) {
				t.Fatalf("%s/%s patch=%v source=%q", fixture.name, check.path, patch, source)
			}
		}
		var documents, embeddings, units int
		if err := current.db.QueryRow("SELECT COUNT(*) FROM documents WHERE fork_key=?", snap.ForkKey()).Scan(&documents); err != nil {
			t.Fatal(err)
		}
		if err := current.db.QueryRow("SELECT COUNT(*) FROM embeddings WHERE document_id=?", snap.Document.DocumentID).Scan(&embeddings); err != nil {
			t.Fatal(err)
		}
		if err := current.db.QueryRow("SELECT COUNT(*) FROM touched_units WHERE fork_key=?", snap.ForkKey()).Scan(&units); err != nil {
			t.Fatal(err)
		}
		want := 1
		if affected {
			want = 0
		}
		if documents != want || embeddings != want || units != want {
			t.Fatalf("%s derived rows: documents=%d embeddings=%d units=%d want=%d", fixture.name, documents, embeddings, units, want)
		}
	}
	rows, err := current.SearchRows(ctx, "test-model", "", "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("search eligibility after migration: %+v err=%v", rows, err)
	}
	for _, table := range []string{"cluster_runs", "clusters", "cluster_members", "lineage_occurrences", "global_lineage"} {
		var count int
		if err := current.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s retained %d derived rows, want only unaffected repo: %v", table, count, err)
		}
	}
	clusters, err := current.GetClusters(ctx, unaffectedRepo)
	if err != nil || len(clusters) != 1 || clusters[0].LineageID != "" || !reflect.DeepEqual(clusters[0].Centroid, []byte{1}) {
		t.Fatalf("unaffected cluster should survive without stale shared lineage: %+v %v", clusters, err)
	}
	pending, err := current.PendingDocuments(ctx, "new-model")
	if err != nil || len(pending) != 2 {
		t.Fatalf("old web evidence still pending for indexing: %+v err=%v", pending, err)
	}

	// Correctly validated web data fetched after upgrade must survive both a
	// regular reopen and an unrelated schema repair; v2 provenance survives cleanup.
	fresh := snapshots[0]
	validatedPatch := "@@ -0,0 +1,2 @@\n+validated first line\n+validated second line\n"
	fresh.CompareFiles[0].Patch = &validatedPatch
	fresh.CompareFiles[0].PatchSource = "github_web_v2"
	fresh.Document.ContentHash = "fresh-validated"
	fresh.Document.Body = "fresh complete evidence"
	if err := current.UpsertSnapshot(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if err := current.UpsertEmbeddings(ctx, []EmbeddingRecord{{DocumentID: fresh.Document.DocumentID, Model: "validated-model", Dim: 1,
		Vector: []byte{0, 0, 128, 63}, ContentHash: fresh.Document.ContentHash, CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	for _, repair := range []bool{false, true} {
		if repair {
			if _, err := current.db.Exec("DROP TABLE touched_units"); err != nil {
				t.Fatal(err)
			}
		}
		if err := current.Close(); err != nil {
			t.Fatal(err)
		}
		current, err = Open(legacy.path)
		if err != nil {
			t.Fatal(err)
		}
		defer current.Close()
		loaded, err := current.LoadRepoSnapshot(ctx, "github", "github.com", "up", "affected")
		if err != nil || loaded.ValidT2(*fresh.T1) == nil {
			t.Fatalf("repair=%v invalidated newly validated T2: %v", repair, err)
		}
		var patch, body string
		if err := current.db.QueryRow("SELECT patch FROM compare_files WHERE fork_key=? AND path='maybe-web.go'", fresh.ForkKey()).Scan(&patch); err != nil || patch != validatedPatch {
			t.Fatalf("repair=%v lost newly validated patch: %q %v", repair, patch, err)
		}
		if err := current.db.QueryRow("SELECT body FROM documents WHERE fork_key=?", fresh.ForkKey()).Scan(&body); err != nil || body != fresh.Document.Body {
			t.Fatalf("repair=%v lost new document: %q %v", repair, body, err)
		}
		if rows, err := current.SearchRows(ctx, "validated-model", "up", "affected"); err != nil || len(rows) != 1 {
			t.Fatalf("repair=%v lost new vector: %+v %v", repair, rows, err)
		}
		if err := current.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
			t.Fatalf("repair=%v version=%d err=%v", repair, version, err)
		}
	}
}

func TestWebDiffMigrationUpgradesAlternateVersionSix(t *testing.T) {
	ctx := t.Context()
	legacy := historicalSchemaTestStore(t, 5)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now}
	seed := func(owner, source string) Snapshot {
		t.Helper()
		t1 := forge.T1Data{ID: owner + "/repo", Owner: owner, Name: "repo", DefaultBranch: "main", PushedAt: now}
		t2 := &forge.T2Data{ComparedBranch: "main", HeadSHA: owner + "-head", AheadCount: 1,
			Diffs: []forge.FileDiff{{Path: "file.go", Status: "added", Additions: 1, Patch: "@@ -0,0 +1 @@\n+complete\n", PatchSource: source}}}
		snap := SnapshotFromForge(repo, t1, t2, 1, 2, now)
		snap.Document = DocumentRecord{DocumentID: DocumentID(snap.ForkKey()), ContentHash: owner, Body: "derived " + owner, UpdatedAt: now}
		if err := legacy.UpsertSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
		if err := legacy.UpsertEmbeddings(ctx, []EmbeddingRecord{{DocumentID: snap.Document.DocumentID, Model: "m", Dim: 1,
			Vector: []byte{0, 0, 128, 63}, ContentHash: owner, CreatedAt: now}}); err != nil {
			t.Fatal(err)
		}
		return snap
	}
	old := seed("legacy", "github_web")
	// Commit 387e8e4 published this different v6: cleanup without main's cluster
	// tables. Reproduce its exact statements, then save new validated evidence.
	for _, stmt := range []string{
		`DELETE FROM documents WHERE fork_key IN (SELECT fork_key FROM compare_files WHERE patch_source='github_web')`,
		`UPDATE forks SET t2_json='', t2_fetched_at='' WHERE fork_key IN (SELECT fork_key FROM compare_files WHERE patch_source='github_web')`,
		`UPDATE compare_files SET patch=NULL, patch_source='' WHERE patch_source='github_web'`,
		`PRAGMA user_version=6`,
	} {
		if _, err := legacy.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	verified := seed("verified", "github_web_v2")
	rest := seed("rest", "compare_rest")
	var count int
	if err := legacy.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='cluster_runs'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("alternate v6 fixture already has cluster tables: %d %v", count, err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(legacy.path)
	if err != nil {
		t.Fatalf("upgrade alternate v6: %v", err)
	}
	defer current.Close()
	if intact, err := schemaIntact(ctx, current.db); err != nil || !intact {
		t.Fatalf("upgrade did not create missing main v6 tables: intact=%v err=%v", intact, err)
	}
	if err := current.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&count); err != nil || count != SchemaVersion {
		t.Fatalf("upgrade version=%d err=%v", count, err)
	}
	loaded, err := current.LoadRepoSnapshot(ctx, "github", "github.com", "up", "repo")
	if err != nil {
		t.Fatal(err)
	}
	for _, snap := range []Snapshot{old, verified, rest} {
		fork := loaded.Fork(snap.T1.ID)
		if fork == nil || !reflect.DeepEqual(fork.T1, *snap.T1) {
			t.Fatalf("T1 changed for %s: %+v", snap.T1.ID, fork)
		}
		if (loaded.ValidT2(*snap.T1) == nil) != (snap.T1.ID == old.T1.ID) {
			t.Fatalf("wrong T2 validity for %s: %+v", snap.T1.ID, fork.T2)
		}
	}
	for _, snap := range []Snapshot{verified, rest} {
		var patch, source, body string
		if err := current.db.QueryRowContext(ctx, `SELECT patch,patch_source FROM compare_files WHERE fork_key=?`, snap.ForkKey()).Scan(&patch, &source); err != nil || patch != *snap.CompareFiles[0].Patch || source != snap.CompareFiles[0].PatchSource {
			t.Fatalf("retained patch changed for %s: %q %q %v", snap.T1.ID, patch, source, err)
		}
		if err := current.db.QueryRowContext(ctx, `SELECT body FROM documents WHERE fork_key=?`, snap.ForkKey()).Scan(&body); err != nil || body != snap.Document.Body {
			t.Fatalf("retained document changed for %s: %q %v", snap.T1.ID, body, err)
		}
	}
	if rows, err := current.SearchRows(ctx, "m", "up", "repo"); err != nil || len(rows) != 2 {
		t.Fatalf("validated/REST vectors lost: %+v %v", rows, err)
	}
	if pending, err := current.PendingDocuments(ctx, "new-model"); err != nil || len(pending) != 2 {
		t.Fatalf("validated/REST documents lost: %+v %v", pending, err)
	}
}
