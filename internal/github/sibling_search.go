package github

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
)

// SiblingSearchHit is the trimmed shape of a /search/repositories item
// that GHSiblingSearcher needs. Defined as a separate type so the
// search API's other fields (description, stargazers_count, etc.)
// don't leak into the package.
type SiblingSearchHit struct {
	FullName  string `json:"full_name"`
	HTMLURL   string `json:"html_url"`
	HasTopics bool   `json:"-"`
}

// siblingSearchResponse is the trimmed /search/repositories envelope.
type siblingSearchResponse struct {
	Items []SiblingSearchHit `json:"items"`
}

// GHSiblingSearcher implements cluster.SiblingSearcher against the
// GitHub /search/repositories endpoint. It picks the first topic from
// the upstream's topic set as the search key, fetches the top
// candidateCandidateLimit (default 50) non-fork repos matching that
// topic (minus the seed's own lineage, see siblingExclusions), embeds
// their READMEs alongside the upstream's README, and returns the max
// cosine of the upstream README to any candidate README.
//
// The implementation is intentionally bounded: one /search call +
// one batched Embed call + N README fetches, regardless of
// candidateCount. The plan calls this the "per-upstream" tier; a
// future "per-fork" tier would re-embed the fork's change digest
// against the same candidate set.
type GHSiblingSearcher struct {
	Client *Client
}

// NewGHSiblingSearcher returns a GHSiblingSearcher backed by c. May be
// nil; the cluster pipeline handles a nil searcher as a no-op.
func NewGHSiblingSearcher(c *Client) *GHSiblingSearcher {
	if c == nil {
		return nil
	}
	return &GHSiblingSearcher{Client: c}
}

// SearchSiblings runs the P2 distant-relation search. Returns (0, 0,
// nil) on no-signal paths (no upstream topic, no candidates, empty
// upstream README) so the caller can always treat absence as "no
// penalty".
func (s *GHSiblingSearcher) SearchSiblings(
	ctx context.Context,
	parent forge.ParentData,
	emb embed.Embedder,
	readmeFetcher cluster.ReadmeFetcher,
	candidateLimit int,
) (float64, int, error) {
	if s == nil || s.Client == nil {
		return 0, 0, nil
	}
	if len(parent.Topics) == 0 {
		return 0, 0, nil
	}
	upstreamOwner, upstreamRepo, ok := splitFullName(parent.FullName)
	if !ok {
		return 0, 0, nil
	}
	upstreamReadme, err := readmeFetcher.FetchReadme(ctx, upstreamOwner, upstreamRepo)
	if err != nil || strings.TrimSpace(upstreamReadme) == "" {
		// No upstream README → no signal. P2 requires both sides.
		return 0, 0, nil
	}
	candidateReadmes, kept, err := s.siblingReadmes(ctx, parent, readmeFetcher, candidateLimit)
	if err != nil || kept == 0 {
		return 0, kept, err
	}
	texts := make([]string, 0, 1+len(candidateReadmes))
	texts = append(texts, upstreamReadme)
	texts = append(texts, candidateReadmes...)
	// One batched embed. The first row is the upstream; rows 1..N
	// are candidates.
	vecs, err := emb.Embed(ctx, texts)
	if err != nil {
		return 0, 0, fmt.Errorf("sibling search embed: %w", err)
	}
	if len(vecs) < 2 || len(vecs[0]) == 0 {
		return 0, 0, nil
	}
	upstreamVec := vecs[0]
	maxSim := 0.0
	for i := 1; i < len(vecs); i++ {
		if len(vecs[i]) != len(upstreamVec) {
			continue
		}
		sim := cosineSimilarity(upstreamVec, vecs[i])
		if math.IsNaN(sim) || math.IsInf(sim, 0) {
			continue
		}
		if sim > maxSim {
			maxSim = sim
		}
	}
	return maxSim, kept, nil
}

func (s *GHSiblingSearcher) siblingReadmes(ctx context.Context, parent forge.ParentData, readmeFetcher cluster.ReadmeFetcher, candidateLimit int) ([]string, int, error) {
	if s == nil || s.Client == nil {
		return nil, 0, nil
	}
	if len(parent.Topics) == 0 {
		return nil, 0, nil
	}
	if candidateLimit <= 0 {
		candidateLimit = 50
	}
	// Pick the first topic — the most specific one — as the search
	// key. The artifact's "topic:PARENT_TOPIC_N" path picks the
	// narrowest; for v1 we use the first in the upstream's order
	// (GitHub returns topics in declared order, so the first is the
	// owner's stated subject).
	searchTopic := parent.Topics[0]
	q := url.QueryEscape(fmt.Sprintf("topic:%s fork:false", searchTopic))
	path := fmt.Sprintf("search/repositories?q=%s&sort=stars&order=desc&per_page=%d", q, candidateLimit)
	var resp siblingSearchResponse
	if err := s.Client.Get(ctx, path, &resp); err != nil {
		return nil, 0, fmt.Errorf("sibling search: %w", err)
	}
	if len(resp.Items) == 0 {
		return nil, 0, nil
	}
	texts := make([]string, 0, len(resp.Items))
	kept := 0
	excluded := siblingExclusions(parent)
	seen := make(map[string]struct{}, len(resp.Items))
	for _, hit := range resp.Items {
		key := strings.ToLower(hit.FullName)
		if _, skip := excluded[key]; skip {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		owner, name, ok := splitFullName(hit.FullName)
		if !ok {
			continue
		}
		readme, ferr := readmeFetcher.FetchReadme(ctx, owner, name)
		if ferr != nil || strings.TrimSpace(readme) == "" {
			continue
		}
		texts = append(texts, readme)
		kept++
	}
	if kept == 0 {
		return nil, 0, nil
	}
	return texts, kept, nil
}

// siblingExclusions returns the lowercase full names a search hit must not
// match: the seed, its network root, and its direct parent. The search is
// keyed on the seed's own first topic, so a non-fork seed always finds
// itself, and a fork's README is a near-copy of its lineage's. Either would
// score ~1.0 as a "distant relation".
func siblingExclusions(parent forge.ParentData) map[string]struct{} {
	out := make(map[string]struct{}, 3)
	for _, name := range []string{parent.FullName, parent.SourceFullPath, parent.DirectParentFullPath} {
		if name != "" {
			out[strings.ToLower(name)] = struct{}{}
		}
	}
	return out
}

func (s *GHSiblingSearcher) SearchForkIntentSiblings(
	ctx context.Context,
	parent forge.ParentData,
	forks []cluster.ForkIntentSiblingInput,
	emb embed.Embedder,
	readmeFetcher cluster.ReadmeFetcher,
	candidateLimit int,
) (map[string]float64, int, error) {
	candidateReadmes, candidateCount, err := s.siblingReadmes(ctx, parent, readmeFetcher, candidateLimit)
	if err != nil || candidateCount == 0 {
		return nil, candidateCount, err
	}
	forkIDs := make([]string, 0, len(forks))
	texts := make([]string, 0, len(forks)+len(candidateReadmes))
	for _, fork := range forks {
		text := forkIntentText(fork.Features)
		if text == "" {
			continue
		}
		forkIDs = append(forkIDs, fork.ForkID)
		texts = append(texts, text)
	}
	if len(forkIDs) == 0 {
		return nil, candidateCount, nil
	}
	texts = append(texts, candidateReadmes...)
	vecs, err := emb.Embed(ctx, texts)
	if err != nil {
		return nil, 0, fmt.Errorf("sibling fork-intent embed: %w", err)
	}
	if len(vecs) != len(texts) {
		return nil, 0, fmt.Errorf("sibling fork-intent embed returned %d vectors for %d texts", len(vecs), len(texts))
	}
	return maxForkIntentSiblingSims(forkIDs, vecs[:len(forkIDs)], vecs[len(forkIDs):]), candidateCount, nil
}

func forkIntentText(features embed.ForkFeatures) string {
	parts := make([]string, 0, 4)
	for _, part := range []string{features.Paths, features.Commits, features.ReadmeDoc, features.DiffChunk} {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n\n")
}

func maxForkIntentSiblingSims(forkIDs []string, forkVecs, candidateVecs []embed.Vector) map[string]float64 {
	out := make(map[string]float64, len(forkIDs))
	for i, forkID := range forkIDs {
		if i >= len(forkVecs) || len(forkVecs[i]) == 0 {
			continue
		}
		maxSim := 0.0
		for _, candidateVec := range candidateVecs {
			if len(candidateVec) != len(forkVecs[i]) {
				continue
			}
			sim := cosineSimilarity(forkVecs[i], candidateVec)
			if math.IsNaN(sim) || math.IsInf(sim, 0) {
				continue
			}
			if sim > maxSim {
				maxSim = sim
			}
		}
		if maxSim > 0 {
			out[forkID] = maxSim
		}
	}
	return out
}

// splitFullName splits "owner/repo" into (owner, repo, true). Returns
// ("", "", false) on bad input.
func splitFullName(s string) (string, string, bool) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// cosineSimilarity assumes L2-normalized vectors and reduces to a dot
// product. The LocalEmbedder and FastEmbedEmbedder both return
// L2-normalized vectors, so the dot product IS the cosine similarity.
// For un-normalized inputs the value is in [-1, 1] but biased by
// magnitude; the P2 score is clamped via ApplySiblingSimilarityToScore.
func cosineSimilarity(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}
