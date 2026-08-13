# TUI keymap

Reconciled **2026-08-12** against [`design-system/docs/tui-gallery.md:43-71`](../../design-system/docs/tui-gallery.md#keyboard-map). The registry in `internal/tui/keymap` is the executable source of truth for this table, dispatch, and contextual help.

## Main fork browser

| Scope | Keys | Action |
| --- | --- | --- |
| Global | `Ctrl+C` | Quit |
| Repository input | `Enter` / `Esc` | Search / return to table |
| Repository input, export, filter, rank | arrows, `Home`/`End`, `Ctrl+B`/`Ctrl+F`, `Ctrl+A`/`Ctrl+E`, `Backspace`, `Delete`, `Ctrl+U` | Edit input |
| Fork table | `Up`/`k`, `Down`/`j`, `PgUp`/`PgDn`, `Home`, `End`/`G` | Move selection or page |
| Fork table | `Enter`, `n`, `/`, `R`, `Esc`, `?` | Details, new repository, filter, intent rank, clear filter, help |
| Fork table | `g`, `s`, `S`, `o`, `d`, `c`, `t`, `f`, `y`, `r`, `Space`, `e`, `E`, `q` | Cluster, sort, reverse, open, compare, enrichment ceiling, theme, chrome, yank, refresh, mark, export, quit |
| Fork details | `Up`/`k`, `Down`/`j`, `PgUp`/`PgDn`, `Home`, `End`/`G` | Scroll |
| Fork details | `Esc`/`b`/`q`, `o`, `d`, `c`, `t`, `f`, `y` | Back, open, compare, enrichment ceiling, theme, chrome, yank |
| Help | `?`/`Esc`/`q`, `Up`/`k`, `Down`/`j`, `PgUp`/`PgDn`, `Home`, `End`/`G` | Close or scroll |
| Topic picker | `Up`/`k`, `Down`/`j`, `Enter`, `Esc`/`q` | Move, choose, cancel |
| Export/filter/rank prompt | `Enter` / `Esc` | Apply or cancel |

**D1 transition (one release):** `t` moved from enrichment ceiling to dark/light theme; `c` moved from compare to enrichment ceiling; `d` moved from unbound to compare. Both table and detail apply `c` and `d`. `f` toggles only existing table/detail chrome and preserves selection, paging, and scroll position.

## Review-thread browser

| Scope | Keys | Action |
| --- | --- | --- |
| Thread list | `Up`/`k`, `Down`/`j` | Move selection |
| Thread list | `r`/`Enter`, `R`, `a`, `Ctrl+A`, `A` | Reply, resolve, apply suggestion, resolve all, unresolve all |
| Thread list | `o`, `c`, `?`, `f`, `q`/`Ctrl+C` | Open PR, counter-propose, help, chrome, quit |
| Composer | `Ctrl+S` / `Esc` | Submit / cancel |
| PR picker | `Up`/`k`, `Down`/`j`, `Enter`, `Esc`/`q`/`Ctrl+C` | Move, choose, cancel |

Confirmation and composer handlers run before the list registry, so background `q`, `t`, and `f` bindings cannot consume an overlay event.

## Intentional deviations from the design-system map

| Binding | Spoon behavior | Reason |
| --- | --- | --- |
| `j`/`k`, `G` | Vi-style movement | Efficient terminal navigation; retained existing Spoon behavior. |
| `/`, `n`, `g`, `s`/`S`, `o`, `d`, `c`, `y`, `r`, `e`/`E`, `R` | Fork discovery, enrichment, export, and intent actions | Spoon-specific fork-browser operations have no gallery counterpart. |
| `b` | Back from details | Retained existing Spoon shortcut alongside `Esc`. |
| Thread `r`, `R`, `a`, `Ctrl+A`, `A`, `o`, `c` | Review workflow actions | Spoon-specific review-thread operations have no gallery counterpart. |

`q`, `?`, `Esc`, arrows, `Home`/`End`, `PgUp`/`PgDn`, `Enter`, `Space`, `t`, and `f` conform to the applicable published intent. The gallery's selected-story fullscreen behavior is translated narrowly to hiding/restoring existing Spoon chrome; Spoon does not add a gallery preview mode.
