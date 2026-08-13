# Task 2 report — vendored palettes and typed theme

## Status

Implemented and locally verified Task 2 only. Profile parsing/quantization, glyph behavior, viewport work, atoms, keymap, settings, and CLI coloring were not implemented.

## Files changed

- `internal/tui/theme/tokens/{dark,light,amber}.tokens.json`: verbatim resolved-token vendor copies.
- `internal/tui/theme/tokens/UPSTREAM.txt`: source checkout, commit, 2026-08-12 sync date, and per-file SHA-256.
- `scripts/sync-design-tokens.sh`: explicit one-path vendor refresh, provenance rewrite, and generation.
- `scripts/check-design-tokens-upstream.sh`: explicit one-path byte comparison plus generation-current check; no mutation.
- `internal/tui/theme/gen.go`: ignored deterministic JSON generator; requires each named role under `tokens.<role>.css`, validates `#rrggbb`, and reports theme/role failures.
- `internal/tui/theme/palette_gen.go`: generated `Palette` and `Dark`, `Light`, `Amber` 23-role values.
- `internal/tui/theme/palette.go`: `PaletteByName`, immutable `Context`, declaration-only `ColorProfile`/`GlyphProfile`, and theme-local ordinal gutter ramp.
- `internal/tui/theme/tokens_test.go`: hermetic provenance, generator parity, 23-field/hex checks, parity spots, API/context checks, and production direct-color scan.
- `internal/tui/styles.go`: named role mappings; heat is `TextFaint → Info → Accent → Warning → Error`.
- `internal/tui/detail.go`, `internal/tui/table.go`, `internal/tui/threads/picker.go`: migrated every remaining production direct color site.
- `internal/tui/testdata/input-view-truecolor.golden`: palette-only ANSI-golden update.

## Provenance

Source checkout: `/home/svnbjrn/projects/spoon-design/design-system`

Upstream commit: `e5a4efa347b3df7bbd766a1547e105099b33c905`

Sync date: `2026-08-12`

| File | SHA-256 |
| --- | --- |
| `dark.tokens.json` | `b40117433750376c114b0654c353aaae80b3a7b94a7879aa02efc3c2fada4897` |
| `light.tokens.json` | `4ffa5ad7e1870e1e3a963a2b2ceb6b81dea204b72746a55d92269c7a82002882` |
| `amber.tokens.json` | `63b963499e9e9c50f0c0da90f280a328e42764b33bcf8b0c5b3ae97dc30cbf8d` |

## Generator and migration evidence

The generated struct has exactly the required 23 fields. Generation is deterministic; `go generate ./internal/tui/theme` left the committed generated output unchanged. The hermetic test reruns the generator with `-check`; it never requires a sibling checkout or network.

Migrated direct-color sites:

- `styles.go`: 11 former direct literals: five heat stops, six common style foreground/background values; gutter is now a named palette-derived six-color categorical ramp.
- `detail.go`: lone-wolf archetype color → `Dark.AccentRust`.
- `table.go`: lone-wolf badge color → `Dark.AccentRust`.
- `threads/picker.go`: cursor/highlight → `Dark.Accent`; author → `Dark.Info`.

The source guard passed: no non-test production `lipgloss.Color(` remains outside `internal/tui/theme/`.

## Golden review

The Task 1 golden was first observed failing after the palette migration, then regenerated with `env -u CI UPDATE_GOLDEN=1 go test ./internal/tui/... -count=1`. The four changed rendered lines only lost prior ANSI color sequences around the title, error, and help text; text, dimensions, and view structure did not change. A subsequent ordinary non-update test run passed.

## Commands and results

All from the Spoon worktree:

- `go generate ./internal/tui/theme` — exit 0.
- `git diff --exit-code internal/tui/theme/palette_gen.go` — exit 0.
- `go test ./internal/tui/... -count=1` — exit 0.
- `go build ./...` — exit 0.
- `scripts/check-design-tokens-upstream.sh /home/svnbjrn/projects/spoon-design/design-system` — exit 0.
- `git diff --check` — exit 0.

Before golden update, `go test ./internal/tui/... -count=1` correctly reported the expected golden mismatch. The first update attempt correctly failed because inherited `CI` was truthy; the required non-CI invocation above succeeded.

## Script non-mutation and idempotence

The check script was run between hashes of all token-task outputs; both hashes were `67be631ee28fa55c301b2b6d42c1004fabd245243f8fd34e4819d80d149643fb`, proving non-mutation. The sync script was then run twice against the supplied checkout; the same output hash was retained, proving idempotence for this same-date invocation.

## Commit

Implementation: `c967e3203fc1b59bb538ff3cf09b200c81877f94` — `feat(tui): vendor design token palettes`
This report is committed as the Task 2 evidence commit immediately after that implementation commit.

## Self-review concerns
- The runtime TUI remains intentionally dark-only for this task. `Context`, profile, and glyph declarations are compile-time scaffolding for Task 3 and are not wired into rendering.
- The title/help ANSI loss in the golden is a Lip Gloss non-TTY rendering characteristic of the Task 1 forced renderer; the explicit non-vacuity test still proves that ANSI is emitted somewhere in the representative render. The change was limited to palette-driven style role substitutions.
