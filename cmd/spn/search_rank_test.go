package main

import (
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/store"
)

// A single unreadable vector blob (partial write, corruption) must not fail the
// whole search and strand every intact row: rankSearchRows skips and counts it,
// and the caller degrades to a warning rather than CodeInternal (#82).
func TestRankSearchRowsSkipsUnreadableVectors(t *testing.T) {
	// One float32 = 1.0 little-endian; matches Dim:1.
	good := []byte{0, 0, 128, 63}
	rows := []store.SearchRow{
		{DocumentID: "fork:a", ForkKey: "a", Repo: "o/r", Fork: "x/a", Dim: 1, Vector: good, IndexedAt: time.Unix(1, 0)},
		{DocumentID: "fork:bad", ForkKey: "bad", Repo: "o/r", Fork: "x/bad", Dim: 1, Vector: []byte{0, 1}},          // truncated blob
		{DocumentID: "fork:zero", ForkKey: "zero", Repo: "o/r", Fork: "x/zero", Dim: 1, Vector: []byte{0, 0, 0, 0}}, // zero-norm → Cosine fails
		{DocumentID: "fork:b", ForkKey: "b", Repo: "o/r", Fork: "x/b", Dim: 1, Vector: good, IndexedAt: time.Unix(1, 0)},
	}
	query := []float32{1}

	results, skipped := rankSearchRows(query, rows)
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2 (truncated + zero-norm)", skipped)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 intact rows ranked", len(results))
	}
	for _, r := range results {
		if r.ForkID == "bad" || r.ForkID == "zero" {
			t.Fatalf("unreadable row %q leaked into results", r.ForkID)
		}
	}
}
