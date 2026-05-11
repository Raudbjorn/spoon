# gh-toolkit surface extensions

**Date:** 2026-05-11
**Status:** design — implementation plan to follow
**Branch:** `gh-toolkit-cli-gaps`
**Depends on:** PR #7 (`integrate-gh-toolkit`) merging to main first; this branch will rebase onto main once #7 lands.

## Goal

Close three discrete gaps left after PR #7:

1. `internal/github.ListOpenPRs` is reachable only via spoon's `--interactive` TUI picker. Agents using `spn` cannot programmatically discover which PR is open on a repo. Add `spn threads list-prs <owner/repo>` to expose the data as JSON.
2. The TUI's footer lists `[a] apply-suggestion` but lacks a counter-propose keybinding. `WrapSuggestionBody` exists and is already used by `spn threads reply --suggest`, but TUI users have no path to author a counter-proposal from the interactive view. Add `[c] counter-propose` that launches `$EDITOR`, wraps the result, and posts via `threadsops.Reply`.
3. The `using-spn` skill is significantly out of date — eight capabilities shipped on PR #7 have zero documentation. Bring the skill current with all eight plus the two new pieces above.

## Non-goals

- No reconciliation of the spoon `--apply-suggestion <id>` flag vs `spn threads apply-suggestion` verb shape. The asymmetry is intentional per the human/agent bifurcation; the skill documents the parallelism as a feature.
- No new GitHub or GitLab API surface. `ListOpenPRs` already exists on `*github.Client`.
- No new `spoon` flags. Spoon humans already have the `--interactive` TUI picker for PR discovery.
- No TUI parity for any other operation. `[c]` closes only the counter-propose gap; other TUI shortcuts stay as-is.
- No new fields on existing JSON schemas — the new verb's JSON shape is independent.
- No rename of the existing `using-spn` skill. It stays at the same path; its scope narrows to PR review by extracting the fork-related sections into a new sibling skill. Existing references to `using-spn` keep working.

## Architecture

```
cmd/
  spn/threads.go     + doThreadsListPRs   (new verb handler)
                     + listPRsFn          (overridable test seam)
internal/
  github/            unchanged (ListOpenPRs already exists)
  threadsops/        unchanged
  tui/threads/
    model.go         + counter-propose state + 'c' key handling
                     + launchEditor       (overridable test seam)
    view.go          + [c] in footer + help text
    keys.go          + Counter binding
docs/superpowers/skills/
  using-spn/SKILL.md         narrowed to PR review; gains 8 new topics from PR #7
                             plus the new list-prs verb and [c] TUI key.
                             Loses the Embedding & Centrality section and the
                             CSV mode subsection (those migrate to the new skill).
  using-spn-forks/SKILL.md   NEW. Holds everything fork-flavored: forks list
                             NDJSON + CSV, embed status/pull/models, repo
                             centrality, preflight pattern before clustering.
```

Both skill files also live at `/home/svnbjrn/.claude/skills/<skill>/SKILL.md` for personal Claude Code use, synced via `cp` (outside git, per existing convention).

## Surface additions

### `spn threads list-prs <owner/repo>`

**Stdout** — JSON array, one object per open PR:

```json
[
  {
    "number": 42,
    "title": "Add forks novelty cluster",
    "headRefName": "convergence-d",
    "baseRefName": "main",
    "url": "https://github.com/owner/repo/pull/42",
    "isDraft": false,
    "author": "user-or-bot",
    "createdAt": "2026-05-09T12:00:00Z",
    "updatedAt": "2026-05-11T08:30:00Z"
  }
]
```

Empty array when no open PRs.

**Flags**

| Flag | Default | Effect |
| --- | --- | --- |
| `--limit N` | 0 (no cap) | Truncate the response to the first N entries after server-side ordering |
| `--state open\|closed\|all` | `open` | If `ListOpenPRs` does not currently accept a state parameter, scope this PR to `open` only and treat `--state` as a future-work addition (reject other values with `bad_input`) |

**Argument shape:** `<owner/repo>` only — no `#N` suffix. Reject malformed input with `bad_input` and a remediation pointing at the right form.

**Error contract:** standard `agentio.Error` envelope on stderr. `auth_required` for missing token, `not_found` for nonexistent repo, `rate_limited` via the typed-error path already wired through `cmd/spn/threads.go::translateOpErr`.

**Test seam:** `var listPRsFn = func(ctx, client, owner, repo) ([]github.PullRequest, error)` so handler tests can inject a fake without going through `apiFactory`. Mirrors the `detectFn` / `pullFn` pattern in `cmd/spn/embed.go`.

**Dispatch:** add `case "list-prs"` to `runThreadsWith` in `cmd/spn/threads.go`.

### TUI `[c] counter-propose` key

**Behavior**

1. User presses `c` while focused on a thread.
2. TUI suspends Bubble Tea via `tea.ExecProcess` and launches an editor. Resolution order: `$EDITOR` → `$VISUAL` → `vi`. The temp file is created in `$TMPDIR` (or `os.TempDir()`) with a `.md` suffix so editors that key syntax highlighting off extension behave reasonably.
3. The temp file is pre-populated with a 3-line comment header:
   ```
   # Counter-propose for thread <thread-id> on <owner/repo>#<number>
   # Lines beginning with # are stripped.
   # Empty content cancels.
   ```
4. After the editor exits, the TUI reads the file, strips `#`-prefixed lines and trailing whitespace, and if the remainder is empty: status line shows "counter-propose cancelled", no API call, temp file deleted. Otherwise: the body is wrapped via `WrapSuggestionBody` and posted via `threadsops.Reply`.
5. On success: status line shows "counter-propose posted (comment PRC_…)". The thread's `Suggestions` field is re-populated from the response so the new suggestion is visible without a manual refresh.
6. On error: status line shows the wrapped error message, the temp file is preserved at a deterministic path printed in the status (so the user can retry without losing work). Pattern: `/tmp/spoon-counter-propose-<thread-id>-<timestamp>.md`.

**Footer update:** `[c] counter-propose` is added next to `[a] apply-suggestion`. Both keys are unconditionally visible (matching the recent fix where `[a]` is always shown).

**Help text:** one new line under the existing `[a]` line: `c - Counter-propose: opens $EDITOR for a replacement, wraps the content in a suggestion block, and posts it as a reply.`

**Keybinding:** new entry in `internal/tui/threads/keys.go`:

```go
Counter = key.NewBinding(
    key.WithKeys("c"),
    key.WithHelp("c", "counter-propose"),
)
```

**Caveat:** `tea.ExecProcess` is the supported pattern for editor launch in Bubble Tea. The TUI already uses subprocess-launching via `cli/browser` (the `o` key opens the browser); the editor pattern is similar in shape but more complex because we need to wait for the editor to close and then resume. The implementation plan will reference the `tea.ExecProcess` example from the Bubble Tea docs for the exact wiring.

## Skill split

The existing `using-spn` skill (~1300 words) covers two distinct user journeys — PR review threads and fork discovery — that share an output contract but otherwise have no overlap in tasks, verbs, or audience needs. This PR splits them.

### Two skills, one contract

- **`using-spn`** (existing path, content narrowed): PR review threads, `pr status`, suggestions, dry-run, filter modes, code context, the new `list-prs` verb, and a one-line note about the TUI's new `[c]` counter-propose key.
- **`using-spn-forks`** (new): `forks list` (NDJSON + CSV), `embed status` / `pull` / `models`, `repo centrality`, the preflight pattern before clustering.

Both skills duplicate the spn-wide conventions inline: stdout-is-JSON, stderr-is-envelope, exit codes, rate-limit envelope. Duplication is correct here because each skill loads independently; a cross-reference would break when only one is loaded. The duplicated content is short and stable.

### Frontmatter — tighter trigger descriptions

**`using-spn`** (revised):

> Use when addressing PR review threads on a GitHub PR — replying to review comments, resolving threads after fixes, looping through reviewer feedback, applying or counter-proposing suggestion blocks, sweeping outdated threads, checking PR mergeability, or discovering which PRs are open on a repository. Applies when the `spn` CLI is on PATH (`command -v spn`). Prefer spn over hand-rolled `gh api graphql` for review-thread work.

**`using-spn-forks`** (new):

> Use when discovering or scoring forks of a repository, comparing fork novelty, clustering forks with the Ollama embedder, managing the embedding model (probe / pull / list), or fetching directory centrality data for a repo. Applies when the `spn` CLI is on PATH (`command -v spn`). Output is NDJSON streaming by default; pass `--csv` for batched tabular output.

### What moves out of `using-spn`

Two sections are removed (they migrate verbatim to `using-spn-forks`, plus light edits for the new skill's frontmatter context):

- **Embedding & Centrality** (the existing section near the bottom — preflight pattern, `spn embed status`/`pull`/`models`, `spn repo centrality owner/repo`)
- **CSV mode** subsection inside Output Contract

The corresponding Quick Reference rows for `forks list`, `forks list --csv`, `embed status | pull | models`, and `repo centrality` move out too. Common Mistakes rows about forks (if any — verify when editing) also move.

### What gets added to `using-spn` (the PR-narrowed version)

Eight capabilities from PR #7 plus the two new bits from this PR. Organized as five new top-level sections plus targeted updates to existing ones.

#### New top-level section: Filter modes

Placed after **Output Contract**, before **Body-Required Policy**. Table of the five `FilterMode` values with one-line semantics each:

```markdown
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
```

#### New top-level section: Stale-thread sweep

Placed after **Body-Required Policy**.

```markdown
## Stale-thread sweep

`spn threads resolve-all <pr> --outdated` resolves only threads where `isOutdated: true`. Combined with the body-required policy:

- Bot threads with outdated anchors → resolved into `succeeded`
- Bot threads with active anchors → `skipped` with `reason: "not_outdated"`
- Human-raised threads (any state) → `skipped` with `reason: "requires_body"`

Two-step pattern: preview, then act.

\`\`\`bash
spn threads list "$PR" --filter unresolved-outdated  # what would be swept
spn threads resolve-all "$PR" --outdated              # do it
\`\`\`

The thread JSON has an `isOutdated` boolean. An outdated thread anchors to code that has since shifted; the comment may be moot. Agents reviewing a thread should check `isOutdated` before deciding whether a fix is still relevant.

### `BulkSkip.Reason` values

| Reason | Meaning |
| --- | --- |
| `requires_body` | Bulk-resolve refused because the thread has a human commenter — resolve individually with `--body` |
| `not_outdated` | Bulk-resolve with `--outdated` refused because the anchor is still active |
```

#### New top-level section: Code context and verbose mode

```markdown
## Code context and verbose mode

Two opt-in flags add detail to the thread JSON without changing the default shape.

`--show-code N` adds a `codeContext` block per thread, containing N lines on either side of the anchor:

\`\`\`json
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
\`\`\`

Cost: one extra `FetchFileContent` call per unique `(path, ref)` pair — the caching wrapper collapses duplicates across threads anchored to the same file.

`--verbose` adds `createdAt`, `updatedAt`, and `authorUrl` to each thread. Useful when grounding a reply in commit history or building per-author dashboards.

Combined call for a grounded review loop:

\`\`\`bash
spn threads next "$PR" --show-code 6 --verbose
\`\`\`

Returns enough context to write a referenced reply without a separate `gh api` call.
```

#### New top-level section: Suggestions

```markdown
## Suggestions

GitHub review threads can embed `suggestion` fenced blocks (\`\`\`suggestion … \`\`\`) that propose replacement code. spn parses them into a per-thread `suggestions` array:

\`\`\`json
{
  "suggestions": [
    {"body": "newCode()", "index": 0, "commentId": "PRC_..."}
  ]
}
\`\`\`

### Applying a suggestion locally

\`\`\`bash
spn threads apply-suggestion "$PR" "$id"
\`\`\`

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

\`\`\`bash
spn threads reply "$PR" "$id" \\
  --intro "Narrower scope here:" \\
  --suggest "return ctx.Err()"
\`\`\`

`--intro TEXT` prefixes a leading line of prose before the suggestion block.

### TUI parity

When working interactively in the spoon TUI, press `[c]` to open `$EDITOR` for a counter-proposal. The wrapper applies `WrapSuggestionBody` and posts via the same path.

### Spoon flag parallel

`spoon --apply-suggestion <id>` (flag) calls into the same `threadsops.ApplySuggestion` that `spn threads apply-suggestion` (verb) uses. The flag/verb difference is the human/agent bifurcation; both produce identical results.
```

#### New top-level section: Dry-run previews

```markdown
## Dry-run previews

`--dry-run` is available on `resolve`, `resolve-all`, `unresolve-all`, and `apply-suggestion`. Output shape matches the non-dry-run path with an added `dryRun: true` field:

\`\`\`json
{"succeeded": ["PRRT_..."], "failed": [], "skipped": [], "dryRun": true}
\`\`\`

Gating pattern:

\`\`\`bash
preview=$(spn threads resolve-all "$PR" --outdated --dry-run)
count=$(jq -r '.succeeded | length' <<<"$preview")
if [ "$count" -gt 0 ]; then
  spn threads resolve-all "$PR" --outdated
fi
\`\`\`
```

#### Update existing Quick Reference table

Five new rows appended (forks/embed rows move out):

```markdown
| `spn threads list-prs <owner/repo>` | JSON array of open PRs on a repo. Programmatic alternative to spoon's --interactive picker. |
| `spn threads list <pr> --filter MODE` | Filter modes: all, unresolved, current-unresolved, resolved-active, unresolved-outdated. |
| `spn threads apply-suggestion <pr> <id>` | Apply a thread's suggestion block to the local file. Use --dry-run first. |
| `spn threads reply <pr> <id> --suggest T` | Reply that wraps T in a suggestion fenced block. Optional --intro for context. |
| `spn threads * --dry-run` / `--show-code N` / `--verbose` | Cross-cutting modifiers for mutations / context-aware reads / extra metadata. |
```

#### Update existing Common Mistakes table

Three new rows:

```markdown
| Resolving outdated threads individually | Use `spn threads resolve-all <pr> --outdated` to sweep them in one call. Preview with `--dry-run` first. |
| Ignoring `isOutdated` when deciding to reply | Outdated threads anchor to code that has since shifted; the comment may no longer be relevant. Check the field. |
| Calling `gh pr list` then `spn` | Use `spn threads list-prs <owner/repo>` for one round trip in the agent's preferred JSON shape. |
```

#### Light-touch updates to existing sections

- **Core Loop**: one-line note pointing at `--show-code N` / `--verbose` as opt-in enrichments for richer per-iteration context.
- **Output Contract**: bullet-list the new fields agents may see — `isOutdated bool`, `suggestions []Suggestion`, `codeContext *CodeContext` — with one-sentence descriptions.
- **PR Ref Forms**: add a parenthetical "list-prs takes `owner/repo` without the `#N` suffix" for cross-reference.

### The new `using-spn-forks` skill

Greenfield skill at `docs/superpowers/skills/using-spn-forks/SKILL.md`. Word budget: ~700 words (smaller than the PR skill — forks has fewer verbs and less policy surface).

Section structure:

```markdown
---
name: using-spn-forks
description: Use when discovering or scoring forks of a repository, comparing fork novelty, clustering forks with the Ollama embedder, managing the embedding model (probe / pull / list), or fetching directory centrality data for a repo. Applies when the `spn` CLI is on PATH (`command -v spn`). Output is NDJSON streaming by default; pass `--csv` for batched tabular output.
---

# Using `spn` for Fork Discovery and Clustering

`spn` enumerates and scores forks of a GitHub or GitLab repository. Per-fork JSON includes a "heat" score (additive 0–100 across T1/T2/T3 signals), cluster label when clustering ran, and optional T2/T3 enrichment.

## When to Use

- Discovering interesting forks of an upstream repo
- Bulk-scoring forks for triage
- Clustering forks by novelty (requires a reachable Ollama embedder)
- Per-directory centrality for a repo (which directories drive activity)

Do NOT use for: PR review work (see the `using-spn` skill for that) or non-fork repository analysis.

**First check:** `command -v spn`. If absent, fall back to hand-rolled `gh api` calls; otherwise prefer spn for streamed, scored output.

## Core Loop

\`\`\`bash
spn forks list owner/repo | jq -c '. | select(.heat > 60)'
\`\`\`

Streaming NDJSON: one fork per line, ordered by heat-score descending after sorting completes.

## CSV Mode

\`\`\`bash
spn forks list owner/repo --csv > forks.csv
\`\`\`

Collects all enriched forks and emits a single CSV blob on stdout with a fixed header (18 columns covering identity, T1, T2, T3, and cluster fields). Per-fork enrichment errors still go to stderr as compact JSON. Use this when downstream tooling expects tabular data; use the default NDJSON when streaming or jq pipelines fit better.

## Output Contract (shared with the PR-review skill)

| Case | Where | Shape |
| --- | --- | --- |
| Success — NDJSON | stdout | one JSON object per line |
| Success — CSV | stdout | header + rows |
| Failure | stderr | `{"error": {...}}` envelope |

Stdout is exclusively success data. Per-fork enrichment errors go to stderr (NDJSON-shaped); fatal errors go to stderr (full envelope) and exit non-zero.

**Exit codes:** 0 success; 2 user/policy error (do not retry); 1 transient/upstream (check `error.retryable`).

## Rate Limits

GitHub rate-limit hits produce a `rate_limited` envelope with `retry_after_seconds`. See the `using-spn` skill's Rate Limits section for the full envelope shape and retry pattern — identical here.

Detection works on REST API paths. GitHub's GraphQL endpoint (used by the forks-list GraphQL fast path) returns rate-limit hits as the generic `upstream_error` code instead.

## Embedding pipeline (preflight)

`spn forks list` enriches forks with cluster labels when an Ollama embedder is reachable. Three sibling verbs let an agent set that up or query the underlying data:

\`\`\`sh
spn embed status                   # JSON: running, endpoint, installed[], recommended[]
spn embed models                   # JSON: known-good models (name, dim, size, codeAware)
spn embed pull <model>             # NDJSON progress: one {model,phase,pct} per line until phase=="done"
spn repo centrality owner/repo     # JSON: per-directory centrality + top-K core dirs
\`\`\`

Preflight pattern before clustering:

\`\`\`bash
status=$(spn embed status)
running=$(jq -r .running <<<"$status")
if [ "$running" != "true" ]; then
  echo "Ollama not reachable; clustering will be skipped" >&2
fi
installed=$(jq -r '.installed | join(",")' <<<"$status")
if ! grep -q nomic <<<"$installed"; then
  spn embed pull nomic-embed-text | jq -c .   # streaming progress
fi
\`\`\`

## Common Mistakes

| Mistake | What to do instead |
| --- | --- |
| Treating `forks list` as a one-shot batched command by default | NDJSON streaming is the default; pipe through `jq -c` to consume incrementally. Use `--csv` only when the consumer expects tabular data. |
| Running `forks list` without checking the embedder | If clustering is critical, run `spn embed status` first and `spn embed pull` if a recommended model isn't installed. |
| Confusing `repo centrality` and fork centrality | `spn repo centrality <repo>` returns the upstream's directory centrality, not per-fork. There's no per-fork centrality verb yet. |

## Quick Reference

| Verb | Use |
| --- | --- |
| `spn forks list <repo>` | NDJSON stream of enriched forks |
| `spn forks list <repo> --csv` | Batched CSV with fixed 18-column header |
| `spn embed status` | Probe Ollama; report running state, installed models, recommended models |
| `spn embed pull <model>` | Pull a model with streaming progress NDJSON |
| `spn embed models` | List known-good embedding models |
| `spn repo centrality <owner/repo>` | Per-directory centrality JSON for the upstream repo |
```

### Personal-copy sync

Both new skill files also live at `~/.claude/skills/<name>/SKILL.md`. After the in-repo commits land, the implementation plan ends with two `cp` operations (and a `mkdir -p` for the new `using-spn-forks` directory).

### Frontmatter

Current description: "Use when addressing PR review threads on a GitHub PR — replying to review comments, resolving threads after fixes, looping through reviewer feedback, or checking PR mergeability."

Append: ", or discovering which PRs are open on a repository."

This keeps the description under the 1024-char frontmatter limit and triggers the skill when an agent's task is "find which PR to look at."

### New top-level section: Filter modes

Placed after **Output Contract**, before **Body-Required Policy**. Table of the five `FilterMode` values with one-line semantics each:

```markdown
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
```

### New top-level section: Stale-thread sweep

Placed after **Body-Required Policy**.

```markdown
## Stale-thread sweep

`spn threads resolve-all <pr> --outdated` resolves only threads where `isOutdated: true`. Combined with the body-required policy this means:

- Bot threads with outdated anchors → resolved into `succeeded`
- Bot threads with active anchors → moved to `skipped` with `reason: "not_outdated"`
- Human-raised threads (any state) → moved to `skipped` with `reason: "requires_body"` — same policy as the non-outdated bulk

Two-step pattern: preview, then act.

\`\`\`bash
spn threads list "$PR" --filter unresolved-outdated  # what would be swept
spn threads resolve-all "$PR" --outdated              # do it
\`\`\`

The thread JSON has an `isOutdated` boolean. An outdated thread anchors to code that has since shifted; the comment may be moot. Agents reviewing a thread should check `isOutdated` before deciding whether a fix is still relevant.

### `BulkSkip.Reason` values

| Reason | Meaning |
| --- | --- |
| `requires_body` | Bulk-resolve refused because the thread has a human commenter — resolve individually with `--body` |
| `not_outdated` | Bulk-resolve with `--outdated` refused because the anchor is still active |
```

### New top-level section: Code context and verbose mode

```markdown
## Code context and verbose mode

Two opt-in flags add detail to the thread JSON without changing the default shape.

`--show-code N` adds a `codeContext` block per thread, containing N lines on either side of the anchor:

\`\`\`json
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
\`\`\`

Cost: one extra `FetchFileContent` call per unique `(path, ref)` pair — the caching wrapper collapses duplicates across threads anchored to the same file.

`--verbose` adds `createdAt`, `updatedAt`, and `authorUrl` to each thread. Useful when grounding a reply in commit history or building per-author dashboards.

Combined call for a grounded review loop:

\`\`\`bash
spn threads next "$PR" --show-code 6 --verbose
\`\`\`

Returns enough context to write a referenced reply without a separate `gh api` call.
```

### New top-level section: Suggestions

```markdown
## Suggestions

GitHub review threads can embed `suggestion` fenced blocks ("\`\`\`suggestion … \`\`\`") that propose replacement code. spn parses them into a per-thread `suggestions` array:

\`\`\`json
{
  "suggestions": [
    {"body": "newCode()", "index": 0, "commentId": "PRC_..."}
  ]
}
\`\`\`

### Applying a suggestion locally

\`\`\`bash
spn threads apply-suggestion "$PR" "$id"
\`\`\`

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

\`\`\`bash
spn threads reply "$PR" "$id" \\
  --intro "Narrower scope here:" \\
  --suggest "return ctx.Err()"
\`\`\`

`--intro TEXT` prefixes a leading line of prose before the suggestion block.

### Spoon parallel

`spoon --apply-suggestion <id>` (flag) calls into the same `threadsops.ApplySuggestion` that `spn threads apply-suggestion` (verb) uses. The flag/verb difference is the human/agent bifurcation; both produce identical results.
```

### New top-level section: Dry-run previews

```markdown
## Dry-run previews

`--dry-run` is available on `resolve`, `resolve-all`, `unresolve-all`, and `apply-suggestion`. The output shape matches the non-dry-run path with an added `dryRun: true` field at the top level:

\`\`\`json
{"succeeded": ["PRRT_..."], "failed": [], "skipped": [], "dryRun": true}
\`\`\`

Gating pattern:

\`\`\`bash
preview=$(spn threads resolve-all "$PR" --outdated --dry-run)
count=$(jq -r '.succeeded | length' <<<"$preview")
if [ "$count" -gt 0 ]; then
  spn threads resolve-all "$PR" --outdated
fi
\`\`\`
```

### Update existing Quick Reference table

Five new rows appended:

```markdown
| `spn threads list-prs <owner/repo>` | JSON array of open PRs on a repo. Programmatic alternative to spoon's --interactive picker. |
| `spn threads list <pr> --filter MODE` | Filter modes: all, unresolved, current-unresolved, resolved-active, unresolved-outdated. |
| `spn threads apply-suggestion <pr> <id>` | Apply a thread's suggestion block to the local file. Use --dry-run first. |
| `spn threads reply <pr> <id> --suggest T` | Reply that wraps T in a suggestion fenced block. Optional --intro for context. |
| `spn threads * --dry-run` / `--show-code N` / `--verbose` | Cross-cutting modifiers for mutations / context-aware reads / extra metadata. |
```

### Update existing Common Mistakes table

Three new rows appended:

```markdown
| Resolving outdated threads individually | Use `spn threads resolve-all <pr> --outdated` to sweep them in one call. Preview with `--dry-run` first. |
| Ignoring `isOutdated` when deciding to reply | Outdated threads anchor to code that has since shifted; the comment may no longer be relevant. Check the field. |
| Calling `gh pr list` then `spn` | Use `spn threads list-prs <owner/repo>` for one round trip in the agent's preferred JSON shape. |
```

### Updates to existing sections

- **Core Loop** (around line 24): one-line note pointing at `--show-code N` / `--verbose` as opt-in enrichments for richer per-iteration context.
- **Output Contract** (around line 59): bullet-list the new fields agents may see — `isOutdated bool`, `suggestions []Suggestion`, `codeContext *CodeContext` — with one-sentence descriptions.
- **PR Ref Forms** (around line 152): add a parenthetical "list-prs takes `owner/repo` without the `#N` suffix" for cross-reference.

## Error handling

All new surfaces use the existing error contract:

- `agentio.CodeBadInput` for malformed args, unknown flag values
- `agentio.CodeAuthRequired` / `CodeAuthScope` for missing auth
- `agentio.CodeNotFound` for nonexistent repo / PR
- `agentio.CodeUpstream` / `CodeRateLimited` for GitHub failures (already wired)
- `agentio.CodeInternal` for unexpected errors

No new error codes. The skill update mentions the existing `rate_limited` envelope's reach: GitHub REST returns it; GraphQL (used by `ListOpenPRs`) falls through as `upstream_error` due to the documented go-gh GraphQL limitation. Worth restating in the list-prs documentation.

## Testing strategy

### `spn threads list-prs`

- `cmd/spn/threads_test.go` — black-box tests:
  - Happy path: stub `listPRsFn` returns 3 PRs; assert stdout is a 3-element JSON array with the expected fields.
  - `--limit 2` truncates to 2 elements.
  - Missing `<owner/repo>` argument → `bad_input` exit 2.
  - Invalid `--state` value → `bad_input` exit 2 (until `--state` is wired, accept only `open` or treat any value as an error).
  - Stubbed rate-limit error from `listPRsFn` → exit 1 with `rate_limited` envelope.

### TUI counter-propose

- `internal/tui/threads/model_test.go` — controller tests:
  - `c` keypress sets `m.confirm` or equivalent transitional state; assert no API call is made yet.
  - After the editor process returns successfully with non-empty content: assert `threadsops.Reply` is invoked with `WrapSuggestionBody` applied.
  - After the editor process returns empty content: assert no Reply call, status shows "cancelled."
  - After the editor process returns an error: assert no Reply call, status shows the error, temp file path is referenced.

Testing `tea.ExecProcess` requires stubbing the editor launch. The model should accept a `launchEditor func(...)` field initialized to the real implementation; tests inject a fake. Mirrors existing test seam patterns.

### Skill

No automated tests for the skill content. Manual review of the rendered Markdown for:
- Frontmatter is well-formed
- Code fences are balanced
- Tables render on GitHub
- Word count under 2200 (target ~2000)

## Commit plan

Single PR, branch `gh-toolkit-cli-gaps`, 5 commits:

1. `spn threads list-prs` verb + tests. Adds `listPRsFn` test seam in `cmd/spn/threads.go`.
2. TUI `[c]` counter-propose key + `Counter` binding + tests. Adds `launchEditor` test seam in `internal/tui/threads/model.go`.
3. Split `using-spn` skill: extract the **Embedding & Centrality** section and the **CSV mode** subsection into a new `docs/superpowers/skills/using-spn-forks/SKILL.md`. Update the `using-spn` frontmatter description to reflect the narrowed scope. Personal-copy sync via `cp` (outside git).
4. Update narrowed `using-spn` skill with PR-7's eight new capabilities + the new `list-prs` verb and TUI `[c]` key. Five new top-level sections; two table expansions. Personal-copy sync.
5. Final verification pass — `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./cmd/spoon ./cmd/spn`, smoke test of the new verb against a real repo, manual render check of both skill files on GitHub.

Each commit independently green under `go test ./...`. The skill commits change only documentation; the personal-copy sync is outside the git commit boundary, per the convention established in earlier work.

## Open questions / future work

- **`--state` on `list-prs`** — if `ListOpenPRs` doesn't already support `closed` / `all`, deferred to a future PR. The flag is reserved in the v1 surface so future expansion doesn't break agent scripts.
- **TUI `[c]` editor pre-fill from suggestion** — current design pops an empty editor. Future enhancement: if the thread already has a suggestion, pre-fill the editor with that suggestion (so the counter-propose starts from the original proposal). Skipping for v1 to keep the implementation simple.
- **`spn pr list-prs`** — currently `list-prs` is under `threads` because that's where the rest of the PR-discovery flow lives. A future move to `spn pr list` (as a sibling of `spn pr status`) would be cleaner namespacing but breaks any agent scripts that adopt `threads list-prs` in the meantime. Deferred until usage justifies the rename.
