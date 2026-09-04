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

Task 10 verification, run 2026-09-04 against `pbakaus/impeccable` (binary
built from commit `8aadff2`, after the Task 10 fix below). All runs used
`--tier 2 --no-cluster --no-embed`; `SPOON_DEBUG=1` was set from the warm
`--touching` run onward to surface the `[triage]`/`[touching]` prose lines
(see caveat under "What SPOON_DEBUG actually gates," below). Full command
lines, rate-limit before/after, and raw output paths are in the Task 10
report.

### Attempt 1: the batch aborted, not a measurement

The first cold-impeccable run (commit `9600e5c`, before `8aadff2`) is kept
as the failure record the fix responds to, not a valid measurement: Phase A
hit alias-scoped GraphQL errors ("Something went wrong while executing your
query", `repository.ref.c10`/`c25`/`c26`/`c32`/`c35`) that the batch call
treated as fatal, aborting after 3 queries and falling every one of 3513
eligible forks back to REST (`compare_summary`: `batch_queries 3 · batch_cost
3 · graphql_batch 0 · rest 3513 · diff_fallback 8`, `batch_error` carrying
the five alias errors verbatim). Fixed by commit `8aadff2` (alias-scoped
errors are re-queried instead of aborting the whole batch). Raw output:
`attempt1-imp.{ndjson,err}`.

### impeccable, cold (`--refresh`), attempt 2 — commit `8aadff2`

Rate limit before: core 5000, graphql 5000 (15:01:50 UTC). The hourly
window reset at 15:21:51 UTC, inside this run, so the before/after `gh api
rate_limit` delta is not a usable request count for this run; the numbers
below come from the run's own `compare_summary` and `acquisition_report`
envelopes instead. Finished 15:24:29 UTC (~23 min wall-clock from launch;
`[triage]` prose unavailable for this run — see caveat below).

| | Before this branch | Expected after (plan) | Measured |
|---|---|---|---|
| REST (compare-path) | >= 3692 (1/fork, reserve stopped the sweep at 2120) | ~ 37 listing + 1 parent + ~69 compares + <= 10 diffs, i.e. < 150 | 320 compares + 8 diffs = 328, plus 64 listing pages (mixed GraphQL+REST acquisition, see below) |
| GraphQL batch queries | 0 (no batch existed) | ~ 74 Phase A + <= 2 Phase B, ~ 76 total | 138 queries, cost 138 |
| `compare_summary` | n/a | n/a | `cached 0 · graphql_batch 2941 · rest 320 · diff_fallback 8 · last_touch_skipped 0` |
| `t2==null` records | n/a | 0 | 0 (of 3261 total records) |
| `t2.source=="graphql_batch"` | n/a | ~3600 | 2941 (exact match to `compare_summary.graphql_batch`) |
| `t2.ahead>0` | n/a | ~69 | 75 |
| `t2.files_truncated==true` | n/a | <= 10 | 4 |
| `budget_skipped` | n/a | 0 | 0 |
| `[triage] batch compare degraded` | n/a | absent | absent (no `batch_error` in `compare_summary`) |

Measured REST and GraphQL both land well under the "before" baseline (a
92%+ cut in compare-path REST calls, and the batch never degrades), but
both are higher than the plan's specific estimates, which were sized
against the previous day's live-network sample (3692 forks). Two things
moved between the two days, both visible in the `acquisition_report`
envelope: `{"method":"graphql+rest","pages":64,"rawRows":5293,"uniqueRows":
2614,"duplicateRows":2679}` — this run's T1 listing fell back from pure
GraphQL to a GraphQL+REST hybrid mid-sweep and produced heavy duplication
(2679 of 5293 raw rows), ending with only 2614 unique forks despite the
network holding at least 4012 (see "Listing-size variance," below). A
listing that undercounts the network by roughly a third does not explain
*higher* REST/GraphQL usage on its own — the more direct driver is that
320 of the 2614 forks it did find were genuinely divergent (12.2%), well
above the ~69/3692 (1.9%) the original doc measured; `pbakaus/impeccable`
is being forked and pushed to continuously (usernames in the listing
suggest automated/bot forking), so the live divergent fraction is not
stable day to day. `batch_queries` (138) also exceeds the naive
`ceil(2614/50) = 53`, consistent with adaptive-halving retries on failed
chunks, though the specific halving events are not directly observable
(see caveat below).

### impeccable, warm `--touching cli/engine/registry/antipatterns.mjs`

Rate limit before: core 5000, graphql 5000 (15:26:23 UTC); after: core
5000, graphql 5000 (15:44:08 UTC) — both full windows, no reset crossing
this time (~18 min wall-clock).

`[triage] 3999 forks; ~100 REST requests (100 divergent of 1511 resolved by
one GraphQL batch); rate headroom 93%`
`[triage] compare sources: cached 2488 · graphql_batch 1411 · rest 97 ·
diff_fallback 1 · last_touch_skipped 3 (batch: 40 queries, cost 40)`
`[touching] matched 4 (4 partial: file list capped at 300) · unmatched 3995
· unknown 0 · never_pushed 0 · centrality="directory" · last_touch: gated 97
· looked_up 3 · skipped 3 · mismatch 0 · unavailable 0`

Expected: 0 REST; `kaushalrog/impeccable` matched; `tarcisiojr/impeccable-
flutter` present with `touching.partial: false` and ~1383 rows in
`compare_files`; `last_touch: looked_up` only for `behind <= CommitsSince`.

Measured: not 0 REST (97 compares + 1 diff + 3 forced last-touch lookups) —
the network grew between the cold run and this one (2614 → 3999 unique
forks per `acquisition_report`, a live-network change, not a regression),
so 1511 forks were newly pending and needed the batch/compare path.
`kaushalrog/impeccable` is confirmed matched. `tarcisiojr/impeccable-
flutter`: `compare_files` holds exactly 1383 rows
(`sqlite3 -readonly ~/.config/spoon/spoon.db "select count(*) from
compare_files where fork_key like '%tarcisiojr/impeccable-flutter'"` → 1383)
and its cached `t2.files_truncated` is `false` — the unbounded `.diff`
fallback did complete it. It does **not** appear in `--touching` output,
though: its own ahead commits do not touch
`cli/engine/registry/antipatterns.mjs` (`touching.status: unmatched`), and
`Emit()` only prints a matched fork or an unmatched-but-partial one
(`internal/forksops/touching.go:76-79`); a complete, unmatched fork is
correctly silent. `last_touch: looked_up 3` matches `behind <= 5`
(`CommitsSince` from the skip-rate sample); `gated 97` covers the rest.

**Two apparent oddities, resolved by inspection, both non-bugs:**

1. **"matched 4 (4 partial: ...)" does not mean the 4 matches are
   partial.** `TouchSummary.Matched` and `TouchSummary.Partial`
   (`internal/forksops/touching.go:178-192`) are independent per-fork
   tallies — `Partial` counts any truncated-file-list fork regardless of
   match status — and the `[touching]` format string
   (`internal/forksops/stream.go:1038`) prints them adjacently in a way
   that reads as related. Per-record check
   (`jq -c 'select(.touching.status=="matched")'`): the 4 matched forks
   (`marianif/impeccable-native`, `kaushalrog/impeccable`, `jesse-
   merhi/impeccable`, `Vedasheersh/impeccable`) all have
   `touching.partial: false`. The 4 genuinely partial forks are a disjoint,
   unmatched set (`Raudbjorn/impeccable`, `bgausden/impeccable`, `Git-
   Dann/impeccable`, `BespokeAgentics/impeccable-microdots`), emitted
   because `Emit()` also prints an unmatched-but-partial fork (the user
   must be told that answer is incomplete). The count "4" for each is
   coincidental. Not a code bug; the log line's phrasing is worth
   revisiting in a follow-up.
2. **`tarcisiojr/impeccable-flutter` absent from `--touching` output** is
   the correct, documented `Emit()` behavior for a complete-but-unmatched
   fork (see above), not evidence the diff fallback failed — the store
   numbers (1383 rows, `files_truncated: false`) confirm the fallback
   worked.

### impeccable, warm `--touching`, `--no-tree-commit-info`

Rate limit before: core 5000, graphql 5000 (15:47:04 UTC); after: core
5000, graphql 5000 (15:55:35 UTC), ~8.5 min wall-clock.

`[triage] 3293 forks; ~9879+ API requests to enrich at tier 2; rate headroom
96%` (old pre-batch estimate line: with everything cached, `pending` was
empty, so the batch never ran this invocation — not a regression, see
`internal/forksops/stream.go:616-660`)
`[triage] compare sources: cached 3293 · graphql_batch 0 · rest 0 ·
diff_fallback 0 · last_touch_skipped 0 (batch: 0 queries, cost 0)`
`[touching] matched 4 (4 partial: file list capped at 300) · unmatched 3044
· unknown 245 · never_pushed 0 · centrality="directory" · last_touch: gated
0 · looked_up 0 · skipped 0 · mismatch 0 · unavailable 0`

Expected and measured agree: 0 REST, and every `last_touch` counter (gated,
looked_up, skipped, mismatch, unavailable) is exactly 0 — `NoLastTouch`
correctly stops the gate from being built at all
(`internal/forksops/lasttouch.go`), so no `tree-commit-info` traffic can
have occurred (there is no direct request counter for the client; these
zeros are the indirect evidence, as the plan anticipated). The 245
`unknown` forks are the acquisition finding more unique forks this pass
(3052, per `acquisition_report`) than have any cached compare row at all;
not independently confirmed to be specifically deleted/404 forks in this
report (that attribution came from the controller's own process
inspection, not from data this run emits — `--touching` never prints
`unknown` records, so it cannot be checked from the NDJSON alone).

### impeccable, `--no-batch-compare --top 20` (sanity)

First attempt hung: see "A process hang, reclassified," below. Retry under
a 15-minute wall-clock guard (`timeout 900`) completed in 7m43s. Rate limit
before: core 5000, graphql 5000 (16:08:13 UTC); after: core 5000, graphql
5000 (16:15:56 UTC).

`[triage] 2991 forks; ~8973+ API requests to enrich at tier 2; rate headroom
88%` (old estimate-line format, confirmed)
`[triage] compare sources: cached 18 · graphql_batch 0 · rest 2 ·
diff_fallback 0 · last_touch_skipped 0 (batch: 0 queries, cost 0)`

`graphql_batch: 0` confirms the batch never ran under `--no-batch-compare`.
Of the 20 deep-scanned forks, 18 were cache hits and 2 got a fresh REST
compare — roughly "one compare per fork" only for the forks this run
itself had to fetch; the other 18 were already resolved by earlier
batch-enabled runs against this same store. Per-record check
(`jq -c 'select(.t2!=null)|{id,source:.t2.source,ahead:.t2.ahead}'`): every
`ahead>0` record has no `t2.source` (18/18 correct — `t2.source` is only
ever set for the batch/last-touch synthesis path, never an ordinary REST
compare, `internal/forksops/touching.go` doc comment), while cached
zero-ahead records still carry `"graphql_batch"` from whichever earlier
run originally computed them. This is expected cache behavior on a store
this branch's own earlier runs already warmed, not a violation of
`--no-batch-compare`'s contract — the flag disables the batch for *this*
invocation's own work, not the provenance tag on rows it reads back
unchanged.

### litellm scale run: not completed

Two attempts, neither completed; the run was abandoned by controller
decision, not by a code failure on this branch.

- **Attempt 1** (`--refresh`, `SPOON_DEBUG=1`, rate limit before: core 5000,
  graphql 5000 at 16:16:42 UTC): ran ~9.5 minutes, then received `SIGQUIT`
  (external, for diagnosis) and exited with a goroutine dump
  (`ll-hung.err`). The dump initially read as a hang (main goroutine
  blocked "9 minutes" on a channel receive at `cmd/spn/forks.go:909`,
  underneath it a worker blocked inside `doGraphQLWithRetry`'s HTTP/2 round
  trip at `internal/github/graphql.go:596`, no request deadline visible on
  that frame). Re-assessed by the controller from independent process
  observation: not stuck, legitimately slow — GitHub's GraphQL responded in
  roughly 7-11 s per call that day (one measured 502 on litellm), and
  Phase A alone needs on the order of 600 sequential batch queries for
  litellm's ~30000 branches (10735 forks + 19221 side branches), which at
  that per-call latency projects to well over an hour, before accounting
  for adaptive-halving retries on any further 5xx. This report did not
  independently re-measure the 7-11 s per-call figure; the closest
  corroborating data point from this run's own files is the `--no-batch-
  compare` sanity run's T1 listing, which averaged roughly 5 s/page (412 s
  across 81 pages) — same order of magnitude, not an exact match.
- **Attempt 2**: relaunched to retry under a clean background run; killed
  almost immediately (`SIGTERM`, exit 143, zero bytes written to either
  output file) as part of the same controller decision to abandon the
  litellm run rather than let a multi-hour batch run to completion.

No litellm REST/GraphQL delta, `compare_summary`, or wall-clock is
reported here as a result — the "before" baseline from the original
research (`REST 3493, reserve-limited`) stands unchallenged, and this
branch's effect on a network at litellm's scale remains unmeasured within
this verification window. See "Not claimed" in the PR body.

### A process hang, reclassified

The first `--no-batch-compare --top 20` attempt sat 11 minutes at zero CPU
with empty stdout/stderr; `SIGQUIT` produced a goroutine dump
(`nb-hung.err`) showing the same shape as the litellm attempt 1 dump above:
the listing goroutine blocked inside an HTTP/2 round trip on a GraphQL
listing page with no visible deadline
(`internal/github/graphql.go:824` -> `doGraphQLWithRetry` ->
`net/http/internal/http2.(*ClientConn).roundTrip`). This was first read as
a client-side timeout gap; the controller's later, better-informed read
(from watching the litellm attempt live) is that both dumps are the same
non-bug — a single slow GraphQL page/query, not an unbounded wait — and
that read is recorded here as the operative one. Whether a *bounded*
client-side timeout would still be good defense-in-depth against a truly
stuck connection is a separate question this verification run did not
settle either way.

### Listing-size variance (pre-existing, not this branch)

Four T1 acquisitions of the same `pbakaus/impeccable` network, minutes
apart, returned different fork counts and used different methods:

| Run | Method | Pages | Raw rows | Unique | Duplicate |
|---|---|---|---|---|---|
| cold (attempt 1, failed batch) | graphql+rest | 59 | 5028 | 2600 | 2428 |
| cold (attempt 2) | graphql+rest | 64 | 5293 | 2614 | 2679 |
| warm `--touching` | **graphql only** | 81 | 4012 | **4012** | **0** |
| `--touching --no-tree-commit-info` | graphql+rest | 96 | 6893 | 3052 | 3841 |
| `--no-batch-compare` sanity | graphql+rest | 81 | 6143 | 2614 | 3529 |

Three of five runs silently fell back from GraphQL to a GraphQL+REST hybrid
(`fallbackChain: ["graphql","rest"]`) and returned heavy duplication and a
smaller unique-fork count than the one run that stayed pure GraphQL
(`fallbackChain: ["graphql"]`, zero duplicates, 4012 forks — the largest,
and probably closest to the network's true size at listing time). No
error or warning line in any run's stderr explains why the method
downgrades; the acquisition envelope's `method`/`fallbackChain` fields are
the only visible signal. This is T1 listing/pagination behavior
(`internal/github` acquisition, unrelated to this branch's GraphQL batch
compare or `tree-commit-info` work) on a network under continuous churn;
it is reported here as observed pre-existing behavior worth a follow-up,
not something this branch changed or fixed.

### What SPOON_DEBUG actually gates

`SPOON_DEBUG=1` (`cmd/spn/forks.go:702-705`) sets `opts.Logger` to `stderr`
instead of `io.Discard`, which is what makes the `[triage]`/`[touching]`
prose lines (`fmt.Fprintf(logger, ...)` in `internal/forksops/stream.go`)
appear at all — the cold-impeccable run above (attempt 2) was run *without*
it and correctly shows no such lines, only the always-on NDJSON envelopes
(`acquisition_report`, `compare_summary`, `touching`). It does **not**
gate the adaptive-halving `slog.Debug(...)` calls in
`internal/github/batch_compare.go:376,482,489`: those use the `log/slog`
package-level default logger, and nothing in `cmd/spn` raises that
logger's level above the stdlib default (`Info`), with or without
`SPOON_DEBUG`. Halving events are therefore not observable through any
documented flag in this build; the only indirect evidence gathered here is
`batch_queries` exceeding the naive `ceil(pending/50)` chunk count (see the
cold-impeccable table above).

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
