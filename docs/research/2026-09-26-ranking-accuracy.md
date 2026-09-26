# Ranking Accuracy: What the llama.cpp Export Got Wrong, and Why

Date: 2026-09-26
Branch: `feat/accuracy-sweep` (on top of `fix/invalid-utf8-patches`, PR #134)
Input: `spoon-export-ggml-org-llama.cpp-2026-09-26.json` (TUI `[E]` export, 27 MB,
gitignored) and an external critique of it
Plan: `docs/superpowers/plans/2026-09-26-ranking-accuracy.md`

## Purpose

A real triage session against `ggml-org/llama.cpp` found spoon's ranking
unusable for picking integration candidates. A keyword grep over fork
descriptions surfaced better forks (`Anbeeld/beellama.cpp`, 1123★) than
anything spoon ranked. A separate 16-fork deep audit (same day) found the
compare-derived statistics misleading on 9 of 16 forks.

This document records what is actually wrong, separating what the export and
source prove from what the critique assumed, and ranks the defects by how much
accuracy each one costs. It is the "why" for the plan.

Evidence labels used below:

- **[export]**: computed from the export file (script output quoted).
- **[live]**: observed against the GitHub API on 2026-09-26.
- **[source]**: read in the source at the cited line.
- **[agent]**: reported by a code-mapping subagent, line not re-read here.

## Headline: 44% of the network was never listed

The critique counted duplicates. The bigger defect is what the duplicates
displaced.

| Quantity | Value | Evidence |
|---|---|---|
| Direct forks GitHub reports (`forks.totalCount`) | 19,981 | [live] |
| Rows in export (`total_count`) | 20,358 | [export] |
| Unique `full_name` in export | 11,171 | [export] |
| Unique forks in `spoon.db` for the repo | 11,171 | [live, local DB] |
| Forks never listed | ~8,810 (44%) | derived |

The fetch returned about as many rows as GitHub has forks, but 9,187 of them
were repeats of forks already seen. Every repeat occupies a slot a missing
fork should have had.

**Root cause [source + live]:** the GraphQL fork listing pages with
`orderBy: {field: STARGAZERS, direction: DESC}` (`internal/github/graphql.go:70`);
the REST fallback uses `sort=stargazers` (`internal/github/forks.go:22`). Most
forks tie at 0 stars, and cursor paging over a heavily tied key is not stable:
pages overlap and others are skipped. The export shows the signature: 94.6% of
duplicated repos have 0★ and the most-starred duplicate has 9★ [export].

**Fix verified [live]:** paging the same repo with
`orderBy: {field: CREATED_AT, direction: ASC}` (100/page, ~7 min) returned
19,981 rows, 19,981 unique. Creation time never changes, so the order is stable.

The fetch already *counts* the repeats (`seen` → `UniqueRows`/`DuplicateRows`,
`graphql.go:253-288` [source]) but still appends every row and never compares
the unique count to `totalCount`.

## Consequences of the duplicates inside the TUI

Everything downstream of `fetchForks` treats each row as a distinct fork:

1. **Heat percentiles are skewed.** `scoreForks` builds population stats from
   the list with repeats (`internal/tui/app.go:1545-1551` [source]), so every
   fork's star/sub-fork percentile and trust multiplier are computed against a
   population padded with 9,187 low-star copies.
2. **Enriched/stub disagreement.** Each copy is dispatched for compare; each
   result updates only the *first* matching row (`processPendingUpdates`,
   `app.go:786-855`, `break` on first `Fork.ID` match [source]). The first copy
   becomes `enriched:true` with T2 heat; the rest stay T1 stubs. 723 repos show
   this split [export], e.g. `SpeederX/siliang-engine`: heat 76.1 (T3) vs 12.6 (T1).
3. **Rank pool padded.** `recomputeShortlist` ranks all rows, repeats included,
   then maps rank by ID so the last copy wins and every copy gets the same rank
   (`internal/tui/shortlist.go:55-72` [source]). 308 rows carry a rank but only
   200 distinct forks [export].
4. **`degraded` and header counts wrong.** `TotalCount = len(rows)` and
   `Degraded = EnrichedCount < TotalCount` (`internal/tui/export.go:389-390`
   [agent]); stub copies can never be enriched, so `degraded` is true even when
   every unique fork was.
5. **Wasted compares (likely, not proven).** Each copy gets its own
   `compareCmd`. Whether a later copy is served by the store cache depends on
   whether the first copy's result was persisted before it ran; under
   concurrency most probably were not.

`spn forks list` shares the fetch (`ListForks` → `FetchForksAuto`,
`internal/github/adapter.go:259-313` [source]), so its NDJSON stream carries
the same repeats and the same missing forks. The store's `ON CONFLICT` upsert
hides them in the DB only.

## Correcting the critique's premise on enrichment

The critique says "only the top-200-by-heat ever get enriched". That is not
what happens:

- **200 is the rank pool** (`rankPoolCap`, `internal/forksops/rank.go:19`
  [agent]); it limits who gets `rank`/`p_score`, not who gets compared.
- The lowest heat among enriched rows is 0, and three of the highest-star
  forks were never enriched: `LostRuins/koboldcpp` (11,870★),
  `TheTom/llama-cpp-turboquant` (2,404★), `Anbeeld/beellama.cpp` (1,123★) [export].

What actually gates enrichment in the TUI:

1. **Rate-limit reserve.** Compares stop at 10% headroom
   (`forksops.ReserveHeadroom`; check at `app.go:1762` [source]).
2. **One REST compare per fork, no batch.** `internal/tui` never calls
   `BatchCompare` [source: grep], which the CLI uses to resolve ahead/behind for
   50 forks per GraphQL query (`internal/forksops/stream.go:634-666` [agent]).
   At ~11k forks the REST budget runs out long before the list does: 2,119 got T2.
3. **Dispatch order is not enforced.** Forks are sorted by `DispatchPriority`,
   but every `compareCmd` is handed to `tea.Batch` at once and they race for a
   channel semaphore (`app.go:1695-1700`, `:1738-1742` [source]). Which forks
   win before the reserve trips is effectively random. The comment at `:1759`
   ("Best-first ordering means the forks already compared are the most
   promising") is false.
4. **Priority barely sees stars.** `ComparePromise` adds `log1p(stars)*0.1`
   (~0.7 for 1123★) against ~10 for a branch newer than upstream
   (`internal/forksops/secretary.go:20-58` [agent]), and branch recency comes
   from only the first 10 branches *alphabetically* (`graphql.go:91` [source]).

CLI-only: `--budget` uses a 1/e secretary rule over listing order (stars
descending), so the highest-star forks all fall in the "observe, don't hire"
window and only re-enter through leftover fill (`secretary.go:79-129` [agent]).

## What the ranking ignores

**Description.** Fetched and stored for every fork, but read only by the
embedding document (`internal/semantic/semantic.go:30-32` [agent]). Heat
(`internal/heat/score.go:28-56`), intent ranking (`internal/embed/querydigest.go:22-40`
— commit subjects + paths only), priors, and the TUI `/` filter
(`internal/tui/filter.go:46-58`, owner/name only by design) all ignore it
[agent]. Intent ranking skips forks with no T2 entirely
(`internal/forksops/stream.go:1477-1482`, `internal/tui/rank.go:134-139` [agent]),
so it structurally cannot find an unenriched fork. The critique's 44
description-keyword hits are exactly this gap.

**Field reliability for unenriched rows.** For a fork without T2, heat is
stars + recency + sub-forks + releases only; `lone_wolf`, `sibling_group`,
`no_ahead` zeroing, `mna`, `sync` are absent; `novelty_score`/`cluster_id` are
README-only [agent]. The export does not distinguish "absent" from "zero"
per field, which is why the critique read them as "proven wrong".

## Compare-stat noise (from the 16-fork audit)

These inflate forks that *were* enriched [agent-mapped, audit-observed]:

| Failure seen in audit | Current handling | Gap |
|---|---|---|
| 4,596 "ahead" = one agent's churn over 15 days | `feature_ratio` from merge-message prefixes (`heat/score.go:84-89`) | Ahead count itself never corrected; no churn/agent detection |
| 238 scraped HF model-card `.md` files dominating a 300-file diff | `ClassifyFile` weights docs 0.5 (`heat/filter.go:78-89`) | No bulk-dump heuristic; embedding `DiffChunk` and paths ignore `ClassifyFile` (`embed/features.go:96-120`) |
| "24 contributors", ~20 of them upstream via merges | `/stats/contributors` taken raw (`github/adapter.go:612-635`) | No upstream-author exclusion; can also break lone-wolf's single-human gate |
| Real work on an abandoned branch; default reset to mirror | `IsBranchWork` set by branch scan (`adapter.go:528-532`) | Display-only; branch scan sees first 10 branches alphabetically |
| Fork name misleading | none | name/description never checked against diff |

## Ranked defect list

| # | Defect | Accuracy cost | Fix size |
|---|---|---|---|
| 1 | Unstable listing: 44% of forks missing, 45% of rows repeats | Critical: missing forks can never rank; repeats skew every score | Small |
| 2 | TUI enrichment: no batch compare, unordered dispatch | High: ~80% unenriched, chosen near-randomly | Medium |
| 3 | Description/topics unused outside embedding; intent rank skips T1-only forks | High: best forks by stated purpose invisible | Medium |
| 4 | `ComparePromise` inputs (stars ~0, alphabetical branches, secretary lockout) | Medium | Small–medium |
| 5 | Compare-stat noise (churn, dumps, upstream contributors) | Medium, on enriched forks | Medium each |
| 6 | Export doesn't mark absent-vs-zero per derived field | Low (interpretation) | Small |

## How "more accurate" gets measured

`spn forks eval` already scores an exported pool against judgments
(`internal/eval/testdata/judgments_*.json` [agent]). The plan builds a
llama.cpp judgment set from the critique's named forks and the 16-fork audit,
and reports recall@K before and after each phase. Without that, accuracy
claims stay anecdotal.
