package cluster

import (
	"context"
	"fmt"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
)

// SiblingSearcher is the optional capability for non-fork sibling
// discovery. Implementations run a /search/repositories query, fetch
// the top candidates' READMEs, and return the maximum cosine
// similarity of the upstream README to the candidate set.
//
// The default SiblingSearcher is nil — the cluster pipeline does not
// perform sibling discovery unless one is wired in by the caller. The
// forksops path passes a noop default and the CLI replaces it when
// --sibling-sim is set.
type SiblingSearcher interface {
	// SearchSiblings returns the sibling-similarity signal for one
	// upstream. Empty SiblingSim on error or disabled.
	SearchSiblings(ctx context.Context, parent forge.ParentData, embedder embed.Embedder, readmeFetcher ReadmeFetcher, candidateLimit int) (siblingSim float64, candidatesChecked int, err error)
}

type SiblingSimMode string

const (
	SiblingSimModeUpstreamReadme SiblingSimMode = "upstream_readme"
	SiblingSimModeForkIntent     SiblingSimMode = "fork_intent"
)

type ForkIntentSiblingInput struct {
	ForkID   string
	Features embed.ForkFeatures
}

type ForkIntentSiblingSearcher interface {
	SearchForkIntentSiblings(ctx context.Context, parent forge.ParentData, forks []ForkIntentSiblingInput, embedder embed.Embedder, readmeFetcher ReadmeFetcher, candidateLimit int) (map[string]float64, int, error)
}

// DefaultSiblingSearcher is the placeholder SiblingSearcher. It
// always returns (0, 0, nil) so the cluster pipeline degrades to "no
// signal" without surfacing a warning.
type DefaultSiblingSearcher struct{}

// SearchSiblings returns (0, 0, nil) — the placeholder behavior.
func (DefaultSiblingSearcher) SearchSiblings(_ context.Context, _ forge.ParentData, _ embed.Embedder, _ ReadmeFetcher, _ int) (float64, int, error) {
	return 0, 0, nil
}

// noopSiblingSearcher is a typed nil for the default case; it is
// interchangeable with DefaultSiblingSearcher.
var noopSiblingSearcher SiblingSearcher = DefaultSiblingSearcher{}

// SearchSiblings is the package-level entry point used by forksops
// and any future caller. It dispatches to the active SiblingSearcher
// when one is wired in; otherwise it returns the placeholder result.
//
// A nil or non-positive-candidateLimit is normalized to 50 (the
// default in the plan). The function is a no-op when the embedder is
// nil (e.g. the embedder failed to construct).
func SearchSiblings(
	ctx context.Context,
	searcher SiblingSearcher,
	parent forge.ParentData,
	embedder embed.Embedder,
	readmeFetcher ReadmeFetcher,
	candidateLimit int,
) (siblingSim float64, candidatesChecked int, err error) {
	if searcher == nil {
		searcher = noopSiblingSearcher
	}
	if candidateLimit <= 0 {
		candidateLimit = 50
	}
	if embedder == nil || readmeFetcher == nil {
		return 0, 0, nil
	}
	sim, n, err := searcher.SearchSiblings(ctx, parent, embedder, readmeFetcher, candidateLimit)
	if err != nil {
		// Wrap the error so the caller can distinguish a sibling-search
		// failure from a transport error elsewhere in the cluster pass.
		return 0, 0, fmt.Errorf("sibling search: %w", err)
	}
	return sim, n, nil
}

func SearchForkIntentSiblings(
	ctx context.Context,
	searcher SiblingSearcher,
	parent forge.ParentData,
	forks []ForkIntentSiblingInput,
	embedder embed.Embedder,
	readmeFetcher ReadmeFetcher,
	candidateLimit int,
) (map[string]float64, int, error) {
	if searcher == nil || embedder == nil || readmeFetcher == nil || len(forks) == 0 {
		return nil, 0, nil
	}
	forkIntentSearcher, ok := searcher.(ForkIntentSiblingSearcher)
	if !ok {
		return nil, 0, nil
	}
	if candidateLimit <= 0 {
		candidateLimit = 50
	}
	sims, n, err := forkIntentSearcher.SearchForkIntentSiblings(ctx, parent, forks, embedder, readmeFetcher, candidateLimit)
	if err != nil {
		return nil, 0, fmt.Errorf("sibling fork-intent search: %w", err)
	}
	return sims, n, nil
}
