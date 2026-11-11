# Napkin

> Persistent working memory for this repository. Tracks mistakes, preferences, and patterns.

## Corrections
| Date | Source | What Went Wrong | What To Do Instead |
|------|--------|----------------|-------------------|
| 2026-11-03 | self | FastEmbed probe sets global `ONNX_PATH` env which breaks offline test suites | Do not rely on ambient ONNX runtime during hermetic tests; isolate env vars |

## User Preferences
- Audit fork processing and embeddings: prune weak/unused parts (e.g. creating tons of embeddings with little projection utility) and strengthen core competencies.

## Patterns That Work
- Fast, local, deterministic evaluation: GraphQL batch compare (`internal/github/batch_compare.go`) resolves 86% of zero-ahead clones in 1 query.
- Pure Go lexical scorer (`internal/embed/queryscore.go`) handles `--query` and TUI `R` relevance instantly without external models or CGO.
- Targeted path filtering (`--touching`) cuts directly to forks modifying relevant files.
- Bayesian normal-normal ranking (`--shortlist`) provides robust probabilistic ranking.
- Worktree and submodule aware git inspection (`internal/threadsops/workspace.go`): resolve `.git` file pointer (`gitdir:`) and linked worktree `commondir` for branch identity; read effective remotes with `git remote get-url` so includes, worktree configuration, and URL rewrites are respected.

- GraphQL Phase A refreshes BehindCount on private CachedT2 copies for the recorded branch and head; changed head/ahead evidence invalidates the cached comparison before a full refresh.
- FindOpenPRForHead resolves open PRs matching headOwner:headBranch using GET /repos/{owner}/{repo}/pulls?head={headOwner}:{headBranch}&state=open, defaulting headOwner to repo owner when empty.
- resolveGitDir handles both standard .git directories and .git pointer files (gitdir: <path>), resolving linked worktree commondir (for config) and worktree-local gitdir (for HEAD).
- Git URLs in SSH format (git@host:owner/repo.git and ssh://...) need normalization to HTTPS before being passed to forge.ParseRepoURL.
- DiscoverPRsInWorkspace queries open PRs against upstream with origin fallback using bounded workers.
- Multi-repo picker shows [owner/repo in ./dir] only when PRs span multiple repositories, and suppresses the suffix when all PRs belong to the same repository.

## Patterns That Don't Work
- Disconnected vector persistence: automatically embedding every listed fork into `spoon.db` via FastEmbed/Voyage when `spn forks list` and TUI never consume the stored vectors.
- Multi-modal clustering overhead (4 embeddings per fork via ONNX) just to add 0–5 points of `NoveltyScore` to Heat.
- Sibling similarity search burning up to 50 GitHub API calls to fetch external READMEs for a 2.5 point score bump.
- Ignoring fork descriptions/topics in Heat and Query scoring, making stated-purpose discovery fail on unenriched forks.

## Domain Notes
- `spoon` is a tool for finding useful forks of Git repositories (GitHub, GitLab, Gitea). Two binaries: `spoon` (TUI) and `spn` (CLI for agents/NDJSON/CSV).
- Heavy fork processing & embedding pipeline exists across `internal/forksops`, `internal/embed`, `internal/cluster`, `internal/semantic`, `internal/store`.
