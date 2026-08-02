package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

// Counting, for every fork, how many of its branches carry commits the
// upstream lacks used to be prohibitively expensive: the REST compare endpoint
// answers one branch per request and returns the entire file + commit payload
// to do it. planning/spoon-plan.md:157 accepted that cost explicitly ("up to 5
// per fork ... Budget accordingly"), which is why the existing scan caps at
// five branches and bails below 20% headroom.
//
// GraphQL retires the tradeoff. Ref.compare(headRef:) accepts a cross-repo
// "owner:branch" head, so an arbitrary number of aliased compares resolve
// against one upstream ref in a single query — measured at cost 1 for 200
// aliases. The whole job is therefore two calls for an entire fork network,
// regardless of its size:
//
//	Phase A: list every fork's branches   (aliased repository(...) { refs })
//	Phase B: compare every branch upstream (aliased ref { compare })
//
// Phase A is not optional: forks disagree about their default branch name, so
// guessing "main" 404s on every fork that still uses "master".
const (
	// refsBatchSize caps forks per phase-A query. GraphQL cost is 1 regardless;
	// this bounds the document size and the blast radius of one failed query.
	refsBatchSize = 50

	// compareBatchSize caps aliased compares per phase-B query. 200 was
	// measured at cost 1; this leaves headroom under GitHub's query-complexity
	// ceiling without needing to probe where exactly that ceiling sits.
	compareBatchSize = 150

	// branchPageSize is the per-fork ref ceiling. Forks with more branches than
	// this are reported via BranchCounts.Truncated rather than silently
	// undercounted.
	branchPageSize = 100
)

// ForkTarget identifies one fork for branch counting. ID is echoed back as the
// result key, so the caller controls correlation.
type ForkTarget struct {
	ID    string
	Owner string
	Name  string
}

// BranchCounts is the result of a divergent-branch sweep.
type BranchCounts struct {
	// Divergent maps fork ID to the number of its branches holding at least
	// one commit the upstream does not have. A fork absent from the map was
	// never resolved (deleted, private, or its query failed) — that is
	// distinct from a present zero, which means "checked, nothing divergent".
	Divergent map[string]int

	// Fingerprint maps fork ID to an identity built from the tip OIDs of its
	// divergent branches — the branches with aheadBy > 0. Two forks sharing a
	// fingerprint carry byte-identical work.
	//
	// Only divergent branches contribute. A fork's default branch is usually
	// just its own sync state (observed in qvr/nonraid: three forks with
	// byte-identical work branches but three different "main" tips), so
	// including every branch would make identical forks look distinct.
	//
	// Empty for a fork with no divergent branch: an inert mirror has no work to
	// be identical about, and grouping them all together would be meaningless.
	Fingerprint map[string]string

	// Truncated lists forks with more than branchPageSize branches, whose
	// counts are lower bounds.
	Truncated []string
}

// gqlAliasBatch decodes a GraphQL document whose top level mixes dynamically
// named aliases with the fixed rateLimit block.
type gqlAliasBatch struct {
	Aliases map[string]json.RawMessage
	RL      gqlRateLimit
}

func (r *gqlAliasBatch) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	r.Aliases = make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		if k == "rateLimit" {
			if err := json.Unmarshal(v, &r.RL); err != nil {
				return err
			}
			continue
		}
		r.Aliases[k] = v
	}
	return nil
}

func (r *gqlAliasBatch) graphqlRateLimit() *gqlRateLimit { return &r.RL }

type refsNode struct {
	Refs struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Name   string `json:"name"`
			Target struct {
				OID string `json:"oid"`
			} `json:"target"`
		} `json:"nodes"`
	} `json:"refs"`
}

type compareBatch struct {
	Repository struct {
		Ref map[string]json.RawMessage `json:"ref"`
	} `json:"repository"`
	RL gqlRateLimit `json:"rateLimit"`
}

func (r *compareBatch) graphqlRateLimit() *gqlRateLimit { return &r.RL }

// isGraphQLResponseError reports whether err is a GraphQL-level error, i.e. one
// carried in the "errors" array of an otherwise successful HTTP 200 response.
// go-gh decodes "data" into the caller's struct before returning it, so partial
// results are already available.
func isGraphQLResponseError(err error) bool {
	var gqlErr *ghAPI.GraphQLError
	return errors.As(err, &gqlErr)
}

// isPartialLookupError reports whether every GraphQL error is a NOT_FOUND,
// which is the expected shape when one fork or branch disappeared between
// listing and comparison. Any other error class means the query itself is
// suspect and the batch must not be trusted.
func isPartialLookupError(err error) bool {
	var gqlErr *ghAPI.GraphQLError
	if !errors.As(err, &gqlErr) || len(gqlErr.Errors) == 0 {
		return false
	}
	for _, item := range gqlErr.Errors {
		if item.Type != "NOT_FOUND" {
			return false
		}
	}
	return true
}

// gqlString renders s as a GraphQL string literal. GraphQL string syntax is
// JSON's, so this both quotes and escapes — necessary because branch names and
// owner logins reach the query as literals, not bound variables (aliases and
// arguments in a dynamically built document cannot be parameterised).
func gqlString(s string) string { return strconv.Quote(s) }

// FetchDivergentBranchCounts counts, per fork, the branches carrying commits
// absent from baseOwner/baseRepo@baseBranch.
//
// A branch counts when compare reports aheadBy > 0. That is the commit-graph
// reading: a branch whose work was squash- or rebase-merged upstream still
// counts, because those merges rewrite SHAs so the original commits genuinely
// are not in the upstream graph. Distinguishing that case needs the per-commit
// PR probe in upstreamed.go, which costs one REST call per branch and would
// undo the entire point of this path.
func (c *Client) FetchDivergentBranchCounts(
	ctx context.Context,
	baseOwner, baseRepo, baseBranch string,
	forks []ForkTarget,
) (*BranchCounts, error) {
	if baseOwner == "" || baseRepo == "" {
		return nil, fmt.Errorf("divergent branches: upstream not resolved (Parent not called)")
	}
	out := &BranchCounts{
		Divergent:   make(map[string]int, len(forks)),
		Fingerprint: make(map[string]string, len(forks)),
	}
	divergentOIDs := make(map[string][]string, len(forks))
	if len(forks) == 0 {
		return out, nil
	}
	if baseBranch == "" {
		baseBranch = "HEAD"
	}

	type branchRef struct {
		forkID string
		branch string
		oid    string
	}
	var pairs []branchRef

	// Phase A — enumerate branches.
	for _, chunk := range slidingChunks(len(forks), refsBatchSize) {
		batch := forks[chunk.lo:chunk.hi]
		var q strings.Builder
		q.WriteString("query {\n")
		for i, f := range batch {
			fmt.Fprintf(&q, "  f%d: repository(owner: %s, name: %s) { refs(refPrefix: \"refs/heads/\", first: %d) { totalCount nodes { name target { oid } } } }\n",
				i, gqlString(f.Owner), gqlString(f.Name), branchPageSize)
		}
		q.WriteString("  rateLimit { limit remaining used resetAt cost }\n}")

		var resp gqlAliasBatch
		if err := c.doGraphQLWithRetry(ctx, q.String(), nil, &resp); err != nil {
			if !isPartialLookupError(err) {
				return nil, fmt.Errorf("list fork branches: %w", err)
			}
			// Partial NOT_FOUND: the surviving aliases decoded fine.
		}

		for i, f := range batch {
			raw, ok := resp.Aliases[fmt.Sprintf("f%d", i)]
			if !ok || string(raw) == "null" {
				continue // fork vanished or is inaccessible; leave it unknown
			}
			var node refsNode
			if err := json.Unmarshal(raw, &node); err != nil {
				continue
			}
			if node.Refs.TotalCount > branchPageSize {
				out.Truncated = append(out.Truncated, f.ID)
			}
			// Seed a zero so a fork that resolved but diverges nowhere is
			// reported as a real zero rather than as unknown.
			out.Divergent[f.ID] = 0
			for _, n := range node.Refs.Nodes {
				pairs = append(pairs, branchRef{forkID: f.ID, branch: n.Name, oid: n.Target.OID})
			}
		}
	}

	if len(pairs) == 0 {
		return out, nil
	}

	// Phase B — compare each branch against the upstream ref.
	qualified := baseBranch
	if !strings.HasPrefix(qualified, "refs/") {
		qualified = "refs/heads/" + qualified
	}

	for _, chunk := range slidingChunks(len(pairs), compareBatchSize) {
		batch := pairs[chunk.lo:chunk.hi]
		var q strings.Builder
		q.WriteString("query {\n")
		fmt.Fprintf(&q, "  repository(owner: %s, name: %s) {\n", gqlString(baseOwner), gqlString(baseRepo))
		fmt.Fprintf(&q, "    ref(qualifiedName: %s) {\n", gqlString(qualified))
		for i, p := range batch {
			owner := p.forkID
			if idx := strings.IndexByte(owner, '/'); idx > 0 {
				owner = owner[:idx]
			}
			fmt.Fprintf(&q, "      c%d: compare(headRef: %s) { aheadBy }\n",
				i, gqlString(owner+":"+p.branch))
		}
		q.WriteString("    }\n  }\n  rateLimit { limit remaining used resetAt cost }\n}")

		var resp compareBatch
		if err := c.doGraphQLWithRetry(ctx, q.String(), nil, &resp); err != nil {
			if !isPartialLookupError(err) {
				return nil, fmt.Errorf("compare fork branches: %w", err)
			}
		}

		for i, p := range batch {
			raw, ok := resp.Repository.Ref[fmt.Sprintf("c%d", i)]
			if !ok || string(raw) == "null" {
				continue
			}
			var cmp struct {
				AheadBy int `json:"aheadBy"`
			}
			if err := json.Unmarshal(raw, &cmp); err != nil {
				continue
			}
			if cmp.AheadBy > 0 {
				out.Divergent[p.forkID]++
				if p.oid != "" {
					divergentOIDs[p.forkID] = append(divergentOIDs[p.forkID], p.oid)
				}
			}
		}
	}

	for forkID, oids := range divergentOIDs {
		out.Fingerprint[forkID] = BranchFingerprint(oids)
	}

	return out, nil
}

// BranchFingerprint reduces a fork's divergent-branch tip OIDs to one identity
// string. Order-independent, so two forks whose branches were listed in
// different orders still match.
//
// Exported so the same construction is reachable from tests and from callers
// that obtain OIDs another way.
func BranchFingerprint(oids []string) string {
	if len(oids) == 0 {
		return ""
	}
	uniq := make([]string, 0, len(oids))
	seen := make(map[string]struct{}, len(oids))
	for _, o := range oids {
		if o == "" {
			continue
		}
		if _, dup := seen[o]; dup {
			continue
		}
		seen[o] = struct{}{}
		uniq = append(uniq, o)
	}
	if len(uniq) == 0 {
		return ""
	}
	sort.Strings(uniq)
	sum := sha256.Sum256([]byte(strings.Join(uniq, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

type chunkRange struct{ lo, hi int }

// slidingChunks yields [lo,hi) ranges of at most size over n items.
func slidingChunks(n, size int) []chunkRange {
	if size <= 0 || n <= 0 {
		return nil
	}
	out := make([]chunkRange, 0, (n+size-1)/size)
	for lo := 0; lo < n; lo += size {
		hi := min(lo+size, n)
		out = append(out, chunkRange{lo: lo, hi: hi})
	}
	return out
}
