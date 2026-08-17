package tui

import (
	"github.com/svnbjrn/spoon/internal/semantic"
	"github.com/svnbjrn/spoon/internal/store"
)

// attachDocument builds the fork's embeddable document onto a snapshot about to
// be written.
//
// Until this existed, nothing the interactive TUI persisted was embeddable at
// all. store.SnapshotFromForge leaves Snapshot.Document zero and the insert is
// gated on a non-empty document ID, so `spn forks list` was the only producer of
// documents rows -- which is why a store holding 6033 forks held two documents.
// Every embedding surface downstream reads through documents, so a coverage
// column or an "embed these forks" action would have had nothing to work with.
//
// The document ID comes from snap.ForkKey() rather than a separately composed
// key so it cannot disagree with the fork row written in the same transaction.
//
// The body depends on whether T2 enrichment has landed: an un-enriched fork
// yields a thinner document than an enriched one, and re-persisting after a
// compare changes the content hash, which is exactly the signal that marks the
// old embedding stale. `spn forks list` behaves the same way.
func attachDocument(snap *store.Snapshot, fork *ScoredFork) {
	document, _ := semantic.BuildDocument(snap.ForkKey(), fork.Fork, fork.T2)
	if document.Body == "" {
		// A fork with no name, description, language, topics or compare data
		// has nothing to embed. Leaving Document zero skips the insert rather
		// than storing a row that would be permanently pending.
		return
	}
	snap.Document = document
}
