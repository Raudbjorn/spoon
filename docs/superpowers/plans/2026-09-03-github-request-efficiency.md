# GitHub request efficiency for `spn forks list`: batched divergence, diff fallback, last-touch skip

Date: 2026-09-03. Branch: `network-wide-distinguished-file-detection` (on top of the shipped `--touching`).
Source proposal: `/home/svnbjrn/.docs/spoon-github-internal-endpoints-tree-commit-info-network-chunk-proposal-20260903.md`.

## Context

### Why

`spn forks list` spends at least one REST `compare` call per fork, plus a `commits/{sha}/pulls` probe for every ahead branch and up to five more compares when the default branch shows no work (`internal/github/branches.go:153-202`, `internal/github/adapter.go:336-339`). On pbakaus/impeccable (3692 forks) the rate reserve stopped the sweep with 1572 forks never compared; on berriai/litellm (10735 forks, 19k listed side branches) only 3493 got a compare. Yet 97% of impeccable's compared forks (2051/2120) and 63% of litellm's (2213/3493) have zero ahead commits: their compare call bought a confirmed nothing.

### Verified today (live, read-only)

| Fact | Evidence |
|---|---|
| Spoon already has a batched GraphQL cross-repo compare | `internal/github/divergent_branches.go:22-30` (`Ref.compare(headRef:"owner:branch")`, "cost 1 for 200 aliases"), used only by the TUI (`internal/tui/branch_divergence.go:37`). Never wired into `forks list`. |
| The same query also yields `behindBy`, the tip commit and its merged PRs | Live query, 5 aliases with `commits(last:1){oid committedDate associatedPullRequests(first:3){number merged baseRepository{nameWithOwner}}}`: cost 1; a NOT_FOUND alias returns `null` with partial data intact (matches `isPartialLookupError`, `divergent_branches.go:152`). |
| `Accept: application/vnd.github.v3.diff` on the compare URL is unbounded | `tarcisiojr/impeccable-flutter`: 1383 files, 10.7 MB, 47 under `lints/`; JSON `files[]` capped at 300 (`forge.CompareFilesCap`). |
| `tree-commit-info` works anonymously, ref-scoped, 404s cleanly | `github.com/{o}/{r}/tree-commit-info/{ref}/{dir}` returns `{entries:{name:{oid,date}}}`; bogus ref and missing repo → 404. |
| Its skip rate on the motivating file is ~0 right now | 24 random impeccable forks: 0 same SHA, 12 differ, 12 predate the directory. Upstream edited `antipatterns.mjs` on 2026-09-02; only forks synced after that can match. |
| Upstream-side gate inputs cost one GraphQL query | `defaultBranchRef.target.history(first:1,path:P)` → last-touch commit C; `compare(headRef: C)` → `behindBy` = commits upstream made after C (5 today). |

### Live store numbers (`~/.config/spoon/spoon.db`, read-only)

| | impeccable | litellm |
|---|---|---|
| forks / with T2 | 3692 / 2120 | 10735 / 3493 |
| ahead == 0 / ahead > 0 | 2051 / 69 | 2213 / 1280 |
| never pushed (pushed_at ≤ created_at) / of those ahead>0 | 3331 / 1 | 5224 / 38 |
| forks with listed side branches / total side branches | 0 / 0 | 5215 / 19221 |
| is_branch_work | 6 | 929 |
| compare_files at the 300 cap | 10 | 58 |

### User decisions (2026-09-03)

- Tier-0 = **both**: GraphQL batched divergence on by default (`--no-batch-compare` to disable) for every authenticated run, **plus** `tree-commit-info` as the proposal's last-touch skip, applied only where the fork can still contain upstream's last-touch commit (the "timing curve", computed exactly, not guessed).
- Diff fallback runs for **every** truncated fork (one extra REST call each).
- Proposal 3 (discovery mode) becomes a **follow-up spec only**.
- `network/meta` and `network/chunk`: not pursued (cookie-gated, no paths, 50-fork window).

### Design in one paragraph

After T1 collection and before dispatch, `Stream` asks the provider for the whole network's divergence in one GraphQL batch (`aheadBy`/`behindBy`/tip/merged-PR per listed branch). A pure selection function reproduces `FetchCompareWithBranchScan`'s branch policy from that data. Zero-ahead forks get a synthesised T2 with no REST call; divergent forks get **one** REST compare on the already-selected branch (no branch scan, no PR probe). If that compare hits the 300-file cap, one unbounded `.diff` fetch replaces the file list. Under `--touching` with literal paths, a divergent fork whose selected branch can contain upstream's last-touch commit C is checked against `tree-commit-info`; equal SHA proves the path untouched and skips the REST compare. Everything falls back to today's path on any failure. Expected REST cost on impeccable: ~120 calls (listing + 69 divergent + ≤10 diffs) instead of ≥3692; GraphQL cost ≈ 30 queries.

## Global constraints

- Go 1.26 per `go.mod`; repo vendors; no new modules (`net/http`, `bufio`, `encoding/json` only).
- Every file-path verdict still sources paths from `forge.T2Data.Diffs`, merge-base-relative. The last-touch skip is the only new negative proof and must carry the named guard comment (see Task 7).
- `Stream` never drops a `Result`; NDJSON contract unchanged; new fields omit-when-not-computed (`cmd/spn/forks.go:1289-1295` pattern). CSV columns untouched.
- Gitea/GitLab providers do not implement the new capabilities and keep today's path.
- Per task: `go test ./<pkg>/... && go vet ./...`. Final: `go build ./cmd/... && go test ./...`. One commit per task; trailer from the session environment.

---

## Task 0: measure batch cost, pin batch size (no product code)

Script in the scratchpad (`gh api graphql`), against pbakaus/impeccable `main`: 50, 100 and 150 aliased `compare` blocks with the nested `commits(last:1){oid committedDate associatedPullRequests(first:5){...}}` shape, reading `rateLimit.cost`. Record the numbers in Task 9's research doc.

**Measured 2026-09-03 (done):** 50 aliases with the nested shape → cost 1, HTTP 200. 75 and 100 with the nested shape, and 100/150 with `commits(last:1)` but no PRs → HTTP 502 (GitHub times out walking history for that many cross-repo compares). 150 aliases of bare `{aheadBy}` is the already-proven shape (`divergent_branches.go:42`). Upstream-side `history(first:1,path:)` + `compare(headRef:<sha>)` → cost 1.

**Ruling:** the batch is two phases. Phase A: `{aheadBy behindBy}` per branch, `batchCompareSize = 150`. Phase B: only for branches with `aheadBy > 0`, `commits(last:1){nodes{oid committedDate associatedPullRequests(first:5){nodes{number merged baseRepository{nameWithOwner}}}}}`, `batchTipSize = 50`. Tip SHAs for zero-ahead branches come from the fork listing (Task 2 extends the listing query with `oid`).

## Task 1: forge types and the branch-selection policy

Files: `internal/forge/types.go` (after `LinearHistoryProvider`, `:375`), new `internal/forge/divergence.go`, `internal/forge/divergence_test.go`.

- Types: `BranchDivergence{Name, TipSHA string; TipCommittedAt time.Time; AheadBy, BehindBy int; UpstreamedPR int}`, `ForkDivergence{Default BranchDivergence; Sides []BranchDivergence; Resolved bool}`, `BranchSelection{Branch string; Ahead, Behind int; TipSHA string; Upstreamed bool; UpstreamedPR int; IsSide bool; NeedsREST bool}`.
- Interfaces: `BatchStats{Queries, Cost int}`; `BatchCompareProvider{ BatchCompare(ctx, forks []T1Data) (map[string]ForkDivergence, BatchStats, error) }` and `ResolvedCompareProvider{ CompareResolved(ctx, fork T1Data, sel BranchSelection) (T2Data, error) }`. Doc comments in the style of `types.go:352-358`. Exported sentinel `ErrBatchCompareUnavailable` (provider has no GraphQL) so callers fall back silently.
- `SelectDivergentBranch(d ForkDivergence) BranchSelection`: pure port of `branches.go:153-202` + `:56-140`: default ahead>0 and not upstreamed wins; else newest side (by `TipCommittedAt`) ahead>0 and not upstreamed; else default ahead>0 upstreamed; else newest upstreamed side; else default with `NeedsREST=false`. `NeedsREST = Ahead > 0`.
- `T2Data` (`types.go:262`): add `CompareSource string` (`""` = provider REST as today, `"graphql_batch"` = synthesised), `FilesComplete bool` (set only by the diff fallback) and `FilesTruncatedReason string` (why a capped list stayed capped after the fallback was attempted). Add the method `func (t T2Data) IsFilesTruncated() bool { return t.FilesTruncated || (len(t.Diffs) >= CompareFilesCap && !t.FilesComplete) }` with a doc comment explaining the pre-flag-rows fallback; callers switch to it in Task 4. All round-trip through `t2_json` (`store.go:1044`, `:1248`) with no schema change.
- `BranchRef` (`types.go:110`): add `TipSHA string`. `T1Data` (`types.go:140`): add `DefaultTipSHA string` (tip of the default branch as listed; empty on the REST listing path). Both are additive to `t1_json`.
- Tests: table over the five policy branches; ties on `TipCommittedAt` stable.

## Task 2: GraphQL batch divergence in the GitHub client

Files: new `internal/github/batch_compare.go`, `internal/github/batch_compare_test.go`; `internal/github/adapter.go` (add `BatchCompare`, compile-time assertion next to `:98`).

- Listing query extension (so zero-ahead branches have a tip SHA without Phase B): `internal/github/graphql.go:86` becomes `defaultBranchRef { name target { ... on Commit { oid committedDate } } }`, `:91-97` refs `target { ... on Commit { oid committedDate } }`, and the same two changes in `buildBatchedForksQuery` (`:560-566`). `gqlRefNode` (`:174`) gains `OID`; `BranchInfo` gains `TipSHA`; `sortBranches` (`:770`) carries it; `T1Extra` (`types.go:194`) gains `DefaultTipSHA`; `forkInfoToT1` (`adapter.go:493`) maps to `BranchRef.TipSHA` and `T1Data.DefaultTipSHA`. Existing graphql tests keep passing (fields additive).
- `BatchTarget{ID, Owner, Name, DefaultBranch, DefaultTipSHA string; DefaultCommittedAt time.Time; Sides []BatchBranch{Name, TipSHA string; CommittedAt time.Time}}`; `FetchBatchDivergence(ctx, baseOwner, baseRepo, baseBranch string, targets []BatchTarget) (map[string]forge.ForkDivergence, BatchStats{Queries, Cost int}, error)`.
- **Phase A** (per Task 0 ruling): one query per `batchCompareSize = 150` branch pairs, document shape exactly as `divergent_branches.go:284-295` but selecting `{ aheadBy behindBy }`. Reuse `gqlString`, `slidingChunks`, `compareBatch`, `doGraphQLWithRetry`, `isPartialLookupError`; nil `ref` map is an error exactly as `divergent_branches.go:313-315`. Zero-ahead branches take `TipSHA`/`TipCommittedAt` from the target (listing values).
- **Phase B**: only branches with `aheadBy > 0`, one query per `batchTipSize = 50`, same document skeleton with `compare(headRef:...) { commits(last:1){ nodes{ oid committedDate associatedPullRequests(first:5){ nodes{ number merged baseRepository{ nameWithOwner } } } } } }`. Fills `TipSHA`, `TipCommittedAt`, `UpstreamedPR`. A Phase B failure for a branch leaves the listing tip and `UpstreamedPR = 0` (treated as genuine work, mirroring `tipUpstreamed`'s fail-open at `branches.go:47-52`).
- `UpstreamedPR` = first PR with `merged` and `EqualFold(baseRepository.nameWithOwner, base)` (mirror `upstreamed.go:80-105`). A fork whose every Phase A alias is `null` gets `Resolved=false`.
- `GHProvider.BatchCompare`: returns `forge.ErrBatchCompareUnavailable` when `!p.client.HasGraphQL()` (`client.go:423`); sides come from `T1Data.Branches` (≤5, `adapter.go:493`); reads the baseline under `p.mu` like `:86-90`; also returns the `BatchStats` through a provider method or a second return so Task 5 can report them (add `BatchStats` to the `forge.BatchCompareProvider` return: `(map[string]ForkDivergence, BatchStats, error)` — Task 1 defines `forge.BatchStats{Queries, Cost int}`).
- Tests: httptest GraphQL stub as `divergent_branches_test.go:16-45` (route Phase A vs Phase B documents by the `commits(last:1)` substring); cases: zero-ahead fork (no Phase B query issued, tip from listing), side-branch winner, merged PR marks upstreamed, partial NOT_FOUND leaves fork unresolved, unresolved upstream ref errors, Phase B 5xx leaves ahead branch with listing tip and `UpstreamedPR = 0`.

## Task 3: `CompareResolved` (one REST compare, no scan, no probe)

Files: `internal/github/adapter.go` (`Compare` at `:301-400`), `internal/github/adapter_test.go` or new `compare_resolved_test.go`.

- Extract `:344-398` into `finishT2(ctx, sourceOwner, sourceRepo, scan BranchScan, fork forge.T1Data) forge.T2Data` (compareToT2 → web-diff patch fill → branch-work flags → upstreamed flags). `Compare` calls it unchanged.
- `CompareResolved`: baseline guard as `:306-318`; `FetchCompare(ctx, base…, fork.Owner, sel.Branch)` once; build `BranchScan{Compare, Branch: sel.Branch, Upstreamed: sel.Upstreamed, UpstreamedPR: sel.UpstreamedPR}`; return `finishT2(...)`. Any error → propagate; the stream falls back to `Compare`.
- Compile-time `var _ forge.ResolvedCompareProvider = (*GHProvider)(nil)`.
- Tests: httptest REST server counting requests: exactly one compare, zero `pulls`, zero side-branch compares; branch-work flags set for a side selection.

## Task 4: unbounded diff fallback for the 300-file cap

Files: new `internal/unidiff/unidiff.go` + `_test.go` (fixture under `testdata/`), new `internal/github/compare_diff.go` + `_test.go`, `internal/github/client.go` (backend struct `:107`, construction `:245-284`, `ensurePool`), `internal/github/adapter.go` (`finishT2`), `cmd/spn/forks.go:1331`, `internal/forksops/touching.go:113`, `internal/tui/*` readers of `FilesTruncated` (grep `CompareFilesCap`).

- `backend.RestDiff *ghAPI.RESTClient`: built beside `Rest` via `newVersionedRESTClient` with `Headers["Accept"] = "application/vnd.github.v3.diff"` (`rest_version.go:21` preserves caller headers). Same transport, same token, same `REST` budget (it is one core call).
- `doGet` (`client.go:456`) gains a private variant selecting `b.RestDiff`; `FetchCompareDiff(ctx, parentOwner, parentRepo, parentBranch, forkOwner, forkBranch string) (files []forge.FileDiff, complete bool, err error)` reads through `io.LimitReader(maxDiffBytes+1)` with `maxDiffBytes = 32 << 20`; over-limit → `complete=false`; 404/406/422 → `(nil,false,nil)` fail-soft (`isNotFound`, new `isNotAcceptable`).
- `unidiff.Parse(r io.Reader, maxPatchBytes int) ([]forge.FileDiff, error)`: streams `diff --git a/X b/Y`, `new file mode`, `deleted file mode`, `rename from/to`, `copy from/to`, `Binary files`, `--- /dev/null`, quoted paths with C escapes; counts `+`/`-` hunk lines (skip `+++`/`---`, `\ No newline`); status names as GitHub's (`added`, `removed`, `renamed`, `copied`, `modified`); `Patch` = hunk text (no `diff --git` header, same as GitHub's `patch`), `PatchSource="compare_diff"`, emptied with `PatchSource="compare_diff_oversize"` above `maxPatchBytes` (64 KiB).
- Wiring in `finishT2`: `if t2.FilesTruncated { files, complete, err := FetchCompareDiff(...); if err == nil && complete && len(files) >= len(t2.Diffs) { carry REST patches over by path; t2.Diffs = files; recompute TotalAdditions/Deletions and MNA via computeMNAFromDiffs (`adapter.go:582`); t2.FilesTruncated = false; t2.FilesComplete = true } else { t2.FilesTruncatedReason records why (error text, "diff exceeds 32 MiB", "406") } }`.
- Readers: replace `r.T2.FilesTruncated || len(r.T2.Diffs) >= forge.CompareFilesCap` at `forks.go:1331` and `touching.go:113` (and any TUI reader found by grepping `CompareFilesCap`) with `T2Data.IsFilesTruncated()` from Task 1.
- Tests: fixture diff with one of each status, a binary file, a quoted path, a 2-file rename; assert counts against `git diff --numstat` of the same fixture; client test asserts the `Accept` header, the byte cap and the 406 path; a `finishT2` test with a 300-entry JSON compare plus a 305-file diff.

## Task 5: stream integration of the batch

Files: `internal/forksops/stream.go` (Options `:100-190`, pre-dispatch `:562-601`, worker `:720-759`, summary `:815-818`), new `internal/forksops/batchcompare.go`, `internal/forksops/stream_test.go` (fakeForge `:21-72`), `internal/forksops/budget.go:42`.

- Options: `NoBatchCompare bool`; `CompareReport *CompareSummary{Cached, Batch, REST, DiffFallback, LastTouchSkipped, BatchQueries, BatchCost int; BatchError string}` (caller-owned like `TouchReport`).
- Pre-dispatch (after `tier` is known, before the `[triage]` estimate at `:570`): when `tier >= 2 && !opts.NoBatchCompare` and `provider` implements `forge.BatchCompareProvider`: `pending` = forks where `eligible(...)` and `opts.CachedT2 == nil || opts.CachedT2(fork) == nil`; call `BatchCompare` (provider chunks); on `ErrBatchCompareUnavailable` do nothing; on other error log `[triage] batch compare degraded: <err>; falling back to per-fork REST` and keep whatever map came back. Store as `divergence map[string]forge.ForkDivergence`.
- Estimate line: when the batch ran, print `~%d REST requests (%d divergent of %d resolved by one GraphQL batch)` instead of `EstimateRequests`.
- Worker, placed with the cache lookup (`:723-728`, before the reserve gate at `:729`): if `r.T2 == nil` and the fork is in `divergence` with `Resolved`: `sel := forge.SelectDivergentBranch(d)`; if `!sel.NeedsREST` → `r.T2 = synthesiseT2(d, sel)` (`Performed:true, AheadCount:0, BehindCount: d.Default.BehindBy, HeadSHA: d.Default.TipSHA, CompareSource:"graphql_batch"`), `T2FromCache=false` so it persists (`forks.go:979-989` writes it with an empty `compare_files` set, which is exact for zero ahead). Otherwise keep `sel` on the worker for the REST step.
- REST step (`:738-739`): `if sel present && provider implements ResolvedCompareProvider → CompareResolved(ctx, fork, sel)` else `provider.Compare(...)`. A `CompareResolved` error falls through to `Compare` once (log at debug), then to today's error handling.
- Summary (`:815`): add `[triage] compare sources: cached %d · graphql_batch %d · rest %d · diff_fallback %d · last_touch_skipped %d (batch: %d queries, cost %d)`.
- Tests (fakeForge gains `batch map[string]forge.ForkDivergence`, `batchErr`, `resolvedCalls *[]string`): zero-ahead forks never hit `Compare`; divergent fork hits `CompareResolved` exactly once with the side branch selected; batch error → every fork goes through `Compare`; `NoBatchCompare` → no `BatchCompare` call; synthesised T2 survives `rescore` and `deriveVisibility` (ahead 0 → same annotations as a REST zero).

## Task 6: `tree-commit-info` client and the upstream last-touch lookup

Files: new `internal/github/treecommitinfo/treecommitinfo.go` + `_test.go`; new `internal/github/last_touch.go` + `_test.go`; `internal/github/client.go` (`EnableWebDiff` neighbourhood `:330-340` for the enable hook).

- `treecommitinfo.Client`: `New(gate func(ctx) error)`; `LastTouch(ctx, owner, repo, ref, dir string) (map[string]string /*name→oid*/, Outcome)`; GET `https://github.com/{o}/{r}/tree-commit-info/{ref}/{dir}` (path-escaped per segment) with `Accept: application/json`, `X-Requested-With: XMLHttpRequest`; no cookies, no auth header, `CheckRedirect` refuses redirects (as `webdiff.go:38-50`); 15 s timeout; body cap 1 MiB. Own token bucket (`newLimiter`-style, 1 req/s, burst 3; not the REST or GraphQL pools) and a run-scoped breaker: first 403/429/5xx disables the client for the run and records the reason. Outcomes: `OK`, `NotFound` (404: ref or path absent), `Disabled`, `Error`. All non-OK mean "no information".
- `Client.FetchPathLastTouch(ctx, owner, repo, branch string, paths []string) (map[string]PathLastTouch{SHA string; CommittedAt time.Time; CommitsSince int}, error)`: one GraphQL query aliasing `history(first:1, path:)` per path under `defaultBranchRef.target` (or `ref(qualifiedName)`), then `compare(headRef: <sha>)` per distinct SHA in the same query (verified: `behindBy` = commits after C).
- `Client.EnableTreeCommitInfo()` / accessor; wired from the CLI flag in Task 8.
- `internal/forge/types.go`: `PathLastTouch{SHA string; CommittedAt time.Time; CommitsSince int}`, `LastTouchOutcome` (`LastTouchOK`, `LastTouchNotFound`, `LastTouchDisabled`, `LastTouchError`) and the optional capability `LastTouchProvider{ PathLastTouch(ctx, paths []string) (map[string]PathLastTouch, error); ForkLastTouch(ctx, fork T1Data, ref, dir string) (map[string]string, LastTouchOutcome) }`. `GHProvider` implements it (`ForkLastTouch` returns `LastTouchDisabled` when the client was never enabled); compile-time assertion as `adapter.go:98`. Task 7 only consumes this interface.
- Tests: httptest server (rewrite transport as `webdiff/fetch_test.go:88-110`) for 200 parse, 404 → NotFound, 429 → Disabled for the rest of the run, redirect refused, body cap.

## Task 7: last-touch skip in the touching path

Files: `internal/pathmatch/pathmatch.go` (add `IsLiteral(pattern) bool` using `hasWildcard`, `:35`), `internal/forksops/stream.go` (worker, between selection and REST), new `internal/forksops/lasttouch.go` + `_test.go`, `internal/forksops/touching.go` (`touchOne`, `:89`; `TouchSummary`, `:78`; stderr line `:860`), `internal/forksops/stream.go` `Result` (`:267`), `cmd/spn/forks.go` (`persistForkSnapshot`, `:979`).

- Preconditions (all): `len(opts.Touching) > 0`, every pattern `IsLiteral`, provider exposes the tree-commit-info client (optional interface `LastTouchProvider{ PathLastTouch(ctx, paths) ...; ForkLastTouch(ctx, fork, ref, dir) ... }` on `GHProvider`), batch resolved the fork with `sel.NeedsREST`.
- Once per run: `upstream := PathLastTouch(ctx, opts.Touching)`; missing path → skip disabled for that path.
- Per fork, the named guard:
  ```go
  // LAST-TOUCH SKIP GUARD. Skip the REST compare only when (1) the selected
  // branch can contain upstream's last-touch commit C for every literal
  // target: sel.Behind <= upstream.CommitsSince (contrapositive of "contains
  // C ⇒ misses at most the commits after C"), and (2) the fork's last-touch
  // OID for the target equals C. Equal OIDs mean no commit reachable from the
  // tip after C touched the path, so the merge-base...tip diff cannot include
  // it. A different or missing OID proves nothing and must fall through to
  // the compare. Never treat a differing OID as a signal (mp3wizard case).
  ```
  Outcomes tallied: `behind_last_touch` (gate failed, no lookup), `lookup` (called), `skipped` (all targets equal), `mismatch`, `unavailable`.
- On skip: `r.T2 = &forge.T2Data{Performed:true, AheadCount: sel.Ahead, BehindCount: sel.Behind, HeadSHA: sel.TipSHA, IsBranchWork: sel.IsSide, ActiveBranch, Upstreamed…, CompareSource:"graphql_batch"}`; `r.T2FilesUnfetched = true` (new `Result` field). `touchOne`: when `r.T2FilesUnfetched` → `TouchUnmatched` with new `Reason: "last_touch"` (needs `Reason` allowed on unmatched; extend the comment at `touching.go:45`). `persistForkSnapshot`: `if r.T2 != nil && !r.T2FromCache && !r.T2FilesUnfetched` (scalars-only T2 must not become a cache hit that reads as "no files changed").
- Never-pushed demotion (`stream.go:590-592`) stays; with the batch, never-pushed forks are usually resolved zero-ahead before dispatch, so the demotion only orders the residual REST work.
- Stderr `[touching]` line gains `last_touch: gated %d · looked_up %d · skipped %d · mismatch %d · unavailable %d`.
- Tests: fake provider implementing `LastTouchProvider`; cases: equal OID skips (no `CompareResolved` call, verdict unmatched/last_touch, not persisted); behind > CommitsSince never looks up; 404 falls through; glob pattern disables the stage; mismatch calls `CompareResolved`.

## Task 8: CLI flags, NDJSON fields, help

Files: `cmd/spn/forks.go` (flag switch `:192-502`, validation `:558-566`, option assembly `:820-830`, NDJSON `:1322-1363`, touching JSON `:1320`, stderr summaries `:1715`), `cmd/spn/main.go:160-200`, `cmd/spn/cli_contract_test.go`, `cmd/spn/forks_degrade_test.go`.

- Flags: `--no-batch-compare` (sets `opts.NoBatchCompare`), `--no-tree-commit-info` (leaves the client disabled; default enabled only when `--touching` is present and every pattern is literal; otherwise silently inert). `--local-branch-scan` keeps its meaning for the fallback path only; document that the batch supersedes it when GraphQL is available.
- NDJSON: `t2.source` when `CompareSource != ""`; `t2.files_unfetched: true` when `T2FilesUnfetched`; `t2.files_truncated` uses the Task 4 method; `touching.reason` also emitted for unmatched last-touch verdicts. Stderr: emit `compare_summary` NDJSON envelope from `opts.CompareReport` beside the existing `touching` summary at `:1715`.
- Help text (`main.go:162-200`) and usage line: add both flags with one sentence each; README `:176-210`: replace the "at least one compare per fork" wording, describe the batch, the diff fallback and the last-touch skip, keep the `--web-diff` paragraph.
- Tests: contract test that `--no-tree-commit-info` without `--touching` is accepted; `--no-batch-compare` accepted; help snapshot if one exists.

## Task 9: docs and research record

Files: `docs/research/2026-09-03-network-wide-distinguishing-file-detection.md` (append to Errata `:383`), new `docs/research/2026-09-03-github-request-efficiency.md`, `docs/superpowers/skills/using-spn-forks/SKILL.md` (`:63-67`, quick-reference `:104-110`), new `docs/superpowers/specs/future/future-work-touching-discovery-mode.md`, copy of this plan to `docs/superpowers/plans/2026-09-03-github-request-efficiency.md`.

- Research doc: the verified-endpoints table from the proposal (tree-commit-info adopted with the containment gate; network/meta+chunk rejected), the live measurements above (batch cost from Task 0, 0/24 skip sample, 1383-file diff), the REST/GraphQL before/after from Task 10, and the reasoning for "GraphQL batch first, tree-commit-info second".
- Errata bullet: "GraphQL pre-filter rejected" (`plan:32`) is reversed: the compare is no longer mandatory for zero-ahead forks.
- Follow-up spec (Proposal 3): `--touching re:<regex>` over the full path list, enabled by the diff fallback; plus two notes: GraphQL `history(first:1,path:)` as a batched alternative transport for the fork-side last-touch lookup, and refreshing `BehindCount` for cached forks through the batch.

## Task 10: live verification and PR body

1. `go build ./cmd/... && go vet ./... && go test ./... && go build -o /tmp/spn ./cmd/spn`.
2. Cold impeccable: `gh api rate_limit --jq '.resources|{core:.core.remaining,graphql:.graphql.remaining}'` before/after `/tmp/spn forks list pbakaus/impeccable --refresh --tier 2 --no-cluster --no-embed > /dev/null 2>imp.err`. Expect: REST delta ≈ 37 listing + 1 parent + ~69 compares + ≤10 diffs (< 150; today ≥ 3692); GraphQL delta ≈ 25–40 queries; no `[triage] degraded` line; `jq -c 'select(.t2==null)'` over stdout is empty; `select(.t2.source=="graphql_batch")|length` ≈ 3600.
3. `--touching cli/engine/registry/antipatterns.mjs` (warm): 0 REST; `kaushalrog/impeccable` matched; `tarcisiojr/impeccable-flutter` now `touching.partial: false` with 1383 files in the store (`sqlite3 ... count(*) from compare_files where fork_key like '%tarcisiojr%'`); `[touching] last_touch` shows `looked_up` only for forks with `behind <= CommitsSince`.
4. Scale: `berriai/litellm --refresh --tier 2 --no-cluster --no-embed`: completes without hitting the reserve; REST ≈ divergent forks (~1300) + 58 diffs; record GraphQL queries and cost per query.
5. `--no-batch-compare` on a small network reproduces today's request pattern (sanity).
6. PR body ends with **Not claimed**: batch cost measured on impeccable only; tree-commit-info anonymous rate limits only observed at ~1 req/s; diff fallback not exercised above 32 MiB; synthesised T2 lacks `BaseSHA` (consumers: `tui/cache_bridge.go:37`, fingerprints unaffected); Gitea/GitLab untouched; ToS posture of `tree-commit-info` is best-effort and can be turned off.

## Rejected / out of scope

- `tree-commit-info` as the **only** tier-0 (0/24 skip on the motivating file; default-branch-only unless ref-scoped; one GET per fork).
- `network/meta`, `network/chunk` (cookie-gated, no path data, 50-fork window).
- ETag/304 on compares (the response changes whenever upstream moves).
- Behind-count refresh for cached forks, `history(path:)` transport, discovery regex: follow-up spec (Task 9).
