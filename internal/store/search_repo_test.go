package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A fork indexed under a non-default forge (GitLab) must still be found by
// `spn search --repo owner/repo`, which rarely knows the forge and would
// otherwise compute a github.com default key that matches nothing. SearchRows
// filters by repo identity (owner/name), not the full provider:host key (#82).
func TestSearchRowsFiltersByRepoIdentityAcrossForges(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()

	snap := Snapshot{
		Repo:     RepoRecord{Provider: "gitlab", Host: "gitlab.com", Owner: "inkscape", Name: "inkscape", FirstSeen: now, LastSeen: now},
		Fork:     ForkRecord{ForgeID: "F1", Owner: "alice", Name: "inkscape", URL: "u", UpdatedAt: now},
		Document: DocumentRecord{DocumentID: "fork:doc", ContentHash: "h", Body: "canvas rendering", UpdatedAt: now},
	}
	if err := db.UpsertSnapshot(ctx, snap); err != nil {
		t.Fatalf("upsert snapshot: %v", err)
	}
	if err := db.UpsertEmbeddings(ctx, []EmbeddingRecord{{
		DocumentID: "fork:doc", Model: "m", Dim: 1, Vector: []byte{0, 0, 128, 63}, ContentHash: "h", CreatedAt: now,
	}}); err != nil {
		t.Fatalf("upsert embeddings: %v", err)
	}

	// Search that does not know the forge (would have keyed github.com) still hits.
	rows, err := db.SearchRows(ctx, "m", "inkscape", "inkscape")
	if err != nil {
		t.Fatalf("search rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 — forge-agnostic repo filter failed", len(rows))
	}

	// Case-insensitive on owner/name.
	if rows, err := db.SearchRows(ctx, "m", "Inkscape", "Inkscape"); err != nil || len(rows) != 1 {
		t.Fatalf("case-insensitive match failed: rows=%d err=%v", len(rows), err)
	}

	// A different repo identity must not match.
	if rows, err := db.SearchRows(ctx, "m", "other", "repo"); err != nil || len(rows) != 0 {
		t.Fatalf("unrelated repo matched: rows=%d err=%v", len(rows), err)
	}

	// No filter returns everything for the model.
	if rows, err := db.SearchRows(ctx, "m", "", ""); err != nil || len(rows) != 1 {
		t.Fatalf("unfiltered search failed: rows=%d err=%v", len(rows), err)
	}
}

// SearchRows is keyed by model, so vectors left behind by a previous embedder
// are invisible rather than absent. The counts let the CLI report that instead
// of claiming the index is empty (#82).
func TestEmbeddingModelCountsGroupsByModel(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()

	if counts, err := db.EmbeddingModelCounts(ctx); err != nil || len(counts) != 0 {
		t.Fatalf("empty store: counts=%v err=%v", counts, err)
	}

	records := make([]EmbeddingRecord, 0, 3)
	for i, model := range []string{"m1", "m1", "m2"} {
		id := "fork:doc" + string(rune('a'+i))
		if err := db.UpsertSnapshot(ctx, Snapshot{
			Repo:     RepoRecord{Provider: "github", Host: "github.com", Owner: "o", Name: "r", FirstSeen: now, LastSeen: now},
			Fork:     ForkRecord{ForgeID: "f" + string(rune('a'+i)), Owner: "x", Name: "f", URL: "u", UpdatedAt: now},
			Document: DocumentRecord{DocumentID: id, ContentHash: "h", Body: "b", UpdatedAt: now},
		}); err != nil {
			t.Fatalf("upsert snapshot: %v", err)
		}
		records = append(records, EmbeddingRecord{DocumentID: id, Model: model, Dim: 1, Vector: []byte{0, 0, 128, 63}, ContentHash: "h", CreatedAt: now})
	}
	if err := db.UpsertEmbeddings(ctx, records); err != nil {
		t.Fatalf("upsert embeddings: %v", err)
	}

	counts, err := db.EmbeddingModelCounts(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts["m1"] != 2 || counts["m2"] != 1 || len(counts) != 2 {
		t.Errorf("counts = %v, want m1:2 m2:1", counts)
	}
}
