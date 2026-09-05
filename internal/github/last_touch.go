package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// pathHistoryBatchSize caps paths per phase-A (history) query. GraphQL cost
// is 1 regardless of alias count -- this just bounds document size, mirroring
// compareBatchSize's reasoning in divergent_branches.go.
const pathHistoryBatchSize = 150

// pathCompareBatchSize caps distinct last-touch SHAs per phase-B (compare)
// query, same reasoning.
const pathCompareBatchSize = 150

// pathHistoryBatch decodes phase A: for each requested path pN, the most
// recent commit on the upstream ref that touched it.
type pathHistoryBatch struct {
	Repository struct {
		Ref *struct {
			// Target holds one raw entry per pN alias. It is a map (not a
			// fixed struct) because the alias set is built dynamically per
			// call; a path with no history on this branch is simply absent
			// or null here, both handled the same way below.
			Target map[string]json.RawMessage `json:"target"`
		} `json:"ref"`
	} `json:"repository"`
	RL gqlRateLimit `json:"rateLimit"`
}

func (r *pathHistoryBatch) graphqlRateLimit() *gqlRateLimit { return &r.RL }

// pathCompareBatch decodes phase B: for each distinct last-touch SHA sN, how
// many commits landed on the upstream ref after it (behindBy).
type pathCompareBatch struct {
	Repository struct {
		Ref map[string]json.RawMessage `json:"ref"`
	} `json:"repository"`
	RL gqlRateLimit `json:"rateLimit"`
}

func (r *pathCompareBatch) graphqlRateLimit() *gqlRateLimit { return &r.RL }

// FetchPathLastTouch resolves, for each of paths, the most recent commit on
// branch that touched it and how many commits landed on branch afterward.
// Task 7 uses this against a fork's own last-touch commit (ForkLastTouch,
// via GitHub's tree-commit-info endpoint) to skip a REST compare when they
// agree: proof the fork never itself changed the path.
//
// Two GraphQL round trips regardless of how many paths are requested (more
// if the path or SHA count exceeds the per-query batch caps). They cannot be
// folded into one: the second query compares against SHAs that are only
// known once the first query's response returns. Each query aliases one
// history/compare per path or SHA so its own GraphQL cost stays flat (cost 1
// for up to 150 aliases, same as the batches in divergent_branches.go) --
// the "one query" framing refers to that flat per-round-trip cost, not to
// folding both phases into a single request.
//
// A path absent from the returned map has no history on branch at all (never
// touched, or the branch/repo did not resolve) -- distinct from a path
// present with CommitsSince == 0 (touched, and nothing has landed since).
func (c *Client) FetchPathLastTouch(ctx context.Context, owner, repo, branch string, paths []string) (map[string]forge.PathLastTouch, error) {
	if owner == "" || repo == "" || branch == "" {
		return nil, fmt.Errorf("path last touch: upstream not resolved (Parent not called)")
	}
	out := make(map[string]forge.PathLastTouch, len(paths))
	if len(paths) == 0 {
		return out, nil
	}
	if !c.HasGraphQL() {
		// No working GraphQL backend: nothing can be resolved. Returning an
		// empty map (rather than an error) lets the caller treat every path
		// as "unknown, fall back to REST" the same way it would treat an
		// individually-unresolved path.
		return out, nil
	}

	qualified := branch
	if !strings.HasPrefix(qualified, "refs/") {
		qualified = "refs/heads/" + qualified
	}

	type touch struct {
		path          string
		sha           string
		committedDate time.Time
	}
	var touches []touch

	// Phase A -- last-touch commit per path.
	for _, chunk := range slidingChunks(len(paths), pathHistoryBatchSize) {
		batch := paths[chunk.lo:chunk.hi]
		var q strings.Builder
		fmt.Fprintf(&q, "query {\n  repository(owner: %s, name: %s) {\n    ref(qualifiedName: %s) {\n      target {\n        ... on Commit {\n",
			gqlString(owner), gqlString(repo), gqlString(qualified))
		for i, p := range batch {
			fmt.Fprintf(&q, "          p%d: history(first: 1, path: %s) { nodes { oid committedDate } }\n", i, gqlString(p))
		}
		q.WriteString("        }\n      }\n    }\n  }\n  rateLimit { limit remaining used resetAt cost }\n}")

		var resp pathHistoryBatch
		if err := c.doGraphQLWithRetry(ctx, q.String(), nil, &resp); err != nil {
			if !isPartialLookupError(err) {
				return nil, fmt.Errorf("path last touch: %w", err)
			}
			// Partial NOT_FOUND: the surviving aliases decoded fine.
		}
		if resp.Repository.Ref == nil {
			// Same reasoning as FetchDivergentBranchCounts: a null ref means
			// the branch itself did not resolve (renamed, deleted, or an
			// unqualified name), not that every path individually came back
			// empty. Fabricating "no history anywhere" here would look
			// identical to a healthy answer where nothing has ever touched
			// any of these paths, so fail loudly instead.
			return nil, fmt.Errorf("path last touch: upstream ref %q did not resolve", qualified)
		}
		for i, p := range batch {
			raw, ok := resp.Repository.Ref.Target[fmt.Sprintf("p%d", i)]
			if !ok || string(raw) == "null" {
				continue
			}
			var hist struct {
				Nodes []struct {
					OID           string    `json:"oid"`
					CommittedDate time.Time `json:"committedDate"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal(raw, &hist); err != nil {
				continue
			}
			if len(hist.Nodes) == 0 {
				continue // path has no history on this branch
			}
			touches = append(touches, touch{path: p, sha: hist.Nodes[0].OID, committedDate: hist.Nodes[0].CommittedDate})
		}
	}

	if len(touches) == 0 {
		return out, nil
	}

	// Phase B -- commits landed on branch after each distinct last-touch SHA.
	shaSeen := make(map[string]bool, len(touches))
	var shas []string
	for _, t := range touches {
		if !shaSeen[t.sha] {
			shaSeen[t.sha] = true
			shas = append(shas, t.sha)
		}
	}

	commitsSince := make(map[string]int, len(shas))
	for _, chunk := range slidingChunks(len(shas), pathCompareBatchSize) {
		batch := shas[chunk.lo:chunk.hi]
		var q strings.Builder
		fmt.Fprintf(&q, "query {\n  repository(owner: %s, name: %s) {\n    ref(qualifiedName: %s) {\n",
			gqlString(owner), gqlString(repo), gqlString(qualified))
		for i, sha := range batch {
			// The upstream ref is the base and sha (an ancestor of it) is
			// headRef, so behindBy is how far the base has moved past sha --
			// i.e. commits landed since the path's last touch. Verified live
			// (task brief): compare(headRef: "fa44839f...") -> behindBy 5.
			fmt.Fprintf(&q, "      s%d: compare(headRef: %s) { behindBy }\n", i, gqlString(sha))
		}
		q.WriteString("    }\n  }\n  rateLimit { limit remaining used resetAt cost }\n}")

		var resp pathCompareBatch
		if err := c.doGraphQLWithRetry(ctx, q.String(), nil, &resp); err != nil {
			if !isPartialLookupError(err) {
				return nil, fmt.Errorf("path last touch compare: %w", err)
			}
		}
		if resp.Repository.Ref == nil {
			return nil, fmt.Errorf("path last touch compare: upstream ref %q did not resolve", qualified)
		}
		for i, sha := range batch {
			raw, ok := resp.Repository.Ref[fmt.Sprintf("s%d", i)]
			if !ok || string(raw) == "null" {
				continue
			}
			var cmp struct {
				BehindBy int `json:"behindBy"`
			}
			if err := json.Unmarshal(raw, &cmp); err != nil {
				continue
			}
			commitsSince[sha] = cmp.BehindBy
		}
	}

	for _, t := range touches {
		since, ok := commitsSince[t.sha]
		if !ok {
			// The compare didn't resolve for this SHA (partial NOT_FOUND, or
			// an unmarshal failure). Omit rather than report a fabricated
			// zero -- the caller must treat this path as unresolved.
			continue
		}
		out[t.path] = forge.PathLastTouch{SHA: t.sha, CommittedAt: t.committedDate, CommitsSince: since}
	}

	return out, nil
}
