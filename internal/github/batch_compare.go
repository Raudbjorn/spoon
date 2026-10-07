package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"
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
// {aheadBy behindBy} alone does not carry.
//
// Both phase sizes are a live-measured ceiling, not a query-cost budget:
// measured 2026-09-03 against pbakaus/impeccable's ref(main){ cN:
// compare(headRef:"owner:branch"){...} } shape, the flat {aheadBy behindBy}
// selection was GraphQL cost 1 at both 50 and 75 aliases, returned a
// malformed/empty response at 100, and HTTP 502 at 150 -- so the earlier
// "150 measured safe" note (carried over from divergent_branches.go, whose
// own upstream is far smaller) does not generalize across fork networks.
// GitHub is imposing a document-size ceiling independent of the reported
// query cost, and the only fix that generalizes is adapting the chunk size
// at request time -- see batchMinChunk and isBatchServerFailure below --
// rather than hardcoding a single "safe" number for every network.
const (
	// batchCompareSize caps branch pairs per Phase A query, before adaptive
	// halving. 50 was the largest size measured safe (cost 1) on
	// 2026-09-03; a network whose upstream tolerates less falls back to
	// smaller chunks automatically via isBatchServerFailure.
	batchCompareSize = 50

	// batchTipSize caps branches per Phase B query, before adaptive
	// halving. The nested commits(last:1){ ... associatedPullRequests ... }
	// shape is more expensive per alias than Phase A's flat one, so it
	// keeps the same conservative ceiling measured for it directly.
	batchTipSize = 50

	// batchTipPRPageSize bounds associatedPullRequests per tip commit. The
	// REST probe this replaces (PullsForCommit, upstreamed.go) reads one
	// unpaginated page (30) of the same association list; 20 keeps Phase B
	// documents cheap (50 aliases x 20 nodes ~ cost 10) while covering every
	// realistic tip. A tip with more associations than this and no merged
	// upstream PR among the first page is treated as genuine work -- the
	// same direction the REST path takes when its own list is truncated
	// (branches.go tipUpstreamed) -- and logged at debug.
	batchTipPRPageSize = 20

	// batchMinChunk is the floor adaptive halving stops at: a chunk this
	// size or smaller that still fails as a server-side failure is dropped
	// (its branches fall back to unresolved / listing tip) rather than
	// split further, so a pathological document can't recurse forever.
	batchMinChunk = 10

	// batchAliasRetries caps how many times one alias-scoped GraphQL error
	// (see classifyAliasScopedErrors) is re-queried before that attempt is
	// given up on -- Phase A marks it dropped, Phase B just leaves it out
	// of tips -- rather than re-queried forever.
	batchAliasRetries = 2
)

// isBatchServerFailure reports whether err is a server-side failure that
// warrants retrying with a smaller chunk, as opposed to either tolerating
// it (a partial NOT_FOUND) or failing the whole sweep (a genuine
// GraphQL-level error such as RATE_LIMITED, which halving cannot fix): an
// untyped HTTP 5xx, a gateway/transport error, or a response body that
// failed to decode at all. The GraphQL client surfaces that last case -- observed as
// GitHub's "malformed/empty response" failure mode for an oversized
// document -- as a plain, unwrapped decode error, which is exactly what
// isGatewayOrTransportError's statusCode()==0 branch also catches; that is
// why both isTransientServerError and isGatewayOrTransportError are
// consulted here rather than either alone.
//
// Context cancellation/deadline is deliberately excluded even though it
// also reports statusCode()==0: it is not a GitHub-side failure, and
// classifying it as one would walk the whole halving tree down to
// batchMinChunk (each split re-attempting a query against an already-dead
// context) before giving up, burying the real signal instead of
// propagating it immediately the way the rest of this codebase does.
func isBatchServerFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if isGraphQLResponseError(err) {
		// A GraphQL-level error (including NOT_FOUND) means the query was
		// answered, just with an error payload -- not a server failure.
		return false
	}
	return isTransientServerError(err) || isGatewayOrTransportError(err)
}

// classifyAliasScopedErrors inspects a *gqlResponseError's items and, if
// every one is either NOT_FOUND (already handled by isPartialLookupError
// before this is ever reached) or scoped to one of the current document's
// own aliases -- its Path's last element is "cN" for some N in
// [0, batchSize) -- returns the set of failed alias indexes to re-query.
// ok is false when any error is neither of those (an empty Path, or a Path
// naming something other than one of this document's aliases -- e.g.
// RATE_LIMITED), since no re-query can fix that class and the caller must
// still abort as before.
//
// Observed 2026-09-03 18:37 against pbakaus/impeccable, Phase A chunk of
// 50: GitHub returned HTTP 200 with data for most aliases plus an errors[]
// array of items shaped like {"message":"Something went wrong while
// executing your query on 2026-09-03T18:37:13Z. Please include `7D83:...`
// when reporting this issue.","path":["repository","ref","c10"]} for a
// handful of aliases (also c25, c26, c32, c35) -- no "type" field at all,
// so isPartialLookupError correctly does not tolerate it as NOT_FOUND, but
// the error is unambiguously scoped to that one alias by its Path, and
// simply re-querying that alias on its own succeeded. Treating this as a
// hard failure (the pre-fix behavior) aborted the whole batch over five
// aliases out of thousands, falling back to REST for the entire network.
func classifyAliasScopedErrors(err error, batchSize int) (retry map[int]bool, ok bool) {
	var gqlErr *gqlResponseError
	if !errors.As(err, &gqlErr) || len(gqlErr.Errors) == 0 {
		return nil, false
	}
	retry = make(map[int]bool, len(gqlErr.Errors))
	for _, item := range gqlErr.Errors {
		if item.Type == "NOT_FOUND" {
			continue // tolerated as today; the alias stays null, no retry needed
		}
		idx, scoped := aliasIndexFromPath(item.Path, batchSize)
		if !scoped {
			return nil, false
		}
		retry[idx] = true
	}
	if len(retry) == 0 {
		// Every item was NOT_FOUND after all -- isPartialLookupError should
		// already have tolerated this before classifyAliasScopedErrors was
		// ever called, but stay correct if reached some other way.
		return nil, false
	}
	return retry, true
}

// aliasIndexFromPath extracts N from a GraphQL error Path whose last
// element is "cN", the alias naming scheme every document in this file
// uses, and reports whether the path names one of the current document's
// batchSize aliases at all. Path elements decode from JSON as
// interface{}; a string element (as opposed to a float64 array index)
// only ever appears for a named field like "repository", "ref", or one of
// these aliases.
func aliasIndexFromPath(path []interface{}, batchSize int) (int, bool) {
	if len(path) == 0 {
		return 0, false
	}
	last, ok := path[len(path)-1].(string)
	if !ok || len(last) < 2 || last[0] != 'c' {
		return 0, false
	}
	n, err := strconv.Atoi(last[1:])
	if err != nil || n < 0 || n >= batchSize {
		return 0, false
	}
	return n, true
}

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

	// dropped is true when this attempt's Phase A chunk failed as a
	// server-side failure all the way down to batchMinChunk, or its
	// alias-scoped GraphQL error persisted past batchAliasRetries
	// re-queries, and was abandoned: distinct from resolved==false from a
	// definitive null alias (the branch genuinely doesn't exist), this
	// means Phase A was never actually answered for this branch at all.
	// Since halving and alias-scoped retries both operate on arbitrary
	// sub-slices of a target's attempts independently, one branch of a
	// fork can be dropped while a sibling branch resolves fine -- the
	// fold step must not let that sibling's success stand in for a clean
	// answer about the whole fork.
	dropped bool

	// aheadBehindRetries/tipRetries count how many times this attempt has
	// been re-queried after an alias-scoped GraphQL error (see
	// classifyAliasScopedErrors), separately per phase: the two phases ask
	// different questions about the same branch and a failure in one says
	// nothing about the other, so they must not share a retry budget.
	// Phase B's cap being reached is never fatal (see runTipsChunk) and
	// does not set dropped -- only Phase A's does, since only Phase A's
	// resolution feeds into a fork's Resolved verdict.
	aheadBehindRetries int
	tipRetries         int
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
// A target's entry has Resolved == false unless its own default-branch
// attempt resolved in Phase A *and* none of its attempts (default or side)
// were dropped by adaptive halving -- see forge.ForkDivergence and
// batchAttempt.dropped. That covers a fork that vanished entirely (every
// alias null or dropped), a fork whose default branch alone was renamed or
// deleted between listing and this call (a definitive null alias for just
// that attempt, even alongside a resolved side branch), and a fork with an
// unlucky side-branch chunk that failed all the way to batchMinChunk. Phase
// B is skipped for such a target -- see FetchBatchDivergence's BatchStats,
// which will not carry a document for it -- since its side data would only
// be discarded once folded. A target absent from targets is simply absent
// from the result; forge.BatchCompareProvider's doc covers how callers must
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

	byTarget := make(map[int][]*batchAttempt, len(targets))
	for _, a := range attempts {
		byTarget[a.targetIdx] = append(byTarget[a.targetIdx], a)
	}

	// A target's picture is untrustworthy -- Resolved must end up false --
	// when either (a) any of its attempts belongs to a chunk that failed
	// as a server-side failure all the way down to batchMinChunk and was
	// dropped there (batchAttempt.dropped), or (b) its own default-branch
	// attempt did not resolve at all, whether from a dropped chunk or a
	// definitive null alias (NOT_FOUND: the default branch was renamed or
	// deleted between listing and this call). (b) matters independently
	// of (a): a null default alias can occur inside an otherwise fully
	// successful, non-dropped chunk, tolerated the same way any partial
	// NOT_FOUND is. Computed once, before Phase B, so a doomed target's
	// resolved side branches don't spend Phase B budget on data the fold
	// step below will discard anyway.
	//
	// Halving splits one target's attempts across chunks independently of
	// which target they belong to, and a chunk answers strictly aliased
	// per-branch, not per-fork -- so neither condition can be inferred
	// from "did *any* attempt for this target resolve," which is what an
	// earlier version of this check did. That let a resolved side branch
	// stand in for a fork whose default was never actually answered,
	// fabricating a "0 ahead" Default and skipping the REST fallback for
	// a fork that was never actually checked -- exactly the
	// fabricated-zero bug class this package exists to avoid.
	unresolvedTarget := make(map[int]bool, len(targets))
	for ti := range targets {
		var defaultAttempt *batchAttempt
		anyDropped := false
		for _, a := range byTarget[ti] {
			if a.isDefault {
				defaultAttempt = a
			}
			if a.dropped {
				anyDropped = true
			}
		}
		if anyDropped || defaultAttempt == nil || !defaultAttempt.resolved {
			unresolvedTarget[ti] = true
		}
	}

	var ahead []*batchAttempt
	for _, a := range attempts {
		if !unresolvedTarget[a.targetIdx] && a.resolved && a.aheadBy > 0 {
			ahead = append(ahead, a)
		}
	}
	tips := c.fetchBatchTips(ctx, baseOwner, baseRepo, qualified, ahead, &stats)

	for ti, t := range targets {
		if unresolvedTarget[ti] {
			// The caller must fall back to the REST path for this fork
			// wholesale, same as a target absent from the map entirely --
			// see forge.ForkDivergence.Resolved's doc.
			out[t.ID] = forge.ForkDivergence{Resolved: false}
			continue
		}

		fd := forge.ForkDivergence{Resolved: true}
		for _, a := range byTarget[ti] {
			bd := branchDivergenceFor(a, tips[a])
			if a.isDefault {
				fd.Default = bd
				continue
			}
			if a.resolved {
				// An unresolved side branch (a definitive null alias --
				// deleted between listing and Phase A, not a dropped
				// chunk, which unresolvedTarget already excluded above)
				// carries nothing useful -- zero ahead/behind, no real
				// tip -- and would masquerade as "checked, nothing
				// diverges" in SelectDivergentBranch if included; drop it
				// instead of fabricating a false all-clear. Unlike the
				// default branch, a fork's picture stays trustworthy
				// without every side resolving: a side branch simply not
				// existing is a legitimate answer, not a gap.
				fd.Sides = append(fd.Sides, bd)
			}
		}
		out[t.ID] = fd
	}

	return out, stats, nil
}

// fetchBatchAheadBehind runs Phase A: aliased compare(headRef:){aheadBy
// behindBy} for every attempt, batchCompareSize aliases per top-level
// query (further adaptively split per runAheadBehindChunk), mutating each
// attempt's resolved/aheadBy/behindBy in place.
//
// This is the core divergence signal, so a real (non-partial-NOT_FOUND,
// non-server-failure) GraphQL error or an unresolved upstream ref aborts
// the whole call rather than silently reporting a batch of fabricated
// zeros -- exactly as divergent_branches.go's FetchDivergentBranchCounts
// does. A server-side failure (see isBatchServerFailure) is not such an
// error: see runAheadBehindChunk.
func (c *Client) fetchBatchAheadBehind(
	ctx context.Context,
	baseOwner, baseRepo, qualified string,
	attempts []*batchAttempt,
	stats *forge.BatchStats,
) error {
	for _, chunk := range slidingChunks(len(attempts), batchCompareSize) {
		if err := c.runAheadBehindChunk(ctx, baseOwner, baseRepo, qualified, attempts[chunk.lo:chunk.hi], stats); err != nil {
			return err
		}
	}
	return nil
}

// runAheadBehindChunk sends one Phase A document for batch. Three
// failure classes are handled before falling through to a normal decode:
//
//   - A server-side failure (isBatchServerFailure) adaptively halves:
//     while len(batch) > batchMinChunk it splits batch in two and retries
//     each half recursively (a half may split again); at or below the
//     floor it drops the whole chunk instead -- every attempt in it is
//     marked dropped and stays unresolved, logged at slog.Debug, and the
//     sweep continues rather than failing outright.
//   - A GraphQL-level error whose items are all either NOT_FOUND or
//     scoped to specific aliases (classifyAliasScopedErrors) decodes every
//     alias that did succeed normally, then re-queues just the failed
//     aliases as their own follow-up chunk (recursing through this same
//     function, so a follow-up may itself halve, alias-retry again, or
//     succeed). An alias re-queried batchAliasRetries times without
//     succeeding is dropped, same as a halving-floor drop.
//   - Any other GraphQL-level error (e.g. RATE_LIMITED, which neither
//     halving nor a per-alias retry can fix) or an unresolved upstream ref
//     still aborts the whole call, exactly as before either of the above
//     existed.
func (c *Client) runAheadBehindChunk(
	ctx context.Context,
	baseOwner, baseRepo, qualified string,
	batch []*batchAttempt,
	stats *forge.BatchStats,
) error {
	if len(batch) == 0 {
		return nil
	}

	fields := make([]reflect.StructField, 0, len(batch))
	for i := range batch {
		fields = append(fields, gqlField(fmt.Sprintf("C%d", i), fmt.Sprintf("c%d: compare(headRef: $head%d)", i, i), (*struct{ AheadBy, BehindBy int })(nil)))
	}
	query, vars := gqlRefQuery(baseOwner, baseRepo, qualified, fields)
	for i, a := range batch {
		vars[fmt.Sprintf("head%d", i)] = githubv4.String(a.owner + ":" + a.branch)
	}

	// Above the halving floor, a server-side failure here is retried by
	// splitting the batch in two below (halving IS the retry), so paying
	// doGraphQLWithRetry's own 1s+2s backoff first would double-pay for
	// the same failure at every node on the way down to the floor. Only
	// at or below batchMinChunk, where the chunk can no longer be split
	// further, does doGraphQLWithRetry's backoff earn its cost.
	var resp compareBatch
	var err error
	if len(batch) > batchMinChunk {
		err = c.doGraphQL(ctx, query, vars, &resp)
	} else {
		err = c.doGraphQLWithRetry(ctx, query, vars, &resp)
	}
	stats.Queries++
	stats.Cost += resp.RL.Cost

	// retryIdx names the aliases (by their index within this batch) whose
	// GraphQL error was scoped to just that alias -- decoded normally
	// below like any other absent/null alias, but also re-queried as
	// their own follow-up chunk rather than left unresolved outright.
	var retryIdx map[int]bool
	if err != nil && !isPartialLookupError(err) {
		if isBatchServerFailure(err) {
			if len(batch) > batchMinChunk {
				mid := len(batch) / 2
				if err := c.runAheadBehindChunk(ctx, baseOwner, baseRepo, qualified, batch[:mid], stats); err != nil {
					return err
				}
				return c.runAheadBehindChunk(ctx, baseOwner, baseRepo, qualified, batch[mid:], stats)
			}
			slog.Debug("batch divergence: phase A chunk failed at floor size, branches left unresolved",
				"size", len(batch), "err", err)
			for _, a := range batch {
				a.dropped = true
			}
			return nil
		}
		idx, ok := classifyAliasScopedErrors(err, len(batch))
		if !ok {
			return fmt.Errorf("batch divergence: compare branches: %w", err)
		}
		retryIdx = idx
		// Fall through: The GraphQL transport has already decoded data for every alias
		// that didn't error, so decode those below exactly as on a clean
		// response, then re-queue retryIdx's aliases as a follow-up chunk.
	}
	// Partial NOT_FOUND, or alias-scoped errors with retryIdx set: the
	// surviving aliases decoded fine, fall through.

	// A resolved ref always yields a non-nil map -- see
	// divergent_branches.go:303-315 for why a nil map here means ref
	// itself failed to resolve (a renamed/deleted base branch), not
	// that every child alias individually came back empty. Continuing
	// past that would fabricate a zero for every branch in the batch.
	if resp.Repository.Ref == nil {
		return fmt.Errorf("batch divergence: upstream ref %q did not resolve", qualified)
	}

	var retryBatch []*batchAttempt
	for i, a := range batch {
		if retryIdx[i] {
			if a.aheadBehindRetries >= batchAliasRetries {
				slog.Debug("batch divergence: phase A alias-scoped error persisted past retry cap, branch left unresolved",
					"branch", a.branch, "retries", a.aheadBehindRetries)
				a.dropped = true
				continue
			}
			a.aheadBehindRetries++
			retryBatch = append(retryBatch, a)
			continue
		}
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

	if len(retryBatch) > 0 {
		return c.runAheadBehindChunk(ctx, baseOwner, baseRepo, qualified, retryBatch, stats)
	}
	return nil
}

// fetchBatchTips runs Phase B: aliased compare(headRef:){ commits(last:1) {
// ... associatedPullRequests ... } }, batchTipSize aliases per top-level
// query (further adaptively split per runTipsChunk), for the given
// (already Phase-A-ahead) attempts only.
//
// Unlike Phase A, a Phase B failure is never fatal to the call: it is
// enrichment on top of divergence data Phase A already resolved, not the
// divergence data itself. See runTipsChunk for how a chunk failure is
// handled.
func (c *Client) fetchBatchTips(
	ctx context.Context,
	baseOwner, baseRepo, qualified string,
	ahead []*batchAttempt,
	stats *forge.BatchStats,
) map[*batchAttempt]tipResult {
	upstream := baseOwner + "/" + baseRepo
	tips := make(map[*batchAttempt]tipResult, len(ahead))
	for _, chunk := range slidingChunks(len(ahead), batchTipSize) {
		c.runTipsChunk(ctx, baseOwner, baseRepo, qualified, upstream, ahead[chunk.lo:chunk.hi], stats, tips)
	}
	return tips
}

// runTipsChunk sends one Phase B document for batch, handling the same
// three failure classes as runAheadBehindChunk (a server-side failure
// adaptively halves; alias-scoped GraphQL errors decode what succeeded and
// re-queue only the failed aliases, up to batchAliasRetries; anything else,
// including an upstream ref that somehow failed to resolve here after
// resolving in Phase A, gives up on the chunk). The one difference from
// Phase A: none of these are ever fatal to the whole call here, since
// Phase B is enrichment on divergence data Phase A already resolved, not
// the divergence data itself. A chunk (or an alias past its retry cap)
// that gives up simply leaves its attempts absent from tips, so the
// caller falls back to their listing-seeded tip with UpstreamedPR == 0 --
// their work reads as genuine, mirroring tipUpstreamed's fail-open at
// branches.go:47-52.
func (c *Client) runTipsChunk(
	ctx context.Context,
	baseOwner, baseRepo, qualified, upstream string,
	batch []*batchAttempt,
	stats *forge.BatchStats,
	tips map[*batchAttempt]tipResult,
) {
	if len(batch) == 0 {
		return
	}

	fields := make([]reflect.StructField, 0, len(batch))
	for i := range batch {
		fields = append(fields, gqlField(fmt.Sprintf("C%d", i), fmt.Sprintf("c%d: compare(headRef: $head%d)", i, i), (*gqlTipCompare)(nil)))
	}
	query, vars := gqlRefQuery(baseOwner, baseRepo, qualified, fields)
	vars["prPageSize"] = githubv4.Int(batchTipPRPageSize)
	for i, a := range batch {
		vars[fmt.Sprintf("head%d", i)] = githubv4.String(a.owner + ":" + a.branch)
	}

	// Same halving-is-the-retry reasoning as runAheadBehindChunk: above the
	// floor a server-side failure below splits and retries via halving,
	// so doGraphQLWithRetry's own backoff is only worth paying at or
	// below batchMinChunk, where there is nothing left to split.
	var resp compareBatch
	var err error
	if len(batch) > batchMinChunk {
		err = c.doGraphQL(ctx, query, vars, &resp)
	} else {
		err = c.doGraphQLWithRetry(ctx, query, vars, &resp)
	}
	stats.Queries++
	stats.Cost += resp.RL.Cost

	// retryIdx names the aliases (by their index within this batch) whose
	// GraphQL error was scoped to just that alias -- re-queried as their
	// own follow-up chunk below rather than left on the listing tip
	// outright, mirroring runAheadBehindChunk.
	var retryIdx map[int]bool
	if err != nil && !isPartialLookupError(err) {
		if isBatchServerFailure(err) && len(batch) > batchMinChunk {
			mid := len(batch) / 2
			c.runTipsChunk(ctx, baseOwner, baseRepo, qualified, upstream, batch[:mid], stats, tips)
			c.runTipsChunk(ctx, baseOwner, baseRepo, qualified, upstream, batch[mid:], stats, tips)
			return
		}
		if idx, ok := classifyAliasScopedErrors(err, len(batch)); ok {
			retryIdx = idx
			// Fall through: decode every alias that didn't error below,
			// then re-queue retryIdx's aliases as a follow-up chunk.
		} else {
			slog.Debug("batch divergence: phase B chunk failed, branches keep listing tip",
				"size", len(batch), "err", err)
			return
		}
	}
	// Partial NOT_FOUND, or alias-scoped errors with retryIdx set: decode
	// what did resolve below.

	if resp.Repository.Ref == nil {
		slog.Debug("batch divergence: phase B upstream ref unresolved, branches keep listing tip",
			"size", len(batch))
		return
	}

	var retryBatch []*batchAttempt
	for i, a := range batch {
		if retryIdx[i] {
			if a.tipRetries >= batchAliasRetries {
				// Never fatal: this attempt simply gets no tips[] entry,
				// and branchDivergenceFor falls back to its listing tip
				// with UpstreamedPR == 0 -- the same outcome as any other
				// exhausted Phase B failure. Unlike Phase A's dropped, this
				// must not affect the fork's Resolved verdict: Phase B is
				// enrichment on data Phase A already resolved.
				slog.Debug("batch divergence: phase B alias-scoped error persisted past retry cap, branch keeps listing tip",
					"branch", a.branch, "retries", a.tipRetries)
				continue
			}
			a.tipRetries++
			retryBatch = append(retryBatch, a)
			continue
		}
		raw, ok := resp.Repository.Ref[fmt.Sprintf("c%d", i)]
		if !ok || string(raw) == "null" {
			continue
		}
		var cmp gqlTipCompare
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
		if res.upstreamedPR == 0 && tip.AssociatedPullRequests.TotalCount > batchTipPRPageSize {
			// More associations than the page holds and none of the first
			// page merged upstream: the verdict stays "genuine", which can
			// differ from an exhaustive probe. Observable, not fatal.
			slog.Debug("batch divergence: associated PR page truncated; treating tip as genuine",
				"branch", a.owner+":"+a.branch, "associations", tip.AssociatedPullRequests.TotalCount, "page", batchTipPRPageSize)
		}
		tips[a] = res
	}

	if len(retryBatch) > 0 {
		c.runTipsChunk(ctx, baseOwner, baseRepo, qualified, upstream, retryBatch, stats, tips)
	}
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

type gqlTipCompare struct {
	Commits struct {
		Nodes []struct {
			OID                    string `json:"oid"`
			CommittedDate          string `json:"committedDate"`
			AssociatedPullRequests struct {
				TotalCount int `json:"totalCount"`
				Nodes      []struct {
					Number         int  `json:"number"`
					Merged         bool `json:"merged"`
					BaseRepository struct {
						NameWithOwner string `json:"nameWithOwner"`
					} `json:"baseRepository"`
				} `json:"nodes"`
			} `json:"associatedPullRequests" graphql:"associatedPullRequests(first: $prPageSize)"`
		} `json:"nodes"`
	} `json:"commits" graphql:"commits(last: 1)"`
}
