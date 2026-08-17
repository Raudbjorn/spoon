package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func coverageStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedFork(t *testing.T, db *Store, forgeID, body string) Snapshot {
	t.Helper()
	repo := RepoRecord{Provider: "github", Host: "github.com", Owner: "owner", Name: "repo"}
	snap := Snapshot{
		Repo: repo,
		Fork: ForkRecord{ForgeID: forgeID, Owner: "alice", Name: "fork" + forgeID, URL: "https://example.invalid"},
	}
	snap.Document = DocumentRecord{
		DocumentID: DocumentID(snap.ForkKey()), ContentHash: "hash-" + body, Body: body, UpdatedAt: time.Unix(0, 0).UTC(),
	}
	if err := db.UpsertSnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestEmbeddingCoverage(t *testing.T) {
	const (
		fastModel   = "fastembed:fast-bge-small-en-v1.5:maxlen=512:prompts=bge"
		retiredFast = "fastembed:fast-bge-small-en-v1.5:maxlen=512"
		voyageModel = "voyage:voyage-code-3:dim=1024:input_type=qd"
	)
	ctx := context.Background()
	db := coverageStore(t)

	embedded := seedFork(t, db, "1", "embedded")
	stale := seedFork(t, db, "2", "stale")
	retired := seedFork(t, db, "3", "retired")
	bare := seedFork(t, db, "4", "bare")

	if err := db.UpsertEmbeddings(ctx, []EmbeddingRecord{
		{DocumentID: DocumentID(embedded.ForkKey()), Model: fastModel, Dim: 2, Vector: encode2(), ContentHash: "hash-embedded", CreatedAt: time.Unix(0, 0).UTC()},
		{DocumentID: DocumentID(embedded.ForkKey()), Model: voyageModel, Dim: 2, Vector: encode2(), ContentHash: "hash-embedded", CreatedAt: time.Unix(0, 0).UTC()},
		{DocumentID: DocumentID(stale.ForkKey()), Model: fastModel, Dim: 2, Vector: encode2(), ContentHash: "hash-stale", CreatedAt: time.Unix(0, 0).UTC()},
		// Only under an identity the current build no longer uses.
		{DocumentID: DocumentID(retired.ForkKey()), Model: retiredFast, Dim: 2, Vector: encode2(), ContentHash: "hash-retired", CreatedAt: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatal(err)
	}

	// Staleness has to be produced the way it actually occurs -- the document
	// changes after its vector was computed -- because UpsertEmbeddings refuses
	// to write a record whose content hash does not match the document's
	// current one. Re-persisting fork 2 with a new body is exactly what a T2
	// enrichment does.
	seedFork(t, db, "2", "stale-after-enrichment")

	repoKey := RepoKey("github", "github.com", "owner", "repo")
	coverage, err := db.EmbeddingCoverage(ctx, repoKey, []string{fastModel, voyageModel})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a fork embedded under both models reports both fresh", func(t *testing.T) {
		got := coverage[embedded.ForkKey()]
		if !got.Fresh(fastModel) || !got.Fresh(voyageModel) {
			t.Fatalf("coverage = %+v, want both models fresh", got)
		}
	})

	t.Run("an embedding hashed against an older body is stale, not absent", func(t *testing.T) {
		got := coverage[stale.ForkKey()]
		if !got.Present(fastModel) {
			t.Fatal("a stale embedding is still a row that exists; Present must see it")
		}
		if got.Fresh(fastModel) {
			t.Fatal("an embedding of a superseded body reported as fresh")
		}
	})

	t.Run("a retired model identity does not count as coverage", func(t *testing.T) {
		// This is the case that makes prefix matching wrong. The row is real,
		// but the pending-documents join is on exact model equality, so this
		// fork WILL be re-embedded. Reporting it as covered would show a tick
		// that never changes no matter how many times the user runs a batch.
		got := coverage[retired.ForkKey()]
		if got.Present(fastModel) || got.Fresh(fastModel) {
			t.Fatalf("retired identity %q counted as coverage for %q", retiredFast, fastModel)
		}
	})

	t.Run("a fork with a document and no embeddings reports nothing", func(t *testing.T) {
		got := coverage[bare.ForkKey()]
		if got.Present(fastModel) || got.Present(voyageModel) {
			t.Fatalf("coverage = %+v, want no models", got)
		}
	})

	t.Run("coverage agrees with the pending set", func(t *testing.T) {
		pending, err := db.PendingDocuments(ctx, fastModel)
		if err != nil {
			t.Fatal(err)
		}
		pendingKeys := map[string]bool{}
		for _, p := range pending {
			pendingKeys[p.ForkKey] = true
		}
		// The column and the button must answer the same question: every fork
		// the indexer would work on must be one the column shows as not fresh.
		for _, snap := range []Snapshot{embedded, stale, retired, bare} {
			key := snap.ForkKey()
			if fresh := coverage[key].Fresh(fastModel); fresh == pendingKeys[key] {
				t.Errorf("fork %s: coverage fresh=%v but pending=%v", key, fresh, pendingKeys[key])
			}
		}
	})
}

func encode2() []byte { return []byte{0, 0, 128, 63, 0, 0, 0, 0} }

func TestPendingDocumentsFor(t *testing.T) {
	const fastModel = "fastembed:fast-bge-small-en-v1.5:maxlen=512:prompts=bge"
	ctx := context.Background()
	db := coverageStore(t)

	done := seedFork(t, db, "1", "already-embedded")
	todo := seedFork(t, db, "2", "not-embedded")
	other := seedFork(t, db, "3", "also-not-embedded")

	if err := db.UpsertEmbeddings(ctx, []EmbeddingRecord{{
		DocumentID: DocumentID(done.ForkKey()), Model: fastModel, Dim: 2, Vector: encode2(),
		ContentHash: "hash-already-embedded", CreatedAt: time.Unix(0, 0).UTC(),
	}}); err != nil {
		t.Fatal(err)
	}

	t.Run("returns only the requested forks that need work", func(t *testing.T) {
		pending, err := db.PendingDocumentsFor(ctx, fastModel, []string{done.ForkKey(), todo.ForkKey()})
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 1 || pending[0].ForkKey != todo.ForkKey() {
			t.Fatalf("pending = %+v, want only %s", pending, todo.ForkKey())
		}
		for _, p := range pending {
			if p.ForkKey == other.ForkKey() {
				t.Error("a fork outside the requested set was returned")
			}
		}
	})

	t.Run("an empty selection means nothing, not everything", func(t *testing.T) {
		// "Embed the forks I marked" with nothing marked must be a no-op. If
		// this fell through to the unscoped query it would silently embed the
		// entire store -- billable, under Voyage.
		pending, err := db.PendingDocumentsFor(ctx, fastModel, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 0 {
			t.Fatalf("empty selection returned %d documents", len(pending))
		}
	})

	t.Run("agrees with the unscoped query on the same forks", func(t *testing.T) {
		all, err := db.PendingDocuments(ctx, fastModel)
		if err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(all))
		for _, p := range all {
			keys = append(keys, p.ForkKey)
		}
		scoped, err := db.PendingDocumentsFor(ctx, fastModel, keys)
		if err != nil {
			t.Fatal(err)
		}
		if len(scoped) != len(all) {
			t.Fatalf("scoped query returned %d for the full set, unscoped returned %d", len(scoped), len(all))
		}
	})
}
