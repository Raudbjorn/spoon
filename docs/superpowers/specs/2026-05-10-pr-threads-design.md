# `spoon threads` — PR Review Thread Subcommand

**Status:** Draft
**Date:** 2026-05-10

## Goal

Add a new `spoon threads` subcommand that lists, replies to, and resolves
GitHub pull request review threads. The primary consumer is an AI agent
addressing review feedback; humans get a TUI and bulk operations.

## Non-Goals

- GitLab parity. GitLab uses "discussions" with a different API surface;
  the subcommand prints a clear error if the PR ref points at GitLab.
  A forge-level abstraction can come later if there is demand.
- Any operation that creates new threads (`addPullRequestReviewThread`).
  Threads are produced by reviewers; this tool only operates on existing
  ones.
- Any reasoning about whether a comment was actually addressed in code.
  The tool only carries comment text supplied by the caller; the caller
  is responsible for the judgement.

## Subcommand Surface

```
spoon threads <pr-ref> [flags]
```

`<pr-ref>` accepts:

- `owner/repo#42`
- `https://github.com/owner/repo/pull/42`
- `#42` — uses the current `gh` CLI repo context (`git remote get-url origin`)

### Flags

| Flag | Purpose |
| --- | --- |
| (no flag) | TUI listing of unresolved threads |
| `--json` | Print all unresolved threads as a JSON array; no TUI |
| `--include-resolved` | Include resolved threads in `--json` and TUI listings |
| `--next` | Print the single oldest unresolved thread as JSON (or `null`); exit 0 either way |
| `--reply <thread-id> --body <text>` | Append a reply to a thread |
| `--resolve <thread-id>` | Resolve one thread; if any thread author is a `User` (non-bot), `--body` is required (replies, then resolves) |
| `--resolve-all` | Mark every thread on the PR as resolved via GraphQL. No bot/human policy. No `--body` needed. |
| `--unresolve-all` | Inverse of `--resolve-all`. |
| `--body <text>` | Comment body for `--reply` or `--resolve` |
| `--body-file <path>` | Read body from file (alternative to `--body`); `-` reads stdin |

`--reply`, `--resolve`, `--resolve-all`, `--unresolve-all`, `--next`, and `--json`
are mutually exclusive *modes*. `--body` / `--body-file` are value flags
that pair with `--reply` or `--resolve`. The default (no mode flag) is the
TUI.

### Bot vs. human policy

Only `--resolve <id>` (single-thread resolve) enforces the policy:

- Query the thread's comments. If every author has `__typename = "Bot"`,
  the thread can be resolved without `--body`.
- If any author has `__typename = "User"`, `--body` is required. The
  command replies with `body`, then resolves, in that order. If the
  reply succeeds and the resolve fails, the reply is *not* rolled back —
  the comment is already public — and the error message reports the
  partial state.

`--resolve-all` and `--unresolve-all` do **not** apply this policy; they
are deliberate bulk operations.

### Exit codes

- `0` — success, including `--next` returning `null`
- `1` — operational error (auth, network, unknown thread id, etc.)
- `2` — usage error (missing required flag, mutually-exclusive flags, bad pr-ref)

## JSON Schema

`--json` emits an array; `--next` emits a single object or `null`. Each
thread object:

```json
{
  "id": "PRRT_kwDO...",
  "isResolved": false,
  "path": "internal/heat/score.go",
  "line": 42,
  "startLine": null,
  "side": "RIGHT",
  "diffSide": "RIGHT",
  "reviewerType": "User",
  "reviewerLogin": "alice",
  "comments": [
    {
      "id": "PRRC_kwDO...",
      "author": "alice",
      "authorType": "User",
      "body": "this can panic if `xs` is empty",
      "createdAt": "2026-05-10T09:01:23Z"
    }
  ]
}
```

`reviewerType` and `reviewerLogin` come from the **first** comment's
author (the one who opened the thread). This is what the bot/human
policy keys off.

`--next` orders by `comments[0].createdAt` ascending, so an agent loop
sees threads in a stable order:

```
while [ "$(spoon threads owner/repo#42 --next)" != "null" ]; do
  ...
done
```

## Architecture

### New / changed files

```
cmd/spoon/main.go              -- dispatch on argv[1] == "threads"
cmd/spoon/threads.go           -- new: subcommand entry, flag parsing,
                                  pr-ref parsing, JSON output
internal/github/threads.go     -- new: GraphQL queries + mutations
internal/github/threads_test.go-- new: parser + policy tests
internal/tui/threads/model.go  -- new: bubbletea model for the TUI
internal/tui/threads/view.go   -- new: rendering
internal/tui/threads/keys.go   -- new: keybindings
```

### GraphQL operations

All run through the existing `Client.gql` field, which already reuses
`gh` CLI auth.

**Listing** (paginated):

```graphql
query($owner: String!, $name: String!, $number: Int!, $after: String, $states: [PullRequestReviewThreadState!]) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      title
      reviewThreads(first: 100, after: $after, resolvedStates: $states) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          path
          line
          startLine
          diffSide
          comments(first: 100) {
            nodes {
              id
              body
              createdAt
              author {
                __typename
                login
              }
            }
          }
        }
      }
    }
  }
}
```

For `--json` (default unresolved): `states = [UNRESOLVED]`.
For `--include-resolved`: omit `states` argument.
For `--next`: same as default, `first: 1`, no pagination.

**Reply:** `addPullRequestReviewThreadReply` with
`pullRequestReviewThreadId` and `body`.

**Resolve / unresolve single:** `resolveReviewThread` /
`unresolveReviewThread` with `threadId`.

**Bulk resolve / unresolve:** list all thread ids in the target state
(`--resolve-all` operates on unresolved threads; `--unresolve-all` on
resolved threads), then issue one mutation per thread with a small
worker pool (default 4). Concurrent mutations on the same PR work but
should not be parallel-fanned aggressively.

### TUI integration

The TUI mode is a new top-level model invoked when `spoon threads <pr>`
runs without `--json` or any mutation flag. Layout:

```
┌────────────────────────┬────────────────────────────────┐
│ threads (n unresolved) │ thread detail                  │
│  > alice  score.go:42  │ alice  RIGHT  score.go:42      │
│    bot    deps.go:1    │                                │
│    bob    util.go:88   │ "this can panic if xs is empty"│
│                        │                                │
│                        │ [r] reply  [R] resolve         │
└────────────────────────┴────────────────────────────────┘
```

Keybindings:

| Key | Action |
| --- | --- |
| `↑/↓`, `j/k` | Navigate threads |
| `Enter` | Focus detail / open composer |
| `r` | Reply (opens textarea; submit with `Ctrl+S`, cancel with `Esc`) |
| `R` | Resolve current thread (applies bot/human policy; opens composer if body required) |
| `a` | Resolve all (confirm prompt; equivalent to `--resolve-all`) |
| `A` | Unresolve all (confirm prompt) |
| `o` | Open thread in browser |
| `?` | Help |
| `q` | Quit |

The TUI reuses styles and components from `internal/tui/styles.go`.

## Auth

Reuses existing `gh` CLI auth via `go-gh`. On startup the subcommand
verifies the token has `repo` scope (the existing `CheckAuth` already
returns scope info). If absent, exits 2 with a clear message including
the `gh auth refresh -s repo` hint.

## Testing

### Unit tests (no network)

- `internal/github/threads_test.go`:
  - GraphQL response parsing for `reviewThreads` (table-driven, fixture
    JSON in `testdata/`).
  - `__typename` discrimination — `User`, `Bot`, `Mannequin`, `Team`
    cases.
  - Bot/human policy: `requiresBody(thread)` table tests.
  - `--next` ordering: oldest-first by `comments[0].createdAt`.
- `cmd/spoon/threads_test.go`:
  - PR ref parser: `owner/repo#42`, full URL, `#42` (with mocked git
    remote), GitLab URL → error.
  - Mutually-exclusive flag combinations → exit code 2.

### Integration tests

Gated by `SPOON_INTEGRATION=1` plus a `SPOON_TEST_PR` env var pointing
at a sandbox PR the test runner controls. Mirrors the existing
`internal/github/integration_test.go` pattern.

## Error Handling

- Network errors and GraphQL errors bubble up with the response error
  text and exit 1.
- `--reply`/`--resolve` against an unknown thread id → exit 1 with the
  GraphQL error verbatim ("Could not resolve to a node…").
- `--resolve` on an already-resolved thread is a no-op + warning on
  stderr (the GraphQL mutation itself is idempotent and returns the
  current state).
- Partial failure during `--resolve-all`: on the first mutation error,
  print which thread ids succeeded, which failed, and exit 1. Do not
  attempt to roll back.

## Documentation

- README gets a "PR review threads" section with the most common
  invocations and the agent-loop snippet.
- `spoon threads --help` covers every flag.
