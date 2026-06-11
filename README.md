# spoon

Find useful forks of a Git repository.

`spoon` enumerates the forks of a GitHub or GitLab repo, enriches each with signals about activity and divergence (recency, stars, sub-forks, releases, ahead/behind counts, contributor mix), and ranks them so the interesting ones surface first. It works either as an interactive TUI or as JSON/CSV output for scripting.

## Install

```sh
go install github.com/svnbjrn/spoon/cmd/spoon@latest
```

Or build from source:

```sh
git clone https://github.com/Raudbjorn/spoon.git
cd spoon
go build -o spoon ./cmd/spoon
```

Requires Go 1.26+. The MDG centrality backend (`--full-mdg`) uses
tree-sitter parsers via cgo, so building also needs a working C compiler on
PATH (`gcc`/`clang` on Linux/macOS, MinGW or MSVC on Windows). `CGO_ENABLED=1`
is the Go default; do not unset it.

## Auth

- **GitHub**: `gh auth login` (uses the `gh` CLI's stored token)
- **GitLab**: `glab auth login`, or set `GITLAB_TOKEN`

Unauthenticated requests work but hit much lower rate limits.

## Usage

```sh
spoon                                          # interactive (defaults to GitHub)
spoon golang/go                                 # GitHub repo
spoon gitlab.com/inkscape/inkscape              # GitLab (auto-detected)
spoon --forge gitlab group/repo                 # force provider
spoon --forge-host gitlab.example.com g/repo    # self-hosted GitLab
spoon --json charmbracelet/bubbletea            # JSON to stdout
spoon --csv charmbracelet/bubbletea -o forks.csv
spoon --tier 1 golang/go                        # T1 only (skip compare calls)
spoon --top 5 golang/go                         # only enrich top 5 by T1 score
```

Run `spoon --help` for the full flag list.

### TUI keybindings

| Key | Action |
| --- | --- |
| `↑`/`↓`, `j`/`k` | Navigate |
| `Enter` | Fork details |
| `o` | Open fork in browser |
| `c` | Compare fork vs upstream |
| `y` | Yank clone command |
| `/` | Filter |
| `n` | New repository |
| `s` | Cycle sort column |
| `r` | Refresh (bypass cache) |
| `Space` | Mark/unmark |
| `e` / `E` | Export marked / all forks |
| `?` | Help |
| `q` | Quit |

### PR review threads

The `spoon threads` subcommand lists, replies to, and resolves GitHub PR
review threads. It's the primary way an AI agent reasons about review
feedback.

```sh
spoon threads owner/repo#42                   # interactive TUI
spoon threads owner/repo#42 --json            # all unresolved threads as JSON
spoon threads owner/repo#42 --next            # one thread (or null) for agent loops
spoon threads owner/repo#42 --reply PRRT_… --body "fixed"
spoon threads owner/repo#42 --resolve PRRT_… --body "addressed in 1234abc"
spoon threads owner/repo#42 --resolve-all     # bulk close
spoon threads owner/repo#42 --unresolve-all   # inverse
```

Single-thread `--resolve` enforces a body when any commenter is a human
reviewer. `--resolve-all`/`--unresolve-all` are deliberate bulk
operations and accept no body.

Agent loop pattern:

```sh
while [ "$(spoon threads owner/repo#42 --next)" != "null" ]; do
  thread=$(spoon threads owner/repo#42 --next)
  # ... address the comment in code ...
  id=$(echo "$thread" | jq -r .id)
  spoon threads owner/repo#42 --resolve "$id" --body "addressed in $(git rev-parse --short HEAD)"
done
```

Every invocation prints a status header above its normal output: PR
title, mergeability (`CLEAN` / `DIRTY` / `BLOCKED` / …), review
decision (`APPROVED` / `REVIEW_REQUIRED` / …), check rollup
(`SUCCESS` / `FAILURE` / …), and unresolved thread count. If anything
in the header is red, resolving threads alone will not get the PR
merged — you have to fix the gate first. The header lands on stderr
for `--json` / `--next` so stdout stays pure JSON. Suppress with
`--no-status`.

## Agent CLI (`spn`)

`spn` is a sibling binary aimed at LLM/agent consumption. JSON-only, no
TUI, no color, structured error envelope with remediation hints, NDJSON
streaming for long-running queries.

```sh
go install github.com/svnbjrn/spoon/cmd/spn@latest
```

Verbs:

```sh
spn threads list <pr-ref> [--all]
spn threads next <pr-ref>
spn threads reply <pr-ref> <id> --body T
spn threads resolve <pr-ref> <id> [--body T]
spn threads resolve-all <pr-ref>     # skips human-raised threads (returned in `skipped`)
spn threads unresolve-all <pr-ref>
spn pr status <pr-ref>
spn forks list <repo> [--tier N] [--top N] [...]   # NDJSON
```

Success: bare JSON to stdout. Failure: structured envelope to stderr:

```json
{"error": {"code": "policy_violation", "message": "...", "remediation": "spn threads resolve owner/repo#42 PRRT_... --body \"...\"", "retryable": false, "details": {...}}}
```

See [`docs/superpowers/specs/2026-05-10-spn-bifurcation-design.md`](docs/superpowers/specs/2026-05-10-spn-bifurcation-design.md) for the full design.

## Heat scoring

Forks are ranked by a weighted "heat" score combining several signals:

- `recency` — how recently the fork was pushed to
- `stars` — independent star count
- `sub_forks` — fork-of-fork activity
- `releases` — tagged releases
- `mna` — meaningful net additions (lines added beyond upstream)
- `sync_ratio` — how in-sync the fork is with upstream
- `feature_ratio` — divergence from upstream
- `lone_wolf` — solo-developer signal
- `span` — duration of activity

Override the defaults with `--heat-weights path/to/weights.json` (each value in `[0.0, 2.0]`).

## Project layout

```
cmd/spoon/         CLI entry point
internal/forge/    Provider abstraction (GitHub + GitLab)
internal/github/   GitHub client (REST + GraphQL via gh CLI)
internal/gitlab/   GitLab client
internal/heat/     Scoring, percentiles, filters
internal/dump/     JSON/CSV exporters
internal/tui/      Bubbletea TUI
```

## Development

```sh
go test ./...
go vet ./...
go build ./...
```
