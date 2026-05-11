---
name: using-spn
description: Use when addressing PR review threads on a GitHub PR — replying to review comments, resolving threads after fixes, looping through reviewer feedback, or checking PR mergeability. Applies when the `spn` CLI is on PATH (`command -v spn`). Prefer spn over hand-rolled `gh api graphql` for review-thread work.
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
| Success — streaming (`forks list`) | stdout | NDJSON, one object per line |
| Failure | stderr | `{"error": {"code", "message", "remediation", "retryable", "details"}}` |

Stdout is exclusively success data. A failing command writes nothing to stdout.

**Exit codes:**
- `0` — success
- `2` — user / policy error (`bad_input`, `auth_required`, `auth_scope_missing`, `policy_violation`) — do not retry
- `1` — everything else (`upstream_error`, `rate_limited`, `not_found`, `internal`) — check `error.retryable` before retrying

## Body-Required Policy

The thread JSON has a `requiresBody` boolean. When `true` (any human commenter is present), `spn threads resolve` refuses with `policy_violation` exit 2 unless `--body` is supplied. Bot-only threads (`requiresBody: false`) resolve without a body.

The rule the agent should internalize: **a thread raised by a human gets an explanation when resolved**. Either what was fixed, or why no change was needed.

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

Returns `{title, isDraft, merged, mergeable, mergeStateStatus, reviewDecision, checksState, unresolvedThreads}`. If `mergeStateStatus` is `DIRTY` / `BLOCKED` or `checksState` is `FAILURE`, threads aren't the blocker.

## PR Ref Forms

All commands accept any of:
- `owner/repo#42`
- `https://github.com/owner/repo/pull/42`
- `#42` (resolves owner/repo from the `origin` remote of the current git checkout)

## Common Mistakes

| Mistake | What to do instead |
| --- | --- |
| Hand-rolling `gh api graphql` for review threads | Use `spn` — it handles partial-failure dedup, the body-required gate, and deterministic ordering you'd otherwise have to recreate. |
| `spn threads reply <pr-ref> <id> --body T && spn threads resolve <pr-ref> <id>` (two calls) | `spn threads resolve <pr-ref> <id> --body T` (one atomic call with dedup-safe retry). |
| Bulk-resolving when human reviewers commented | `spn threads resolve-all` skips them into `skipped`; resolve each individually with a real per-thread explanation. |
| Retrying `resolve --body T` with the same body after a partial failure | Check `error.details.comment_posted`; if true, retry with no `--body`. |
| Reading PR thread state via `gh pr view --comments` (HTML/scraped) | `spn threads list` is JSON with stable IDs and the policy flag baked in. |
| Trusting `unresolvedThreads: 0` as "ready to merge" | `spn pr status` also returns `mergeStateStatus` and `checksState` — both must be green. |

## Quick Reference

| Verb | Use |
| --- | --- |
| `spn threads list <pr>` | All unresolved threads as a JSON array (or `--all` for resolved too) |
| `spn threads next <pr>` | Oldest unresolved thread, or `null` |
| `spn threads reply <pr> <id> --body T` | Post a comment without resolving |
| `spn threads resolve <pr> <id> [--body T]` | Resolve (atomically post body if any). Body required for human threads. |
| `spn threads resolve-all <pr>` | Bulk-resolve bot threads; human threads land in `skipped` |
| `spn threads unresolve-all <pr>` | Re-open every resolved thread |
| `spn pr status <pr>` | Mergeability snapshot |
| `spn forks list <repo>` | NDJSON fork enrichment (separate use case, not for PR review) |
