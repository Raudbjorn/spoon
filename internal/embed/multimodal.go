package embed

import (
	"context"
	"fmt"
	"math"
)

var modalityWeights = [4]float32{0.3, 0.3, 0.2, 0.2}

// maxEmbedModalityChars caps each modality blob (paths/commits/readme/diff)
// before it is embedded. Each modality is embedded as its OWN vector here (not a
// shared document), so this path is distinct from the semantic index, whose
// single-document budget lives in features.go/semantic.go. The built-in lexical
// embedder — MultiModalEmbed's default — has no context window, so the cap only
// bounds worst-case tokenization cost on pathological inputs (multi-megabyte
// READMEs). When a windowed embedder (fastembed) runs this path instead, it
// truncates each modality token-safely at 512 tokens internally; the excess is
// simply unused, never corrupting a neighbouring section, so the larger cap is
// intentional and safe.
const maxEmbedModalityChars = 16000

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

// MultiModalEmbed embeds each modality separately and returns a concatenated
// vector weighted [paths 0.3, commits 0.3, readme 0.2, diff 0.2]. Missing
// modalities (empty strings) contribute a zero block. The result is
// L2-normalized.
func MultiModalEmbed(ctx context.Context, e Embedder, fs []ForkFeatures) ([]Vector, error) {
	if e == nil {
		return nil, fmt.Errorf("nil embedder")
	}
	if len(fs) == 0 {
		return nil, nil
	}
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
