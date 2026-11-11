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

- `vendor/` is tracked and contains local changes. Follow the preservation
  procedure below after dependency changes; `-mod=readonly` bypasses those fixes.
- Keep `CGO_ENABLED=1` and matching `CGO_*` hardening flags in both `build()` and
  `check()`: makepkg runs those phases in separate subshells, and tree-sitter
  needs a C compiler. Build both binaries and preserve their version embedding.
- `build()` uses `-mod=vendor -trimpath -buildmode=pie`; `check()` uses
  `-mod=vendor`. Tests locate fixtures through `runtime.Caller`, so copying
  `-trimpath` into the test flags breaks those lookups.
- Compilation and package checks do not establish successful installation or
  native runtime readiness. Report each kind of evidence separately.

### Preserving vendored changes

Regenerate with `go mod vendor` in a disposable checkout containing the intended
`go.mod` and `go.sum` changes. Compare its output with the tracked vendor tree
before applying it to the working branch. Preserve or deliberately port these
local changes when reconciling upstream updates:

- `vendor/github.com/sugarme/tokenizer/util.go`: nil-pair truncation and bounds
  guards; `internal/embed/truncate_vendor_test.go` covers the crash regression.
- `vendor/github.com/sugarme/tokenizer/init.go`: quiet initialization and the
  fallback for an unwritable cache directory.
- `vendor/github.com/anush008/fastembed-go/fastembed.go`: local runtime changes.
- `vendor/github.com/tursodatabase/go-libsql/lib/`: tracked native libraries and
  headers required by the vendored binding.

Do not restore entire old dependency directories over an upstream upgrade;
reconcile the local changes with the new source. Review `vendor/modules.txt`
alongside `go.mod`/`go.sum`, then run the offline tokenizer regression with
`go test -mod=vendor ./internal/embed -run '^TestTruncateEncodings'`, followed by
the relevant package tests and both CLI builds before committing dependency and
vendor changes together. This procedure requires no model download or live API.

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
