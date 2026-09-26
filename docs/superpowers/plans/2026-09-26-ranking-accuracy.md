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

**Rollout note:** the TUI serves a cached fork list for 12 h
(`forkListTTL`); a repo listed before this fix keeps its short list until the
TTL lapses or the user refreshes (`r`, or `spn --refresh`).

**Expect the enriched share to drop until Phase 2:** twice as many forks
listed against the same rate budget and the same unordered dispatch.

**Not in Phase 1:** GitLab lists forks by `last_activity_at`/`updated_at`
(`internal/gitlab/forks.go:107`, `:244`), mutable keys that can shift during
a walk; Gitea paging not checked. Bounded network traversal
(`FetchForksBounded`) already dedups.

## Phase 2 — TUI enrichment that spends budget on the right forks

1. Run `BatchCompare` in the TUI before per-fork REST compares, as the CLI
   does (`internal/forksops/stream.go:634-666`): resolves ahead/behind for
   every fork at ~1 GraphQL point per 50, zero-ahead forks never need a REST
   compare.
2. Replace `tea.Batch` of all closures with an ordered work queue drained by
   N workers, so dispatch order is real and the reserve cuts the *least*
   promising forks. Fix the false "best-first" comment.
3. Export header: `enriched_count`/`total_count` over unique forks; add
   `listed_count` vs `expected_count` from the acquisition report.

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
