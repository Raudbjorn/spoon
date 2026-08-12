package embed

import "context"

// LexicalQueryMethod is the queryMethod label for lexically scored results.
const LexicalQueryMethod = "lexical"

// QueryScorer scores documents against a free-text query, returning one
// relevance score in [0,1] per document, in input order. Implemented by
// VoyageReranker (a cross-encoder, network-backed) and by LexicalQueryScorer
// (the zero-setup, in-process fallback).
//
// Method names the implementation for the queryMethod output field. It is part
// of the interface so the label always comes from the scorer that produced the
// scores; a hardcoded label at the call site outlived the implementation it
// named once already.
type QueryScorer interface {
	Rerank(ctx context.Context, query string, docs []string) ([]float64, error)
	Method() string
}

// LexicalQueryScorer scores query relevance with the built-in lexical
// embedder: query and documents are embedded in one corpus-consistent batch
// and scored by cosine similarity (clamped to [0,1]). Useful as a zero-setup
// fallback when no reranker is configured — strongest when the query shares
// vocabulary with commit messages and file paths.
type LexicalQueryScorer struct{}

// Method implements QueryScorer.
func (LexicalQueryScorer) Method() string { return LexicalQueryMethod }

// Rerank implements QueryScorer.
func (LexicalQueryScorer) Rerank(ctx context.Context, query string, docs []string) ([]float64, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	texts := make([]string, 0, len(docs)+1)
	texts = append(texts, query)
	texts = append(texts, docs...)
	vecs, err := LocalEmbedder{}.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	q := vecs[0]
	out := make([]float64, len(docs))
	for i, d := range vecs[1:] {
		var dot float64
		for j := range q {
			dot += float64(q[j]) * float64(d[j])
		}
		if dot < 0 {
			dot = 0
		} else if dot > 1 {
			// Cosine of near-identical normalized vectors can drift just past
			// 1.0 from float rounding; clamp so scores stay in [0,1] as documented.
			dot = 1
		}
		out[i] = dot
	}
	return out, nil
}
