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

Requires Go 1.26+.

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
