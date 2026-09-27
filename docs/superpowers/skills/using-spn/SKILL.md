---
name: using-spn
description: Use when addressing PR review threads on a GitHub PR — replying to review comments, resolving threads after fixes, looping through reviewer feedback, applying or counter-proposing suggestion blocks, sweeping outdated threads, checking PR mergeability, or discovering which PRs are open on a repository. Applies when the `spn` CLI is on PATH (`command -v spn`). Prefer spn over hand-rolled `gh api graphql` for review-thread work.
---

# Using `spn` for PR Review Threads

`spn` is an agent-shaped CLI for GitHub PR review threads. Bare JSON on stdout, structured error envelopes on stderr, deterministic exit codes. Built for `jq` pipelines and shell loops.

## When to Use

Reach for `spn` when:
- Addressing review feedback on a GitHub PR (one-shot or in a loop)
- Bulk-handling bot review (Copilot, Gemini code-assist, etc.)
- Checking a PR's mergeability gate
- Posting an explanation comment that resolves a thread atomically

Do NOT use for: GitLab MRs (unsupported), issue comments, PR descriptions, file reviews. spn is GitHub PR review threads only.

**First check:** `command -v spn` — fall back to `gh api graphql` only if absent. If spn IS available, do not hand-roll a GraphQL script.

## Core Loop

```bash
PR="owner/repo#42"
while true; do
  thread=$(spn threads next "$PR")
  [ "$thread" = "null" ] && break

  id=$(jq -r .id <<<"$thread")
  requires_body=$(jq -r .requiresBody <<<"$thread")
  path=$(jq -r .path <<<"$thread")
  line=$(jq -r .line <<<"$thread")
  body=$(jq -r .comments[0].body <<<"$thread")

  # ... address the feedback in code ...

  if [ "$requires_body" = "true" ]; then
    spn threads resolve "$PR" "$id" --body "Addressed in $(git rev-parse --short HEAD)" || break
  else
    spn threads resolve "$PR" "$id" || break
  fi
done
```

`spn threads next` returns the **oldest unresolved thread**, sorted by `(firstCommentCreatedAt, threadID)` for determinism, or `null` when none remain.

For richer per-iteration context, add `--show-code N` and `--verbose` to the `spn threads next` call (see Code context and verbose mode).

## Bulk Sweep for Bot Threads

After you've fixed all the issues in subsequent commits and just need to clear the noise:

```bash
spn threads resolve-all owner/repo#42
# → {"succeeded": [...], "failed": [...], "skipped": [...]}
```

`skipped` carries threads spn refused to bulk-resolve because they have a human commenter — those need individual `spn threads resolve <pr-ref> <thread-id> --body "..."` calls with real per-thread explanations.

## Output Contract

| Case | Where | Shape |
| --- | --- | --- |
| Success — single | stdout | one JSON object (or `null` for `threads next` empty) |
| Success — collection | stdout | JSON array |
| Failure | stderr | `{"error": {"code", "message", "remediation", "retryable", "details"}}` |

Stdout is exclusively success data. A failing command writes nothing to stdout.

`spn threads resolve` returns the resolved **thread object** (same shape as `threads next`), not a bulk envelope; only `resolve-all` / `unresolve-all` return `{succeeded, failed, skipped}`.

Threads may carry these optional fields when the corresponding flag is set:
- `dryRun` — `true` when the mutation was previewed rather than issued
- `isOutdated` (always present after PR #7) — true when the anchored code has shifted
- `suggestions` — non-empty when the thread contains `suggestion` fenced blocks
- `codeContext` — populated by `--show-code N`
- `createdAt`, `updatedAt`, `authorUrl` — populated by `--verbose`

**Exit codes:**
- `0` — success
- `2` — user / policy error (`bad_input`, `auth_required`, `auth_scope_missing`, `policy_violation`) — do not retry
- `1` — everything else (`upstream_error`, `rate_limited`, `not_found`, `internal`) — check `error.retryable` before retrying

### Rate Limits

When GitHub rate-limits a request, the error envelope uses `code: "rate_limited"`, populates `retry_after_seconds`, and carries `details.reset_at` (RFC3339 UTC) plus `details.remaining`:

```json
{"error": {
  "code": "rate_limited",
  "message": "rate limit exceeded",
  "remediation": "Rate limit exceeded. Wait until <details.reset_at> ...",
  "retryable": true,
  "retry_after_seconds": 1234,
  "details": {"reset_at": "2026-05-11T14:30:00Z", "remaining": 0}
}}
```

Retry pattern:

```bash
out=$(spn pr status "$PR" 2>/tmp/err.json) || {
  code=$(jq -r .error.code /tmp/err.json)
  if [ "$code" = "rate_limited" ]; then
    wait=$(jq -r .error.retry_after_seconds /tmp/err.json)
    sleep "$wait"
    out=$(spn pr status "$PR")
  fi
}
```

Note: detection works on REST API paths. GitHub's GraphQL endpoint (used internally by `spn threads list/next/reply/resolve` and the forks-list GraphQL fast path) returns rate-limit hits as the generic `upstream_error` code instead. The error remains `retryable: true` in both cases; the difference is whether `retry_after_seconds` is populated.

### CSV mode

`spn forks list <repo> --csv` collects all enriched forks and emits a single CSV blob on stdout with a fixed 24-column header covering identity, T1, T2, T3, cluster, and shortlist rank fields — the six rank columns (`expected_rank,p_score,p_top_k,p_first,rank_lo,rank_hi`) are empty without `--shortlist`. Per-fork enrichment errors still go to stderr as compact JSON. Use this when downstream tooling expects tabular data; use the default NDJSON when streaming or jq pipelines fit better.

Fork work is a different use case — see the `using-spn-forks` skill for the full flag surface.

## Filter modes

`spn threads list <pr> --filter <mode>` selects which threads to surface.

| Mode | Includes |
| --- | --- |
| `all` | every thread, resolved + unresolved, active + outdated |
| `unresolved` (default) | every unresolved thread |
| `current-unresolved` | unresolved AND not outdated — the most urgent set |
| `resolved-active` | resolved threads whose anchored code is still active |
| `unresolved-outdated` | unresolved threads whose anchored code has shifted (sweep candidates) |

Rule of thumb: `unresolved` (default) for active review work; `current-unresolved` to exclude stale threads; `unresolved-outdated` to find sweep candidates before bulk-resolving.

`--all` is shorthand for `--filter all`. Passing it with a *different* `--filter` value is `bad_input` exit 2 (`--all conflicts with --filter <mode> (drop one)`); `--all --filter all` agrees and is accepted.

## Body-Required Policy

The thread JSON has a `requiresBody` boolean. It is `true` unless *every* comment on the thread is authored by a Bot (an empty thread also counts as `true`), and `spn threads resolve` then refuses with `policy_violation` exit 2 unless `--body` is supplied. Bot-only threads (`requiresBody: false`) resolve without a body.

The rule the agent should internalize: **a thread raised by a human gets an explanation when resolved**. Either what was fixed, or why no change was needed.

`--body-file PATH` is accepted anywhere `--body` is (on `reply` and `resolve`) and reads the body from disk — use it for multi-line explanations rather than fighting shell quoting. `--body` wins when both are given. On `reply`, mixing either with `--suggest` / `--suggest-file` is `bad_input` exit 2 — a reply is prose or a suggestion block, not both.

## Stale-thread sweep

`spn threads resolve-all <pr> --outdated` resolves only threads where `isOutdated: true`. Combined with the body-required policy:

- Bot threads with outdated anchors → resolved into `succeeded`
- Any thread with an active anchor → `skipped` with `reason: "not_outdated"`
- Human-raised threads with outdated anchors → `skipped` with `reason: "requires_body"`

Precedence matters: the outdated filter runs first, so a human-raised thread whose anchor is still active reports `not_outdated`, **not** `requires_body`. Don't branch on the reason to decide whether a human is involved — read `requiresBody` on the thread itself.

Two-step pattern: preview, then act.

```bash
spn threads list "$PR" --filter unresolved-outdated  # what would be swept
spn threads resolve-all "$PR" --outdated              # do it
```

The thread JSON has an `isOutdated` boolean. An outdated thread anchors to code that has since shifted; the comment may be moot. Agents reviewing a thread should check `isOutdated` before deciding whether a fix is still relevant.

### `BulkSkip.Reason` values

| Reason | Meaning |
| --- | --- |
| `requires_body` | Bulk-resolve refused because the thread has a human commenter — resolve individually with `--body` |
| `not_outdated` | Bulk-resolve with `--outdated` refused because the anchor is still active (checked first, so it masks `requires_body`) |

`--outdated` exists on `resolve-all` only; `unresolve-all` rejects it.

## Code context and verbose mode

Two opt-in flags add detail to the thread JSON without changing the default shape. **They are accepted on `list`, `next`, and `resolve` only** — `reply`, `resolve-all`, `unresolve-all`, `apply-suggestion`, and `list-prs` reject them with `bad_input` exit 2.

`--show-code N` adds a `codeContext` block per thread, containing N lines on either side of the anchor (`--show-code=N` is equivalent):

```json
{
  "id": "PRRT_...",
  "path": "internal/foo.go",
  "line": 42,
  "codeContext": {
    "ref": "deadbeef",
    "startLine": 36,
    "endLine": 48,
    "lines": ["...", "...", "..."]
  }
}
```

Cost: one extra `FetchFileContent` call per unique `(path, ref)` pair — the caching wrapper collapses duplicates across threads anchored to the same file.

`--verbose` (alias `-v`) adds `createdAt`, `updatedAt`, and `authorUrl` to each thread. Useful when grounding a reply in commit history or building per-author dashboards.

**`-v` means two different things depending on position.** Before a noun it is the global version flag (`spn -v` prints `spn 0.1.0-dev` and exits 0); after a verb it is `--verbose`. The other global, `--no-color`, must likewise precede the noun — after the noun (or `--`) it is passed to the command unchanged and will be rejected as an unknown flag.

Combined call for a grounded review loop:

```bash
spn threads next "$PR" --show-code 6 --verbose
```

Returns enough context to write a referenced reply without a separate `gh api` call.

## Suggestions

GitHub review threads can embed `suggestion` fenced blocks (```suggestion … ```) that propose replacement code. spn parses them into a per-thread `suggestions` array:

```json
{
  "suggestions": [
    {"commentId": "PRC_...", "body": "newCode()", "applicable": true}
  ]
}
```

`applicable` is true when the thread has a path + line range so `apply-suggestion` can write to the file. Suggestions appear in document order within a comment; pick by position via `--suggestion-index N`.

### Applying a suggestion locally

```bash
spn threads apply-suggestion "$PR" "$id"
```

Writes the replacement to the local file at the thread's anchor. Flags:

| Flag | Effect |
| --- | --- |
| `--suggestion-index N` | Pick the N-th suggestion when the thread has multiple (default 0) |
| `--dry-run` | Print what would change as JSON; do not write the file |
| `--force` | Apply even when the target file has uncommitted changes (default refuses) |
| `--repo-root PATH` | Anchor for relative path resolution (default `pwd`) |

The verb refuses if the path traverses outside the repo root.

### Counter-proposing

`spn threads reply --suggest BODY` (or `--suggest-file PATH`) wraps the body in a suggestion fenced block before posting:

```bash
spn threads reply "$PR" "$id" \
  --intro "Narrower scope here:" \
  --suggest "return ctx.Err()"
```

`--intro TEXT` prefixes a leading line of prose before the suggestion block.

### TUI parity

When working interactively in the spoon TUI, press `[c]` to open `$EDITOR` for a counter-proposal. The wrapper applies `WrapSuggestionBody` and posts via the same path.

### Spoon flag parallel

`spoon --apply-suggestion <id>` (flag) calls into the same `threadsops.ApplySuggestion` that `spn threads apply-suggestion` (verb) uses. The flag/verb difference is the human/agent bifurcation; both produce identical results.

## Dry-run previews

`--dry-run` is available on `resolve`, `resolve-all`, `unresolve-all`, and `apply-suggestion`. Output shape matches the non-dry-run path with an added `dryRun: true` field:

```json
{"succeeded": ["PRRT_..."], "failed": [], "skipped": [], "dryRun": true}
```

On `resolve` the preview is the thread object with **`isResolved` flipped to `true`** alongside `dryRun: true`. Check `dryRun` before believing `isResolved` — nothing was mutated.

Resolving an already-resolved thread is idempotent: exit 0, the thread comes back with `isResolved: true`, no mutation is issued, and no `dryRun` marker appears even under `--dry-run` (the outcome is the same either way). Safe to re-run a loop over a partially-processed PR.

Gating pattern:

```bash
preview=$(spn threads resolve-all "$PR" --outdated --dry-run)
count=$(jq -r '.succeeded | length' <<<"$preview")
if [ "$count" -gt 0 ]; then
  spn threads resolve-all "$PR" --outdated
fi
```

## Partial-Failure Dedup (Critical for Retry Loops)

`spn threads resolve <pr-ref> <thread-id> --body T` is internally two GraphQL mutations: post the comment, then resolve. If the reply succeeds but the resolve mutation fails:

```json
{"error": {
  "code": "upstream_error",
  "retryable": true,
  "details": {"comment_posted": true, "comment_id": "PRC_..."}
}}
```

**The retry must omit `--body`**, otherwise the comment double-posts. Capture the error and check `comment_posted`:

```bash
if ! spn threads resolve "$PR" "$id" --body "$msg" 2>/tmp/err.json; then
  comment_posted=$(jq -r '.error.details.comment_posted // false' /tmp/err.json)
  if [ "$comment_posted" = "true" ]; then
    spn threads resolve "$PR" "$id"   # no --body — comment is already on the thread
  else
    spn threads resolve "$PR" "$id" --body "$msg"   # transient; retry with body
  fi
fi
```

`spn` recognizes when the most recent comment is the agent's own resolution comment and skips the body-required gate on the retry.

## Mergeability Gate

Resolving threads doesn't merge a PR. Check the gate:

```sh
spn pr status owner/repo#42
```

Returns `{title, isDraft, merged, mergeable, mergeStateStatus, reviewDecision, checksState, unresolvedThreads, outdatedThreads, headSHA}`. If `mergeStateStatus` is `DIRTY` / `BLOCKED` or `checksState` is `FAILURE`, threads aren't the blocker.

`outdatedThreads` counts threads whose anchors have shifted across the whole PR — a cheap pre-check for whether a `resolve-all --outdated` sweep has anything to do. `headSHA` is the PR head commit.

## PR Ref Forms

All commands accept any of:
- `owner/repo#42`
- `https://github.com/owner/repo/pull/42`
- `#42` (resolves owner/repo from the `origin` remote of the current git checkout)

(`list-prs` takes `<owner/repo>` without the `#N` suffix, since the whole point is *discovering* PR numbers.)

Each `list-prs` record is `{number, title, author, headBranch, state, url, createdAt, updatedAt}`. `--limit N` caps the result count; `--state` accepts only `open` today — any other value is `bad_input` exit 2 rather than a silent fallback.

## Common Mistakes

| Mistake | What to do instead |
| --- | --- |
| Hand-rolling `gh api graphql` for review threads | Use `spn` — it handles partial-failure dedup, the body-required gate, and deterministic ordering you'd otherwise have to recreate. |
| `spn threads reply <pr-ref> <id> --body T && spn threads resolve <pr-ref> <id>` (two calls) | `spn threads resolve <pr-ref> <id> --body T` (one atomic call with dedup-safe retry). |
| Bulk-resolving when human reviewers commented | `spn threads resolve-all` skips them into `skipped`; resolve each individually with a real per-thread explanation. |
| Retrying `resolve --body T` with the same body after a partial failure | Check `error.details.comment_posted`; if true, retry with no `--body`. |
| Reading PR thread state via `gh pr view --comments` (HTML/scraped) | `spn threads list` is JSON with stable IDs and the policy flag baked in. |
| Trusting `unresolvedThreads: 0` as "ready to merge" | `spn pr status` also returns `mergeStateStatus` and `checksState` — both must be green. |
| Treating `rate_limited` as `upstream_error` | Check `code` explicitly — `rate_limited` has a known `retry_after_seconds`. Sleeping that long is reliable; blind retry on `upstream_error` may keep hitting the limit. |
| Resolving outdated threads individually | Use `spn threads resolve-all <pr> --outdated` to sweep them in one call. Preview with `--dry-run` first. |
| Ignoring `isOutdated` when deciding to reply | Outdated threads anchor to code that has since shifted; the comment may no longer be relevant. Check the field. |
| Calling `gh pr list` then `spn` | Use `spn threads list-prs <owner/repo>` for one round trip in the agent's preferred JSON shape. |
| Assuming a flag is cross-cutting (`--dry-run` on `list`, `--show-code` on `reply`) | The flag surfaces are per-verb; an unsupported flag is `bad_input` exit 2, not a no-op. See the Quick Reference modifier rows. |

## Quick Reference

| Command / modifier | Use |
| --- | --- |
| `spn threads list <pr>` | All unresolved threads as a JSON array (or `--all` for resolved too) |
| `spn threads next <pr>` | Oldest unresolved thread, or `null` |
| `spn threads reply <pr> <id> --body T` | Post a comment without resolving |
| `spn threads resolve <pr> <id> [--body T]` | Resolve (atomically post body if any). Body required for human threads. |
| `spn threads resolve-all <pr>` | Bulk-resolve bot threads; human threads land in `skipped` |
| `spn threads unresolve-all <pr>` | Re-open every resolved thread |
| `spn pr status <pr>` | Mergeability snapshot |
| `spn threads list-prs <owner/repo>` | JSON array of open PRs on a repo. Programmatic alternative to spoon's --interactive picker. |
| `spn threads list <pr> --filter MODE` | Filter modes: all, unresolved, current-unresolved, resolved-active, unresolved-outdated. |
| `spn threads apply-suggestion <pr> <id>` | Apply a thread's suggestion block to the local file. Use --dry-run first. |
| `spn threads reply <pr> <id> --suggest T` | Reply that wraps T in a suggestion fenced block. Optional --intro for context. |
| `--dry-run` | On `resolve`, `resolve-all`, `unresolve-all`, `apply-suggestion` only. Previews the mutation; output gains `dryRun: true`. |
| `--show-code N` / `--verbose` (`-v`) | On `list`, `next`, `resolve` only. Code context around the anchor / extra thread metadata. |
| `spn --no-color <noun> …` / `spn -v` | Globals, and they must precede the noun. `-v` before a noun is *version*, after a verb it is *verbose*. |
| `spn forks list <repo>` | NDJSON fork enrichment (separate use case, not for PR review) |
| `spn forks list <repo> --csv` | Batched CSV with fixed header; switches off NDJSON streaming. Use for spreadsheet/tabular consumers. |
