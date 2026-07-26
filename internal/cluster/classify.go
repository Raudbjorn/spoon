package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/svnbjrn/spoon/internal/embed"
)

// ForkCategory is a zero-shot category assigned by comparing a fork's
// change digest against anchor descriptions in embedding space. Categories
// are advisory facets ("what kind of fork is this"), not exclusive truths.
type ForkCategory struct {
	Name   string
	Anchor string
}

// DefaultCategories are the anchor texts for zero-shot classification.
// Anchors are written as descriptions of the *changes*, since the digest
// being classified is commit subjects + touched paths.
var DefaultCategories = []ForkCategory{
	{"feature", "implements a new feature or capability: new functionality, new module, new command, new API surface"},
	{"bugfix", "fixes incorrect behavior: bug fixes, crash fixes, memory leaks, race conditions, error handling"},
	{"security", "security hardening: vulnerability patches, authentication, input sanitization, CVE fixes, permission checks"},
	{"ci-build", "build and automation changes: continuous integration pipelines, github actions, dockerfiles, makefiles, packaging, release scripts"},
	{"docs", "documentation changes: readme updates, tutorials, examples, manual pages, code comments"},
	{"localization", "translations and internationalization: locale files, language support, i18n"},
	{"dependencies", "dependency maintenance: version bumps, lockfile updates, library upgrades, vendoring"},
	{"port", "porting to another platform or environment: operating system support, architecture support, compatibility layers, alternative backends"},
	{"config", "personal configuration tweaks: settings changes, defaults, themes, dotfile-style adjustments"},
}

// classifyMinScore is the cosine floor below which a fork stays
// uncategorized — a digest that matches no anchor meaningfully should not
// be forced into one.
const classifyMinScore = 0.25

// classifyDigestMaxChars bounds the digest text per fork.
const classifyDigestMaxChars = 1500

// ClassifyForks assigns a zero-shot category to each fork digest by cosine
// similarity against DefaultCategories in one embedding batch. Returns
// (categories, scores) parallel to features; empty category = unclassified.
// Intended for semantic embedders (fastembed); lexical hashing makes anchor
// matching meaningless, so callers gate on backend.
func ClassifyForks(ctx context.Context, e embed.Embedder, features []embed.ForkFeatures) ([]string, []float64, error) {
	if len(features) == 0 {
		return nil, nil, nil
	}
	texts := make([]string, 0, len(DefaultCategories)+len(features))
	for _, c := range DefaultCategories {
		texts = append(texts, c.Anchor)
	}
	for _, f := range features {
		texts = append(texts, classifyDigest(f))
	}
	vecs, err := e.Embed(ctx, texts)
	if err != nil {
		return nil, nil, err
	}
	if len(vecs) != len(texts) {
		return nil, nil, fmt.Errorf("classify: embedder returned %d vectors, want %d", len(vecs), len(texts))
	}
	anchors := vecs[:len(DefaultCategories)]
	digests := vecs[len(DefaultCategories):]

	categories := make([]string, len(features))
	scores := make([]float64, len(features))
	for i, d := range digests {
		best, bestScore := "", 0.0
		for j, a := range anchors {
			s := cosine32(d, a)
			if s > bestScore {
				best, bestScore = DefaultCategories[j].Name, s
			}
		}
		if bestScore >= classifyMinScore {
			categories[i] = best
			scores[i] = bestScore
		}
	}
	return categories, scores, nil
}

func classifyDigest(f embed.ForkFeatures) string {
	var b strings.Builder
	b.WriteString(f.Commits)
	if f.Paths != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(f.Paths)
	}
	d := b.String()
	if runes := []rune(d); len(runes) > classifyDigestMaxChars {
		d = string(runes[:classifyDigestMaxChars])
	}
	return d
}

func cosine32(a, b embed.Vector) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot float64
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot // inputs are L2-normalized by the embedders
}
