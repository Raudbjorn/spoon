# spoon

Find useful forks of a Git repository.

`spoon` enumerates the forks of a GitHub or GitLab repo, enriches each with signals about activity and divergence (recency, stars, sub-forks, releases, ahead/behind counts, contributor mix), and ranks them so the interesting ones surface first. It works either as an interactive TUI or as JSON/CSV output for scripting.

## Build

Two binaries: `spoon` (interactive TUI) and `spn` (the agent-shaped
JSON/NDJSON CLI). Build both from a clone:

```sh
git clone https://github.com/Raudbjorn/spoon.git
cd spoon
go build -o spoon ./cmd/spoon      # interactive TUI
go build -o spn   ./cmd/spn        # agent CLI (JSON/NDJSON)
```

Requires Go 1.26.2+, matching the `go` directive in `go.mod`. The MDG centrality backend
(`--full-mdg`) uses tree-sitter parsers via cgo, so building also needs a
working C compiler on PATH (`gcc`/`clang` on Linux/macOS, MinGW or MSVC on
Windows). `CGO_ENABLED=1` is the Go default; do not unset it.

> **`go install …@latest` does not work.** The module path is
> `github.com/svnbjrn/spoon`, but no repository is published there — the code
> lives at [`github.com/Raudbjorn/spoon`](https://github.com/Raudbjorn/spoon).
> Until the module path is renamed to match, build from a clone as above.

## Auth

- **GitHub**: `gh auth login` (uses the `gh` CLI's stored token). For the
  round-robin dispatcher, add one or more PATs under `github.tokens` in the
  config file (mode `0600`) — see the rate-limit note below.
- **GitLab**: `glab auth login`, or set `GITLAB_TOKEN`

Unauthenticated requests work but hit much lower rate limits.

## Usage

```sh
spoon                                          # interactive (defaults to GitHub)
spoon golang/go                                 # GitHub repo
spoon gitlab.com/inkscape/inkscape              # GitLab (auto-detected)
spoon --forge gitlab group/repo                 # force provider
spoon --forge-host gitlab.example.com g/repo    # self-hosted GitLab
spn forks list charmbracelet/bubbletea --json   # JSON to stdout (agent CLI)
spn forks list charmbracelet/bubbletea --csv    # batched CSV (see "Agent CLI" below)
spn forks list golang/go --tier 1               # T1 only (skip compare calls)
spn forks list golang/go --top 5                # only enrich top 5 by T1 score
spoon topic:terminal                            # GitHub topic → repo picker → forks
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

`spn` is the second binary from [Build](#build) above.

Verbs:

```sh
spn threads list <pr-ref> [--all]
spn threads next <pr-ref>
spn threads reply <pr-ref> <id> --body T
spn threads resolve <pr-ref> <id> [--body T]
spn threads resolve-all <pr-ref>     # skips human-raised threads (returned in `skipped`)
spn threads unresolve-all <pr-ref>
spn pr status <pr-ref>
spn forks list <repo> [--tier N] [--top N] [--budget N] [--shortlist N] [--query "T"] [--priors PATH] [...]
    [--rpm N] [--files] [--commits] [--commit-files] [--commit-file-budget N] [--web-diff]   # NDJSON
spn forks list topic:zig [--topic-repos 5] [...]                        # whole-topic prospecting
spn search "oauth rate limiting" [--repo owner/repo] [--top N]          # persistent semantic search
spn forks eval <repo> --judgments FILE                                  # score ranking vs a labeled set -> JSON report
spn repo centrality <owner/repo>                                        # upstream module/dir centrality
```

`spn forks list` writes every emitted fork snapshot to
`$XDG_DATA_HOME/spoon/spoon.db` (default
`~/.local/share/spoon/spoon.db`) before printing it. `--files` and
`--commits` opt into detailed wire output without changing what SQLite
retains. `--commit-files` implies both and attributes files to at most 100
commits per run by default; override with `--commit-file-budget N`.

GitHub traffic is capped at 300 requests/minute by default. Set `--rpm`,
`SPOON_GITHUB_RPM`, or `github.requestsPerMinute` (maximum 900). Configured
PATs are probed at startup and round-robin only across distinct GitHub
logins: several PATs for one login still share that user's 5,000-request/hour
primary budget and are deduplicated. REST and GraphQL primary budgets remain
separate. A config containing raw PATs or credential-file references must be
mode `0600`.

`--web-diff` is an explicitly unstable HTML fallback for missing REST patch
text. It reads a cookie only from `SPOON_GH_COOKIE`, never persists it, and is
not a supported GitHub API. REST metadata remains authoritative if the HTML
adapter fails.

Success: bare JSON to stdout. Failure: structured envelope to stderr:

```json
{"error": {"code": "policy_violation", "message": "...", "remediation": "spn threads resolve owner/repo#42 PRRT_... --body \"...\"", "retryable": false, "details": {...}}}
```

See [`docs/superpowers/specs/2026-05-10-spn-bifurcation-design.md`](docs/superpowers/specs/2026-05-10-spn-bifurcation-design.md) for the full design.

## Heat scoring

Forks are ranked by a 0–100 "heat" score built from additive tiers (T1
surface signals up to 40 pts, T2 divergence up to 40, T3 behavior up to 20):

- `recency` — exp-decay since last push, half-life adapted to upstream pace
- `stars` — independent star count (log-scaled)
- `sub_forks` — fork-of-fork activity
- `releases` — tagged releases
- `mna` — meaningful net additions (lines added beyond upstream)
- `sync_ratio` — how much of the divergence is the fork's own work
  (behind-counts are √-damped so forks of fast upstreams aren't drowned)
- `feature_ratio` — fraction of non-merge/non-sync commits
- `lone_wolf` — solo-developer signal (Sniper / Feature Builder / Drifter)
- `span` — duration of activity
- `novelty` — distance from the fork's cluster (set by the cluster pipeline)

The raw score is then finalized: a trust multiplier (stars/sub-fork
percentile within the fork set), a hard zero for forks with no commits
ahead, a 30-point cap for archived forks, and a dampener for
bottom-quintile recency. Repos with <10 forks score on the same tiers but
skip the percentile trust (too few samples).

Override component weights with `--heat-weights path/to/weights.json`
(each value in `[0.0, 2.0]`; works on both `spoon` and `spn forks list`).

## Curated priors (`--priors`)

Bias the ordering toward a subsystem you care about — without hiding any fork
or changing its heat. Pass a JSON interest spec to `spn forks list`:

```json
{
  "paths": ["internal/auth", "cmd/*.go"],
  "keywords": ["oauth", "rate limit"],
  "languages": ["Go", "Rust"],
  "owners": {"allow": ["torvalds"], "deny": ["fork-farmer"]}
}
```

```sh
spn forks list golang/go --priors interest.json
```

Each fork gains a `priorScore` (0–1) and `priorReasons` — e.g.
`path:internal/auth`, `keyword:oauth`, `language:go`, `owner_allow:torvalds`,
`owner_deny:fork-farmer` — computed from already-fetched data at zero extra API
cost. When neither `--query` nor `--shortlist` is active, matched forks are
listed before unmatched ones (heat order within each lane). Priors never hide a
fork or touch heat; a denied owner scores 0 but is still emitted, carrying its
`owner_deny` reason.

Path matching is deliberately simple: a wildcard-free entry matches by exact
file or **directory prefix** (`internal/auth` covers everything beneath it),
while an entry containing a glob uses single-segment `path.Match` (`cmd/*.go`
matches `cmd/main.go` but not `cmd/sub/x.go`). Recursive `**` is not supported.

## Fork profiles

Every `spn forks list` record also carries a `profile`: a deterministic,
one-word label derived only from fields already on the record (visibility,
lone-wolf archetype, momentum, penalties, priors). It is presentation-only —
never affecting scoring, ordering, or visibility — and exists purely to make
records easier to skim. First match wins, with `standard` as the floor:

| `profile` | when |
| --- | --- |
| `hidden` | upstreamed / no commits ahead (non-actionable) |
| `focused_change` | lone-wolf Sniper archetype |
| `focused_feature` | lone-wolf Feature Builder archetype |
| `broad_maintenance` | lone-wolf Drifter archetype |
| `emerging_active` | momentum rising or newly observed |
| `stale` | archived / low-recency penalty |
| `matches_priors` | matched a `--priors` spec (nothing higher fired) |
| `standard` | everything else |

A `profileReasons` array accompanies the label when it carries explanatory
facts (e.g. `archetype:feature_builder`, `momentum:rising`).

## Topic mode

Point spoon at a [GitHub topic](https://github.com/topics) instead of a
repo and it selects the repositories that best represent the topic —
scored by stars, fork-network size, and recency (archived repos are damped,
fork-less repos excluded; the selection breakdown is reported) — then runs
its normal fork evaluation over each.

- `spoon topic:NAME` shows the selection as a picker; Enter prospects the
  chosen repo's forks.
- `spn forks list topic:NAME` streams fork records for every selected repo,
  each tagged with an `upstream` field; selections are emitted as structured
  info envelopes on stderr. Cap the set with `--topic-repos N` (default 5).

## Embedding, clustering & semantic search

spoon uses a single embedder — **fastembed** — the fixed
`fast-bge-small-en-v1.5` BGE model (384 dimensions, max length 512) run
in-process via native Go and ONNX Runtime. It powers persistence, the semantic
index (`spn search`), and — when available — clustering plus the zero-shot
`category` facet.

FastEmbed runs by **default** on every `spn forks list` (it is opt-out, not
opt-in):

- It needs ONNX Runtime. Set `ONNX_PATH=/path/to/libonnxruntime.so` and run
  `spoon setup` once — it downloads the model (cached under
  `$XDG_CACHE_HOME/spoon/models/fastembed`) and persists the config. The exact
  semantic identity is `fastembed:fast-bge-small-en-v1.5:maxlen=512`.
- If ONNX Runtime is unavailable, the run **degrades gracefully**: it emits an
  `embed_unavailable` warning and continues without semantic indexing —
  listing, scoring, and clustering are unaffected.
- Pass `--no-embed` (or `SPOON_NO_EMBED=1`) to skip embedding entirely.

Clustering itself never requires a native runtime: when FastEmbed is active it
is used for clustering; otherwise clustering falls back to a **built-in
deterministic lexical embedder** over each fork's touched paths, commit
messages, README, and diff shape (zero setup). The interactive `spoon` TUI
always clusters with this lexical engine — semantic search and persistence live
in the `spn` agent CLI. Clusters get deterministic heuristic labels (dominant
directory prefix + the most discriminative commit/path tokens). Tune with
`--cluster-epsilon` / `--cluster-min-size`, cap the embedded set with
`--cluster-top`, or disable with `--no-cluster`.

The optional `--query` reranker and the LLM cluster-label polisher still use the
OpenVINO runtime (loaded at run time via `dlopen`, no OpenVINO SDK needed for
the default build); `spoon setup` provisions them, and
`SPOON_OPENVINO_LIB` / `SPOON_OPENVINO_GENAI_LIB` point at off-path libraries.

Each completed list run embeds only new or changed documents in batches of 32.
`spn search "<query>" --top 20` queries every indexed fork (or one upstream with
`--repo owner/repo`) and emits deterministic score-descending NDJSON. An empty
index is a successful empty result with a `semantic_index_empty` warning.

## Project layout

```
cmd/spoon/         Interactive CLI entry point
cmd/spn/           Agent-shaped CLI (JSON/NDJSON)
internal/forge/    Provider abstraction (GitHub + GitLab + Gitea)
internal/genai/    OpenVINO GenAI label polisher for cluster labels
internal/github/   GitHub REST/GraphQL dispatcher, token/proxy pools, web-diff adapter
internal/gitlab/   GitLab client
internal/heat/     Scoring, percentiles, filters
internal/mdg/      Module Dependency Graph centrality backend (opt-in via --full-mdg)
internal/threadsops/ PR thread ops shared by `spoon threads` and `spn threads`
internal/agentio/  Structured error envelope + JSON writers for `spn`
internal/embed/    Fixed FastEmbed embedder + built-in lexical fallback
internal/semantic/ Deterministic documents, vector codec, incremental indexing
internal/store/    Durable SQLite snapshots and embeddings
internal/cluster/  Clustering, novelty, heuristic labels
internal/forksops/ Streaming fork enumeration/enrichment
internal/tui/      Bubbletea TUI
```

## Development

```sh
go test ./...
go vet ./...
go build ./...
```
