package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestSnapshotReplaceRemovesStaleRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	patch := "@@ -1 +1 @@\n-old\n+new"
	snapshot := Snapshot{
		Repo:         RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now},
		Fork:         ForkRecord{ForgeID: "fork/repo", Owner: "fork", Name: "repo", Topics: []string{"z", "a"}, UpdatedAt: now},
		T2Present:    true, // authoritative compare/commit data — exercises the replace path
		CompareFiles: []FileRecord{{Path: "a.go", Status: "modified", Patch: &patch, PatchSource: "compare_rest"}, {Path: "stale.go"}},
		Commits: []CommitRecord{
			{SHA: "keep", CommittedAt: now, Files: []FileRecord{{Path: "a.go", Patch: &patch, PatchSource: "commit_rest"}}},
			{SHA: "stale", CommittedAt: now},
		},
		Document: DocumentRecord{DocumentID: "fork:doc", ContentHash: "h1", Body: "body", UpdatedAt: now},
	}
	if err := db.UpsertSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CompareFiles = snapshot.CompareFiles[:1]
	snapshot.Commits = snapshot.Commits[:1]
	snapshot.Document.ContentHash = "h2"
	if err := db.UpsertSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	forkKey := ForkKey(RepoKey("github", "github.com", "up", "repo"), "fork/repo")
	for table, want := range map[string]int{"compare_files": 1, "commits": 1, "commit_files": 1, "documents": 1} {
		var got int
		query := "SELECT count(*) FROM " + table + " WHERE fork_key=?"
		if table == "documents" || table == "compare_files" || table == "commits" || table == "commit_files" {
			if err := db.db.QueryRow(query, forkKey).Scan(&got); err != nil {
				t.Fatal(err)
			}
		}
		if got != want {
			t.Fatalf("%s rows=%d want=%d", table, got, want)
		}
	}
}

func TestDegradedSnapshotPreservesPriorEnrichment(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now}
	fork := ForkRecord{ForgeID: "fork/repo", Owner: "fork", Name: "repo", UpdatedAt: now}
	// First: an authoritative scan populates compare/commit rows.
	if err := db.UpsertSnapshot(context.Background(), Snapshot{
		Repo: repo, Fork: fork, T2Present: true,
		CompareFiles: []FileRecord{{Path: "a.go", Status: "modified"}},
		Commits:      []CommitRecord{{SHA: "keep", CommittedAt: now}},
	}); err != nil {
		t.Fatal(err)
	}
	// Then: a degraded scan (T2 absent) must NOT erase the prior enrichment.
	if err := db.UpsertSnapshot(context.Background(), Snapshot{Repo: repo, Fork: fork}); err != nil {
		t.Fatal(err)
	}
	forkKey := ForkKey(RepoKey("github", "github.com", "up", "repo"), "fork/repo")
	for table, want := range map[string]int{"compare_files": 1, "commits": 1} {
		var got int
		if err := db.db.QueryRow("SELECT count(*) FROM "+table+" WHERE fork_key=?", forkKey).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s rows=%d want=%d (degraded scan must preserve prior data)", table, got, want)
		}
	}
}

func TestEmbeddingContentHashInvalidates(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	snapshot := Snapshot{
		Repo:     RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now},
		Fork:     ForkRecord{ForgeID: "f", Owner: "o", Name: "f", UpdatedAt: now},
		Document: DocumentRecord{DocumentID: "fork:doc", ContentHash: "h1", Body: "one", UpdatedAt: now},
	}
	if err := db.UpsertSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertEmbeddings(context.Background(), []EmbeddingRecord{{DocumentID: "fork:doc", Model: "m", Dim: 1, Vector: []byte{0, 0, 128, 63}, ContentHash: "h1", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingDocuments(context.Background(), "m")
	if err != nil || len(pending) != 0 {
		t.Fatalf("unchanged pending=%v err=%v", pending, err)
	}
	snapshot.Document.ContentHash = "h2"
	snapshot.Document.Body = "two"
	if err := db.UpsertSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	pending, err = db.PendingDocuments(context.Background(), "m")
	if err != nil || len(pending) != 1 {
		t.Fatalf("changed pending=%v err=%v", pending, err)
	}
}

// Regression: a .diff-sourced patch carrying a stray non-UTF-8 byte (seen live
// as "+vQ\xff" in an added YAML-ish file) made libsql refuse to bind the patch
// as TEXT, aborting the whole snapshot write and the embed run with it.
func TestUpsertSnapshotSanitizesInvalidUTF8FromForge(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now}
	t1 := forge.T1Data{ID: "fork/repo", Owner: "fork", Name: "repo"}
	t2 := &forge.T2Data{Diffs: []forge.FileDiff{{
		Path: "cfg\xff.yaml", Status: "added", Additions: 4,
		Patch: "@@ -0,0 +1,4 @@\n+version: 0.34\n+vQ\xff", PatchSource: "complete",
	}}}
	snap := SnapshotFromForge(repo, t1, t2, 0, 0, now)
	if err := db.UpsertSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("UpsertSnapshot: %v", err)
	}
	var path, patch string
	if err := db.db.QueryRow("SELECT path, patch FROM compare_files").Scan(&path, &patch); err != nil {
		t.Fatal(err)
	}
	if path != "cfg\uFFFD.yaml" || patch != "@@ -0,0 +1,4 @@\n+version: 0.34\n+vQ\uFFFD" {
		t.Fatalf("got path=%q patch=%q, want U+FFFD in place of the invalid byte", path, patch)
	}
}

// Records built outside FilesFromForge (spn's storeFiles builds FileRecords
// directly) and commit fields must be cleaned where they are bound, not only
// in one constructor (PR #134 review).
func TestUpsertSnapshotCleansDirectRecordsAndCommits(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	patch := "+vQ\xff"
	snap := Snapshot{
		Repo:         RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now},
		Fork:         ForkRecord{ForgeID: "fork/repo", Owner: "fork", Name: "repo", UpdatedAt: now},
		T2Present:    true,
		CompareFiles: []FileRecord{{Path: "a\xff.go", Patch: &patch}},
		Commits: []CommitRecord{{
			SHA: "c1", Message: "fix \xfe thing", AuthorLogin: "who\xff", CommittedAt: now,
			Files: []FileRecord{{Path: "b\xff.go", Patch: &patch}},
		}},
	}
	if err := db.UpsertSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("UpsertSnapshot: %v", err)
	}
	var msg, login, cpath, cpatch string
	if err := db.db.QueryRow("SELECT message, author_login FROM commits").Scan(&msg, &login); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow("SELECT path, patch FROM commit_files").Scan(&cpath, &cpatch); err != nil {
		t.Fatal(err)
	}
	if msg != "fix \uFFFD thing" || login != "who\uFFFD" || cpath != "b\uFFFD.go" || cpatch != "+vQ\uFFFD" {
		t.Errorf("got message=%q login=%q path=%q patch=%q", msg, login, cpath, cpatch)
	}
}
