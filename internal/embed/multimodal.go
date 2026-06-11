package embed

import (
	"context"
	"fmt"
	"math"
	"strings"
)

var modalityWeights = [4]float32{0.3, 0.3, 0.2, 0.2}

// maxEmbedModalityChars caps each modality blob (paths/commits/readme/diff)
// before it is sent to the embedder. A fork with a large diff can otherwise
// exceed a small embedding model's context window (e.g. nomic-embed-text's
// 2048 tokens), which makes Ollama return HTTP 500 ("the input length exceeds
// the context length") and aborts the entire clustering pass. The leading
// chunk is representative for similarity clustering. ~3000 chars stays well
// under 2048 tokens even for dense code/diffs.
const maxEmbedModalityChars = 3000

// truncateForEmbed caps s to maxEmbedModalityChars runes (rune-safe).
func truncateForEmbed(s string) string {
	if len(s) <= maxEmbedModalityChars {
		return s // fast path: byte length already within cap
	}
	n := 0
	for i := range s { // i is the byte offset of each rune start
		if n >= maxEmbedModalityChars {
			return s[:i]
		}
		n++
	}
	return s
}

func isCodeAwareModel(name string) bool {
	if name == "" {
		return false
	}
	base := strings.ToLower(baseModelName(name))
	for _, m := range PreferredEmbeddingModels {
		if m.CodeAware && strings.ToLower(m.Name) == base {
			return true
		}
	}
	return false
}

// MultiModalEmbed embeds each modality separately and returns a concatenated
// vector weighted [paths 0.3, commits 0.3, readme 0.2, diff 0.2]. Missing
// modalities (empty strings) contribute a zero block. The result is
// L2-normalized.
//
// When embedderModel matches a CodeAware entry in PreferredEmbeddingModels,
// the call switches to a single-call path: it joins the four modalities with
// structural separators and embeds once per fork. Known limitation: the
// CodeAware path does not escape HTML-like characters inside the modality
// blobs, so a fork whose data legitimately contains the literal "</paths>"
// (etc.) would produce ambiguous prompts. We accept this for the embedding
// use case (small models don't parse the tags, just attend to them) and
// trade away escaping cost.
//
// embedderModel is the resolved model name (e.g., "nomic-embed-text"). Required because the CodeAware fast path depends on the model, and Embedder has no model-introspection method. This deviates from the plan signature as a deliberate simplification.
func MultiModalEmbed(ctx context.Context, e Embedder, embedderModel string, fs []ForkFeatures) ([]Vector, error) {
	if e == nil {
		return nil, fmt.Errorf("nil embedder")
	}
	if len(fs) == 0 {
		return nil, nil
	}
	if isCodeAwareModel(embedderModel) {
		return codeAwareEmbed(ctx, e, fs)
	}
	return modalityBlendEmbed(ctx, e, fs)
}

func codeAwareEmbed(ctx context.Context, e Embedder, fs []ForkFeatures) ([]Vector, error) {
	prompts := make([]string, len(fs))
	for i, f := range fs {
		var b strings.Builder
		b.WriteString("<paths>")
		b.WriteString(truncateForEmbed(f.Paths))
		b.WriteString("</paths><commits>")
		b.WriteString(truncateForEmbed(f.Commits))
		b.WriteString("</commits><readme>")
		b.WriteString(truncateForEmbed(f.ReadmeDoc))
		b.WriteString("</readme><diff>")
		b.WriteString(truncateForEmbed(f.DiffChunk))
		b.WriteString("</diff>")
		prompts[i] = b.String()
	}
	vecs, err := e.Embed(ctx, prompts)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(prompts) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d prompts", len(vecs), len(prompts))
	}
	for i := range vecs {
		l2Normalize(vecs[i])
	}
	return vecs, nil
}

func modalityBlendEmbed(ctx context.Context, e Embedder, fs []ForkFeatures) ([]Vector, error) {
	const modalityCount = 4
	prompts := make([]string, 0, len(fs)*modalityCount)
	present := make([][modalityCount]bool, len(fs))
	for i, f := range fs {
		mods := [modalityCount]string{f.Paths, f.Commits, f.ReadmeDoc, f.DiffChunk}
		for j, m := range mods {
			if m == "" {
				continue
			}
			present[i][j] = true
			prompts = append(prompts, truncateForEmbed(m))
		}
	}

	var raw []Vector
	if len(prompts) > 0 {
		var err error
		raw, err = e.Embed(ctx, prompts)
		if err != nil {
			return nil, err
		}
		if len(raw) != len(prompts) {
			return nil, fmt.Errorf("embedder returned %d vectors for %d prompts", len(raw), len(prompts))
		}
	}

	dim := e.Dim()
	if dim == 0 && len(raw) > 0 {
		dim = len(raw[0])
	}
	if dim == 0 {
		dim = 1
	}

	out := make([]Vector, len(fs))
	cursor := 0
	for i := range fs {
		combined := make(Vector, dim*modalityCount)
		for j := 0; j < modalityCount; j++ {
			block := combined[j*dim : (j+1)*dim]
			if !present[i][j] {
				continue
			}
			v := raw[cursor]
			cursor++
			w := modalityWeights[j]
			n := dim
			if len(v) < n {
				n = len(v)
			}
			for k := 0; k < n; k++ {
				block[k] = v[k] * w
			}
		}
		l2Normalize(combined)
		out[i] = combined
	}
	return out, nil
}

func l2Normalize(v Vector) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1.0 / math.Sqrt(sum))
	for i, x := range v {
		v[i] = x * inv
	}
}
