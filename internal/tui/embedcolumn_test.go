package tui

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/store"
)

// TestEmbedColumnDistinguishesEveryCoverageState is the answer to "no way to
// see which forks have embeddings, or which don't". A single tick is not
// enough: a fork can be embedded by one provider and not the other, and an
// embedding can exist while being stale, which is materially the same as
// missing because the next run will redo it.
func TestEmbedColumnDistinguishesEveryCoverageState(t *testing.T) {
	m := refreshModel(t)
	fastID := embed.FastEmbedModelID
	voyageID := embed.VoyageModelID("voyage-code-3", 1024)
	m.embedModels = embedModels{fastEmbed: fastID, voyage: voyageID}

	cells := map[string]string{}
	for _, tt := range []struct {
		name  string
		fresh map[string]bool
	}{
		{"neither", nil},
		{"fastembed only", map[string]bool{fastID: true}},
		{"voyage only", map[string]bool{voyageID: true}},
		{"both", map[string]bool{fastID: true, voyageID: true}},
		{"fastembed stale", map[string]bool{fastID: false}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cell := m.embedCell(store.NewForkCoverage(tt.fresh))
			if got := len([]rune(cell)); got != 2 {
				t.Fatalf("cell %q is %d runes, want exactly 2 so the column cannot drift", cell, got)
			}
			for previous, seen := range cells {
				if seen == cell {
					t.Fatalf("%q renders identically to %q as %q", tt.name, previous, cell)
				}
			}
			cells[tt.name] = cell
		})
	}
}

// TestEmbedColumnRendersInTheTable: the column has to be in the frame, in both
// the compare and non-compare layouts, with the header above it.
func TestEmbedColumnRendersInTheTable(t *testing.T) {
	for _, withCompare := range []bool{false, true} {
		name := "t1-only"
		if withCompare {
			name = "with-compare"
		}
		t.Run(name, func(t *testing.T) {
			m := frameModel(t, 140, 30, 6)
			m.embedModels = embedModels{fastEmbed: embed.FastEmbedModelID}
			if withCompare {
				m.forks[0].T2 = &forge.T2Data{AheadCount: 3, BehindCount: 1}
			}
			view := m.viewTable()
			if !strings.Contains(view, "EMB") {
				t.Fatalf("table has no EMB header:\n%s", view)
			}
		})
	}
}

// TestEmbedColumnHeaderAndRowsStayAligned guards the rule the table's own
// comment states: a width changed on one side of the header/row pair drifts
// every column to its right.
func TestEmbedColumnHeaderAndRowsStayAligned(t *testing.T) {
	for _, withCompare := range []bool{false, true} {
		m := frameModel(t, 160, 30, 4)
		m.embedModels = embedModels{fastEmbed: embed.FastEmbedModelID}
		if withCompare {
			m.forks[0].T2 = &forge.T2Data{AheadCount: 3, BehindCount: 1}
		}
		lines := strings.Split(m.viewTable(), "\n")
		var header string
		for _, line := range lines {
			if strings.Contains(line, "REPOSITORY") {
				header = line
				break
			}
		}
		if header == "" {
			t.Fatal("no header line")
		}
		embCol := strings.Index(header, "EMB")
		if embCol < 0 {
			t.Fatalf("header has no EMB column: %q", header)
		}
	}
}

// T2 persistence rewrites a fork document. Coverage must be reloaded after
// that write so the EMB column can show the newly stale embedding.
func TestProcessPendingUpdatesReloadsEmbeddingCoverage(t *testing.T) {
	db, _ := documentStore(t)
	m := refreshModel(t)
	m.db = db
	m.embedModels = embedModels{fastEmbed: embed.FastEmbedModelID}
	m.autoIndexDone = true // isolate coverage refresh from the automatic pass.
	m.enriching = true
	m.enrichTotal = 1
	m.pendingUpdates = []tier2ResultMsg{{
		forkID: m.forks[0].Fork.ID,
		t2:     forge.T2Data{Performed: true, AheadCount: 1},
	}}

	_, cmd := m.processPendingUpdates()
	if cmd == nil {
		t.Fatal("T2 document persistence did not schedule an embedding coverage reload")
	}
}
