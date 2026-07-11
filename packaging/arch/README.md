# Arch Linux Packaging

`PKGBUILD` builds `spoon` and `spn` from the latest `main` and installs:

- `/usr/bin/spoon`
- `/usr/bin/spn`
- `/usr/share/spoon/skills/using-spn/SKILL.md`
- `/usr/share/spoon/skills/using-spn-forks/SKILL.md`
- `/usr/share/doc/spoon-git/{README.md,spn-design.md}`

## Build locally

```sh
cd packaging/arch
makepkg -si
```

`makepkg -si` will run `go test ./...` during the `check()` phase. Append `--nocheck` to skip.

## Activate the skills for Claude Code

The skills ship under `/usr/share/spoon/skills/` because Claude Code doesn't auto-discover system paths. The post-install hook prints the activation commands; or run them manually:

```sh
mkdir -p ~/.claude/skills
ln -s /usr/share/spoon/skills/using-spn       ~/.claude/skills/using-spn
ln -s /usr/share/spoon/skills/using-spn-forks ~/.claude/skills/using-spn-forks
```

## Publishing to AUR

1. Generate `.SRCINFO`:
   ```sh
   makepkg --printsrcinfo > .SRCINFO
   ```
2. Push `PKGBUILD`, `.SRCINFO`, and `spoon-git.install` to the AUR git repo for `spoon-git`.

## Known gaps

- **No `LICENSE` file in the upstream repo.** The `license=` field uses `custom:unknown` as a placeholder. Once a license is added upstream, update both `license=()` and ship the license file via `install -Dm644 LICENSE ...` inside `package()`.
