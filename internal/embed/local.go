package embed

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// BuiltinModelName identifies the in-process lexical embedder in cluster
// cache keys and user-facing output. Bump the version suffix whenever the
// tokenization or weighting scheme changes, so stale cluster caches are
// invalidated by key mismatch.
const BuiltinModelName = "builtin-lexical-v1"

// localDim is the per-text vector width. 512 signed-hash buckets keeps
// collision noise low for the token volumes spoon embeds (path lists, commit
// subjects, README excerpts, diffstat lines are a few hundred tokens each)
// while the concatenated multi-modal vector stays small (4×512 floats).
const localDim = 512

// LocalEmbedder is a deterministic, in-process lexical embedder. It maps each
// text to a fixed-width vector via signed feature hashing of unigram and
// bigram tokens, weighted by sublinear TF × batch-level smoothed IDF, then
// L2-normalized.
//
// IDF is computed over the texts of a single Embed call. The clustering
// pipeline embeds an entire run's corpus in one call, so vectors are
// IDF-weighted against exactly the fork set being clustered — common
// boilerplate (shared paths, "readme" headers) cancels out and
// fork-distinctive tokens dominate the cosine geometry. Vectors from
// different Embed calls are therefore not directly comparable; spoon only
// compares within a run, and the cluster cache stores cluster assignments,
// not vectors.
//
// The zero value is ready to use. Safe for concurrent use: Embed is
// stateless and Dim is constant.
type LocalEmbedder struct{}

// Dim returns the fixed per-text vector width.
func (LocalEmbedder) Dim() int { return localDim }

// Embed converts texts to vectors. Empty texts produce zero vectors. The
// only error source is context cancellation.
func (LocalEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tokenized := make([][]string, len(texts))
	df := make(map[string]int)
	for i, t := range texts {
		toks := lexTokens(t)
		tokenized[i] = toks
		seen := make(map[string]struct{}, len(toks))
		for _, tok := range toks {
			if _, dup := seen[tok]; dup {
				continue
			}
			seen[tok] = struct{}{}
			df[tok]++
		}
	}

	n := float64(len(texts))
	out := make([]Vector, len(texts))
	for i, toks := range tokenized {
		v := make(Vector, localDim)
		if len(toks) > 0 {
			counts := make(map[string]int, len(toks))
			for _, tok := range toks {
				counts[tok]++
			}
			for tok, c := range counts {
				tf := 1 + math.Log(float64(c))
				idf := 1 + math.Log((1+n)/float64(1+df[tok]))
				bucket, sign := hashToken(tok)
				v[bucket] += float32(tf*idf) * sign
			}
			l2Normalize(v)
		}
		out[i] = v
	}
	return out, nil
}

// hashToken maps a token to a (bucket, sign) pair via FNV-1a 64. One hash
// bit supplies the sign so that colliding tokens cancel rather than
// systematically inflate a bucket (standard signed feature hashing).
func hashToken(tok string) (int, float32) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(tok))
	sum := h.Sum64()
	bucket := int(sum % localDim)
	if sum&(1<<63) != 0 {
		return bucket, -1
	}
	return bucket, 1
}

// lexTokens lowercases the text, splits it into alphanumeric runs per line,
// and emits unigrams plus within-line adjacent bigrams. Bigrams let path
// hierarchy ("internal cluster") and commit phrasing ("rate limit") carry
// structure that unigrams alone lose. Pure-number runs and single-character
// runs are dropped.
func lexTokens(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.ToLower(text), "\n") {
		words := splitAlnum(line)
		for i, w := range words {
			out = append(out, w)
			if i > 0 {
				out = append(out, words[i-1]+" "+w)
			}
		}
	}
	return out
}

// splitAlnum returns the alphanumeric runs of s, dropping runs that are a
// single character or all digits (line numbers, counts, and hex-ish noise
// carry no clustering signal).
func splitAlnum(s string) []string {
	var out []string
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		run := s[start:end]
		start = -1
		if len(run) < 2 || allDigits(run) {
			return
		}
		out = append(out, run)
	}
	for i, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(s))
	return out
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
