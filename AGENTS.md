# AGENTS.md

Shared repository guidance for coding agents. Codex loads this file directly;
`CLAUDE.md` points here so repository facts have one maintained copy.

## What this is

`spoon` finds useful forks of a Git repository (GitHub, GitLab, Gitea): it enumerates forks, enriches each with activity/divergence signals, and ranks them. Two binaries share one pipeline and never fork behaviour, only output shape: `spoon` (Bubbletea TUI, `cmd/spoon`) and `spn` (JSON/NDJSON/CSV agent CLI, `cmd/spn`). README.md has the full user-facing reference and architecture diagrams.

## Development and delegation

- Confirm cwd, branch, HEAD and dirty files before work. Source and current
  callers take precedence over plans and historical reviews.
- Keep to the assigned outcome and files; preserve other contributors' edits.
  One implementer owns each shared file per task. Request a broader mandate when
  needed; the request alone does not grant it. Continue independent work if the
  runtime supports messaging; otherwise return the request and unfinished scope.
- Review assignments leave source unchanged. Implementations use the smallest
  complete change, trace affected callers and add focused regressions for changed
  nontrivial behavior. Report the revision, evidence and verification limits.
- Use fixtures, temporary databases, isolated XDG paths and fake credentials.
  Development does not authorize opening a user's live store, spending API or
  paid-service budgets, downloading models, or exercising real PR mutations.
  Honor existing session authorization; do not ask again merely because it was
  omitted from a handoff. Test gates and inherited credentials are not permission.
  Never expose secret values; authorized authentication uses existing mechanisms.
- When implementing, delegating or reviewing, read the relevant working-contract
  and review sections in `docs/development/agents.md`. For work you cannot or will
  not handle, use its reporting procedure. These sections explicitly authorize
  direct issue/review publication and define the narrower adjudication boundary.
  The coordinator must arrange required independent review; labels do not do it.

These rules are shared by all native roles. Maintain common guidance here;
role entrypoints guide navigation, not ownership.

## Commands

```sh
go build ./cmd/...                 # builds both; needs a C compiler (tree-sitter via cgo, keep CGO_ENABLED=1)
go build -o spoon ./cmd/spoon && go build -o spn ./cmd/spn   # /spoon and /spn are gitignored
go test ./...
go vet ./...
go test -run TestName ./internal/forksops/                   # single test
go test -tags integration -run TestFetchForksIntegration ./internal/github/   # network; unauthenticated = 60 req/hr
```

- Go version is pinned by `go.mod` (1.26.2). The module path `github.com/svnbjrn/spoon` is not a published repo (real remote is `Raudbjorn/spoon`), so `go install ...@latest` does not work.
- `vendor/` is **tracked**, including local fixes and native libraries absent from regenerated output. After `go.mod` changes, follow the preservation procedure in `packaging/arch/README.md` before committing vendor changes; a bare `go mod vendor` drops required patches.
- No CI workflows and no Makefile; the verification path is the three commands above.

## Architecture (the parts that span files)

- **Pipeline:** `internal/forksops` owns streaming fork enumeration, enrichment, ranking and profiles. It calls forge adapters (`internal/forge` abstracts `github`, `gitlab`, `gitea`), then scoring (`heat`, `priors`, `cluster`, optional `mdg`), then persistence (`store`, a libsql DB caching repos, forks, compares and embeddings). `spn forks list` is: detect forge, enumerate and enrich, score/rank, snapshot, embed changed docs, emit NDJSON.
- **GitHub layer** (`internal/github`) is a dispatcher, not a single client: token pool with failover, proxy pool, a token-bucket `limiter.go`, GraphQL with REST fallback, and a web-diff fallback. Fork lists are cached scoped to API version and credential identity. REST pins `X-GitHub-Api-Version: 2022-11-28` and uses `google/go-github/v90` through path-based `restGet`; GraphQL uses `shurcooL/githubv4`. Credentials come from Spoon config/environment; `cli/go-gh/v2` remains for hostname helpers and the browser opener, not token discovery. Use `httpFailure`/`statusCode` to handle transport errors.
- **Cost model:** the code is organised as a cheap-to-expensive tiered pipeline (list, then compare/divergence, then diff/blob fetch). Most work here is about spending as few API points as possible, so check existing batching (`batch_compare.go`, `last_touch.go`, `divergent_branches.go`) before adding calls. Undocumented routes such as private `/network/meta` are deliberately not used.
- **TUI:** a single `Update` loop in `internal/tui/app.go`. Embedding and clustering are separate paths: embed data is read back through the store (`internal/tui/embedcolumn.go`), while the cluster pipeline (`cluster_bridge.go`) works on the in-memory `Model.forks` slice — `maybeStartClusterPipeline` captures it, `runTUIClusterPipeline` passes `*Heat` pointers into the pipeline, and `handleClusterResult` re-sorts those mutated values. Nothing round-trips clusters through the store, and `cache_bridge.go` holds only forge→export conversions (the JSON cache it once served is gone).
- **`spn` contract:** machine-readable errors go through `internal/agentio` envelopes; PR thread operations are shared between both binaries in `internal/threadsops`.
- **Config/auth:** zero-config bootstrap writes `~/.config/spoon/config.json` on first run; `SPOON_NO_CONFIG=1` ignores the config file entirely. Both CLIs share login/logout through `internal/authops` and OAuth authorization-code login with PKCE/state through `internal/oauth`. GitHub tokens default to OS keyring storage (`internal/secrets`); `internal/config/secrets.go` resolves `keyring:<entry>` references on load and verifies new entries before publishing references on save. Unavailable keyring writes fall back to inline storage with a warning. `SPOON_SECRET_STORE=file` or `secrets.store: file` selects inline writes. Tests inject stores with `config.UseSecretStore`; the default keyring opener is disabled under `testing.Testing()`.

## Repo conventions

- Tests sit beside their package; external credentials, paid services and native runtimes (FastEmbed/ONNX, Voyage) are opt-in via build tag or env, so `go test ./...` must stay hermetic.
- `spoon-export-*.json` and `.do-not-commit/` are gitignored. `.do-not-commit/` holds third-party reference clones (shhgit, GitDorker, codeql, ...) used for research; do not vendor or copy code from it (Treasure has no licence).
- Long-form references live in `docs/` (`embedders.md`, `keymap.md`, `ranking.md`); dated research in `docs/research/`; specs and plans in `docs/superpowers/`.

## Planned work: GitHub recon capabilities

Read `docs/research/2026-10-01-github-recon-capabilities.md` before touching dangling-commit enumeration, secret scanning, SARIF output or `go-github`. Key points from it (live-observed 2026-10-01, small samples):

- GH Archive PushEvents no longer carry `size`/`commits` (dropped between 2025-09-15 and 2025-10-15), so the "zero-commit force-push" filter is gone. Detect rewrites with `compare before...head` (`diverged`, `behind`, no common ancestor).
- Orphaned `before` commits were still served by SHA; GraphQL `object(oid:)` aliases cost 1 point per ~20 lookups, so batch existence checks in GraphQL and use REST `compare` only on survivors.
- Alert APIs (code scanning, secret scanning, push protection) are not readable on third-party repos; do not plan fork-prospecting signals on them.
- Decided: native Go enumeration (no TruffleHog shell-out), and a content scan (`internal/secretscan`, not present in this checkout) that inspects only what a fork adds. The research document's later resolution records adoption of `go-github/v90` for REST transport; that migration is now in main.
- Scope is defensive/authorized use. Never validate discovered credentials, and do not read or print scanned commit contents in logs or tests.
