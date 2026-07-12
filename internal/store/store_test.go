package store

import (
	"context"
	"path/filepath"
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
