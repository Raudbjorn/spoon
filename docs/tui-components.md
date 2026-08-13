# TUI component inventory

Classification date: **2026-08-12**.

This ledger records a terminal-native adaptation, not mechanical parity with the
Crepus source. `molecules.crepus` defines the thirteen templates listed first;
Card, Modal, Sheet, Tabs, NavBar, and Breadcrumb are Spoon-owned compositions
required by phase 4b and are absent from that upstream template. `views.crepus`
is declined: Spoon has no consumer for gallery-level screens.

| Component | Classification | Consumer or gate-4 reason | File | Source |
| --- | --- | --- | --- | --- |
| Button | adopted | repository input search action | button.go | `molecules.crepus:29-38` |
| Link | declined | no current Spoon link interaction; browser opening remains a key action | — | `molecules.crepus:40-49` |
| StatCard | adopted | fork detail heat metric | statcard.go | `molecules.crepus:51-70` |
| Input | adopted-at-atom-layer | repository/filter/rank/export prompts | atoms.go | `molecules.crepus:72-106` |
| Select | adopted-at-atom-layer | phase-4a atom API; no duplicate molecule | atoms.go | `molecules.crepus:108-127` |
| Checkbox | adopted-at-atom-layer | phase-4a atom API; no duplicate molecule | atoms.go | `molecules.crepus:129-142` |
| Radio | adopted-at-atom-layer | phase-4a atom API; no duplicate molecule | atoms.go | `molecules.crepus:144-149` |
| Switch | adopted-at-atom-layer | phase-4a atom API; no duplicate molecule | atoms.go | `molecules.crepus:151-162` |
| Alert | adopted | repository input validation and operation notices | alert.go | `molecules.crepus:164-175` |
| Tooltip | declined | contextual help is a full Sheet; no separate anchored-help consumer | — | `molecules.crepus:178-185` |
| Table | adopted | fork table header and selected row chrome | table.go | `molecules.crepus:187-198` |
| Timeline | declined | no chronological event-list consumer | — | `molecules.crepus:200-210` |
| CodeBlock | declined | no code-view consumer; compare opens the browser | — | `molecules.crepus:212-227` |
| Card | adopted | fork detail grouping | card.go | `phase-4b-molecules.md:30-31,60-64` |
| Modal | adopted | main-model consequence overlay | modal.go | `phase-4b-molecules.md:32,65-71` |
| Sheet | adopted | help heading and main-model overlay | sheet.go | `phase-4b-molecules.md:32-33,65-71` |
| Tabs | declined | settings sections do not exist yet, so a Tabs helper would be unconsumed | — | `phase-4b-molecules.md:34,60-64` |
| NavBar | adopted | fork-table status bar | navbar.go | `phase-4b-molecules.md:37,60-64` |
| Breadcrumb | declined | no immediate settings section-path consumer; do not ship dead composition code | — | `phase-4b-molecules.md:38,60-64` |

The inventory is deliberately scoped to `molecules.crepus` and phase-4b
compositions. It makes no claim that Spoon tracks future upstream changes.
