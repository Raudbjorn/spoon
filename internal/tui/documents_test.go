package tui

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/tursodatabase/go-libsql"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/store"
)

// documentStore opens a store plus a second read-only handle on the same file,
// so assertions can query the schema directly rather than forcing the store to
// grow exported helpers that exist only for tests.
func documentStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spoon.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	raw, err := sql.Open("libsql", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return db, raw
}

func countRows(t *testing.T, raw *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := raw.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestTUIPersistsEmbeddableDocuments is the foundation the whole embedding
// feature stands on, and it was simply absent.
//
// documents rows were only ever built by `spn forks list`; store.SnapshotFromForge
// leaves Snapshot.Document zero and the insert is gated on a non-empty document
// ID, so every fork the interactive TUI persisted had a forks row and nothing to
// embed. The live store shows the consequence exactly: 2 documents against 6033
// forks. Without this, an embedding-coverage column reads empty forever no
// matter how many times the user asks for a run.
func TestTUIPersistsEmbeddableDocuments(t *testing.T) {
	db, raw := documentStore(t)
	m := refreshModel(t)
	m.db = db
	m.auth = forge.AuthInfo{Provider: forge.ProviderGitHub, Host: "github.com"}

	cmd := m.persistForkList()
	if cmd == nil {
		t.Fatal("persistForkList returned no command")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("persist failed: %v", msg)
	}

	forks := countRows(t, raw, "SELECT COUNT(*) FROM forks")
	documents := countRows(t, raw, "SELECT COUNT(*) FROM documents")
	if forks == 0 {
		t.Fatal("no forks were persisted at all")
	}
	if documents != forks {
		t.Fatalf("persisted %d forks but only %d documents; the rest can never be embedded", forks, documents)
	}

	// Every document must attach to a fork that exists. A document keyed off a
	// separately-derived repo key would insert happily and join to nothing,
	// which is invisible until a coverage query returns zero for everything.
	joined := countRows(t, raw, "SELECT COUNT(*) FROM documents d JOIN forks f ON f.fork_key = d.fork_key")
	if joined != documents {
		t.Fatalf("%d of %d documents are orphaned from their forks", documents-joined, documents)
	}
}

// TestSnapshotForkKeyMatchesWhatTheStoreWrites pins the seam the test above
// depends on: the document's fork key must be derived the same way the store
// derives it when inserting the fork, or the two silently disagree.
func TestSnapshotForkKeyMatchesWhatTheStoreWrites(t *testing.T) {
	db, raw := documentStore(t)
	repo := store.RepoRecord{
		Provider: "github", Host: "github.com", Owner: "owner", Name: "repo",
	}
	snap := store.SnapshotFromForge(repo, forge.T1Data{
		ID: "12345", Owner: "alice", Name: "repo", URL: "https://example.invalid",
	}, nil, 1, 1, time.Unix(0, 0).UTC())

	if err := db.UpsertSnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := raw.QueryRowContext(context.Background(), "SELECT fork_key FROM forks").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := snap.ForkKey(); got != want {
		t.Fatalf("store wrote fork_key %q, Snapshot.ForkKey() reports %q", got, want)
	}
}
