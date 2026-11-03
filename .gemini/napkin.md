# Napkin

> Persistent working memory for this repository. Tracks mistakes, preferences, and patterns.

## Corrections
| Date | Source | What Went Wrong | What To Do Instead |
|------|--------|----------------|-------------------|

## User Preferences
- (accumulate here as you learn them)

## Patterns That Work
- GraphQL Phase A batch compare refreshes BehindCount in-place on CachedT2 forks while preserving cached AheadCount and diffs without incurring REST re-fetch overhead.
- FindOpenPRForHead resolves open PRs matching headOwner:headBranch using GET /repos/{owner}/{repo}/pulls?head={headOwner}:{headBranch}&state=open, defaulting headOwner to repo owner when empty.
- resolveGitDir handles both standard .git directories and .git pointer files (gitdir: <path>), resolving linked worktree commondir (for config) and worktree-local gitdir (for HEAD).
- Git URLs in SSH format (git@host:owner/repo.git and ssh://...) need normalization to HTTPS before being passed to forge.ParseRepoURL.
- DiscoverPRsInWorkspace queries open PRs against upstream with origin fallback using bounded workers.
- Multi-repo picker shows [owner/repo in ./dir] only when PRs span multiple repositories, and suppresses the suffix when all PRs belong to the same repository.

## Patterns That Don't Work
- (approaches that failed and why)

## Domain Notes
- (project/domain context that matters)
