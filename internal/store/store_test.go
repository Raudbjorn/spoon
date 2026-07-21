package store

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

// SQLite creates the -wal and -shm sidecars lazily on first write, so Open
// alone cannot secure them. Securing moved from every-upsert to once-after-
// first-write; this pins the property that matters — every artifact on disk is
// owner-only once data has been written.
func TestStoreArtifactsAreOwnerOnlyAfterWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spoon.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	snapshot := Snapshot{
		Repo: RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now},
		Fork: ForkRecord{ForgeID: "fork/repo", Owner: "fork", Name: "repo", UpdatedAt: now},
	}
	// More than one upsert: securing is once-only, so later writes must not
	// leave a freshly created artifact unprotected.
	for i := range 3 {
		snapshot.Fork.ForgeID = "fork/repo" + strconv.Itoa(i)
		if err := db.UpsertSnapshot(context.Background(), snapshot); err != nil {
			t.Fatal(err)
		}
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if os.IsNotExist(err) {
			continue // sidecar not materialised in this mode
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode=%o want=600", path+suffix, got)
		}
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("store dir mode=%o want=700", got)
	}
}
