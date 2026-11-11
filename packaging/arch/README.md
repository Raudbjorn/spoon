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

## Maintaining the recipe

- `vendor/` is tracked and contains a tokenizer fix. After `go.mod` changes,
  reconcile `go mod vendor` output without discarding intentional patches;
  `-mod=readonly` bypasses that vendored fix.
- Keep `CGO_ENABLED=1` and matching `CGO_*` hardening flags in both `build()` and
  `check()`: makepkg runs those phases in separate subshells, and tree-sitter
  needs a C compiler. Build both binaries and preserve their version embedding.
- `build()` uses `-mod=vendor -trimpath -buildmode=pie`; `check()` uses
  `-mod=vendor`. Tests locate fixtures through `runtime.Caller`, so copying
  `-trimpath` into the test flags breaks those lookups.
- Compilation and package checks do not establish successful installation or
  native runtime readiness. Report each kind of evidence separately.

## Activate the skills for Claude Code

The skills ship under `/usr/share/spoon/skills/` because Claude Code doesn't auto-discover system paths. The post-install hook prints the activation commands; or run them manually:

```sh
mkdir -p ~/.claude/skills
ln -sfn /usr/share/spoon/skills/using-spn       ~/.claude/skills/using-spn
ln -sfn /usr/share/spoon/skills/using-spn-forks ~/.claude/skills/using-spn-forks
```

## Publishing to AUR

1. Generate `.SRCINFO`:
   ```sh
   makepkg --printsrcinfo > .SRCINFO
   ```
2. Push `PKGBUILD`, `.SRCINFO`, and `spoon-git.install` to the AUR git repo for `spoon-git`.

## Known gaps

- **No `LICENSE` file in the upstream repo.** The `license=` field uses `custom:unknown` as a placeholder. Once a license is added upstream, update both `license=()` and ship the license file via `install -Dm644 LICENSE ...` inside `package()`.
