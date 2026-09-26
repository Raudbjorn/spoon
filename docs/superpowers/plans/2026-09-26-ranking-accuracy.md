# Ranking Accuracy Plan

Date: 2026-09-26
Analysis: `docs/research/2026-09-26-ranking-accuracy.md`
Branch: `feat/accuracy-sweep`

Phases are ordered by accuracy gained per unit of work. Each phase is its own
PR. Phase 1 is implemented on this branch; later phases are scoped here and
get their own detailed plan when started.

## Phase 0 — Measurement baseline (before Phase 2)

- Build `internal/eval/testdata/judgments_llamacpp.json` from:
  - critique hits: `Anbeeld/beellama.cpp`, `LaurentZuijdwijk/llama.cpp`,
    `Torchit1/llama.cpp`, `Fenix46/llama.cpp`, `k0zi/llama.cpp`, MoE
    expert-cache forks (`Ghimli`, `Andrei-Dr`, `CurtisAccelerate`), MTP forks
    (`ironblock`, `gelubodrug`);
  - the 16-fork audit verdicts (memory: `fork-prospecting-stats-unreliable`).
- Record recall@10/@50/@200 for the current export via `spn forks eval`.
- Re-run after each phase; a phase that does not move recall is questioned
  before merge.

## Phase 1 — Complete, duplicate-free fork listing (implemented on this branch)

**Goal:** every direct fork listed exactly once; shortfall reported.

1. `internal/github/graphql.go` fork query: page by
   `orderBy: {field: CREATED_AT, direction: ASC}` (stable; verified live:
   19,981/19,981 unique on llama.cpp).
2. `FetchForksGraphQL`: skip IDs already `seen` (keep `allForks`/`allExtras`
   parallel, dedup before `annotateDepths`); `onPage` receives only fresh rows.
   After paging, stable-sort by stars desc, ID asc so downstream listing-order
   consumers (secretary rule, dispatch tie-break) keep today's semantics.
3. REST `FetchForks`: `sort=oldest` (stable) + same dedup + same final sort.
4. `forge.AcquisitionReport`: add `ExpectedRows` (GraphQL `forks.totalCount`;
   0 when unknown) so a shortfall (`UniqueRows < ExpectedRows`) is recorded.
   Only `spn`'s stderr acquisition report shows it today; the TUI stores the
   report (`m.acquisition`) but never renders it. Surfacing it is Phase 2.3.
   `RawRows`/`DuplicateRows` stay as diagnostics.
5. TUI backstop: `fetchForks` (`internal/tui/app.go:1470`) drops repeated
   `Fork.ID` so no future provider can reintroduce the enriched/stub split.
6. Tests:
   - GraphQL fetch against a fake server returning overlapping pages →
     unique rows, `DuplicateRows` counted, `ExpectedRows` set, final order
     stars desc.
   - Query text uses `CREATED_AT` (guard against regression to a tied key).
   - TUI: repeated `ForkMsg` → unique `m.forks`.

**Live check (observed once, 2026-09-26, spoon's own `FetchForksAuto` on
ggml-org/llama.cpp):** 20,430 forks returned, 20,430 unique, stars-sorted;
top three `LostRuins/koboldcpp`, `antimatter15/alpaca.cpp`,
`TheTom/llama-cpp-turboquant`. Two surprises:

- GraphQL failed partway and the REST fallback finished the walk
  (`method: graphql+rest`, 100 cross-source repeats dropped). Cause not
  captured (the fallback logs at Debug).
- 20,430 > `ExpectedRows` 19,981. A standalone REST `sort=oldest` walk lists
  20,431 forks; 450 of them are absent from GraphQL, and 12/12 sampled
  return 404 on `GET /repos` and GraphQL (deleted, disabled or spam-hidden
  accounts). REST lists them; GraphQL does not. Follow-up (Phase 2): drop
  REST-only rows that GraphQL cannot resolve, or at least never spend a
  compare on them.

**Rollout note:** the TUI serves a cached fork list for 12 h
(`forkListTTL`); a repo listed before this fix keeps its short list until the
TTL lapses or the user refreshes (`r`, or `spn --refresh`).

**Expect the enriched share to drop until Phase 2:** twice as many forks
listed against the same rate budget and the same unordered dispatch.

**Not in Phase 1:** GitLab lists forks by `last_activity_at`/`updated_at`
(`internal/gitlab/forks.go:107`, `:244`), mutable keys that can shift during
a walk; Gitea paging not checked. Bounded network traversal
(`FetchForksBounded`) already dedups.

## Phase 2 — TUI enrichment that spends budget on the right forks (implemented, branch `feat/accuracy-phase2`)

1. **Batch first.** `startEnrichment` runs the provider's `BatchCompare` over
   every fork without a valid compare, as `spn forks list` does. Zero-ahead
   forks are settled from the batch (same apply/persist path as a REST
   result); divergent forks carry the batch's branch into `CompareResolved`;
   a batch error degrades to per-fork REST. Status line covers the batch
   phase, which has no per-fork progress.
2. **Ordered queue** (`internal/tui/enrich_queue.go`). Commands are still
   launched by `tea.Batch`, but each takes the next fork in
   `DispatchPriority` order only once it holds a semaphore slot, so work
   starts in priority order whichever command wins the race. Exactly one pop
   per command, including on cancel, so `enrichDone` reaches `enrichTotal`.
   Re-enrichment appends and re-sorts the unconsumed tail. Found and fixed on
   the way: a cancelled pass could still win a free slot (`select` picks at
   random) and spend a compare.
3. **Listing shortfall visible.** Header reads "N of M forks listed" when
   `ExpectedRows` exceeds the list; export carries a `listing` block
   (`listed`, `expected`, `repeats_dropped`, `unreachable`, `method`).
   `enriched_count`/`total_count` are already over unique forks since Phase 1.
4. **Vanished forks.** After the batch, forks it could not resolve are
   checked with aliased `repository(owner,name){id}` lookups; a null answer
   (and only that) drops the fork with no compare spent. Header shows
   "N gone", export `listing.unreachable`.

5. **Chunked batch.** The batch runs 500 forks at a time in priority order;
   each chunk's compares start as it resolves, and the next chunk is
   requested alongside them.

**Live measurement (observed once, 2026-09-26, spoon's provider calls on
ggml-org/llama.cpp via a headless harness, not the TUI loop):**

| | Before (export) | Batch-first |
|---|---|---|
| Forks listed | 11,171 | 20,433 (expected 19,985; 250 repeats dropped) |
| Settled with no REST compare (zero-ahead) | – | 17,608 (86%) |
| Need a REST compare | every uncached fork | 2,377 (2,361 divergent + 16 unresolved) |
| Vanished repos detected | – | 448 of 464 unresolved (6 s) |
| Divergence batch | – | 873 queries, GraphQL cost 870, **42 min unchunked** |

All six named forks resolved with real ahead counts (koboldcpp 4,732,
TheTom 532, beellama 1,030, LaurentZuijdwijk 108, PrismML 127, unsloth 286);
koboldcpp and TheTom have the two highest dispatch priorities of the six.
2,377 REST compares fit one 5,000/h window, so nearly every divergent fork
can be compared, where before ~19% were, chosen near-randomly. The 42-minute
unchunked batch is what item 5 fixes; the chunked end-to-end timing in the
TUI was not measured. The GraphQL listing failed partway in both live runs
and the REST fallback finished it; cause not captured (Debug log).

Deferred: rows for vanished forks stay in `spoon.db` (the list persist runs
before the check); a cached reload re-lists them until the batch drops them
again.

## Phase 3 — Use what forks say about themselves

1. Include description, topics and name in the intent-ranking input for every
   fork, T2 or not (today `QueryDigest` is commit subjects + paths, and T1-only
   forks are skipped).
2. TUI `/` filter: opt-in description search (e.g. `/d:kv cache`), keeping the
   owner/name default the current comment defends.
3. Description-vs-upstream novelty as a cheap `ComparePromise` input
   (non-empty, differs from parent, not a template string).

## Phase 4 — Compare priority inputs

1. Stars weight in `ComparePromise` scaled so a 1000★ fork outranks a 0★ fork
   with one fresh branch.
2. Fetch branches by commit date (`refs(orderBy: TAG_COMMIT_DATE)` or the
   divergent-branches sweep) instead of first 10 alphabetical.
3. Secretary rule: run over a shuffled or created-at order, or exempt the top
   stars percentile from the observe window.

## Phase 5 — Compare-stat noise

1. Bulk-dump detection: many same-extension non-source files added in one
   commit → weight 0 in MNA, `DiffChunk`, paths.
2. `DiffChunk`/`buildPaths` honour `ClassifyFile`.
3. Contributors: exclude authors present in upstream history.
4. Ahead-count sanity: flag single-author high-rate churn; expose, don't hide.
5. `IsBranchWork` evidence in score, not display only.

## Phase 6 — Export honesty

Per-row `evidence_tier` (T1/T2/T3) and null (not 0) for derived fields that
had no input, so a consumer can filter on what was actually measured.
