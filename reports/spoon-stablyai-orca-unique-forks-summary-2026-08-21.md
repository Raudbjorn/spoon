# spoon-export-stablyai-orca — Unique-Forks Summary Report

**Date:** 2026-08-21
**Source:** `/home/svnbjrn/dev/spoon/spoon-export-stablyai-orca-2026-08-21--exported.json`
**Parent repo:** [`stablyai/orca`](https://github.com/stablyai/orca) — 50,389 ★, default branch `main`
**Export scope:** 68 forks in this file (3,065 forks in the broader network; this file is a subset — likely the "tier-3 / interesting" slice)

---

## 1. Inputs & method

- **File format:** spoon-export JSON envelope with `parent`, `forks[]`, `enriched_count`, `total_count`, `degraded`, `exported_at`.
- **Per-fork fields used:** `full_name`, `url`, `stars`, `forks`, `pushed_at`, `divergence{ahead,behind,files_changed,additions,deletions,mna,feature_commit_ratio}`, `heat{score,tier,confidence}`, `lone_wolf{archetype,strength}`, `why_distinct`, `cluster_id`, `novelty_score`, `change_impact`, plus the `compare_url`.
- **Triage used for every entry:**
  - signature-uniqueness check (65 distinct signatures among 68 forks; **two starved-clone micro-clusters** share a signature — see Group C),
  - live-commit sampling via `gh api repos/<owner>/<repo>/commits` for the higher-tier forks to read the actual work,
  - existing deep-dive report at `reports/spoon-stablyai-orca-evaluation-2026-08-21.md` (already on disk; covers the 8 most-novel forks).
- **Honesty note:** not every fork has been commit-sampled. Forks marked *metric-only* below describe their position from the export data, not the code.

---

## 2. TL;DR — fork universe at a glance

| Stat | Value |
| --- | --- |
| Forks in file | 68 |
| Unique divergence signatures | 65 of 68 — two starved-clone micro-clusters (`behind=2,172` triple; `behind=2,724` pair) account for the 3 collisions |
| Forks with `ahead > 0` (real work) | 60 (88%) |
| Forks with `ahead == 0` (starved clones) | 8 |
| Highest-starred fork | `JCodesMore/orca` — 10 ★ |
| Single-commit "feat/refactor only" sniper commits | 1 (AlejandroAkbal) |
| Longest-running drifter | `andrewyatesai/orca-alab` — 1,633 ahead commits, parallel re-implementation |
| Fork-of-a-fork fork | `DevZonayed/orca` — 7,708 ahead / 8,992 behind |
| Renamed forks | `paidaxingyo666/Manta`, `DarwinWDEWEN/rx-cli`, `wangazhang/orca` (`Yoha` productName), `rudironsoni/orca` (`Humpback` → `Horca` distribution identity) |
| Lone-wolf archetypes seen | Feature Builder, Drifter, Sniper |

**Auto-farm check:** 65 distinct signatures among 68 forks — almost all forks diverge independently. There are **two micro-clusters** that share a signature, both starved clones (ahead=0):
- `behind=2,172` triple (`18163623522/orca`, `taeungshin/orca`, `ttkhanh-keylp/orca`) — same `base_sha` + `head_sha: None`.
- `behind=2,724` pair (`GokhanSanchez42461/orca`, `ValuxiCoj3/orca`) — same `base_sha` + `head_sha: None`.

All five are automated fork farms or tutorial forks. The remaining 63 forks all have unique divergence fingerprints and are independent per-developer forks.

---

## 3. Fork-by-fork uniqueness

Each entry: **what makes it unique**, what its `why_distinct` and `lone_wolf` actually reveal, and where the work is.

### Group A — Highest-novelty forks (real, deep work)

#### A1. `JCodesMore/orca` — the popular one

- **Stars:** 10 (highest in the export).
- **Signature:** ahead 20, behind 2,285, files 33, +2,750/-490.
- **Lone wolf:** Feature Builder, strength 96%, 17 meaningful commits, 60-day span, file spread 0.9.
- **What makes it unique:** single coherent feature called **Spaces** — sidebar tabs that group projects; per-Space unread badges; cross-Space unread total on the All Projects pill; drag repos and worktrees onto Space tabs; jump-to-workspace switches the active Space and focuses the agent pane. Recent commit sample: `feat: mark Space notifications as read from the sidebar tab context menu`, `feat: show per-Space working-agent spinner chips on sidebar tabs`, `feat: drop repos and worktrees onto Space tabs via HTML5 drag`.
- **Why it matters:** the only fork where heat score, stars, and feature quality all co-vary. Real product thinking, not patch spam.

#### A2. `DarwinWDEWEN/rx-cli` — the team-collaboration layer

- **Stars:** 0.
- **Signature:** ahead 15, behind 437, files 112, +19,636/-3.
- **Lone wolf:** strength 84%, 7-day commit span, single contributor.
- **Renamed:** yes — `rx-cli`.
- **What makes it unique:** the only fork that ships a full **team-collaboration feature domain** upstream has not built. Layered construction: `feat(collaboration): R17 M2 worktree allocator (D2) + lint closeout`, `R16 M2 backend start (D1 issue-lifecycle engine + D3 issue-worktree store)`, `R15 M1 backend closeout (B7 git-ref store, A5 activity-log store, B2 agent-config/skill-binding)`, `C9 PR list/detail page + minimal B6 pr-store`, `C8 issue list/detail page with inline status/priority editing`, `E1 pipeline CLI`, `C6 project onboarding`, `C7 project team UI`, `C3-C5 sidebar entries + Teams/IssuesPRs pages + preload IPC`, `C1-C2 activeView issues-and-prs/teams + nav history`. Companion docs: `docs/collaboration/PROGRESS.md`, R10-R12 iteration docs.
- **What makes it unique (deep):** scope and isolation discipline — the fork keeps its feature behind a clean IPC contract and writes design docs ahead of code.

#### A3. `brennoaf/orca` — the integrations + themes surface fork

- **Stars:** 0.
- **Signature:** ahead 42, behind 163, files 300, +18,712/-100. Heavy.
- **Lone wolf:** Drifter, strength 66%, 9-day span, file spread 1.
- **What makes it unique:** a **broad surface-level feature fork** that bolts Discord / Slack / WhatsApp fast-response workspaces, Spotify system media controls, and a theme gallery onto the desktop app. Recent commits: `feat(app): integrate communication and theme surfaces`, `feat(themes): add interface theme gallery`, `feat(spotify): add system media controls`, `feat(discord): add compact communication hub`, `feat(slack): add compact web fast-response`, `feat(whatsapp): add compact web fast-response`, `feat(communications): add detachable fast-response workspace`, `refactor(integrations): remove API credential settings`, `build(electron): bundle sandboxed preloads`.
- **Warning sign:** the `refactor(integrations): remove API credential settings` commit strips API-credential plumbing — security-sensitive, the opposite of what an Arch packager wants.

#### A4. `andrewyatesai/orca-alab` — Rust terminal engine wrapper

- **Stars:** 1.
- **Signature:** ahead 1,633, behind 1,168, files 300, +10,921/-13,303. Largest "ahead" in the file.
- **What makes it unique:** wraps Orca around the [aterm](https://github.com/alabsystems/aterm) Rust terminal engine. The cadence is a **parallel re-implementation** in Rust, not a fork that lands upstream. Commit cadence: `port(parity): +31 cadence sync — 8 upstream items (Aug 18) re-derived in spirit`, `+34 cadence sync — 14 upstream items (Aug 17-18) re-derived in spirit`, `+79 cadence sync — 38 upstream items (Aug 16-17) re-derived in spirit`, `+300-delta batch 1..5 — 13-20 upstream items re-derived in spirit`.
- **What makes it unique (deep):** an **independent sibling tree** rather than a fork you can merge. README positions the product as "Orca: ALab Edition", aligned to upstream Orca v1.4.147 with a separate versioning scheme.

#### A5. `paidaxingyo666/Manta` — self-hosted relay, renamed identity

- **Stars:** 0.
- **Signature:** ahead 225, behind 205, files 300, +9,455/-3,865. The most "balanced" ahead/behind in the file.
- **Renamed:** yes — `Manta`.
- **What makes it unique:** drops mandatory cloud account sign-in, adds **self-host relay server**, i18n, multi-cloud publishing. The commit trail: `feat(relay): publish versioned images to Docker Hub, Aliyun, and Tencent`, `fix(cloud): stop defaulting sign-in at a private relay`, `perf(relay): build each architecture on its own runner`, `fix(relay): commit the lockfile the image build needs`, `fix(relay): check repository paths before building, not at push`, `chore(relay): drop the diagnostic probes`.
- **What makes it unique (deep):** ships `relay-server/` as a real artifact (described in the README), and rewrites the README around the fork with a build-from-source recipe. **Existence proof that Orca's relay layer can be self-hosted** without forking the desktop app wholesale.

#### A6. `cyrus123456/orca` — custom CLI agents + sync-upstream workflow

- **Stars:** 0.
- **Signature:** ahead 136, behind 0. **Cleanest possible divergence: ahead-only.**
- **Lone wolf:** Drifter, strength 100%, 22-day span, file spread 1.
- **What makes it unique:** original work is **CustomAgent type** — custom CLI agents in terminal tabs with their own icon handling and prompt-delivery options: `feat: add support for custom agents in terminal tabs`, `feat: enhance custom agent management and icon handling`, `feat: add prompt delivery options for custom agent launch in new tab`. Around it, a discipline layer: `chore: add workflow to sync with upstream main branch`, `ci: build unsigned Windows release when upstream cuts a stable release`, `chore: update sync workflow to merge upstream main instead of fast-forward`, `fix(build): drop broken version-match gate, force-set version in build job`.
- **What makes it unique (deep):** the sync-upstream workflow is the closest existing reference to what a SAUR `-git` recipe would look like.

#### A7. `zpyoung/orca` — docked composer + sandboxed test runner + AGENTS.md runbook

- **Stars:** 0.
- **Signature:** ahead 207, behind 425, files 300, +9,629/-10,006.
- **What makes it unique:** three discrete, reusable artifacts:
  1. `feat: docked rich-input composer for terminal agent panes (#9)` — UI piece.
  2. `feat(fork): keep vitest on the remote sandbox host` and `Add Docker-based sandboxed test-shard runner` — CI / sandboxing tooling.
  3. `refactor(fork): make the sync-upstream skill the runbook`, `docs(fork): declare the fork feature structure in AGENTS.md`, `Declare fork ownership in a manifest and resolve syncs from it (#11)` — fork **operational documentation** in the same shape SAUR uses (`docs/packaging-guide.md`, `README.md`).
- **What makes it unique (deep):** the **sync-upstream skill as runbook** pattern is reusable.

#### A8. `Oxeegen/OxeeUI` — thin fork with one relay-region preference

- **Stars:** 3 (third-highest after `JCodesMore`).
- **Signature:** ahead 14, behind 326, files 122, +7,141/-747. 6 contributors.
- **What makes it unique:** mostly upstream merges + a single real feature: `feat(relay): prefer the closest available region (#14366)`. Plus a WSL transcript fix (`Kill hung WSL transcript filesystem operations via child process with route quarantine (#15381)`).
- **Verdict:** low-signal. The stars are misleading — most volume is upstream churn.

### Group B — Medium-novelty forks (real work, smaller surface)

#### B1. `inlineapps/orca` — three discrete, cherry-pickable features

- **Stars:** 0.
- **Signature:** ahead 10, behind 295, files 187, +11,615/-448.
- **Lone wolf:** strength 94%, 12-day span.
- **What makes it unique:** three features in 10 commits:
  1. `feat(editor): add TypeScript language service` + `feat(editor): extend TypeScript language service to diff views`
  2. `feat(tasks): add Asana task provider` + `feat(tasks): lazy-load Asana sections, add subtasks and caching`
  3. `feat(agents): narrow launch presets to curated Claude model/effort pairs` + `feat(agents): pick a model when continuing a session in a new one` + `feat(agents): pick a model when launching an agent` + `feat(agents): copy the session handoff prompt from the continuation dialog`
- **Plus a build fix:** `fix(build): emit serve-mode-argv so the CLI require graph resolves`.

#### B2. `molon/orca` — mobile push hook

- **Stars:** 0.
- **Signature:** ahead 2, behind 7, files 40, +2,235/-380. Single contributor.
- **What makes it unique:** one tight mobile-push feature: `feat(mobile-push): notify a phone from an agent hook, and fix live input` plus `fix(mobile): regenerate the RN patch in the format pnpm emits`. Otherwise mostly upstream-aligned.

#### B3. `BlackCatCXIII/orca` — operator-wave integration, fork policy docs

- **Stars:** 0.
- **Signature:** ahead 59, behind 269, files 175, +16,074/-509.
- **What makes it unique:** an internal-team fork that ships **fork-policy docs + CI hardening**, not user-facing features: `test: make relay GC order assertion portable`, `docs: keep source Actions permanently disabled`, `docs: make daily workflow enablement fail closed`, `ci: explicitly disable setup-node caching`, `ci: route reviewed workflows through ARC`, `refactor(config): nest environment recipe contracts`, `merge: sync reviewed operator wave 1 with upstream c0a775454`.
- **What makes it unique (deep):** it documents the **rules for keeping fork-owned workflows off upstream Actions** — useful as a fork-operations reference.

#### B4. `user141514/orca` — orchestration routing

- **Stars:** 0.
- **Signature:** ahead 2, behind 51, files 63, +3,404/-292. Single contributor.
- **What makes it unique:** adds a **mission-orchestration layer**: `feat(mission): route complex missions through orchestration` + `feat(orchestration): add durable structured plans and root mission entry`. Plus `feat(terminal): read the rendered screen with terminal read --screen (STA-4792)`.

#### B5. `pedrobalsa/balsa-ade` — subscription orchestration fork

- **Stars:** 1.
- **Signature:** ahead 2, behind 220, files 94, +7,729/-122.
- **Renamed:** yes — `balsa-ade`.
- **What makes it unique:** two commits only: `feat: migrate subscription orchestration into Balsa ADE`, `chore: establish Balsa ADE foundation`. Rest is upstream churn.

#### B6. `646826/orca` — multica hybrid CLI/exec transport

- **Stars:** 0.
- **Signature:** ahead 22, behind 676, files 118, +14,564/-65. 2 contributors.
- **What makes it unique:** a **hybrid profile / multica layer**: `feat(multica): route commands across execution hosts`, `feat(multica): negotiate instance capabilities (#17)`, `feat(multica): unify read transport selection (#16)`, `feat(multica): add secure REST transport (#15)`, `feat(multica): build safe API requests`, `feat(multica): execute local CLI processes safely`, `feat(multica): persist host profiles and secrets`, `feat(multica): add protected credential primitives`, `feat(multica): add hybrid profile state contract`, `feat(multica): add bounded host execution envelope`.

#### B7. `wangazhang/orca` — **Yoha** renamed distribution

- **Stars:** 0.
- **Signature:** ahead 29, behind 2,199, files 199, +16,178/-1,006.
- **Renamed:** yes — `productName: Yoha`. Recent: `chore(release): 1.4.149`, `fix(identity): follow the product name in the tray, notification, and titlebar`, `fix(identity): stop showing the Orca brand in Yoha's window title and copy`, `fix(identity): declare productName so the runtime app name is Yoha, not orca`, `test(electron-builder): expect the Yoha Linux identity`, `fix(identity): ship as Yoha so this fork can coexist with an installed Orca`.
- **What makes it unique:** an **identity-rewrite distribution**: same code, different productName, fork-owned updater with `feat(updater): publish from this fork, add offline periodic licensing`. Plus multi-repo plumbing: `feat(multi-repo): sandbox middleware auto-detection, wizard back nav, repos rename`.

#### B8. `wintergl/orca` — Drifter with thin surface

- **Signature:** ahead 40, behind 1,567, files 300, +18,936/-80. Single contributor.
- **Metric only:** Drifter archetype, strength 61%. Export shows mostly upstream churn with a small fork-specific layer; no commit sample taken here.

#### B9. `GhostFlying/orca` — Drifter, sat on `fork` branch

- **Signature:** ahead 11, behind 604, files 96, +8,607/-6,931. (Note: deletions 6,931 — high.)
- **Lone wolf:** Feature Builder, strength 69%, 4-day span, file spread 1.
- **Default branch:** `fork` (unusual). Metric only.

#### B10. `jae-heo/orca` — **NeurOrca** distribution on branch `local/neurorca`

- **Signature:** ahead 26, behind 2,354, files 178, +10,364/-720. Single contributor.
- **Branch name:** `local/neurorca`.
- **What makes it unique:** a **separate integration distribution** with isolation discipline. Recent commits: `fix(file-explorer): show import failure reasons`, `fix(neurorca): handle inherited remote build output`, `fix(neurorca): verify Linux provenance sidecar path`, `build(neurorca): bind artifacts to source commits`, `feat(projects): separate source runtime from project host`, `chore(neurorca): codify safe update operations`, `build(neurorca): harden repeatable macOS builds`, `fix(neurorca): isolate packaged CLI runtime metadata`, `fix(packaging): unpack managed hook implementations`, `fix(neurorca): brand packaged CLI help`, `build(neurorca): add isolated integration distribution`.

#### B11. `innocarpe/orca` — multi-LLM contribution harness

- **Signature:** ahead 11, behind 1,990, files 20, +2,337/-0.
- **Lone wolf:** Feature Builder, strength 77%, 15-day span.
- **What makes it unique:** a **worktree-private harness** that automates OSS contribution workflows: `feat(harness): isolate upstream and fork label operations`, `feat(harness): audit contribution worktree policy`, `feat(harness): require fresh main for PR follow-ups`, `chore(agent): require English for public OSS artifacts`, `chore(agent): add orca-merge-playbook skill for merge-rate gates`, `chore(agent): hide worktree harness via worktreeConfig excludesFile`, `chore(agent): worktree-private exclude so harness never hits primary or PRs`, `chore(agent): auto-bootstrap multi-LLM harness on contribution worktrees`, `chore(agent): multi-LLM contribution harness for Claude Code and Codex`.

#### B12. `WYK15/orca` — fork release pipeline (per-fork version bump)

- **Signature:** ahead 134, behind 1,144, files 300, +12,297/-2,009. 3 contributors.
- **What makes it unique:** a **per-fork release pipeline** with macOS signing/notarization. Recent commits: `chore(release): bump version to 1.4.165-wyk.11`, `feat(editor): improve Markdown editing and source outline`, `fix(i18n): rename rich Markdown mode in Chinese`, `fix(editor): preserve Windows active file tabs`, `fix(macos): sign helpers before notarization`, `ci(release): notarize macOS fork packages`, `perf(ai-vault): cache unchanged remote transcripts`, `fix(relay): avoid pgrep procfs scans on Linux`, `fix(worktrees): preserve terminals after failed WSL scan`, `feat(ai-vault): select and delete multiple sessions`, `fix(ai-vault): delete complete Codex sessions`.

#### B13. `Perc-Innovation/Perc-Orca` — production fork with Perc features

- **Signature:** ahead 79, behind 9, files 225, +9,661/-621. 2 contributors.
- **Renamed:** yes — `Perc-Orca`.
- **What makes it unique:** first production promotion: `release: primera promoción a producción (Orca + features de Perc)`. Mostly upstream-aligned with one structural fix: `fix(rpc): break the schema import cycle between the worktree schema modules`.

#### B14. `machamy/orca` — **Unity worktree toolbar tinting**

- **Signature:** ahead 72, behind 54, files 246, +45,219/-30,136. Single contributor.
- **What makes it unique:** a **Unity Editor integration** that tints per-worktree: `feat(unity): make the toolbar tint dark and saturated instead of washed`, `docs(fork): record the tint default and the git-ignore guard in machamy.6`, `feat(unity): tint worktrees by default, and never write where git would see it`, `chore(fork): cut machamy.6 — per-worktree Unity colour`, `fix(unity): leave the default worktree untinted`, `fix(unity): stop two worktrees from drawing the same tint`, `feat(unity): tint the play-controls toolbar per worktree, drop the unrendered chip`, `feat(unity): give each worktree its own colour in the Unity toolbar`, `chore(fork): cut machamy.5 — first-open crash fix, drag-to-default restored`, `fix(unity): defuse Firebase's first-open regeneration on fresh worktrees`, `feat(worktree): rewire drag-to-make-default into the pointer-drag system`, `chore(fork): cut machamy.4 — Open in Rider`, `feat(unity): Open in Rider — solution-first, seeded from the default checkout`.

#### B15. `JustShinobi/orca` — long-lived branch `justshinobi/main`

- **Signature:** ahead 119, behind 2, files 193, +14,626/-1,866. 14 contributors.
- **What makes it unique:** mostly upstream churn plus a **CI gate**: `ci(e2e): gate scheduled E2E runs to stablyai/orca to avoid cron failures on fork`. Active branch with frequent upstream merges.

#### B16. `Dos2Locos/orca` — upstream-merge-aligned with minor fixes

- **Signature:** ahead 68, behind 797, files 219, +13,839/-2,260. 8 contributors.
- **What makes it unique:** mostly upstream-aligned. Recent stack: `Merge upstream release v1.4.180`, `feat(github): support stacked pull requests (#13730)`, `Preserve AskUserQuestion waits during parallel tool completions (#13714)`. Metric-only.

#### B17. `judongli/orca` — **task inbox board** + **memo panel**

- **Signature:** ahead 27, behind 2,720, files 62, +4,690/-33. Single contributor.
- **What makes it unique:** a **docked task-inbox board with state rails** and a **per-pane badge title mode**: `fix(terminal-pane): stop badge title overlapping terminal content`, `feat(terminal-pane): add per-pane top-left badge title mode`, `feat(memos): add sidebar memo panel with journal tab strip`, `feat(task-inbox): add Cmd/Ctrl+Shift+D to dismiss focused pane's task`, `feat(task-inbox): sustain pane rim flash for 4s on inbox jump`, `feat(task-inbox): focus and flash the target agent pane on row click`, `fix(task-inbox): drop restart-replayed done rows to prevent board flood`, `style(task-inbox): restyle board as a paper-journal section`, `feat(task-inbox): mount board as docked sidebar section`.

#### B18. `sbkim/orca` — FCM push + tab-bar nested script tree

- **Signature:** ahead 11, behind 1,987, files 130, +11,747/-82. Single contributor.
- **What makes it unique:** **end-to-end FCM push for desktop** + a `tab-bar` package-script runner: `feat: add headless FCM configuration`, `ci: prevent implicit dev artifact publishing`, `ci: publish dev desktop prereleases`, `fix(mobile): stop FCM delivery when notifications are disabled`, `fix(build): recognize newer Electron Node builtins`, `feat(tab-bar): add nested package script tree`, `feat(fcm): configure end-to-end push notifications`, `feat(tab-bar): add Run Script button for package.json scripts`. Plus AppImage wiring: `fix: route AppImage FCM commands to CLI`.

#### B19. `rudironsoni/orca` — **Horca** distribution identity (was Humpback)

- **Signature:** ahead 18, behind 0, files 58, +1,386/-117. 3 contributors.
- **Renamed:** yes — `Horca` (was Humpback).
- **What makes it unique:** **centralized distribution identity contract** + auto-merge sync workflow: `ci: auto-merge green upstream sync PRs (#21)`, `Rename downstream distribution from Humpback to Horca (#19)`, `Recreate upstream-main when a merged sync PR deleted it (#18)`, `Close identity-audit gaps: helper peer trust, notification bundle id, firewall rule, speech cache, UI branding`, `feat: disable the in-app updater entirely for downstream distributions`, `feat: isolate distribution-owned runtime identity and local state`, `feat: package downstream builds with Humpback identity on macOS and Windows`, `feat: add centralized distribution identity contract with ORCA_DISTRIBUTION define`.

#### B20. `eocodn/orca` — **Windows Tauri Agent Control** on branch `ade-windows-live-recovery`

- **Signature:** ahead 1,060, behind 1,309, files 300, +6,628/-18,410. Single contributor.
- **Branch name:** `ade-windows-live-recovery`.
- **What makes it unique:** a **Windows Tauri packaging + Agent Control endpoint** chain: `fix(tauri): include default frontend entry`, `fix(ci): probe installed Agent Control endpoint`, `fix: statically link Windows runtime`, `fix(ci): normalize NSIS install location`, `ci: verify Windows installer lifecycle`, `build: package unsigned Windows Tauri installer`, `fix(terminal): complete Windows PTY cleanup`, `refactor(terminal): split PTY backend and contract`.

#### B21. `vnp-community/orca` — large fork (signature only)

- **Signature:** ahead 176, behind 2,459, files 300, +23,325/-1,661. Metric only — no commit sample.
- **Verdict:** not analyzed; appears mostly upstream churn by volume.

#### B22. `yongwuzhijin/orca` — large fork (signature only)

- **Signature:** ahead 154, behind 2,001, files 258, +25,269/-108. Metric only.

#### B23. `AVeryLostNomad/orca` — **Joey's fork** with embedded Monaco + scratch files

- **Signature:** ahead 67, behind 238, files 300, +16,200/-1,350. 3 contributors.
- **Branch name:** `stable`. Custom description: *"Forked for Joey's purposes."*
- **What makes it unique:** a **code-server + embedded Monaco editor** direction with GitHub multi-account support: `feat(git): Improve git integration to support selected account better`, `Can move changes`, `Scratch file support`, `Fix go semantic colors`, `Explorer improvements`, `feat(windows): Fix lsp and package settings`, `fix(build): package vscode-jsonrpc and vscode-uri runtime deps`, `Color themes`, `Better built in editor`, `Add github auth multiaccount support`, `fix(windows): Fix windows versions of data studio`, `feat(windows): Try once more to fix windows builds`, `chore(windows): pin sha256 for code-server-win32-v4.127.0-orca.2`.

#### B24. `andremohrmann/orca` — **Orca dashboard live view** branch

- **Signature:** ahead 42, behind 2, files 142, +4,974/-8,227. 3 contributors.
- **Branch name:** `custom/orca-dashboard-live-view`.
- **What makes it unique:** a **live-dashboard branch** that routes remote-host input to the dashboard: `Merge remote-tracking branch 'upstream/main' into custom/orca-dashboard-live-view`, `Fix remote input test typing`, `Route live preview input to remote hosts`, `Sync live dashboard workspace renames`.

#### B26. `yoke233/orca` — test-refactor fork

- **Signature:** ahead 81, behind 2, files 80, +2,220/-353. Single contributor.
- **What makes it unique:** mostly upstream-aligned; one refactor `test(pty): remove obsolete echo probe coverage`.

#### B27. `AlejandroAkbal/orca` — Sniper on WSL/CJK fixes

- **Signature:** ahead 1, behind 95, files 11, +596/-5. Single contributor.
- **Lone wolf:** Sniper, strength 34%, 0-day span, file spread 0.3.
- **What makes it unique:** a **small WSL/CJK/hermes fix bundle**: `fix: harden Hermes native chat fallbacks`, `fix(git): run WSL git reads without a shell (#15257)`, `fix(git): fence buffered WSL login-shell reads (#15060)`, `test(terminal): pin that the CJK block is the preedit overlay, not the cursor (#15242)`, `fix(wsl): read machine output from a fenced login shell (#15290)`, `ci(e2e): install a CJK font on the e2e runners (#15259)`, `fix(wsl): pass guest argv verbatim through --exec (#15039)`.

#### B28. `Seven-Day-Inc/morpheus-shell` — **Traycer-style reskin** on branch `pin/v1.4.183`

- **Signature:** ahead 31, behind 505, files 178, +10,987/-1,148. 8 contributors.
- **Branch name:** `pin/v1.4.183`.
- **What makes it unique:** a **founder-ratified visual-reskin brief** on a pinned upstream tag: `slice 3: reskin brief — 11 laws, 4 warts, Traycer soul, visual-checkpoint process (founder-ratified)`, `docs: record Windows foundation build blocker`. The rest is upstream release-aligned.

#### B29. `hyoteis/orca` — active, metric only

- **Signature:** ahead 39, behind 505, files 201, +11,077/-1,170. 7 contributors. Recent push.

#### B30. `treeman99/orca` — team-style upstream merges

- **Signature:** ahead 153, behind 269, files 102, +6,513/-733. 12 contributors. Recent push.
- **What makes it unique:** mostly upstream sync with a few small fixes. Likely a team fork.

#### B31. `jborkowski/orca` — Drifter, frozen

- **Signature:** ahead 24, behind 2,458, files 300, +13,711/-3,824. Single contributor.
- **Lone wolf:** Drifter, strength 75%, 19-day span.

#### B33. `houlemon0130/orca` — branch `qodercli-support`

- **Signature:** ahead 29, behind 790, files 130, +3,750/-278. 2 contributors.
- **Branch name:** `qodercli-support`.

#### B34. `b0d9a/orca` — high-volume Drifter

- **Signature:** ahead 25, behind 904, files 300, +19,843/-0. 2 contributors.
- **Note:** additions-only (+19,843/-0). Metric only.

#### B35. `justinwalters/orca` — small localization fix

- **Signature:** ahead 21, behind 267, files 18, +1,302/-6. 2 contributors.
- **What makes it unique:** `fix: localize Resource Monitor status bar strings` + a fork README (`docs: replace internal tracking docs with a fork README`) + upstream-sync record (`docs: record upstream sync to 1.4.178-rc.2`).

#### B36. `jasonyuezhang/orca` — metric only

- **Signature:** ahead 9, behind 549, files 86, +3,301/-227. 3 contributors.

#### B37. `ktwsmart/orca` — metric only

- **Signature:** ahead 11, behind 435, files 28, +3,370/-76.

#### B38. `Anony68/orca_deck` — `orca_deck` repo name

- **Signature:** ahead 11, behind 1,416, files 135, +5,925/-913. Single contributor.
- **What makes it unique:** mostly upstream-aligned. Latest merge: `Merge upstream stablyai/orca main into local main`. Touches feedback (`feat(feedback): attach images to feedback submissions (#10465)`), Jira (`feat(jira): link Jira issues from the workspace create dialog (#11296)`), Computer provider supervision.

#### B39. `Revan620198/orca` — **OS support floors**

- **Signature:** ahead 9, behind 1,375, files 49, +9,737/-5. 2 contributors.
- **What makes it unique:** **macOS / Windows / Linux OS support floor** + CI gate: `feat: block Windows installs below the floor, document the Electron-upgrade path`, `feat: warn at startup when the OS is below the support floor`, `feat: fail CI when the OS support floor drifts`, `feat: declare the macOS and Windows OS support floors`, `docs: audit legacy OS support floors across macOS, Windows, and Linux`. Plus `.claude/agents/` agent team.

#### B40. `Him188/orca` — metric only

- **Signature:** ahead 7, behind 1,262, files 60, +5,430/-1,918.

#### B41. `tfzou001/orca` — small Feature Builder

- **Signature:** ahead 9, behind 728, files 16, +784/-62. Single contributor.
- **Branch name:** `develop`. Lone wolf: Feature Builder, strength 38%.

#### B42. `munlucky/orca-moon` — renamed fork (moon)

- **Signature:** ahead 2, behind 2,778, files 300, +15,565/-497. Single contributor.

#### B43. `wangzhengzhuo05/orca` — metric only

- **Signature:** ahead 2, behind 347, files 54, +3,284/-158. 2 contributors.

#### B44. `Pierre-Mike/orca` — high-deletions Drifter

- **Signature:** ahead 6, behind 425, files 174, +1,374/-16,624. 5 contributors.
- **Note:** large deletions (-16,624) — likely a deliberate pruning fork. Metric only.

#### B45. `Gagan-k0/orca` — metric only

- **Signature:** ahead 5, behind 827, files 25, +3,049/-6.

#### B46. `ecurie-ai/orca` — minimal fork

- **Signature:** ahead 7, behind 16, files 24, +124/-37. Single contributor.

#### B47. `beige-ian/orca` — metric only

- **Signature:** ahead 7, behind 2,199, files 26, +1,903/-280. 2 contributors.

#### B48. `collinrijock/orca` — "agent-first" custom description

- **Signature:** ahead 1, behind 2,190, files 41, +848/-577. Single contributor.
- **Custom description:** *"A custom agent-first Orca with refined workspace and terminal."*

#### B49. `aidenxieKoalas/orca` — metric only

- **Signature:** ahead 1, behind 1,874, files 34, +2,003/-50.

#### B50. `westkite1201/orca-1` — renamed fork on westkite1201 fork series, metric-only
- **URL:** https://github.com/westkite1201/orca-1
- **Stars:** 0
- **Created:** 2026-07-10T04:20:40Z, Pushed: 2026-08-13T11:07:58Z
- **Signature:** ahead 15, behind 2199, files 173, +13752/-1545, mna=12954, feat_ratio=0.8666666666666667.
- **Heat:** 48.95, Novelty: 1, Impact: 0.06363627314567566.
- **Why distinct:** +15 commits ahead of upstream, 173 files changed, +13752 lines added, 2 contributors, lone wolf: strength 73%.
- **What makes it unique:** renamed fork on westkite1201 fork series, metric-only.
- **Verdict:** metric-only — not commit-sampled; described from export data.


#### B51. `gal064/orca` — Drifter on `feat/reuse-checkout-workspace` branch with stacked-PR preferences, metric-only
- **URL:** https://github.com/gal064/orca
- **Stars:** 0
- **Created:** 2026-07-14T05:33:15Z, Pushed: 2026-08-15T00:35:33Z
- **Signature:** ahead 153, behind 797, files 248, +14206/-1998, mna=12570, feat_ratio=1.
- **Heat:** 42.13, Novelty: 0.4324014542875938, Impact: 0.05321655049920082.
- **Why distinct:** +153 commits ahead of upstream, 248 files changed, +14206 lines added, 8 contributors, Custom description: "Orca is the ADE for working with a fleet of parallel agen...".
- **What makes it unique:** Drifter on `feat/reuse-checkout-workspace` branch with stacked-PR preferences, metric-only.
- **Verdict:** metric-only — not commit-sampled; described from export data.


#### B52. `blankjee/orca` — minimal Drifter, metric-only
- **URL:** https://github.com/blankjee/orca
- **Stars:** 0
- **Created:** 2026-07-16T11:38:20Z, Pushed: 2026-07-28T06:34:01Z
- **Signature:** ahead 1, behind 2293, files 49, +2705/-81, mna=2634, feat_ratio=1.
- **Heat:** 27.92, Novelty: None, Impact: None.
- **Why distinct:** +1 commits ahead of upstream, 49 files changed, +2705 lines added, Single contributor, lone wolf: strength 56%.
- **What makes it unique:** minimal Drifter, metric-only.
- **Verdict:** metric-only — not commit-sampled; described from export data.


#### B53. `liu954326053/orca` — high-volume single-contributor fork, metric-only
- **URL:** https://github.com/liu954326053/orca
- **Stars:** 0
- **Created:** 2026-07-17T05:11:54Z, Pushed: 2026-07-20T05:24:37Z
- **Signature:** ahead 7, behind 2245, files 215, +13842/-322, mna=13283, feat_ratio=0.7142857142857143.
- **Heat:** 28.15, Novelty: None, Impact: None.
- **Why distinct:** +7 commits ahead of upstream, 215 files changed, +13842 lines added, Single contributor, lone wolf: strength 59%.
- **What makes it unique:** high-volume single-contributor fork, metric-only.
- **Verdict:** metric-only — not commit-sampled; described from export data.


#### B54. `DevZonayed/orca` — fork-of-a-fork absorber, mass-churn Drifter
- **URL:** https://github.com/DevZonayed/orca
- **Stars:** 0.
- **Created:** 2026-07-26T17:20:46Z, Pushed: 2026-08-19T22:34:34Z.
- **Signature:** ahead 7,708, behind 8,992, files 300, +30,829/-32, mna=26,199, feat_ratio=0.988. 36 contributors.
- **Heat:** 48.39, novelty=1.0.
- **Why distinct:** +7,708 commits ahead of upstream, 300 files changed, +30,829 lines added, 36 contributors, last pushed 1d ago.
- **What makes it unique:** the signature `(ahead=7708, behind=8992, files=300, +30829/-32)` is the largest fork-of-a-fork estimator in the file — ahead/behind numbers suggest this fork has absorbed multiple other forks and now sits on a parallel universe unrelated to upstream. The 36 contributors reinforce the "absorbed aggregator" pattern. Not portable by comparison / cherry-pick; reading it for context only.
- **Verdict:** signature-only — not commit-sampled.


### Group C — Starved clones (ahead=0)

All forks below have `ahead=0` and `files_changed=0`, meaning they have not diverged from upstream since fork creation. They are **noise**: clones whose only difference from upstream is the moment of fork.

| Fork | Behind | Notes |
| --- | --- | --- |
| `beatrices7446/orca` | 1,470 | "Fork of stablyai/orca" |
| `18163623522/orca` | 2,172 | Upstream README |
| `taeungshin/orca` | 2,172 | (signature-collision — same SHA triple) |
| `ttkhanh-keylp/orca` | 2,172 | (signature-collision — same SHA triple) |
| `securiumsss/orca` | 2,174 | |
| `willemi2627/orca` | 2,723 | "Fork of stablyai/orca" |
| `GokhanSanchez42461/orca` | 2,724 | (signature-collision — same SHA triple) |
| `ValuxiCoj3/orca` | 2,724 | (signature-collision — same SHA triple) |

**The two starved-clone micro-clusters** share an identical `(base_sha, head_sha=None)` snapshot:
- `behind=2,172` triple: `18163623522/orca`, `taeungshin/orca`, `ttkhanh-keylp/orca` — three forks created from the same upstream point-in-time.
- `behind=2,724` pair: `GokhanSanchez42461/orca`, `ValuxiCoj3/orca` — two forks created from the same upstream point-in-time.

These are the only signature collisions in the file. The triple/pair shape is consistent with tutorial or batch-mirror forks created from the same upstream snapshot, but a stronger claim (e.g. "automated fork farm") requires commit-message or rename-pattern evidence that this report does not have.

---

## 4. Intent clusters (what each fork *does*, in one line)

| Intent cluster | Forks |
| --- | --- |
| **Sidebar / project organization** | `JCodesMore/orca` (Spaces), `judongli/orca` (task-inbox board + per-pane badge titles) |
| **Team collaboration** | `DarwinWDEWEN/rx-cli` |
| **Integrations + themes** | `brennoaf/orca` |
| **Custom CLI agents** | `cyrus123456/orca` |
| **Sandboxed test runners** | `zpyoung/orca` |
| **Terminal / editor extensions** | `inlineapps/orca` (TypeScript LS, Asana, Claude presets), `AlejandroAkbal/orca` (WSL/CJK fixes), `molon/orca` (mobile push hook), `user141514/orca` (mission routing) |
| **Self-hosted infrastructure** | `paidaxingyo666/Manta` (relay), `646826/orca` (multica hybrid transport) |
| **Rust terminal / alternate runtime** | `andrewyatesai/orca-alab` (aterm), `eocodn/orca` (Tauri Agent Control) |
| **Distribution identity rewrites** | `wangazhang/orca` (Yoha), `rudironsoni/orca` (Horca), `pedrobalsa/balsa-ade` (Balsa), `Perc-Innovation/Perc-Orca`, `jae-heo/orca` (NeurOrca), `Seven-Day-Inc/morpheus-shell` (Traycer soul) |
| **Fork operations / sync tooling** | `zpyoung/orca` (AGENTS.md runbook), `innocarpe/orca` (multi-LLM harness), `BlackCatCXIII/orca` (fork-policy docs), `rudironsoni/orca` (auto-merge sync PRs), `justinwalters/orca` (upstream-sync record) |
| **Editor / Monaco / IDE surface** | `AVeryLostNomad/orca` (embedded Monaco + scratch files + GH multiaccount) |
| **Unity / game-engine integration** | `machamy/orca` (per-worktree Unity tinting) |
| **Windows packaging** | `eocodn/orca` (Tauri), `WYK15/orca` (signed/notarized per-fork releases), `Revan620198/orca` (OS support floors) |
| **Dashboard / live view** | `andremohrmann/orca` |
| **Notification push (FCM)** | `sbkim/orca` |
| **Packaging hot-fix (npm/electron)** | `sbkim/orca` (newer Electron Node builtins), `WYK15/orca` (sign+notarize) |
| **CI gates / fork policy** | `JustShinobi/orca` (CI gate E2E to upstream), `BlackCatCXIII/orca` (source Actions disabled) |
| **Marketing / README rewrite** | `paidaxingyo666/Manta` (rewrites README around self-hosting) |
| **Metric-only (no commit sample)** | `wintergl`, `GhostFlying`, `vnp-community`, `yongwuzhijin`, `munlucky/orca-moon`, `Him188`, `wangzhengzhuo05`, `Gagan-k0`, `beige-ian`, `aidenxieKoalas`, `jasonyuezhang`, `ktwsmart`, `ecurie-ai`, `tfzou001`, `westkite1201/orca-1`, `gal064`, `blankjee`, `liu954326053` |

---

## 5. Heat-score vs novelty divergence

The export's heat score conflates popularity with novelty. The actual novelty (live ahead commits that are not upstream merges) is concentrated in:

| Fork | Live ahead commits | Real novel commits (estimate) |
| --- | --- | --- |
| `cyrus123456/orca` | 136 | ~10 (custom agent feature + sync workflow) |
| `JCodesMore/orca` | 20 | ~20 (Spaces feature) |
| `brennoaf/orca` | 42 | ~10 (themes + integrations) |
| `inlineapps/orca` | 10 | ~7 (Asana + TS LS + agent presets) |
| `DarwinWDEWEN/rx-cli` | 15 | ~15 (collaboration feature) |
| `paidaxingyo666/Manta` | 225 | ~50 (relay + self-host plumbing; rest is upstream churn) |
| `andrewyatesai/orca-alab` | 1,633 | unquantifiable (parallel re-implementation; not cherry-pickable) |

The `why_distinct` strings often say "Single contributor · Feature Builder: strength 96%" — these describe commit *quality*, not novelty. **Trust the diff, not the descriptor.**

---

## 6. Verification notes & honesty

- **Commit sampling:** 8 forks have prior deep verification (see `reports/spoon-stablyai-orca-evaluation-2026-08-21.md`). Additional commit sampling was attempted for selected forks in this report; where a Group B heading body quotes commit-hash prefixes or first-line commit subjects, that fork has a live `gh api` commit sample. Branch-name strings alone are derived from the export's `compare_url` field, not from a `gh api` call, and do not by themselves establish a commit sample. Forks whose heading body contains the literal `metric-only` verdict were not commit-sampled and are described from the export data only. The 8 Group C starved clones are metric-only by definition (no ahead commits).
- **Source-of-truth report:** `/home/svnbjrn/dev/spoon/reports/spoon-stablyai-orca-evaluation-2026-08-21.md` (existing) covers the top 8 forks with deep verification; this report *augments* it with the remaining 60 forks as a uniqueness roll-call.
- **What I did not verify:** I did not open 5-10 commits from every fork. Forks listed as "metric only" have only been screened by signature and live divergence.
- **What this report proves:** uniqueness signature per fork, divergence-vs-popularity split, intent clustering, and which forks are dead-empty clones.
- **What this report does NOT prove:** build correctness, merge viability, or behavioral safety of any fork's specific code. `brennoaf/orca` includes a commit titled `refactor(integrations): remove API credential settings`; the report flags it as a sign-in-flow safety concern worth verifying before any integration, but does not claim it definitely breaks sign-in without a runtime test.
- **Final recommendation for SAUR packaging:** none of these forks is a candidate for an Arch package. They all build from the same source (the upstream tag) with extra patches that are not generally useful to a packaging consumer. The SAUR `stably-orca` recipe should stay pinned to upstream `v1.4.187` as written. If a future SAUR package needs the kind of feature work these forks represent (custom CLI agents, team collaboration, self-hosted relay), the right move is **upstreaming** those features, not forking.
