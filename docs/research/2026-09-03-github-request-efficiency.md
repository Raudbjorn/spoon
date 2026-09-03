# GitHub Request Efficiency for `spn forks list`

Date: 2026-09-03
Branch: `network-wide-distinguished-file-detection` (on top of the shipped `--touching`)
Source proposal: `/home/svnbjrn/.docs/spoon-github-internal-endpoints-tree-commit-info-network-chunk-proposal-20260903.md`
Plan: `docs/superpowers/plans/2026-09-03-github-request-efficiency.md`

## Purpose

`spn forks list` spent at least one REST `compare` call per fork, plus a
`commits/{sha}/pulls` probe for every ahead branch and up to five more
compares when the default branch showed no work
(`internal/github/branches.go:153-202`, `internal/github/adapter.go:336-339`).
On `pbakaus/impeccable` (3692 forks) the rate reserve stopped the sweep with
1572 forks never compared; on `berriai/litellm` (10735 forks, 19221 listed
side branches) only 3493 got a compare. Yet 97% of impeccable's compared
forks (2051/2120) and 63% of litellm's (2213/3493) have zero ahead commits —
their compare call bought a confirmed nothing.

This document is the decision record for closing that gap: it evaluates the
web endpoints a companion proposal surfaced, states the live measurements
that pinned the design's batch sizes and gates, and records the rulings made
while implementing it. Task-by-task detail is in the plan; this document is
the "why" and the "what was verified" behind it.

## Verified endpoints and final verdicts

The source proposal evaluated three of GitHub's internal/undocumented web
endpoints. Its own table plus this branch's final ruling:

| Endpoint | Proposal's verdict | Final verdict (this branch) |
|---|---|---|
| `github.com/{o}/{r}/tree-commit-info/{ref}/{dir}` | Adopt as a tier-0 pre-filter ahead of every compare, literal paths only | **Adopted, but not as tier-0.** It sits behind the GraphQL batch: only forks the batch found divergent are candidates, and only when the selected branch's `BehindCount` still lets it contain upstream's last-touch commit (the containment gate, Task 7's guard). Zero-ahead forks are already resolved by the batch and never reach this stage. |
| `github.com/{o}/{r}/network/meta` | Skip — cookie-gated, no content spn needs | **Rejected, unchanged.** |
| `github.com/{o}/{r}/network/chunk?nethash=...` | Skip for now — cookie-gated, capped to the 50 most recently pushed forks, no path data | **Rejected, unchanged.** |

The GraphQL batch itself is not from the proposal — the proposal only
flagged batching as an open question (its ad hoc alias-batching for branch
names, and whether "GraphQL failed the whole batch on one bad repo" was a
blocker). This branch answers that question: Spoon already had a proven
batched cross-repo compare (`internal/github/divergent_branches.go`, used
only by the TUI) that a partial-NOT_FOUND alias does not fail; extending it
to `forks list` became the primary tier.

## Measurements (live, 2026-09-03)

### GraphQL batch shape and size

Query shape: `repository(owner,name){ ref(qualifiedName:"refs/heads/B"){
c0: compare(headRef:"owner:branch"){ aheadBy behindBy ... } ... } }`, aliased
per branch, against `pbakaus/impeccable`'s default branch.

| Shape | Aliases | Result |
|---|---|---|
| `{aheadBy behindBy}` | 50 | HTTP 200, cost 1 |
| `{aheadBy behindBy}` | 75 | HTTP 200, cost 1 |
| `{aheadBy behindBy}` | 100 | malformed/empty response (failure) |
| `{aheadBy behindBy}` | 150 | HTTP 502 |
| bare `{aheadBy}` (no `behindBy`) | 150 | HTTP 502 |
| `commits(last:1){oid committedDate associatedPullRequests(first:3){...}}` | 5 | HTTP 200, cost 1 |
| `commits(last:1){oid committedDate associatedPullRequests(first:5){...}}` | 50 | HTTP 200, cost 1 |
| same nested shape | 75, 100 | HTTP 502 |
| `commits(last:1)` with no `associatedPullRequests` | 100, 150 | HTTP 502 |

The `{aheadBy behindBy}` failures at 100/150 aliases contradict
`divergent_branches.go:42`'s "150 measured safe" note (a bare `{aheadBy}`
shape 502s at 150 too): that figure was measured against a far smaller
upstream and does not generalize — GitHub is imposing a document-size
ceiling on `ref(...)`'s compare block independent of the reported GraphQL
query cost (every successful response above was cost 1 regardless of alias
count). The nested `commits(last:1)` shape fails at a lower alias count
(75) than the flat shape (100) for the same reason — more document per
alias, same underlying ceiling — not because walking commit history itself
is what times out.

**Ruling (superseded, see below):** the plan's Task 0 ruling set
`batchCompareSize = 150` for Phase A on the strength of
`divergent_branches.go`'s "150 measured safe" note, without re-measuring it
against `pbakaus/impeccable` specifically. A same-day re-measurement (the
table above) found that note does not hold on this upstream: Phase A fails
at 100 and 150 aliases. **Shipped instead** (`internal/github/batch_compare.go`,
commit `da2ae73`): `batchCompareSize = 50` for Phase A and `batchTipSize =
50` for Phase B — the largest size measured safe for both shapes — plus
adaptive halving: a chunk that fails with a server-side error (5xx, a
gateway/transport error, or an undecodable body) is split and retried
recursively down to `batchMinChunk = 10`, and a chunk still failing at that
floor is dropped (its forks left unresolved, falling back to the ordinary
REST path) rather than failing the whole batch call. A genuine GraphQL-level
error that is not a server-side failure (e.g. `RATE_LIMITED`) still aborts
Phase A outright, since a smaller document cannot fix that. Tip SHAs for
zero-ahead branches come from the fork-listing query instead (extended to
carry `oid`), so Phase B never needs to ask about them.

A NOT_FOUND alias (fork renamed, deleted, or privated between listing and
the batch call) returns `null` in `data` for that alias with a corresponding
`errors[]` entry of type `NOT_FOUND`; every other alias in the same query
still decodes normally (`isPartialLookupError`, matching
`divergent_branches.go:152`).

Upstream-side lookup for the last-touch gate — `defaultBranchRef.target
.history(first:1,path:P)` to find the last-touch commit, then
`compare(headRef:<sha>){behindBy}` to find how many commits upstream made
since — costs 1 per query, run once per `spn forks list` invocation
regardless of network size.

### Unbounded `.diff` fallback

`Accept: application/vnd.github.v3.diff` on
`repos/pbakaus/impeccable/compare/main...tarcisiojr:main`: 1383 files, 10.7
MB, 47 of them under `lints/`. The JSON compare response's `files[]` field
caps at 300 (`forge.CompareFilesCap`) regardless of the true file count —
confirmed against both a fresh REST call and Spoon's own cached
`compare_files` rows.

### `tree-commit-info`

`github.com/{o}/{r}/tree-commit-info/{ref}/{dir}`, called anonymously with
`Accept: application/json` and `X-Requested-With: XMLHttpRequest`: HTTP 200
with `{entries:{<name>:{oid,date}}}` for a real ref/dir; HTTP 404 for a
bogus ref and for a missing repo (both a plain miss, not a breaker trip —
see `treecommitinfo.Client`'s `NotFound` outcome). The `?path=` form
resolves against the fork's default branch — confirmed against
`watcharin101ac/impeccable`, whose default branch is `questionaire`, not
`main`.

### Skip-rate sample

24 random `pbakaus/impeccable` forks checked against upstream's last-touch
SHA for `cli/engine/registry/antipatterns.mjs` (`fa44839f`, committed
2026-09-02): 0 had the same SHA, 12 had a different SHA, 12 predate the
directory's existence entirely. Upstream made 5 commits to the repository
after that SHA (`compare(headRef:"fa44839f...")` → `behindBy 5`), so on this
sample, only a fork whose selected branch is `behindBy <= 5` can possibly
match today — most of the network is already too far behind for the gate to
even attempt a lookup.

This is a small, single-file, single-repo sample; it measures the shape of
the gate's live behavior, not a general skip rate. See Risks.

### Store numbers (`~/.config/spoon/spoon.db`, read-only)

| | impeccable | litellm |
|---|---|---|
| forks / with T2 | 3692 / 2120 | 10735 / 3493 |
| ahead == 0 / ahead > 0 | 2051 / 69 | 2213 / 1280 |
| never pushed (`pushed_at <= created_at`) / of those ahead>0 | 3331 / 1 | 5224 / 38 |
| forks with listed side branches / total side branches | 0 / 0 | 5215 / 19221 |
| `is_branch_work` | 6 | 929 |
| `compare_files` at the 300 cap | 10 | 58 |

## Design

After T1 collection and before dispatch, `Stream` asks the provider for the
whole network's divergence in one GraphQL batch (`aheadBy`/`behindBy`/tip/
merged-PR per listed branch). A pure selection function
(`forge.SelectDivergentBranch`) reproduces `FetchCompareWithBranchScan`'s
branch policy from that data, so the batch changes performance, not which
branch a fork's work is attributed to.

Zero-ahead forks get a synthesised T2 with no REST call at all
(`CompareSource: "graphql_batch"`). Divergent forks get exactly one REST
compare, on the branch the batch already selected — no branch scan, no PR
probe (`forge.ResolvedCompareProvider.CompareResolved`). If that compare
hits the 300-file cap, one unbounded `.diff` fetch replaces the file list
(`internal/unidiff`, `internal/github/compare_diff.go`). Under `--touching`
with literal paths, a divergent fork whose selected branch can contain
upstream's last-touch commit is checked against `tree-commit-info`; an equal
SHA proves the path untouched and skips the REST compare entirely.
Everything falls back to today's per-fork path on any failure — the batch,
the diff fallback, and the last-touch gate are all best-effort layers in
front of the same REST compare that ran before this branch, never a
replacement for it.

Rationale for "GraphQL batch first, `tree-commit-info` second" rather than
the proposal's original tier-0 framing: the batch alone already resolves
the large zero-ahead majority (97% on impeccable) with no REST call and no
dependency on an undocumented endpoint. `tree-commit-info` only has
something to add for the divergent minority the batch cannot resolve to
zero — and even there, only when the selected branch is close enough behind
to possibly contain the commit being checked against (the skip-rate sample
above shows that gate is often the binding constraint, not the lookup
itself). Running `tree-commit-info` before the batch would spend anonymous,
best-effort HTTP calls on forks the authenticated GraphQL batch was going to
resolve for free anyway.

## Flags and NDJSON/stderr fields shipped

| Flag | Effect |
|---|---|
| `--no-batch-compare` | Disables the pre-dispatch GraphQL batch; restores one REST compare per fork (today's pre-branch path). |
| `--no-tree-commit-info` | Leaves the `tree-commit-info` client disabled. Default: enabled only when `--touching` is present and every pattern is literal (`pathmatch.IsLiteral`); otherwise silently inert. Sets `forksops.Options.NoLastTouch`, which stops the last-touch gate from being built at all — see Rulings. |

| NDJSON/stderr field | Meaning |
|---|---|
| `t2.source` | `"graphql_batch"` when the compare was synthesised or skipped by the batch/last-touch stage; omitted (not empty-string) for an ordinary live or cached compare. |
| `t2.files_unfetched` | `true` when the last-touch skip stood in for a REST compare — the record's ahead/behind/head SHA are known, but no file list was ever fetched. Omitted when false. |
| `t2.files_truncated` | Now sourced from `T2Data.IsFilesTruncated()`, not the raw `FilesTruncated` flag: true only when the file list is still capped after the diff fallback was attempted (or never attempted). |
| `touching.reason: "last_touch"` | Emitted on an `unmatched` touching verdict when the compare was skipped by the last-touch proof rather than actually run (paired with `t2.files_unfetched`). |
| `compare_summary` (stderr envelope) | `cached`, `graphql_batch`, `rest`, `diff_fallback`, `last_touch_skipped` counts plus `batch_queries`/`batch_cost` (from `forge.BatchStats`) and `batch_error` when the batch degraded. Mirrors the existing `touching` summary's shape. |
| `[touching]` stderr `last_touch` block | `gated` (containment check failed, no lookup made), `looked_up`, `skipped`, `mismatch`, `unavailable` — tallies for `lasttouch.go`'s `decide()` outcomes. |

## Rulings made during implementation

- **Two-phase batch, two sizes.** Task 0's measurement (above) forced a
  two-phase design rather than one query per branch: `batchCompareSize =
  50` for `{aheadBy behindBy}`, `batchTipSize = 50` for the nested
  tip+PR shape — plus adaptive halving of a chunk that fails with a
  server-side error, down to a `batchMinChunk = 10` floor before it is
  dropped. Tip SHAs for zero-ahead branches are seeded from the listing
  query instead of asked for in Phase B.
- **The plan's Task 0 ruling of `batchCompareSize = 150` was superseded**
  (commit `da2ae73`, same day). The plan's own Task 0 measurement borrowed
  `divergent_branches.go`'s "150 measured safe" figure for the flat
  `{aheadBy behindBy}` shape without separately re-measuring it against
  `pbakaus/impeccable`; a same-day re-measurement (see "GraphQL batch shape
  and size" above) found Phase A itself fails at 100 and 150 aliases on
  this upstream, because `divergent_branches.go`'s figure was measured
  against a smaller one. At 150, Phase A would 502, `doGraphQLWithRetry`
  would exhaust its retries against the same oversized document, and the
  whole batch call would error out — silently falling back to REST for
  every fork on exactly the large networks this feature exists for. Fixed
  by dropping to 50 and adding adaptive halving, above.
- **`BatchStats` return shape.** `forge.BatchCompareProvider.BatchCompare`
  returns `(map[string]ForkDivergence, forge.BatchStats, error)` rather than
  folding query/cost accounting into a side channel, so `Stream` can report
  it in `compare_summary` without a second provider call.
- **Zero-ahead synthesis leaves `Diffs` nil.** An empty `compare_files` set
  is exact for a fork with nothing ahead of upstream; persisting nil rather
  than an empty slice avoids a spurious write, matching
  `cmd/spn/forks.go`'s existing "write `compare_files` only from `Diffs`"
  persistence rule.
- **`CompareResolved` failure falls through to `Compare` once**, not
  treated as fatal — a batch-selected branch can go stale between the batch
  call and dispatch (deleted, force-pushed away); falling through keeps a
  resolvable fork from becoming a hard per-fork error.
- **`FetchPathLastTouch` is necessarily two sequential GraphQL queries**,
  not one round trip: `compare(headRef: <sha>)` needs the SHA `history(
  first:1, path:)` resolves first, so there is no single query that could
  ask both at once. Pre-authorised in the Task 6 dispatch; costs one extra
  cost-1 query per run, not per fork.
- **`--no-tree-commit-info` needed its own option, not just "don't enable
  the client."** Without `forksops.Options.NoLastTouch`, the last-touch gate
  still built even when the client was never enabled: it would spend the
  once-per-run `PathLastTouch` GraphQL query and tally every fork as
  `unavailable`, wasting two cost-1 queries and reporting misleading tallies
  for a stage the flag was supposed to fully disable. Fixed in Task 8's fix
  round (commit `802dbf0`): the CLI sets `NoLastTouch` whenever
  `EnableTreeCommitInfo` was not called, and `newLastTouchGate` returns nil
  immediately when it is set, before the upstream lookup runs.
- **Never-treat-a-differing-OID-as-a-signal, restated as code.** The
  last-touch guard comment (`internal/forksops/lasttouch.go`, `decide`) is
  the named guard the plan required; it exists specifically because a
  differing SHA is not proof of anything (the `mp3wizard` case in the source
  proposal: byte-identical content, differing last-touch SHA, because a
  merge commit records as "touching" a path even when its result matches
  one parent exactly).

## Measured on the live network

Filled in by the verification run (Task 10).

Expected numbers, from the plan's Task 10 verification steps (not yet
measured):

| | Before this branch | Expected after |
|---|---|---|
| impeccable REST requests | >= 3692 (one compare per fork; reserve stopped the sweep at 2120) | ~ 37 listing + 1 parent + ~69 compares + <= 10 diffs, i.e. < 150 |
| impeccable GraphQL queries | 0 (no batch existed) | ~ 74 Phase A (3692 branches / 50 per query) + <= 2 Phase B (69 ahead branches / 50), ~ 76 total |
| litellm REST requests | 3493 (reserve-limited) | ~ 1300 divergent forks + 58 diffs |
| litellm GraphQL queries | 0 (no batch existed) | ~ 600 Phase A (~30000 branches [10735 forks + 19221 side branches] / 50) + ~ 40 Phase B (1280 ahead branches / 50, plus retries from adaptive halving) |

## Risks

- **`tree-commit-info` is undocumented and unversioned.** GitHub owes it no
  stability guarantee. The client fails soft on any non-OK outcome
  (`treecommitinfo.Outcome`: `NotFound`, `Disabled`, `Error` all mean "no
  information") and trips a run-scoped breaker on the first 403/429/5xx.
- **Anonymous rate limits are unmeasured at scale.** The source proposal's
  and this branch's live checks made on the order of dozens of calls with
  no throttling observed; neither exercised the thousands-of-calls sweep a
  full network scan could reach. `tree-commit-info`'s own token bucket (1
  req/s, burst 3) is a conservative guess, not a measured ceiling.
- **The 32 MiB diff cap is untested above that size.** `maxDiffBytes = 32 <<
  20` in `internal/github/compare_diff.go` was sized against the largest
  real sample seen (10.7 MB); a fork with a genuinely larger unified diff
  falls back to `FilesTruncatedReason` rather than completing, and that path
  has not been exercised against a real oversized diff.
- **A synthesised (`graphql_batch`) T2 has no `BaseSHA` and no MNA.** It
  carries only `AheadCount`, `BehindCount`, `HeadSHA`, and
  `CompareSource` — consumers that read `T2.BaseSHA` (e.g.
  `internal/tui/cache_bridge.go:37`) see it empty for these records.
  Fingerprints and heat scoring are unaffected since a zero-ahead fork
  scores identically either way.
- **Gitea and GitLab providers are untouched.** Neither implements
  `forge.BatchCompareProvider`, `forge.ResolvedCompareProvider`, or
  `forge.LastTouchProvider`; every fork on those providers keeps today's
  per-fork compare path unconditionally.
- **ToS posture of `tree-commit-info` is best-effort, not endorsed.** It is
  the same "scrape the web frontend" pattern the source proposal flagged as
  distinct from the documented, rate-limited REST/GraphQL API — framed
  internally as an optional, gracefully-degrading optimization, and it can
  be turned off entirely with `--no-tree-commit-info`.
