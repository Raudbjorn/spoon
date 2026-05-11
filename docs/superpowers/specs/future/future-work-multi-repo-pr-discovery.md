# Future Work: Multi-Repo PR Discovery for `spoon threads`

**Status:** Deferred from the `integrate-gh-toolkit` PR.
**Parent:** the gh-pr / gh-resolve toolkit integration (PR #7).
**Estimated effort:** 2–3 days.

## Context

The bash gh-toolkit at `gh/gh-pr-detect` includes a "workspace mode" that scans the current directory's immediate subdirectories for sibling git checkouts, finds the open PR (if any) associated with each one's current branch, and presents an aggregated picker so the user can jump to any of them in one command.

This is most useful in a layout like:

```
~/projects/acme/
├── frontend/      .git, on branch fix-login → PR #42 in acme/frontend
├── backend/       .git, on branch fix-login → PR #88 in acme/backend
└── infra/         .git, on branch main      → no PR
```

The user is co-developing several services for the same feature, each in its own clone, each with its own PR. `gh-pr-detect` lets them type one command and get a picker covering all three repos.

spoon's existing `--interactive` flag (G7 in PR #7) only covers the *single-repo* case: it lists every open PR in the current repo. The multi-repo case is similar in spirit but materially different in implementation, and the use case is more niche, so it was deferred.

## Origin of the idea

Directly from `gh/gh-pr-detect::select_pr_from_repos`:

```bash
find_git_repos() {                  # cwd + immediate subdirs containing .git
    local repos=()
    [ -d ".git" ] && repos+=(".")
    for dir in */; do
        [ -d "${dir}.git" ] && repos+=("$dir")
    done
    printf '%s\n' "${repos[@]}"
}

get_pr_from_directory() {           # cd in, ask gh CLI for the current-branch PR
    local dir="$1"
    (cd "$dir" && gh pr view --json number,title,headRefName,baseRefName)
}

select_pr_from_repos() {            # aggregate + gum picker
    # ... loop over repos, call get_pr_from_directory, present choices ...
}
```

`gh pr view` under the hood is `GET /repos/{owner}/{repo}/pulls?head={owner}:{branch}&state=open`, which finds an open PR whose head ref matches the local current branch.

## Implementation sketch

### New REST helper

`internal/github/pulls.go` (existing file from G7) gains:

```go
// FindOpenPRForHead returns the open PR in (owner, repo) whose head ref equals
// the given headBranch. Returns (nil, nil) if no such PR exists. If headOwner
// is empty, defaults to owner (the same-repo PR case); set headOwner to a fork
// account to look up cross-repo PRs.
//
// Calls GET /repos/{owner}/{repo}/pulls?head={headOwner}:{headBranch}&state=open.
func (c *Client) FindOpenPRForHead(ctx context.Context, owner, repo, headOwner, headBranch string) (*PullRequest, error)
```

### Local git-state reader

`internal/threadsops/workspace.go` (new):

```go
package threadsops

// WorkspaceRepo describes a sibling git checkout found in a workspace scan.
type WorkspaceRepo struct {
    Dir          string  // absolute path
    OriginURL    string  // raw value of remote.origin.url
    Owner        string  // parsed from OriginURL via forge.ParseRepoURL
    Repo         string
    Provider     string  // "github" / "gitlab" / etc.
    CurrentBranch string // from HEAD or refs/heads/<branch>
}

// ScanWorkspace returns one entry per .git directory found at the immediate
// subdirectory level of root, plus root itself if it has a .git. Maximum
// scan depth is 1 by default; opts.Depth can extend it (cap at 3 to bound
// recursion).
//
// Returns entries with origin URL + parsed owner/repo + current branch read
// directly from .git/HEAD (no shelling out to git). Entries whose origin
// can't be parsed are returned with Owner/Repo empty but still listed so
// the caller can warn the user.
func ScanWorkspace(root string, opts ScanOptions) ([]WorkspaceRepo, error)

type ScanOptions struct {
    Depth  int  // default 1
    IgnoreDirs []string // additional names to skip (vendor/, node_modules/, etc.)
}
```

The HEAD-file parse is straightforward:
- `<dir>/.git/HEAD` is either `ref: refs/heads/<branch>\n` (normal) or 40 hex bytes (detached HEAD; treat as no current branch).
- `<dir>/.git/config` has an `[remote "origin"]` section with `url = ...`. Walk it line by line; no need for a full INI parser. Treat the `core.worktree`-pointing forms (worktrees, submodules) the same as a normal repo — `.git/HEAD` and `.git/config` still resolve.

### Aggregator

```go
// DiscoverPRsInWorkspace runs ScanWorkspace, then concurrently calls
// FindOpenPRForHead for each entry. Returns one entry per repo that has an
// open PR for its current branch. Repos without an open PR are omitted.
//
// Concurrency: up to opts.Parallelism (default 8) calls in flight. Per-call
// errors are logged via opts.Logger but don't fail the whole scan; the
// returned entry's PR field is nil for failed lookups.
func DiscoverPRsInWorkspace(ctx context.Context, fetcher PRFetcher, repos []WorkspaceRepo, opts DiscoverOptions) []WorkspaceDiscovery

type WorkspaceDiscovery struct {
    Repo WorkspaceRepo
    PR   *github.PullRequest  // nil if no open PR for current branch
    Err  error                // non-nil if the lookup itself failed
}
```

### CLI surface

`cmd/spoon/threads.go` extends the existing `--interactive` flag rather than introducing `--scan`:

- `--interactive` (existing): if PR ref absent AND only the current repo has `.git` (no siblings), behave as today (G7: `ListOpenPRs` on the current repo).
- `--interactive` (new): if PR ref absent AND multiple sibling `.git` directories exist, run the workspace-scan flow: `ScanWorkspace(cwd, {Depth:1})` → `DiscoverPRsInWorkspace` → render in the picker.

`--scan-depth N` flag (new): explicit override for the scan depth. Default 1. Cap at 3.

`--scan-only` flag (new): force the workspace path even when only one repo exists (useful for testing).

The picker (`internal/tui/threads/picker.go`) is extended to render entries with an origin column when results span multiple repos:

```
PR #42: Fix login bug              [acme/frontend in ./frontend]   (updated 3h ago)
PR #88: Add session timeout       [acme/backend  in ./backend]    (updated 6h ago)
PR #91: Doc the new auth flow     [acme/docs     in ./docs]       (updated 2d ago)
```

When all results are in the same repo (the original G7 case), the trailing `[...]` is suppressed.

### Selected PR → existing threads flow

Same as G7: the picker's selection produces a `forge.ParseRepoURL`-compatible string (`owner/repo#N`) and the rest of the threads pipeline runs unchanged. No additional plumbing needed downstream.

## Cost analysis

| Resource | Estimate |
| --- | --- |
| Per-scan disk I/O | reads `.git/HEAD` + `.git/config` for each scanned dir; typically 8 files for a workspace of 4 repos |
| API calls | 1 `FindOpenPRForHead` per repo with a current branch; default Parallelism=8 |
| Memory | O(repos) — typically <10 entries |
| Latency | dominated by GitHub round-trip; ~200-500ms total for a typical workspace |

No new third-party deps.

## Acceptance criteria

1. **One-level scan correctness.** Given a tmpdir with 3 sibling git checkouts (synthetic `.git/HEAD` + `.git/config` files), `ScanWorkspace` returns all three with correct owner/repo parsed from `origin`.
2. **HEAD parse robustness.** Detached HEAD → `CurrentBranch=""`, no panic.
3. **Origin parsing.** Origins like `git@github.com:owner/repo.git`, `https://github.com/owner/repo`, `https://gitlab.com/group/sub/repo.git` all parse correctly via `forge.ParseRepoURL`.
4. **Concurrent discovery.** With 8 repos × 100ms simulated API latency, total `DiscoverPRsInWorkspace` time is <250ms (i.e., concurrent, not serial).
5. **No-PR repos are omitted.** A repo whose current branch has no open PR doesn't appear in the picker.
6. **Per-repo failure is non-fatal.** Mocked transport returning 500 for one repo: the other repos' results still surface; the failed one is logged but doesn't error the whole call.
7. **Picker rendering.** When results span >1 repo, the picker shows the repo/dir suffix; when they're all in the same repo, the suffix is suppressed.
8. **Single-repo case unchanged.** Running `--interactive` in a directory with exactly one `.git` (the G7 case) produces identical output to today's behavior.

## Risks and open questions

- **Origin parsing breakage.** Repos cloned via mirror URLs, custom SSH configs, or hostname aliases may not parse. Mitigation: gracefully degrade — list the repo with empty Owner/Repo and a warning that its PR can't be looked up.
- **Submodules and worktrees.** `.git` may be a *file* (not a dir) pointing at the real gitdir. The HEAD/config parser must follow that indirection or skip such entries with a documented limitation. Easy enough — `git rev-parse` semantics — but adds code.
- **Cross-repo PRs.** Today's `FindOpenPRForHead` looks for `head={owner}:{branch}`. If the user is on a fork branch, the head owner differs from the repo owner; needs the `headOwner` param to be sourced from the actual fork's origin. Acceptable for v1 to skip cross-repo and document that fork PRs need an explicit ref.
- **Scan-depth bikeshed.** Should the default be 1 (immediate children) or recursive with a smart skip-list? 1 is unambiguous; recursive needs `.gitignore` handling and is slow. v1 stays at depth=1.
- **Discovery vs. listing semantics.** This feature is about "PRs whose head matches my local branches" — fundamentally different from G7's "all open PRs in this repo." Some users will want both: e.g., scan all sibling repos AND show all open PRs in each. Adds another flag axis; v1 chooses just the "current-branch" path.
- **What if NO sibling has an open PR for its branch?** Fall back to a clear message ("No open PRs found for current branches in `frontend`, `backend`, `infra`. Use `--interactive` in a single repo to list all open PRs, or pass a PR ref directly.")

## How to complete

1. **`internal/github/pulls.go`** — add `FindOpenPRForHead`. Tests against an httptest fake covering: hit (returns the one PR), miss (empty array → nil, nil), 404 / 403 propagation, multiple results (shouldn't happen but return the first or error).
2. **`internal/threadsops/workspace.go`** — implement `ScanWorkspace` using stdlib (`os.ReadDir`, `os.ReadFile`, `strings.Split`). Tests use tmpdirs with synthetic `.git/HEAD` + `.git/config` files; cover normal repo, detached HEAD, `.git` file (worktree), missing config, unparseable origin.
3. **`DiscoverPRsInWorkspace`** — concurrent fan-out using `errgroup` or a small worker pool. Test with a stub `PRFetcher` that returns deterministic results and simulated latency; assert wall-clock concurrency.
4. **Picker extension** — modify `internal/tui/threads/picker.go::View()` to render the repo/dir suffix when entries span multiple repos. Add tests against the picker model.
5. **CLI wiring** — extend `cmd/spoon/threads.go::runThreads` so that when `--interactive` is set, the dispatcher chooses between single-repo (existing) and workspace (new) flow based on `ScanWorkspace` results.
6. **Documentation** — update `cmd/spoon`'s `--help` text to mention the workspace mode. Note in the threads docs.
7. **Validation** — hand-test in a real workspace with sibling clones; verify the right PRs surface, the picker selection produces a valid ref, and the subsequent threads flow runs unchanged.

## Why this is a separate feature, not part of G7

G7 (single-repo interactive picker) calls `ListOpenPRs` which is one REST call against the current repo. Multi-repo discovery calls `FindOpenPRForHead` once per detected sibling — different endpoint, different shape of the result, different UI affordance (the picker shows repo+dir per entry instead of just title+author).

The shared infrastructure (the picker, the dispatch into the threads flow) is already in place from G7. This feature is purely additive: a new scan-and-aggregate layer that produces a picker-feed compatible with what G7 already renders.

## References

- `gh/gh-pr-detect` — the bash original (124 lines), specifically `find_git_repos` and `select_pr_from_repos`.
- `gh/gh-pr-help` — describes the workspace UX from a user's perspective.
- G7 implementation: commit `4b8ce34` on the `integrate-gh-toolkit` branch (the single-repo picker this feature extends).
