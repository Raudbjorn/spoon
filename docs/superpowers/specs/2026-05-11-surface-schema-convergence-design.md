# Surface & schema convergence (Phase D)

**Date:** 2026-05-11
**Status:** design — implementation plan to follow
**Branch:** `convergence-d`

## Goal

Eliminate the silent v1/v2 fork-scoring schema split, retire `spoon`'s non-TUI output paths, and wire up the typed rate-limit error path that `agentio.CodeRateLimited` already anticipates. After this PR the bifurcation is purely about **UX shape** (TUI vs JSON, color vs no-color, NDJSON streaming vs single blob) rather than about which package's scoring functions ran.

A subsequent PR (Phase C) will use the cleaned surface to add new agent-facing verbs (`spn heat score`, `spn cache info`, etc.).

## Non-goals

- No new agent verbs in this PR. Phase C handles `spn heat score`, `spn cache info`, `--include-centrality` enrichments, etc.
- No changes to the `spn threads` / `spn pr` / `spn repo` / `spn embed` verbs already shipped.
- No change to `internal/cluster`, `internal/embed`, `internal/repo` public APIs. Their scoring/clustering/centrality outputs are already v2-shaped.
- No GitLab rate-limit detection in this PR (`*RateLimitError` is added only to `internal/github`; GitLab can mirror it later).
- No commit-author-name fallback resurrection. `forge.UniqueAuthors` (email-fallback) stays canonical.

## Audiences

- **Humans** running `spoon`: TUI only after this PR. No `spoon --json`, no `spoon --csv`. The `spoon threads` / `spoon embed` subcommands stay (they're operational, not data export).
- **Agents and scripters** running `spn`: NDJSON by default for `forks list`; opt into batched CSV via `--csv`. All other verbs unchanged. Rate-limit errors now arrive as a proper `rate_limited` envelope with `retry_after_seconds`, not as generic `upstream_error`.

## Architecture

Two non-TUI Go binaries, one module, shared internals:

```
cmd/
  spoon/          TUI-only (+ threads/embed subcommands). No --json/--csv/-o.
  spn/            unchanged verbs + forks list --csv mode + first-class rate_limited code.
internal/
  agentio/        unchanged (RemediationRateLimited finally gets a caller)
  cluster/        unchanged
  dump/           DELETED
  embed/          unchanged
  forge/          unchanged
  forksops/       wires DetectLoneWolfV2; routes RateLimitError; new "rate_limited" Err.Code
  github/         adds *RateLimitError type; HTTP layer detects 403/429 + headers
  gitlab/         unchanged (out of scope; can mirror later)
  heat/           v1 scoring deleted; Signal / LoneWolfSignal types deleted; HeatResult.Signals/.LoneWolf fields deleted
  repo/           unchanged
  threadsops/     adds OpCodeRateLimited; errors.As routes RateLimitError before the upstream_error fallback
  tui/            tui/app.go, detail.go, export.go migrated to v2 (Components, LoneWolfV2)
```

`go install github.com/svnbjrn/spoon/cmd/{spoon,spn}@latest` still works.

## Surface convergence

After this PR, the non-TUI output landscape:

| Command | Stdout | Use case |
| --- | --- | --- |
| `spoon` (no args or with `owner/repo`) | TUI | interactive humans |
| `spoon threads <pr-ref> …` | unchanged | spoon's existing threads UX |
| `spoon embed status` | human-readable text | sanity check before clustering |
| `spn forks list <repo>` | **NDJSON** (default) | streaming agent loop |
| `spn forks list <repo> --csv` | **batched CSV** with header | spreadsheet/tabular use |
| `spn pr status <pr-ref>` | one JSON object | mergeability snapshot |
| `spn threads {list,next,reply,resolve,resolve-all,unresolve-all}` | unchanged | PR review threads |
| `spn embed {status,pull,models}` | unchanged | clustering preflight |
| `spn repo centrality <repo>` | unchanged | directory centrality |

**Removed flags / commands** on `spoon`:

- `--json` (was: emit v1-scored fork JSON to stdout via `dump.Run`)
- `--csv` (was: emit v1-scored fork CSV to stdout via `dump.Run`)
- `-o` / `--output` (was: redirect `--json`/`--csv` output to a file; no remaining consumer)

**Migration policy:** hard cut. Project is pre-release; expected external `spoon --json` consumers: none. Anyone scripting against the prior schema migrates to `spn forks list`.

### `spn forks list --csv` schema

Fixed column order, always with header row:

```
id,owner,name,url,stars,pushed_at,is_archived,sub_forks,releases,
heat,tier,
t2_ahead,t2_behind,t2_mna,
t3_contributors,t3_commit_span_days,
cluster_name,cluster_score
```

Notes:
- `pushed_at` is RFC3339 UTC.
- `t2_*` cells are blank when the fork wasn't enriched (tier 1 only, beyond top-N, or compare failed).
- `t3_*` cells are blank when tier < 3 or contributors fetch failed.
- `cluster_*` cells are blank when clustering didn't run (no embedder reachable, `--no-cluster`, or fork not in any cluster).
- Per-fork enrichment errors do not appear in stdout CSV. They go to stderr as compact JSON, matching the NDJSON path.
- Fatal stream errors (auth, parent fetch) abort: stdout is empty, exit code 1, structured error envelope on stderr.

`--csv` and the implicit NDJSON are mutually exclusive; passing `--csv` switches to batched mode. The previously promised `--json` flag does not exist on `forks list` — NDJSON is the default.

## Schema convergence

### v1 deletion in `internal/heat`

After the TUI migrates off v1 (see TUI section), remove:

- `func ComputeTier1(...) HeatResult` (weighted-signal T1 scorer)
- `func ComputeTier2(...) HeatResult` (weighted-signal T2 scorer)
- `func DetectLoneWolf(...) *LoneWolfSignal` (legacy lone-wolf wrapper; `DetectLoneWolfV2` is the active path)
- `type Signal struct` (v1 output component)
- `type LoneWolfSignal struct` (v1 lone-wolf output)
- `HeatResult.Signals []Signal` field
- `HeatResult.LoneWolf *LoneWolfSignal` field

**Verification before each deletion:** `grep -rn "heat\.<symbol>\b" --include="*.go" .` returns zero non-test, non-heat-internal callers. Removing each symbol is a separate commit step so a missed reference fails on its own commit.

### Preservation map — what stays intact

Building blocks that future ClassifyHub-style ensembles or fork-provenance work need are explicitly preserved:

| Primitive | Where | Role |
| --- | --- | --- |
| `heat.ComputeMNA` | `internal/heat` | T2 quality measure; feeds `Tier2ParamsV2.MNA` |
| `heat.IsJunkHeavy` | `internal/heat` | T2 junk-heavy flag; feeds `Tier2ParamsV2.IsJunkHeavy` |
| `heat.ClassifyFile`, `heat.FileWeight`, `heat.FileWeightV2`, `heat.WeightedAdditions`, `heat.WeightedDeletions` | `internal/heat` | File-category weighting used by provider adapters and by `ComputeMNA` |
| `heat.DetectLoneWolfV2` | `internal/heat` | v2 lone-wolf archetype detection; this PR finally wires it into `forksops.Stream` |
| `heat.NormalizeDiff` | `internal/heat` | Diff text normalization for embedding |
| `heat.LogNorm`, `DecayNorm`, `InverseLogNorm`, `ClampRatio`, `LogNormRange`, `ExpDecay`, `Percentile` | `internal/heat` | Normalization math; the building blocks weak classifiers need to produce 0..1 probabilities |
| `cluster.BuildWeakSignals`, `SignalsToFeatureVec`, `FileExtDist`, `LangOneHot`, `NameHash`, `DetectArchetype`, `HeuristicLabel` | `internal/cluster/signals.go` | Weak-signal extractors (file-extension, language one-hot, name hash) — exactly the primitives a ClassifyHub-style ensemble would call |
| `internal/github/{tree_for_repo,readme,commits_for_repo}.go` | provider | Tree / README / commit-message data primitives |
| `forge.UniqueAuthors` | `internal/forge/util.go` | Author identity set with email fallback — serves both scalar count (ClassifyHub MetadataClassifier) and provenance identity (fork prospector). |

Nothing on this list is touched by this PR.

### Why `forge.UniqueAuthors` keeps the email fallback

The two `UniqueAuthors` implementations are not identical: `github.UniqueAuthors` falls back to commit `Author.Name`; `forge.UniqueAuthors` falls back to `AuthorEmail`. The name-fallback is dead (no production caller) and inferior for the use cases that matter:

- **Scalar count** (ClassifyHub MetadataClassifier): both implementations produce the same value in all but pathological cases.
- **Identity set** (fork-provenance prospecting): email is a more stable dedup key than git `user.name`. GitHub's API frequently returns null `login` for commits whose email is not linked to an account; falling back to email captures these, falling back to name would dedup poorly when a single developer commits under different `user.name` strings across machines.

`github.UniqueAuthors` is deleted; `forge.UniqueAuthors` stays.

### TUI v2 migration

Three files in `internal/tui/`:

**`tui/app.go`** — replace v1 scoring with `heat.NewScorer(stats)` + `Scorer.ScoreRaw(input)`, mirroring `forksops.Stream`. Per-fork `HeatResult` gets populated with `Components`, `TierScores`, `Trust`, `Penalties`. The lone-wolf computation switches to `heat.DetectLoneWolfV2(t3.Commits, ...)` and populates `Tier3ParamsV2.LoneWolf` before scoring.

**`tui/detail.go`** — the detail pane currently iterates `Heat.Signals[]` (rendering Name / Weight×Value / Raw) and displays a `Heat.LoneWolf` block (showing the v1 label string). Switch to:

- Components loop: `Name Points/Max (Raw)`
- LoneWolfV2 block: `Archetype.String()`, `Strength`, `MeaningfulCommits`, `CommitSpanDays`, `FileSpread`, `RevertCount`, `MsgQualityScore`. The archetype-first display is a small UX upgrade that comes for free.

**`tui/export.go`** — the export feature (TUI's `e`/`E` keystrokes) currently dumps `ExportLoneWolf` derived from v1. Switch to derive from `Heat.LoneWolfV2`. Replace the exported JSON's `signals: [...]` with `components: [...]` and add `tierScores`, `trust`, `penalties` so the TUI export matches `spn forks list`'s per-fork shape.

### `DetectLoneWolfV2` wiring (the side bug)

`forksops.Stream` currently passes `LoneWolf: nil` to `Tier3ParamsV2` with a TODO comment. After this PR, the rescore step calls `heat.DetectLoneWolfV2(r.T2.Commits, ...)` when T2 is populated and stores the result in the rebuilt `ScoreInput`. The TUI work needs this wired anyway; the TUI and `spn` would diverge if it stayed nil in stream but live in app.go.

Behavior change for existing `spn forks list` callers: previously, every fork's `Heat.LoneWolfV2` was `nil`. After this PR, forks at tier ≥ 3 whose T2 has commits will have populated `LoneWolfV2` data. No field disappears — newly populated, never blanked.

## Output contract additions

`agentio` is already shaped for `CodeRateLimited`. This PR makes it actually emit:

### `*github.RateLimitError`

New typed error in `internal/github`:

```go
// RateLimitError is returned by *Client when the GitHub API responds with a
// rate-limit signal: HTTP 403 + X-RateLimit-Remaining: 0, or HTTP 429 with
// Retry-After. ResetAt is the absolute time the limit resets; Remaining is
// the documented per-window remaining count at the time of the failure.
type RateLimitError struct {
    ResetAt   time.Time
    Remaining int
    cause     error
}
```

The HTTP layer in `internal/github/client.go` detects these conditions on every response that fails. Pre-existing rate-limit *header tracking* (in `client.go`) stays; the new error type is an additional failure mode.

### `threadsops.OpCodeRateLimited`

New constant in `internal/threadsops/types.go` (string value `"rate_limited"`, matching `agentio.CodeRateLimited`). Every threadsops op that wraps a github error checks `errors.As(err, &rateLimitErr)` before falling through to `OpCodeUpstream`:

```go
// retrySeconds clamps a possibly-past ResetAt at 0 so agents don't see
// negative wait values when the limit has just rolled over.
retrySeconds := int(time.Until(rl.ResetAt).Seconds())
if retrySeconds < 0 {
    retrySeconds = 0
}
return nil, false, &OpError{
    Code:      OpCodeRateLimited,
    Message:   "rate limit exceeded",
    Retryable: true,
    Details: map[string]any{
        "reset_at":            rl.ResetAt.UTC().Format(time.RFC3339),
        "retry_after_seconds": retrySeconds,
        "remaining":           rl.Remaining,
    },
}
```

### `forksops.Stream` rate-limit handling

Two paths:

- **Fatal** (Parent fetch, ListForks): return a synchronous error wrapping the typed `*RateLimitError`. The cmd/spn handler uses `errors.As` to route to `CodeRateLimited`.
- **Per-fork** (Compare, Contributors): `r.Err = &Error{Code: "rate_limited", Details: { reset_at, retry_after_seconds }}`. Stream continues with subsequent forks (they may succeed if rate limit recovers; otherwise they fail the same way). One compact error line per failing fork on stderr.

### `cmd/spn` translation

`translateOpErr` and `translateResolveErr` add a case for `agentio.CodeRateLimited`:

```go
case agentio.CodeRateLimited:
    resetAt, _ := op.Details["reset_at"].(string)
    seconds, _ := op.Details["retry_after_seconds"].(int)
    rem = agentio.RemediationRateLimited(resetAt, seconds)
    // ... and call .WithRetryAfter(seconds) on the agentio.Error
```

The `agentio.Error` envelope's existing `RetryAfterSeconds` field is finally populated.

## Removed: `internal/dump/`

The whole package goes:

- `dump.go` (the `Run` function)
- `cluster_pipeline.go` (orchestration that `forksops.Stream` already duplicates)
- `cluster_pipeline_test.go`
- Public types: `Options`, `ClusterOptions`, `EnrichedFork` alias, `ReadmeFetcher` alias, `ClusterInputs` alias

The type aliases re-exported `cluster.*` types; deleting them does not affect the underlying types in `internal/cluster`. The `ClusterInputs = cluster.PipelineInputs` alias goes away; consumers (none outside dump itself) reference `cluster.PipelineInputs` directly if needed.

The orchestration logic in `dump.runClusterPipeline` duplicates `forksops.Stream`'s clustering wiring. `forksops.Stream` is the single canonical path after this PR.

## Removed: `github.UniqueAuthors`

The function in `internal/github/compare.go` and its test in `compare_test.go` (if present) — superseded by `forge.UniqueAuthors`. See the "Why forge.UniqueAuthors keeps the email fallback" section above.

## Code-sharing refactor (summary)

| Layer | Change |
| --- | --- |
| `internal/github` | + `*RateLimitError` (new typed error). – `UniqueAuthors`. Client HTTP layer detects rate-limit responses. |
| `internal/threadsops` | + `OpCodeRateLimited` constant. Ops use `errors.As(err, &rateLimitErr)` before the upstream_error fallback. |
| `internal/forksops` | + `DetectLoneWolfV2` wiring in `rescore()`. + rate-limit propagation (fatal sync error or per-fork `Err.Code = "rate_limited"`). |
| `internal/heat` | – `ComputeTier1`, `ComputeTier2`, `DetectLoneWolf`, `Signal`, `LoneWolfSignal`, `HeatResult.{Signals, LoneWolf}` fields. |
| `internal/dump` | DELETED entirely. |
| `internal/tui/{app,detail,export}.go` | migrate from v1 → v2: `Components`, `LoneWolfV2`, `Scorer.ScoreRaw`. |
| `cmd/spoon` | – `--json`, `--csv`, `-o` flags. Help text shrinks. |
| `cmd/spn/forks.go` | + `--csv` batched mode. + rate-limit case in translation helpers. |
| `cmd/spn/{threads,pr,repo,embed}.go` | + rate-limit case in their respective translation paths. |

## Testing strategy

Per-commit gates: `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./cmd/spoon ./cmd/spn` all clean.

Specific test additions:

- `internal/forksops/stream_test.go` — new case where T2 has non-empty Commits; assert `Result.Heat.LoneWolfV2.Detected` is set per `DetectLoneWolfV2` logic.
- `internal/forksops/stream_test.go` — new case where the fake forge returns `*github.RateLimitError` for Compare; assert per-fork `Result.Err.Code == "rate_limited"` and Details carry `reset_at` / `retry_after_seconds`.
- `internal/forksops/stream_test.go` — fatal-rate-limit case: fake forge returns `*RateLimitError` for Parent; `Stream(...)` returns a synchronous error and the channel is closed without any results.
- `internal/threadsops/{ops,resolve,bulk}_test.go` — one test per op that uses a fake client returning `*RateLimitError`; assert `OpError.Code == OpCodeRateLimited` and Details carry the rate-limit fields.
- `cmd/spn/threads_test.go` and friends — black-box: inject an apiFactory whose stub returns the typed error; assert exit 1, stderr envelope has `code: "rate_limited"`, `retryable: true`, `retry_after_seconds` populated, remediation references the reset timestamp.
- `cmd/spn/forks_test.go` — new test for `--csv` mode: stub provider with two forks, assert stdout is one header line + two data lines, all 18 columns present, `pushed_at` is RFC3339, blank cells for missing enrichment.
- `cmd/spn/forks_test.go` — mutex test: when `--csv` is set, NDJSON is not emitted (no per-fork lines on stdout before the CSV).
- `cmd/spoon/main_test.go` — existing assertions on `--json` / `--csv` / `-o` are deleted along with the flags. New assertions: passing those flags yields a "unknown flag" error and exit 2.
- `internal/tui/*_test.go` — existing TUI tests get updated to assert against v2 shapes (`Components` lengths, `LoneWolfV2.Archetype` strings) instead of v1.
- Grep-verification step in CI / pre-commit (one-shot, not a permanent gate): after step 3, no production file references `heat.ComputeTier1`, `heat.ComputeTier2`, `heat.DetectLoneWolf`, `Heat.Signals`, `Heat.LoneWolf`, or `Signal{` outside the heat package's own deletions.

Manual smoke before merge:

- `./spoon` against a small public repo — verify the TUI table renders, detail pane shows v2 components and the new archetype field, export to JSON includes v2 fields.
- `./spn forks list <repo>` against a small public repo — verify NDJSON still streams.
- `./spn forks list <repo> --csv` — verify a clean CSV emerges with the header and right column count.
- `./spn pr status <unauthorized-pr>` — rate-limit detection requires hitting a real limit, which is hard to force; skip or use a recorded fixture.

## Skill updates

The `using-spn` skill ships in two copies that must stay in sync:

- `docs/superpowers/skills/using-spn/SKILL.md` (in-repo, ships with the project)
- `~/.claude/skills/using-spn/SKILL.md` (personal Claude Code config)

This PR adds two new agent-facing capabilities that the skill needs to document.

### 1. `rate_limited` error envelope

Add a new subsection to **Output Contract** (or extend the Exit Codes block) describing the typed rate-limit error:

```markdown
### Rate Limits

When the GitHub API rate-limits a request, the error envelope uses
`code: "rate_limited"`, populates `retry_after_seconds`, and carries
`details.reset_at` (RFC3339 UTC) plus `details.remaining`:

\`\`\`json
{"error": {
  "code": "rate_limited",
  "message": "rate limit exceeded",
  "remediation": "Rate limit exceeded. Wait until <details.reset_at> ...",
  "retryable": true,
  "retry_after_seconds": 1234,
  "details": {"reset_at": "2026-05-11T14:30:00Z", "remaining": 0}
}}
\`\`\`

Retry pattern:

\`\`\`bash
out=$(spn pr status "$PR" 2>/tmp/err.json) || {
  code=$(jq -r .error.code /tmp/err.json)
  if [ "$code" = "rate_limited" ]; then
    wait=$(jq -r .error.retry_after_seconds /tmp/err.json)
    sleep "$wait"
    out=$(spn pr status "$PR")
  fi
}
\`\`\`
```

Also add a row to the Common Mistakes table:

```markdown
| Treating `rate_limited` as `upstream_error` | Check `code` explicitly — `rate_limited` has a known `retry_after_seconds`. Sleeping that long is reliable; blind retry on `upstream_error` may keep hitting the limit. |
```

### 2. `spn forks list --csv` batched mode

Add to the **Quick Reference** table:

```markdown
| `spn forks list <repo> --csv` | Batched CSV with fixed header; switches off NDJSON streaming. Use for spreadsheet/tabular consumers. |
```

And a short subsection under **Output Contract**:

```markdown
### CSV mode

`spn forks list <repo> --csv` collects all enriched forks and emits a single
CSV blob on stdout with a fixed header (`id,owner,name,url,stars,
pushed_at,is_archived,sub_forks,releases,heat,tier,t2_ahead,t2_behind,
t2_mna,t3_contributors,t3_commit_span_days,cluster_name,cluster_score`).
Per-fork enrichment errors still go to stderr as compact JSON. Use this
when downstream tooling expects tabular data; use the default NDJSON
when streaming or jq pipelines fit better.
```

### 3. Section ordering and word budget

The skill currently sits around 1000 words; adding the rate-limit section (~120 words) and CSV section (~50 words) keeps it under 1200. The new sections slot between **Output Contract** and **Body-Required Policy** (rate limit) and at the end of **Output Contract** (CSV mode).

### 4. Personal copy sync

The implementation plan ends with a `cp docs/superpowers/skills/using-spn/SKILL.md ~/.claude/skills/using-spn/SKILL.md` step (outside the git commit since the personal copy isn't under version control). The commit message names this explicitly so future readers know both copies must move together.

## Commit plan

Single PR, branch `convergence-d`, 9 commits:

1. Wire `DetectLoneWolfV2` into `forksops.Stream`.
2. Migrate `internal/tui/{app,detail,export}.go` to v2 heat.
3. Delete v1 from `internal/heat` (`ComputeTier1`, `ComputeTier2`, `DetectLoneWolf`, types, fields).
4. Delete `internal/dump/`.
5. Remove `spoon --json/--csv/-o` from `cmd/spoon/main.go` + tests.
6. Add `spn forks list --csv` batched mode + tests.
7. Wire `OpCodeRateLimited`: `*github.RateLimitError`, threadsops detection, forksops propagation, cmd/spn translation, full test coverage.
8. Delete `github.UniqueAuthors` + its test.
9. Update `using-spn` skill (both copies) with the new `rate_limited` envelope docs and the `--csv` mode docs.

Each commit independently green under `go test ./...`. The PR is reviewable as a sequence of small steps.

## Open questions / future work

- **Fork-provenance verb (Phase C candidate):** add `spn forks list --include-authorship` (or a new `spn forks provenance <repo>` verb) that computes `original_authorship_ratio = |fork_authors − upstream_authors| / |fork_authors|` per fork. Email-fallback in `forge.UniqueAuthors` is the right primitive; the name-fallback in the deleted `github.UniqueAuthors` would silently inflate the ratio for unsigned-commit forks. Out of scope for this PR.
- **`AuthorName string` on `forge.AheadCommit`:** if a future signal extractor wants the commit author's display name (distinct from login and email), the right place is a new field on `forge.AheadCommit`. Out of scope.
- **GitLab rate-limit detection:** mirror `*RateLimitError` in `internal/gitlab` once a GitLab caller hits the limit in practice. Out of scope.
- **`--no-cluster` interaction with `--csv`:** when clustering is off, the `cluster_*` columns are always blank. Acceptable; CSV's stable column set means consumers can ignore the columns.
- **`--no-header` for `--csv`:** not in v1. If a consumer needs raw rows, they can pipe through `tail -n +2`.
- **TUI table-view visual regression test:** none planned in this PR. Manual smoke is the gate.
