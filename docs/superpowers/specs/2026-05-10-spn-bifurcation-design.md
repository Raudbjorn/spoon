# `spn` — agent-shaped CLI bifurcation

**Date:** 2026-05-10
**Status:** design — implementation plan to follow

## Goal

Ship a second CLI binary, `spn`, whose entire surface is shaped for LLM / agent consumption. Functional parity with `spoon` (forks + heat scoring + PR threads + PR status), but JSON-only, no TUI, no color, no TTY adaptation, with structured errors carrying machine-readable remediation hints. `spoon` keeps its existing CLI unchanged.

## Non-goals

- No removal or deprecation of any spoon flag. `spoon --json` / `--csv` / `spoon threads --json|--reply|--resolve|...` stay exactly as they are.
- No CSV in `spn`.
- No TUI in `spn`.
- No machine-readable command schema (`--commands --json`) in v1.
- No global cross-process rate-limit throttle.
- No new GitHub or GitLab API surface. `spn` only re-presents what spoon already calls.

## Audiences

- **`spoon`** — human at a terminal. TUI-first, with `--json`/`--csv` escape hatches and human-readable text on stderr. Behavior unchanged by this work.
- **`spn`** — LLM / agent running in a non-interactive shell. Stable JSON shapes. Errors carry codes, remediations, and retry hints. Never reads from a TTY, never adapts to one.

## Architecture

Two binaries, one module, shared internals.

```
cmd/
  spoon/          unchanged behavior — existing TUI + --json/--csv/threads
  spn/            NEW — agent-shaped CLI
internal/
  forge/          unchanged
  github/         unchanged
  gitlab/         unchanged
  heat/           unchanged
  tui/            unchanged
  dump/           unchanged (still backs spoon --json/--csv)
  threadsops/     NEW — hoisted from cmd/spoon/threads.go
  forksops/       NEW — hoisted fetch/score/enrich pipeline, streaming-capable
  agentio/        NEW — JSON writers, error envelope, error-code constants, remediation patterns
```

`go install github.com/svnbjrn/spoon/cmd/spn@latest` installs the new binary.

## Command surface

Verb-per-action under three nouns. PR refs accept the same forms as today: `owner/repo#42`, full GitHub URL, or `#42` (resolved from `origin` inside a git checkout).

### `spn threads`

| Command | Stdout |
| --- | --- |
| `spn threads list <pr-ref> [--all]` | JSON array. Unresolved by default; `--all` includes resolved. Each thread carries a `requiresBody` boolean (see below). |
| `spn threads next <pr-ref>` | One JSON object or `null`. Sort is `(firstCommentCreatedAt ASC, threadID ASC)` — explicit tiebreaker. |
| `spn threads reply <pr-ref> <thread-id> --body T \| --body-file PATH` | JSON of the new comment. |
| `spn threads resolve <pr-ref> <thread-id> [--body T \| --body-file PATH]` | JSON of the resolved thread. See body-required policy and partial-failure handling below. |
| `spn threads resolve-all <pr-ref>` | `{"succeeded":[...], "failed":[...], "skipped":[...]}`. Human-raised threads land in `skipped` with `reason: "requires_body"`. |
| `spn threads unresolve-all <pr-ref>` | Same `{succeeded, failed, skipped}` shape; `skipped` is currently always empty for this verb but the field is present for symmetry. |

### `spn pr`

| Command | Stdout |
| --- | --- |
| `spn pr status <pr-ref>` | One JSON object: `PullRequestStatus` (title, mergeability, review decision, checks rollup, unresolved count). |

### `spn forks`

| Command | Stdout |
| --- | --- |
| `spn forks list <repo> [--tier 1\|2\|3] [--top N] [--heat-weights PATH] [--bot-allowlist L] [--refresh] [--forge github\|gitlab] [--forge-host H]` | **NDJSON** — one scored fork object per line, terminated by EOF. Exit-zero = complete; exit-nonzero = stream was truncated. |

No `--json` flag — `spn` is JSON by default, no other modes. No `--no-color` — never emits ANSI regardless of TTY.

### Body-required policy

Inherited from spoon and tightened for bulk operations.

- **Single resolve.** `spn threads resolve <id>` fails with `policy_violation` if `requiresBody` is true and `--body` is empty.
- **Bulk resolve.** `spn threads resolve-all` resolves only threads where `requiresBody` is false (bot-only threads). Threads with any human commenter go in `skipped` with `reason: "requires_body"`. The agent must resolve each individually with its own explanation. This is stricter than spoon's `--resolve-all`, which silently bulk-resolves human threads.

`requiresBody` is derived from the existing `ReviewThread.RequiresBody()` rule (true unless every comment in the thread is from a Bot). It is surfaced in the thread JSON so agents don't have to re-derive the policy client-side.

### Resolve idempotency

`spn threads resolve` on an already-resolved thread → exit 0, stdout = JSON of the thread's current state. No stderr noise. Agents re-running the same command get the same shape they got on first success.

### Resolve partial-failure semantics

`spn threads resolve <id> --body T` is two GraphQL calls under the hood: post the comment, then resolve. The atomic case (both succeed) returns the resolved thread JSON. The partial-failure case (comment posted, resolve failed) returns:

```json
{"error": {
  "code": "upstream_error",
  "message": "comment posted but resolve failed: <provider error>",
  "remediation": "Retry: spn threads resolve <pr-ref> <thread-id>  (omit --body; the comment is already posted)",
  "retryable": true,
  "details": {"comment_posted": true, "comment_id": "PRC_..."}
}}
```

For the retry to satisfy the body-required policy without re-posting, `threadsops.Resolve` recognises one case as "body already satisfied": when `requiresBody` is true and the most recent comment on the thread is authored by the current authenticated user. This avoids double-posting on agent retry.

The "authored by the current authenticated user" check requires knowing the agent's login. Today `internal/github/adapter.go` sets `AuthInfo.Username` to the empty string — populating it (via `gh api user` or an equivalent lookup at startup) is a prerequisite for this dedup. If username resolution fails, fall back to permissive behavior: skip the body-required gate when the most recent comment was posted within the last 60 seconds. Either path is documented in `threadsops.Resolve` with the chosen signal recorded in `details.body_satisfied_via`.

## Output contract

### Success → bare JSON on stdout

| Command class | Shape |
| --- | --- |
| Single-resource reads (`pr status`, `threads next`, `threads reply`, `threads resolve`) | One JSON object, or `null` for `next` when nothing remains |
| Collection reads (`threads list`) | One JSON array |
| Bulk writes (`threads resolve-all`, `threads unresolve-all`) | `{"succeeded": [...], "failed": [...], "skipped": [...]}` |
| Streaming (`forks list`) | NDJSON — one JSON value per line, EOF when done |

No envelope around success. Stdout is exclusively success data — failing commands write nothing to stdout.

### Failure → structured JSON on stderr + nonzero exit

```json
{"error": {
  "code": "policy_violation",
  "message": "thread has a human commenter; --body is required",
  "remediation": "Resolve with an explanation: spn threads resolve owner/repo#42 PRRT_kwDO... --body \"<what you fixed, or why no change was needed>\"",
  "retryable": false,
  "details": {"thread_id": "PRRT_kwDO...", "pr_ref": "owner/repo#42"}
}}
```

| Field | Purpose |
| --- | --- |
| `code` | Stable category string (vocabulary below) |
| `message` | What went wrong — human and agent-readable |
| `remediation` | Executable next step — names a command or set of options, not a concept |
| `retryable` | `true` for `rate_limited` and `upstream_error`; `false` for everything else (`bad_input`, `auth_required`, `auth_scope_missing`, `policy_violation`, `not_found`, `internal`) |
| `retry_after_seconds` | Optional. Set when known (rate-limit reset). |
| `details` | Free-form machine-readable context (PR ref, thread ID, reset timestamp, `comment_posted`, etc.) |

### Error vocabulary

| Code | Exit | When |
| --- | --- | --- |
| `bad_input` | 2 | Malformed flag, bad PR ref, missing required argument |
| `auth_required` | 2 | Not authenticated (`gh auth login` missing) |
| `auth_scope_missing` | 2 | Missing OAuth scope (e.g., `repo`) |
| `policy_violation` | 2 | Body required for human reviewer; bulk on otherwise-eligible call rejected by policy |
| `not_found` | 1 | PR / thread / repo doesn't exist or is inaccessible |
| `upstream_error` | 1 | Provider API call failed |
| `rate_limited` | 1 | Provider returned 403/429 with rate-limit headers |
| `internal` | 1 | Unexpected error / bug |

### Remediation patterns

Patterns live in `internal/agentio/remediations.go` as named constants with placeholders the call site fills in.

| Code | Pattern |
| --- | --- |
| `bad_input` | "Run `spn <noun> <verb> --help` to see accepted forms. PR refs accept `owner/repo#42`, a full URL, or `#42` from inside a git checkout." |
| `auth_required` | "Authenticate with `gh auth login` (GitHub) or set `GITLAB_TOKEN` (GitLab), then retry." |
| `auth_scope_missing` | "Refresh your token with the required scope: `gh auth refresh -s <scope>`, then retry." |
| `policy_violation` (body) | "Resolve with an explanation: `spn threads resolve <pr-ref> <thread-id> --body \"<what you fixed, or why no change was needed>\"`." |
| `policy_violation` (bulk → human threads) | "Some threads need individual responses. List them with `spn threads list <pr-ref>`, then resolve each with `spn threads resolve <pr-ref> <id> --body \"...\"`. Bulk-resolve will not touch human-raised threads." |
| `not_found` | "Verify the PR / repo / thread exists and that your token has access. PR refs and IDs are case-sensitive." |
| `upstream_error` | "Provider API failed. Retry in a few seconds. If persistent, check the provider's status page." |
| `rate_limited` | "Rate limit exceeded. Wait until `<details.reset_at>` (<retry_after_seconds>s), then retry. Authenticate (`gh auth login`) for a higher unauthenticated limit." |
| `internal` | "Unexpected error. Re-run with the same arguments; if it persists, report at the project's issue tracker with the full stderr output." |

### TTY behavior

`spn` never adapts to a TTY. No ANSI, no glyphs, no color on stdout or stderr regardless of `isatty`. (`spoon`'s TTY adaptation in `RenderStatusBlock` and the TUI is unchanged.)

### Schema versioning

Not introduced in v1. Field names are treated as stable within a spoon major version. If breaking changes are needed later, a `$schema_version` field will be introduced at that time.

## Code-sharing refactor

All extraction is **behavior-preserving** for spoon — every flag, every exit code, every stderr/stdout split that exists today must still exist after.

### `internal/threadsops`

Hoist from `cmd/spoon/threads.go`. Callable from both binaries.

```go
package threadsops

// Pure parsing
func ParsePRRef(s, fallbackOwner, fallbackRepo string) (owner, repo string, number int, err error)
func DetectRepoContext() (owner, repo string) // origin → (owner, repo) for GitHub

// Operations — return data + categorized error, no IO formatting
type OpError struct{ Code, Message string; Retryable bool; Details map[string]any }

func List(ctx context.Context, client *github.Client, owner, repo string, number int, includeResolved bool) (github.PullRequestStatus, []ReviewThreadWithPolicy, *OpError)
func Next(ctx context.Context, client *github.Client, owner, repo string, number int) (github.PullRequestStatus, *ReviewThreadWithPolicy, *OpError)
func Reply(ctx context.Context, client *github.Client, threadID, body string) (github.ThreadComment, *OpError)
func Resolve(ctx context.Context, client *github.Client, owner, repo string, number int, threadID, body string) (*ReviewThreadWithPolicy, *OpError)
func ResolveAll(ctx context.Context, client *github.Client, owner, repo string, number int, skipHumanThreads bool) (BulkResult, *OpError)
func UnresolveAll(ctx context.Context, client *github.Client, owner, repo string, number int) (BulkResult, *OpError)

// Embeds github.ReviewThread + RequiresBody field for JSON output
type ReviewThreadWithPolicy struct {
    github.ReviewThread
    RequiresBody bool `json:"requiresBody"`
}

type BulkResult struct {
    Succeeded []ReviewThreadWithPolicy `json:"succeeded"`
    Failed    []BulkFailure            `json:"failed"`
    Skipped   []BulkSkip               `json:"skipped"`
}
```

`cmd/spoon/threads.go` shrinks to: argv parsing → call into `threadsops` → format using the existing text/glyph emitters (`RenderStatusBlock`, `emitJSON`, `emitNext`). The `skipHumanThreads` parameter on `ResolveAll` is false for spoon (preserves current "bulk accepts no body" semantics) and true for spn (the policy tightening). `cmd/spoon`'s observable behavior does not change.

`cmd/spn/threads.go` calls the same `threadsops` functions and formats with `agentio`. Tiebreaker in `Next` is `(firstCommentCreatedAt, threadID)` ascending.

### `internal/forksops`

Hoist the fetch → score → enrich pipeline from `cmd/spoon/main.go::createProvider` and `internal/dump/dump.go::Run`. The orchestration becomes streaming-capable.

```go
package forksops

type Options struct {
    Refresh      bool
    Tier         int
    TopN         int
    BotAllowlist map[string]bool
    HeatWeights  map[string]float64
}

type Result struct {
    Fork forge.T1Data
    T2   *forge.T2Data
    T3   *forge.T3Data
    Heat heat.HeatResult
    Err  *OpError
}

func Stream(ctx context.Context, provider forge.Forge, owner, repo string, opts Options) (<-chan Result, error)
```

`internal/dump` continues to back `spoon --json/--csv`. For v1 it can either call `Stream` and buffer the results into the existing batched output shape, or remain as-is — to be decided in the implementation plan. Either way, spoon's JSON/CSV output stays identical.

`spn forks list` ranges over `Stream` and writes one line per `Result` via `agentio.WriteNDJSON`. If `Result.Err` is non-nil for a single fork, that fork is skipped and an NDJSON line of the form `{"error": {"code": "upstream_error", "message": "...", "details": {"fork": "owner/repo"}}}` is written to **stderr** (one line, not the full envelope, to bound noise on a many-fork stream). Streaming continues; the overall command still exits zero on EOF. Only fatal errors (auth failure, parent fetch failure, ctx cancel) terminate the stream early with a nonzero exit and a single full error envelope on stderr.

### `internal/agentio`

```go
package agentio

// Output
func WriteJSON(w io.Writer, v any) error
func WriteNDJSON(w io.Writer, v any) error
func WriteNull(w io.Writer) error

// Errors
type Code string
const (
    CodeBadInput     Code = "bad_input"
    CodeAuthRequired Code = "auth_required"
    CodeAuthScope    Code = "auth_scope_missing"
    CodePolicy       Code = "policy_violation"
    CodeNotFound     Code = "not_found"
    CodeUpstream     Code = "upstream_error"
    CodeRateLimited  Code = "rate_limited"
    CodeInternal     Code = "internal"
)

type Error struct {
    Code              Code
    Message           string
    Remediation       string
    Retryable         bool
    RetryAfterSeconds *int
    Details           map[string]any
}

func NewError(code Code, message, remediation string) *Error
func (e *Error) WithDetails(d map[string]any) *Error
func (e *Error) WithRetryAfter(seconds int) *Error
func (e *Error) Emit(stderr io.Writer) int // writes JSON + returns exit code
```

`retryable` is derived from `code` by default (see vocabulary table above), overridable per-instance. Exit-code mapping is keyed off `code` so callers don't pass an integer separately.

### What spoon does not change

- `cmd/spoon/main.go` flag parsing
- `cmd/spoon/threads.go` exit codes and stdout/stderr split
- `RenderStatusBlock` glyph/text rendering
- `internal/dump` output shapes (the existing `--json` / `--csv` schemas)
- `internal/tui` (zero touch — `spn` never reaches it)

Existing `cmd/spoon` tests pass unmodified.

## Testing strategy

- `internal/threadsops` — table-driven tests on `ParsePRRef`, `Next` tiebreaker, `ResolveAll` skip-human policy, idempotent resolve on already-resolved threads, partial-failure detection. Reuse the testdata JSON fixtures already in `internal/github/testdata`.
- `internal/forksops` — exercise `Stream` against a fake `forge.Forge`. Verify per-fork errors don't kill the stream, fatal errors do.
- `internal/agentio` — unit tests on JSON / NDJSON shape, error envelope schema, exit-code mapping. Snapshot-test a few representative errors so the schema is easy to review by diff.
- `cmd/spn` — end-to-end tests that exec the binary against the fake forge and assert the exact bytes on stdout / stderr and the exit code. Cover at least:
  - `threads list` happy path
  - `threads next` returning `null`
  - `threads resolve` body-required policy violation (with full error envelope)
  - `threads resolve` partial failure (comment posted, resolve failed)
  - `threads resolve-all` skipping human threads
  - `pr status` happy path
  - `forks list` NDJSON streaming with a per-fork error mid-stream
- `cmd/spoon` — existing tests run unmodified. A focused regression test confirming `spoon --resolve-all` still resolves human threads (the legacy permissive behavior).

## Open questions

None blocking. Items deferred:
- Stable command schema export for tool discovery.
- Cache surface (the existing `~/.cache/spoon/` cache is shared; `spn --refresh` works the same as `spoon --refresh`). No spn-specific cache invalidation primitives in v1.
- GitLab parity for `spn threads ...` — current threads operations are GitHub-only. `spn` inherits that constraint. GitLab PR/MR thread support is out of scope for this work.
