package tui

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/tursodatabase/go-libsql"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/semantic"
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

func TestCachedInvalidationReplacesSearchDocumentBeforeAutoIndex(t *testing.T) {
	ctx := context.Background()
	db, raw := documentStore(t)
	fake := &orderFakeForge{tierFakeForge: tierFakeForge{headroom: 0}}
	m := queueTestModel(fake)
	m.db = db
	m.refresh = false
	m.auth = forge.AuthInfo{Provider: forge.ProviderGitHub, Host: "github.com"}
	m.parent = &forge.ParentData{FullName: "up/repo"}
	m.forks = m.forks[:1]
	m.enriching, m.enrichTotal = true, 1
	m.embedModels = embedModels{fastEmbed: "test:local", voyage: "test:remote"}
	fk := tierTestFork()
	fk.Description = "current repository metadata"
	cached := &forge.T2Data{Performed: true, ComparedBranch: "main", HeadSHA: "head", AheadCount: 2,
		Diffs:   []forge.FileDiff{{Path: "obsolete.go", Patch: "+obsolete implementation"}},
		Commits: []forge.AheadCommit{{SHA: "head", Message: "obsolete feature"}},
	}
	m.forks[0].Fork, m.forks[0].T2, m.forks[0].Enriched = fk, cached, true
	m.cached = store.NewRepoSnapshot([]store.CachedFork{{T1: fk, T2: cached}})
	repo := store.RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo"}
	snap := store.SnapshotFromForge(repo, fk, cached, 1, 2, time.Now())
	attachDocument(&snap, &m.forks[0])
	if !strings.Contains(snap.Document.Body, "obsolete") {
		t.Fatal("fixture must contain compare evidence in its document")
	}
	if err := db.UpsertSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	models := []string{m.embedModels.fastEmbed, m.embedModels.voyage}
	for _, model := range models {
		if err := db.UpsertEmbeddings(ctx, []store.EmbeddingRecord{{DocumentID: snap.Document.DocumentID,
			Model: model, Dim: 1, Vector: semantic.EncodeVector([]float32{1}), ContentHash: snap.Document.ContentHash,
		}}); err != nil {
			t.Fatal(err)
		}
		if rows, err := db.SearchRows(ctx, model, "up", "repo"); err != nil || len(rows) != 1 {
			t.Fatalf("seeded %s vector is not searchable: rows=%d err=%v", model, len(rows), err)
		}
	}

	_, cmd := m.handleBatchDivergence(batchDivergenceMsg{ctx: m.enrichCtx,
		order: []enrichEntry{{fork: fk, cached: cached}},
		divergence: map[string]forge.ForkDivergence{fk.ID: {Resolved: true, CountsOnly: true,
			Default: forge.BranchDivergence{Name: "main", TipSHA: "head", AheadBy: 0}}},
	})
	want, _ := semantic.BuildDocument(snap.ForkKey(), fk, nil)
	var body, hash string
	if err := raw.QueryRowContext(ctx, "SELECT body, content_hash FROM documents WHERE document_id=?", snap.Document.DocumentID).Scan(&body, &hash); err != nil {
		t.Fatal(err)
	}
	if body != want.Body || hash != want.ContentHash || hash == snap.Document.ContentHash || strings.Contains(body, "obsolete") {
		t.Fatalf("invalidation left stale search evidence: body=%q hash=%q", body, hash)
	}
	for _, model := range models {
		if rows, err := db.SearchRows(ctx, model, "up", "repo"); err != nil || len(rows) != 0 {
			t.Fatalf("stale %s vector remains searchable: rows=%d err=%v", model, len(rows), err)
		}
	}

	coverageRefreshed, reserveSkipped := false, false
	for _, result := range runBatch(cmd) {
		switch msg := result.(type) {
		case embedCoverageMsg:
			if msg.err != nil {
				t.Fatal(msg.err)
			}
			for _, model := range models {
				if !msg.coverage[snap.ForkKey()].Stale(model) {
					t.Fatalf("coverage did not mark %s stale", model)
				}
			}
			coverageRefreshed = true
		case tier2ResultMsg:
			reserveSkipped = msg.budgetSkipped
			m.pendingUpdates = append(m.pendingUpdates, msg)
		}
	}
	if !coverageRefreshed || !reserveSkipped || len(fake.order) != 0 {
		t.Fatalf("coverage=%v reserveSkipped=%v compare calls=%v", coverageRefreshed, reserveSkipped, fake.order)
	}

	_, automatic := m.processPendingUpdates()
	if !m.autoIndexDone || automatic == nil {
		t.Fatal("reserve completion did not start the automatic indexing pass")
	}
	// The unresolved model probe prevents native/network work while the real
	// automatic worker still persists and offers the final document to indexing.
	for _, result := range runBatch(automatic) {
		if done, ok := result.(embedRunDoneMsg); ok && done.err != nil {
			t.Fatal(done.err)
		}
	}
	for _, model := range models {
		pending, err := db.PendingDocumentsFor(ctx, model, []string{snap.ForkKey()})
		if err != nil || len(pending) != 1 || pending[0].Body != want.Body || pending[0].ContentHash != want.ContentHash {
			t.Fatalf("auto-index input for %s: %+v, err=%v", model, pending, err)
		}
	}
}
