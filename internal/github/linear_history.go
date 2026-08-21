package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
)

// Linear-history: per fork, classify whether the fork's default branch tip is
// reachable from the upstream tip without crossing a merge commit. Strategy:
// for each fork, GraphQL-fetch up to 100 commits on the fork's default branch
// ahead of the upstream merge base; the per-commit parents.totalCount vector
// is the raw signal. Callers derive MergeCommits and LinearHistory themselves
// (see internal/tui/branch_divergence.go) so the derivation lives in one
// place rather than being duplicated per provider.
//
// Hard cap at 100 commits per fork. Forks with deeper histories are reported
// in Truncated so the caller renders a lower-bound glyph. The raw 100-element
// vector is still persisted via the relational merge_commit_history table so
// downstream tools have it.

const (
	// linearHistoryBatchSize caps forks per compare query. The query nests one
	// compare under the upstream ref, so the alias count is also the
	// ref-query fan-out and should match the divergence sweep's batch ceiling.
	linearHistoryBatchSize = 150

	// linearHistoryCommitLimit caps commits per fork. Forks whose ahead-of-
	// upstream history exceeds this are recorded as truncated and the
	// derived MergeCommits is a lower bound.
	linearHistoryCommitLimit = 100
)

// LinearHistories is the result of a linear-history sweep. Forks absent from
// Histories were never resolved (deleted, private, or their query failed) —
// that is distinct from a present empty vector, which means "ahead of
// upstream but no commits" (effectively linear).
type LinearHistories struct {
	Histories map[string][]int
	Truncated []string
}

// FetchMergeCommitHistory returns, per fork, the parents.totalCount vector
// for every commit on the fork's default branch ahead of the upstream merge
// base, capped at linearHistoryCommitLimit per fork.
//
// Compares against the upstream baseline (set via SetCompareBaseline or the
// live Parent() path). Returns an error when the baseline is unset rather
// than silently fabricating zero-length histories.
func (c *Client) FetchMergeCommitHistory(
	ctx context.Context,
	baseOwner, baseRepo, baseBranch string,
	forks []forge.T1Data,
) (*LinearHistories, error) {
	if baseOwner == "" || baseRepo == "" || baseBranch == "" {
		return nil, fmt.Errorf("linear history: upstream not resolved (Parent not called)")
	}
	out := &LinearHistories{
		Histories: make(map[string][]int, len(forks)),
	}
	if len(forks) == 0 {
		return out, nil
	}

	qualified := baseBranch
	if !strings.HasPrefix(qualified, "refs/") {
		qualified = "refs/heads/" + qualified
	}

	type attempt struct {
		forkID       string
		owner        string
		branch       string
		branchKnown  bool
	}
	attempts := make([]attempt, 0, len(forks))
	for _, f := range forks {
		branch := f.DefaultBranch
		attempts = append(attempts, attempt{
			forkID:      f.ID,
			owner:       f.Owner,
			branch:      branch,
			branchKnown: branch != "",
		})
	}

	for _, chunk := range slidingChunks(len(attempts), linearHistoryBatchSize) {
		batch := attempts[chunk.lo:chunk.hi]
		var q strings.Builder
		q.WriteString("query {\n")
		fmt.Fprintf(&q, "  repository(owner: %s, name: %s) {\n", gqlString(baseOwner), gqlString(baseRepo))
		fmt.Fprintf(&q, "    ref(qualifiedName: %s) {\n", gqlString(qualified))
		for i, a := range batch {
			if !a.branchKnown {
				fmt.Fprintf(&q, "      c%d: compare(headRef: %s) { commits(first: %d) { totalCount nodes { parents { totalCount } } } }\n",
					i, gqlString(a.owner+":HEAD"), linearHistoryCommitLimit)
				continue
			}
			fmt.Fprintf(&q, "      c%d: compare(headRef: %s) { commits(first: %d) { totalCount nodes { parents { totalCount } } } }\n",
				i, gqlString(a.owner+":"+a.branch), linearHistoryCommitLimit)
		}
		q.WriteString("    }\n  }\n  rateLimit { limit remaining used resetAt cost }\n}")

		var resp compareBatch
		if err := c.doGraphQLWithRetry(ctx, q.String(), nil, &resp); err != nil {
			if !isPartialLookupError(err) {
				return nil, fmt.Errorf("compare fork histories: %w", err)
			}
		}

		if resp.Repository.Ref == nil {
			// Upstream ref failed to resolve. A single alias cannot fabricate
			// data, so refuse the whole batch rather than seed a confident
			// empty for every fork.
			return nil, fmt.Errorf("linear history: upstream ref %q did not resolve", qualified)
		}

		for i, a := range batch {
			raw, ok := resp.Repository.Ref[fmt.Sprintf("c%d", i)]
			if !ok || string(raw) == "null" {
				continue
			}
			var cmp struct {
				Commits struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						Parents struct {
							TotalCount int `json:"totalCount"`
						} `json:"parents"`
					} `json:"nodes"`
				} `json:"commits"`
			}
			if err := json.Unmarshal(raw, &cmp); err != nil {
				continue
			}
			hist := make([]int, 0, len(cmp.Commits.Nodes))
			for _, n := range cmp.Commits.Nodes {
				hist = append(hist, n.Parents.TotalCount)
			}
			out.Histories[a.forkID] = hist
			// Truncate whenever the returned page is full: a fork whose ahead
			// history is exactly 100 commits also gets cut off, because the
			// caller cannot tell from the in-memory slice whether there are
			// more commits past it. TotalCount is the API's reported total;
			// capping on >= limit (not >) ensures a 100-commit history is
			// marked truncated even though we asked for exactly 100.
			if cmp.Commits.TotalCount >= linearHistoryCommitLimit || len(cmp.Commits.Nodes) >= linearHistoryCommitLimit {
				out.Truncated = append(out.Truncated, a.forkID)
			}
		}
	}

	return out, nil
}