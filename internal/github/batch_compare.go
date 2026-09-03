package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// Batch divergence resolves ahead/behind and upstreamed-PR status for an
// entire fork network's branches in two GraphQL passes, replacing the
// per-fork REST branch scan (ScanBranches + tipUpstreamed in branches.go)
// that spends at least one REST compare per fork.
//
// Phase A asks every candidate branch -- each target's default branch plus
// its listed side branches -- how far ahead/behind it is of the upstream
// baseline, aliased batchCompareSize per query. Phase B then asks, only for
// branches Phase A found ahead > 0, for the tip commit and any merged
// upstream PR heading it: the two facts SelectDivergentBranch needs that
// {aheadBy behindBy} alone does not carry. Restricting Phase B to ahead
// branches keeps its nested commits+associatedPullRequests document small --
// that shape was measured returning HTTP 502 at 75+ aliases, well below
// Phase A's flat {aheadBy behindBy} shape (safe past 150).
const (
	// batchCompareSize caps branch pairs per Phase A query. Matches
	// divergent_branches.go's compareBatchSize: the same flat
	// {aheadBy behindBy} shape was measured safe at 150 aliases.
	batchCompareSize = 150

	// batchTipSize caps branches per Phase B query. The nested
	// commits(last:1){ ... associatedPullRequests ... } shape was measured
	// safe at 50 aliases and returning HTTP 502 at 75+.
	batchTipSize = 50
)

// BatchBranch is one branch a BatchTarget offers for divergence resolution,
// carrying the listing's tip so a branch that turns out zero-ahead (Phase B
// never runs for it) still has a usable TipSHA/TipCommittedAt.
type BatchBranch struct {
	Name        string
	TipSHA      string
	CommittedAt time.Time
}

// BatchTarget is one fork to resolve divergence for: its default branch plus
// the side branches (the listing's top-5-by-recency) to check alongside it.
type BatchTarget struct {
	ID                 string
	Owner              string
	Name               string
	DefaultBranch      string
	DefaultTipSHA      string
	DefaultCommittedAt time.Time
	Sides              []BatchBranch
}

// batchAttempt is one branch's Phase A/B query slot. Attempts are built once
// up front (one per target's default branch and each of its sides) and
// mutated in place as Phase A and Phase B resolve, so the final fold step
// can walk them by target without re-deriving which branch belongs to which
// fork.
type batchAttempt struct {
	targetIdx int
	isDefault bool
	owner     string
	branch    string

	// seedTipSHA/seedCommittedAt are the listing-known tip, used whenever
	// Phase B does not run or does not resolve for this branch -- correct
	// both for a genuinely zero-ahead branch and for the fail-open case
	// where Phase B could not be reached for an ahead branch.
	seedTipSHA      string
	seedCommittedAt time.Time

	resolved bool // Phase A alias present and decoded (branch still exists)
	aheadBy  int
	behindBy int
}

// tipResult is Phase B's answer for one ahead branch: its tip commit and any
// merged upstream PR heading it.
type tipResult struct {
	sha          string
	committedAt  time.Time
	upstreamedPR int
}

// FetchBatchDivergence resolves per-branch ahead/behind, and for ahead
// branches the tip commit and merged-upstream-PR status, for every target's
// default and side branches against baseOwner/baseRepo@baseBranch.
//
// A target every one of whose branches failed to resolve in Phase A (fork
// deleted, renamed away, or made private between listing and this call)
// still gets an entry in the returned map, with Resolved == false -- see
// forge.ForkDivergence. A target absent from targets is simply absent from
// the result; forge.BatchCompareProvider's doc covers how callers must
// handle both cases identically (fall back to the REST path).
func (c *Client) FetchBatchDivergence(
	ctx context.Context,
	baseOwner, baseRepo, baseBranch string,
	targets []BatchTarget,
) (map[string]forge.ForkDivergence, forge.BatchStats, error) {
	// baseBranch must be a real branch name -- see divergent_branches.go's
	// FetchDivergentBranchCounts for why the REST "HEAD" shorthand convention
	// does not translate to GraphQL's ref(qualifiedName:).
	if baseOwner == "" || baseRepo == "" || baseBranch == "" {
		return nil, forge.BatchStats{}, fmt.Errorf("batch divergence: upstream not resolved (Parent not called)")
	}
	out := make(map[string]forge.ForkDivergence, len(targets))
	var stats forge.BatchStats
	if len(targets) == 0 {
		return out, stats, nil
	}

	qualified := baseBranch
	if !strings.HasPrefix(qualified, "refs/") {
		qualified = "refs/heads/" + qualified
	}

	attempts := make([]*batchAttempt, 0, len(targets)*2)
	for ti, t := range targets {
		attempts = append(attempts, &batchAttempt{
			targetIdx:       ti,
			isDefault:       true,
			owner:           t.Owner,
			branch:          t.DefaultBranch,
			seedTipSHA:      t.DefaultTipSHA,
			seedCommittedAt: t.DefaultCommittedAt,
		})
		for _, s := range t.Sides {
			attempts = append(attempts, &batchAttempt{
				targetIdx:       ti,
				isDefault:       false,
				owner:           t.Owner,
				branch:          s.Name,
				seedTipSHA:      s.TipSHA,
				seedCommittedAt: s.CommittedAt,
			})
		}
	}

	if err := c.fetchBatchAheadBehind(ctx, baseOwner, baseRepo, qualified, attempts, &stats); err != nil {
		return nil, stats, err
	}

	var ahead []*batchAttempt
	for _, a := range attempts {
		if a.resolved && a.aheadBy > 0 {
			ahead = append(ahead, a)
		}
	}
	tips := c.fetchBatchTips(ctx, baseOwner, baseRepo, qualified, ahead, &stats)

	byTarget := make(map[int][]*batchAttempt, len(targets))
	for _, a := range attempts {
		byTarget[a.targetIdx] = append(byTarget[a.targetIdx], a)
	}
	for ti, t := range targets {
		tAttempts := byTarget[ti]
		anyResolved := false
		for _, a := range tAttempts {
			if a.resolved {
				anyResolved = true
				break
			}
		}
		if !anyResolved {
			// Every alias for this fork came back null: it vanished (or
			// was renamed/privated) between listing and this call. The
			// caller must fall back to the REST path for it, same as a
			// target absent from the map entirely.
			out[t.ID] = forge.ForkDivergence{Resolved: false}
			continue
		}

		fd := forge.ForkDivergence{Resolved: true}
		for _, a := range tAttempts {
			bd := branchDivergenceFor(a, tips[a])
			if a.isDefault {
				fd.Default = bd
				continue
			}
			if a.resolved {
				// An unresolved side branch (deleted between listing and
				// Phase A) carries nothing useful -- zero ahead/behind, no
				// real tip -- and would masquerade as "checked, nothing
				// diverges" in SelectDivergentBranch if included; drop it
				// instead of fabricating a false all-clear.
				fd.Sides = append(fd.Sides, bd)
			}
		}
		out[t.ID] = fd
	}

	return out, stats, nil
}

// fetchBatchAheadBehind runs Phase A: aliased compare(headRef:){aheadBy
// behindBy} for every attempt, batchCompareSize aliases per query, mutating
// each attempt's resolved/aheadBy/behindBy in place.
//
// This is the core divergence signal, so it fails loudly exactly as
// divergent_branches.go's FetchDivergentBranchCounts does: a non-partial
// GraphQL error or an unresolved upstream ref aborts the whole call rather
// than silently reporting a batch of fabricated zeros.
func (c *Client) fetchBatchAheadBehind(
	ctx context.Context,
	baseOwner, baseRepo, qualified string,
	attempts []*batchAttempt,
	stats *forge.BatchStats,
) error {
	for _, chunk := range slidingChunks(len(attempts), batchCompareSize) {
		batch := attempts[chunk.lo:chunk.hi]
		var q strings.Builder
		q.WriteString("query {\n")
		fmt.Fprintf(&q, "  repository(owner: %s, name: %s) {\n", gqlString(baseOwner), gqlString(baseRepo))
		fmt.Fprintf(&q, "    ref(qualifiedName: %s) {\n", gqlString(qualified))
		for i, a := range batch {
			fmt.Fprintf(&q, "      c%d: compare(headRef: %s) { aheadBy behindBy }\n",
				i, gqlString(a.owner+":"+a.branch))
		}
		q.WriteString("    }\n  }\n  rateLimit { limit remaining used resetAt cost }\n}")

		var resp compareBatch
		err := c.doGraphQLWithRetry(ctx, q.String(), nil, &resp)
		stats.Queries++
		stats.Cost += resp.RL.Cost
		if err != nil {
			if !isPartialLookupError(err) {
				return fmt.Errorf("batch divergence: compare branches: %w", err)
			}
			// Partial NOT_FOUND: the surviving aliases decoded fine.
		}

		// A resolved ref always yields a non-nil map -- see
		// divergent_branches.go:303-315 for why a nil map here means ref
		// itself failed to resolve (a renamed/deleted base branch), not
		// that every child alias individually came back empty. Continuing
		// past that would fabricate a zero for every branch in the batch.
		if resp.Repository.Ref == nil {
			return fmt.Errorf("batch divergence: upstream ref %q did not resolve", qualified)
		}

		for i, a := range batch {
			raw, ok := resp.Repository.Ref[fmt.Sprintf("c%d", i)]
			if !ok || string(raw) == "null" {
				continue // branch vanished or fork is inaccessible; leave unresolved
			}
			var cmp struct {
				AheadBy  int `json:"aheadBy"`
				BehindBy int `json:"behindBy"`
			}
			if err := json.Unmarshal(raw, &cmp); err != nil {
				continue
			}
			a.resolved = true
			a.aheadBy = cmp.AheadBy
			a.behindBy = cmp.BehindBy
		}
	}
	return nil
}

// fetchBatchTips runs Phase B: aliased compare(headRef:){ commits(last:1) {
// ... associatedPullRequests ... } }, batchTipSize aliases per query, for
// the given (already Phase-A-ahead) attempts only.
//
// Unlike Phase A, a Phase B failure is never fatal to the call: it is
// enrichment on top of divergence data Phase A already resolved, not the
// divergence data itself. A chunk that errors (non-partial GraphQL error, or
// an upstream ref that somehow failed to resolve here after resolving in
// Phase A) is logged and skipped; its attempts are simply absent from the
// returned map, so the caller falls back to their listing-seeded tip with
// UpstreamedPR == 0 -- their work reads as genuine, mirroring
// tipUpstreamed's fail-open at branches.go:47-52.
func (c *Client) fetchBatchTips(
	ctx context.Context,
	baseOwner, baseRepo, qualified string,
	ahead []*batchAttempt,
	stats *forge.BatchStats,
) map[*batchAttempt]tipResult {
	upstream := baseOwner + "/" + baseRepo
	tips := make(map[*batchAttempt]tipResult, len(ahead))

	for _, chunk := range slidingChunks(len(ahead), batchTipSize) {
		batch := ahead[chunk.lo:chunk.hi]
		var q strings.Builder
		q.WriteString("query {\n")
		fmt.Fprintf(&q, "  repository(owner: %s, name: %s) {\n", gqlString(baseOwner), gqlString(baseRepo))
		fmt.Fprintf(&q, "    ref(qualifiedName: %s) {\n", gqlString(qualified))
		for i, a := range batch {
			fmt.Fprintf(&q, "      c%d: compare(headRef: %s) { commits(last: 1) { nodes { oid committedDate associatedPullRequests(first: 5) { nodes { number merged baseRepository { nameWithOwner } } } } } }\n",
				i, gqlString(a.owner+":"+a.branch))
		}
		q.WriteString("    }\n  }\n  rateLimit { limit remaining used resetAt cost }\n}")

		var resp compareBatch
		err := c.doGraphQLWithRetry(ctx, q.String(), nil, &resp)
		stats.Queries++
		stats.Cost += resp.RL.Cost
		if err != nil {
			if !isPartialLookupError(err) {
				slog.Debug("batch divergence: phase B query failed, branches keep listing tip",
					"err", err, "branches", len(batch))
				continue
			}
			// Partial NOT_FOUND: decode what did resolve below.
		}
		if resp.Repository.Ref == nil {
			slog.Debug("batch divergence: phase B upstream ref unresolved, branches keep listing tip",
				"branches", len(batch))
			continue
		}

		for i, a := range batch {
			raw, ok := resp.Repository.Ref[fmt.Sprintf("c%d", i)]
			if !ok || string(raw) == "null" {
				continue
			}
			var cmp struct {
				Commits struct {
					Nodes []struct {
						OID                    string `json:"oid"`
						CommittedDate          string `json:"committedDate"`
						AssociatedPullRequests struct {
							Nodes []struct {
								Number         int  `json:"number"`
								Merged         bool `json:"merged"`
								BaseRepository struct {
									NameWithOwner string `json:"nameWithOwner"`
								} `json:"baseRepository"`
							} `json:"nodes"`
						} `json:"associatedPullRequests"`
					} `json:"nodes"`
				} `json:"commits"`
			}
			if err := json.Unmarshal(raw, &cmp); err != nil {
				continue
			}
			if len(cmp.Commits.Nodes) == 0 {
				continue
			}
			tip := cmp.Commits.Nodes[0]
			res := tipResult{sha: tip.OID}
			if t, perr := time.Parse(time.RFC3339, tip.CommittedDate); perr == nil {
				res.committedAt = t
			}
			// First PR that is merged and whose base is the upstream repo,
			// mirroring upstreamed.go's CheckUpstreamed (EqualFold: GitHub
			// repo full names are case-insensitive).
			for _, pr := range tip.AssociatedPullRequests.Nodes {
				if pr.Merged && strings.EqualFold(pr.BaseRepository.NameWithOwner, upstream) {
					res.upstreamedPR = pr.Number
					break
				}
			}
			tips[a] = res
		}
	}
	return tips
}

// branchDivergenceFor builds the BranchDivergence for one attempt. Phase B's
// tip, when present, wins over the listing-seeded tip; otherwise the seed
// stands -- correct for a zero-ahead branch (Phase B never runs for it) and
// for the fail-open case where Phase B could not be reached for a
// genuinely ahead branch.
func branchDivergenceFor(a *batchAttempt, tip tipResult) forge.BranchDivergence {
	bd := forge.BranchDivergence{
		Name:           a.branch,
		TipSHA:         a.seedTipSHA,
		TipCommittedAt: a.seedCommittedAt,
		AheadBy:        a.aheadBy,
		BehindBy:       a.behindBy,
	}
	if tip.sha != "" {
		bd.TipSHA = tip.sha
		bd.TipCommittedAt = tip.committedAt
		bd.UpstreamedPR = tip.upstreamedPR
	}
	return bd
}
