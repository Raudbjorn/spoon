package embed

import "context"

// QueryScorer scores documents against a free-text query, returning one
// relevance score in [0,1] per document. Implemented by the OpenVINO
// cross-encoder Reranker and by LexicalQueryScorer (the no-model fallback).
type QueryScorer interface {
	Rerank(ctx context.Context, query string, docs []string) ([]float64, error)
}

// LexicalQueryScorer scores query relevance with the built-in lexical
// embedder: query and documents are embedded in one corpus-consistent batch
// and scored by cosine similarity (clamped to [0,1]). Useful as a zero-setup
// fallback when the OpenVINO reranker is not configured — strongest when the
// query shares vocabulary with commit messages and file paths.
type LexicalQueryScorer struct{}

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
