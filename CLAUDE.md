# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`spoon` finds useful forks of a Git repository (GitHub, GitLab, Gitea): it enumerates forks, enriches each with activity/divergence signals, and ranks them. Two binaries share one pipeline and never fork behaviour, only output shape: `spoon` (Bubbletea TUI, `cmd/spoon`) and `spn` (JSON/NDJSON/CSV agent CLI, `cmd/spn`). README.md has the full user-facing reference and architecture diagrams.

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
- `vendor/` is **tracked**. After any `go.mod` change run `go mod vendor` and commit the result, or builds fail with an inconsistent-vendoring error.
- No CI workflows and no Makefile; the verification path is the three commands above.

## Architecture (the parts that span files)

- **Pipeline:** `internal/forksops` owns streaming fork enumeration, enrichment, ranking and profiles. It calls forge adapters (`internal/forge` abstracts `github`, `gitlab`, `gitea`), then scoring (`heat`, `priors`, `cluster`, optional `mdg`), then persistence (`store`, a libsql DB caching repos, forks, compares and embeddings). `spn forks list` is: detect forge, enumerate and enrich, score/rank, snapshot, embed changed docs, emit NDJSON.
- **GitHub layer** (`internal/github`) is a dispatcher, not a single client: token pool with failover, proxy pool, a token-bucket `limiter.go`, GraphQL with REST fallback, and a web-diff fallback. Fork lists are cached scoped to API version and credential identity. REST pins `X-GitHub-Api-Version: 2022-11-28`. The HTTP client is `cli/go-gh/v2`; `go-github` is not a dependency.
- **Cost model:** the code is organised as a cheap-to-expensive tiered pipeline (list, then compare/divergence, then diff/blob fetch). Most work here is about spending as few API points as possible, so check existing batching (`batch_compare.go`, `last_touch.go`, `divergent_branches.go`) before adding calls. Undocumented routes such as private `/network/meta` are deliberately not used.
- **TUI:** a single `Update` loop in `internal/tui/app.go`; embed and cluster data reach the view through `cache_bridge` / `cluster_bridge` reading the store rather than in-memory copies.
- **`spn` contract:** machine-readable errors go through `internal/agentio` envelopes; PR thread operations are shared between both binaries in `internal/threadsops`.
- **Config:** zero-config bootstrap writes `~/.config/spoon/config.json` on first run; `SPOON_NO_CONFIG=1` ignores the config file entirely.

## Repo conventions

- Tests sit beside their package; external credentials, paid services and native runtimes (FastEmbed/ONNX, Voyage) are opt-in via build tag or env, so `go test ./...` must stay hermetic.
- `spoon-export-*.json` and `.do-not-commit/` are gitignored. `.do-not-commit/` holds third-party reference clones (shhgit, GitDorker, codeql, ...) used for research; do not vendor or copy code from it (Treasure has no licence).
- Long-form references live in `docs/` (`embedders.md`, `keymap.md`, `ranking.md`); dated research in `docs/research/`; specs and plans in `docs/superpowers/`.

## Planned work: GitHub recon capabilities

Read `docs/research/2026-10-01-github-recon-capabilities.md` before touching dangling-commit enumeration, secret scanning, SARIF output or `go-github`. Key points from it (live-observed 2026-10-01, small samples):

- GH Archive PushEvents no longer carry `size`/`commits` (dropped between 2025-09-15 and 2025-10-15), so the "zero-commit force-push" filter is gone. Detect rewrites with `compare before...head` (`diverged`, `behind`, no common ancestor).
- Orphaned `before` commits were still served by SHA; GraphQL `object(oid:)` aliases cost 1 point per ~20 lookups, so batch existence checks in GraphQL and use REST `compare` only on survivors.
- Alert APIs (code scanning, secret scanning, push protection) are not readable on third-party repos; do not plan fork-prospecting signals on them.
- Decided: native Go enumeration (no TruffleHog shell-out), and a content scan (`internal/secretscan`, not yet created) that inspects only what a fork adds. The `go-github` addition is flagged "open" in that doc; confirm before adding.
- Scope is defensive/authorized use. Never validate discovered credentials, and do not read or print scanned commit contents in logs or tests.
