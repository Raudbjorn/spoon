# `spoon threads` — Mergeability Status Header

**Status:** Draft
**Date:** 2026-05-10

## Goal

Above every `spoon threads` invocation's normal output, surface a compact
PR-level status block: mergeability, review decision, check rollup, and
unresolved thread count. The signal: **if anything in the header is red,
resolving threads alone will not get the PR merged.**

## Non-Goals

- Listing the specific files that conflict in a `DIRTY` merge state.
  GitHub's GraphQL and REST APIs do not expose this directly; users can
  see it on the PR page.
- Surfacing per-check details (job names, failure logs). Only the rolled
  up state is shown.
- Branch protection rule inspection. `mergeStateStatus` already
  summarizes whether protection rules are blocking.

## Status Block

Four lines, always in the same order:

```
PR #1 — Add spoon threads subcommand
  Mergeable: ✓ CLEAN
  Reviews:   APPROVED
  Checks:    SUCCESS
  Threads:   0 unresolved
```

If any signal is non-green:

```
PR #1 — Add spoon threads subcommand
  Mergeable: ✗ DIRTY (merge conflicts)
  Reviews:   REVIEW_REQUIRED
  Checks:    FAILURE
  Threads:   3 unresolved
```

A blank line follows the block, then mode output begins.

### Line semantics

| Line | Source | Green | Yellow | Red |
| --- | --- | --- | --- | --- |
| Mergeable | `mergeable` + `mergeStateStatus` | `CLEAN` | `BEHIND`, `UNSTABLE`, `HAS_HOOKS`, `UNKNOWN` | `DIRTY`, `BLOCKED`, `DRAFT` |
| Reviews | `reviewDecision` | `APPROVED` | (none) | `REVIEW_REQUIRED`, `CHANGES_REQUESTED` |
| Checks | `commits[-1].statusCheckRollup.state` | `SUCCESS`, `EXPECTED` | `PENDING` | `FAILURE`, `ERROR` |
| Threads | local count from same query | `0 unresolved` | (none) | `N unresolved` (N>0) |

The leading marker is `✓` for green, `⏳` for yellow, `✗` for red. A
plain ASCII fallback (`[OK]`, `[..]`, `[X]`) is used when stderr is not a
TTY *or* `--no-color` is set.

## Output Routing

| Mode | Block destination | Output destination |
| --- | --- | --- |
| TUI (default) | header panel inside the bubbletea view | — |
| `--reply` / `--resolve` / `--resolve-all` / `--unresolve-all` | stdout, above the success/error line | stdout |
| `--json` | **stderr** (keeps stdout pure JSON) | stdout |
| `--next` | **stderr** (keeps stdout pure JSON or `null`) | stdout |

The `--no-status` flag suppresses the block entirely (any mode). Useful
for tight piping where even stderr is consumed.

For mutation failures (e.g., GraphQL error during resolve), the status
block is still printed first, then the error. This preserves the "above
any other output" contract.

## GraphQL Query

A single round trip fetches status + threads. The existing
`ListThreads` query gains the status fields and now returns both. This
is one network call per invocation, no worse than today.

```graphql
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      title
      isDraft
      merged
      mergeable
      mergeStateStatus
      reviewDecision
      commits(last: 1) {
        nodes {
          commit {
            statusCheckRollup { state }
          }
        }
      }
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes { ... existing ... }
      }
    }
  }
}
```

The status fields are returned on every page request, but we only read
them from the first page. On subsequent paginated pages we ignore the
duplicates (paying a few bytes is cheaper than a second query).

## Code Shape

- A new public type `PullRequestStatus` lives in
  `internal/github/threads.go`:

  ```go
  type PullRequestStatus struct {
      Title             string
      IsDraft           bool
      Merged            bool
      Mergeable         string // MERGEABLE | CONFLICTING | UNKNOWN
      MergeStateStatus  string // CLEAN | BEHIND | DIRTY | BLOCKED | DRAFT | HAS_HOOKS | UNSTABLE | UNKNOWN
      ReviewDecision    string // APPROVED | REVIEW_REQUIRED | CHANGES_REQUESTED | "" (no rule)
      ChecksState       string // SUCCESS | FAILURE | PENDING | ERROR | EXPECTED | "" (no checks)
      UnresolvedThreads int    // populated by the caller after filtering
  }
  ```

- `(*Client).ListThreads` is renamed to `(*Client).FetchPR` returning
  `(PullRequestStatus, []ReviewThread, error)`. The renamer is a single
  search-and-replace across the codebase plus updates to call sites in
  `cmd/spoon/threads.go` and `internal/tui/threads/model.go`.

- The renderer lives in `cmd/spoon/status.go`:
  - `func RenderStatusBlock(w io.Writer, s PullRequestStatus, number int, useColor bool) error`
  - Handles the ✓/⏳/✗ vs ASCII fallback, color rules, and the
    blank-line separator.

- A `useColor` heuristic: if stderr is a TTY and `--no-color` is not
  set, use the glyphs and ANSI color; otherwise the ASCII brackets and
  no color.

## Flags

- `--no-status` — suppress the status header in all modes.
- `--no-color` — already exists at the top-level (`spoon --no-color`);
  reuse the same flag for the threads subcommand.

## TUI Integration

A new header method on the TUI Model. Render four lines at the top of
the view, then a divider, then the existing thread list. When the
`?` help overlay is on, hide the header (the help panel already shows
keybindings).

## Testing

Unit:

- `internal/github/threads_test.go`: parser tests for the expanded
  GraphQL response. Fixture `testdata/threads_status_basic.json` with a
  PR that has `mergeable=CONFLICTING`, `mergeStateStatus=DIRTY`,
  `reviewDecision=REVIEW_REQUIRED`, `checks=FAILURE`, and 2 unresolved
  threads. Assert all fields populated.
- `cmd/spoon/status_test.go`: table tests for `RenderStatusBlock`.
  Cases: all green, all red, mixed, no checks (`ChecksState == ""` →
  shown as `—`), no review rule (`ReviewDecision == ""` → shown as
  `—`), ASCII vs glyph rendering.
- `internal/tui/threads/model_test.go`: existing tests get the wider
  `Model` struct (adds a `status PullRequestStatus` field) but cursor /
  composer / confirm tests don't change.

Integration: live smoke against PR #1 after merge — verify the header
shows the actual mergeable/reviews/checks state.

## Backwards Compatibility

- `--json` schema unchanged. Stdout is still `[ReviewThread, ...]`.
  Agents that consume the JSON do not need to update.
- `--next` schema unchanged.
- New flag `--no-status` is additive; default behavior changes
  (header now printed to stderr) but agents reading stdout are
  unaffected.

## Error Handling

If the status query partially succeeds (e.g., `statusCheckRollup` is
nil because there are no checks on the PR), the affected line shows
`—` instead of erroring. The block prints regardless.

If the whole query fails, the existing error path (exit 1) fires. No
block is printed (we have nothing to print).

## Open Questions Closed in Brainstorm

- Output routing for `--json`/`--next`: **stderr** (chosen A).
- Combined query vs separate: **combined**.
- Always on, opt-out: **opt-out via `--no-status`**.
