package store

import (
	"context"
	"strings"
)

// ForkCoverage records which embedding models hold a vector for one fork, and
// whether that vector was computed from the document's current body.
//
// Freshness is not a nicety. The pending set is a join on
// (document_id, model, content_hash), so an embedding of a superseded body is
// re-embedded on the next run; a display that showed it as done would show a
// tick that a batch run never changes.
type ForkCoverage struct {
	// models maps model identity to whether its content hash still matches the
	// document's. Absent from the map means no row at all.
	models map[string]bool
}

// NewForkCoverage builds a coverage value from a model-to-freshness map. It
// exists so consumers can construct the states they render without standing up
// a store.
func NewForkCoverage(fresh map[string]bool) ForkCoverage {
	return ForkCoverage{models: fresh}
}

// Present reports whether any embedding row exists for this model, fresh or not.
func (c ForkCoverage) Present(model string) bool {
	_, ok := c.models[model]
	return ok
}

// Fresh reports whether an embedding exists and was computed from the current
// document body.
func (c ForkCoverage) Fresh(model string) bool { return c.models[model] }

// Stale reports an embedding that exists but no longer matches the document.
func (c ForkCoverage) Stale(model string) bool { return c.Present(model) && !c.Fresh(model) }

// PendingDocumentsFor is PendingDocuments narrowed to a set of forks: the
// documents among them that have no vector for model, or whose vector was
// computed from a body that has since changed.
//
// It exists so "embed the forks I marked" can mean exactly that. Reusing the
// unscoped query and filtering afterwards would load every pending document in
// the store -- across every repository ever listed -- to throw almost all of
// them away.
//
// An empty forkKeys returns nothing rather than everything: the caller asked for
// a specific set, and a selection that turns out to be empty must not silently
// become "all".
func (s *Store) PendingDocumentsFor(ctx context.Context, model string, forkKeys []string) ([]PendingDocument, error) {
	if len(forkKeys) == 0 {
		return nil, nil
	}

	args := make([]any, 0, len(forkKeys)+1)
	args = append(args, model)
	placeholders := make([]string, len(forkKeys))
	for i, key := range forkKeys {
		placeholders[i] = "?"
		args = append(args, key)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT d.document_id,d.fork_key,d.content_hash,d.body FROM documents d
		LEFT JOIN embeddings e ON e.document_id=d.document_id AND e.model=?
		WHERE d.fork_key IN (`+strings.Join(placeholders, ",")+`)
		AND (e.document_id IS NULL OR e.content_hash<>d.content_hash) ORDER BY d.document_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pending []PendingDocument
	for rows.Next() {
		var doc PendingDocument
		if err := rows.Scan(&doc.DocumentID, &doc.ForkKey, &doc.ContentHash, &doc.Body); err != nil {
			return nil, err
		}
		pending = append(pending, doc)
	}
	return pending, rows.Err()
}

// EmbeddingCoverage reports, for every fork of repoKey that has a document,
// which of the given models hold a vector for it and whether that vector is
// current.
//
// Models are matched exactly rather than by a "fastembed:"/"voyage:" prefix.
// A model identity encodes its parameters -- changing the max length or the
// output dimension re-partitions the index and strands the old rows -- so a
// prefix match would report a stranded row as coverage while PendingDocuments,
// which joins on exact equality, would keep offering the same fork for work.
// The column and the action have to answer the same question.
func (s *Store) EmbeddingCoverage(ctx context.Context, repoKey string, models []string) (map[string]ForkCoverage, error) {
	coverage := make(map[string]ForkCoverage)
	if len(models) == 0 {
		return coverage, nil
	}

	args := make([]any, 0, len(models)+1)
	args = append(args, repoKey)
	placeholders := make([]string, len(models))
	for i, model := range models {
		placeholders[i] = "?"
		args = append(args, model)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT d.fork_key, e.model, e.content_hash = d.content_hash
		FROM documents d
		JOIN forks f ON f.fork_key = d.fork_key
		JOIN embeddings e ON e.document_id = d.document_id
		WHERE f.repo_key = ? AND e.model IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var forkKey, model string
		var fresh bool
		if err := rows.Scan(&forkKey, &model, &fresh); err != nil {
			return nil, err
		}
		entry, ok := coverage[forkKey]
		if !ok {
			entry = ForkCoverage{models: make(map[string]bool, len(models))}
		}
		entry.models[model] = fresh
		coverage[forkKey] = entry
	}
	return coverage, rows.Err()
}
