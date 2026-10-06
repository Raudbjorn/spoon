# spoon-export-stablyai-orca — fork evaluation

**Date:** 2026-08-21
**Export:** `/home/svnbjrn/dev/spoon/spoon-export-stablyai-orca-2026-08-21.json`
**Parent:** `stablyai/orca` (50,389★, `main`)
**Totals:** 3,065 forks · 2,558 enriched · 133 tier-3 · `degraded: true` (507 unenriched)

---

## Inputs & method

- Read the export with `jq`; never trusted the heuristic `why_distinct` strings for novelty — every recommendation below is grounded in `gh api repos/.../compare/main...<fork>:main` against the current upstream `main` HEAD at 2026-08-21.
- Tier-3 heat score was used as a *ranking* signal only. Live `compare` numbers were used as the *truth* signal — many top-of-export forks turn out to be far behind upstream (`ahead: 0, behind: 1500+`) once you re-verify.
- Auto-fork farms were detected by signature-clustering `(ahead, files_changed, additions, deletions)` for tier-3 forks: no shared signatures (each fork has a unique divergence fingerprint). The dominant pattern in this export is **not** a fork farm — it is one-off per-developer forks with no automated duplication. The auto-farm trap that hit the warpdotdev/warp network is largely absent here.
- 507 forks are unenriched (no `divergence`). These are typically brand-new forks created minutes before the export ran; they show up in `recently pushed` but have nothing to compare. Treat as noise.

---

## TL;DR — what's actually exciting in this fork network

| Fork | One-line positioning | Verdict for a SAUR / packaging maintainer |
|---|---|---|
| [`DarwinWDEWEN/rx-cli`](https://github.com/DarwinWDEWEN/rx-cli) | Renamed fork. New `collaboration` feature: team worktrees, issues, PRs, agent/skill bindings, with its own docs and IPC. Single contributor. | **Most novel standalone work in the network.** Worth a deep read. |
| [`JCodesMore/orca`](https://github.com/JCodesMore/orca) | "Spaces" sidebar tabs grouping projects. 10★, the highest-starred fork in the export. | **Most popular user-visible feature.** Real feature, real code. |
| [`brennoaf/orca`](https://github.com/brennoaf/orca) | Adds compact Discord/Slack/WhatsApp "fast-response" workspace + Spotify media controls + theme gallery + sandboxed preloads. 0★ but heavy work. | **Surface-level fork that ships visible integrations.** |
| [`andrewyatesai/orca-alab`](https://github.com/andrewyatesai/orca-alab) | "ALab Edition". Wraps Orca around the [aterm](https://github.com/alabsystems/aterm) Rust terminal engine; long-running "parity sync" cadence (1,633 ahead). | **Real architectural divergence.** Read for context only — too big to cherry-pick. |
| [`inlineapps/orca`](https://github.com/inlineapps/orca) | TypeScript language service in diff views + Asana tasks provider + curated Claude agent presets. | **Three discrete, small features.** Cherry-pick-friendly. |
| [`paidaxingyo666/Manta`](https://github.com/paidaxingyo666/Manta) | Self-hosted-relay fork. Drops sign-in, rewires relay/registries, multi-arch Docker images, multi-cloud publishing. Renamed identity. | **Auth/relay alternative.** Not a packaging input. |
| [`cyrus123456/orca`](https://github.com/cyrus123456/orca) | Adds custom CLI agents (`CustomAgent` type) + persistent sync-upstream workflow. Clean, well-isolated, has its own CI. | **Cherry-pickable feature, low risk.** |
| [`zpyoung/orca`](https://github.com/zpyoung/orca) | Docked rich-input composer for terminal agent panes + Docker-based sandboxed test-shard runner + sync-upstream `AGENTS.md` runbook. | **Tooling-layer fork; the AGENTS.md + sync pattern is reusable.** |
| [`Oxeegen/OxeeUI`](https://github.com/Oxeegen/OxeeUI) | 3★, mostly upstream merges with `feat(relay): prefer closest available region` and a WSL transcript fix. | **Skip — too thin.** |
| [`treeman99/orca`](https://github.com/treeman99/orca) | 153 ahead, mostly upstream + a few small fixes; 12 contributors (likely a team). | **Skip — not enough novelty.** |

---

## Tier-3 deep dives

### 1. `DarwinWDEWEN/rx-cli` — *team-collaboration fork*

**Live divergence:** ahead 15, behind 437, diverged.
**Stars:** 0.
**Last push:** 2026-08-20.
**Default branch:** `main`.

This is the only top-tier fork that *renamed itself* (`rx-cli`). It owns a feature domain upstream has not built: a full team-collaboration layer. The recent commits reveal a layered construction:

- `C1-C2 activeView issues-and-prs/teams + nav history`
- `C3-C5 sidebar entries + Teams/IssuesPRs pages + preload IPC`
- `B7 git-ref store`, `A5 activity-log store`, `B2 agent-config/skill-binding`
- `D1 issue-lifecycle engine`, `D3 issue-worktree store`
- `C6 project onboarding`, `C7 project team UI`
- `C8 issue list/detail page with inline status/priority editing`
- `C9 PR list/detail page + minimal B6 pr-store`
- `E1 pipeline CLI`

**What it means for SAUR packaging:** none directly. But the **scope and isolation discipline** are notable — the fork keeps its feature behind a clear IPC contract and ships docs (`docs/collaboration/PROGRESS.md`, R10-R12 iteration docs) ahead of code. If you ever build a `orca-serve`-style companion on SAUR, this is the read.

### 2. `JCodesMore/orca` — *Spaces (sidebar tabs grouping projects)*

**Live divergence:** ahead 20, behind 2,285, diverged.
**Stars:** 10 — the highest in the export.
**Last push:** 2026-07-16.
**Default branch:** `main`.
**Lone wolf:** Feature Builder, 17 meaningful commits, 60-day span, strength 96%.

Twenty ahead-only commits cluster around one feature: **Spaces** (named sidebar tabs that group projects, drag repos onto them, per-Space unread badges, cross-Space unread total on All Projects pill, jump-to-workspace switches Space and focuses the agent's pane).

**Falsifier I checked:** the 20 ahead commits are real, not just merge-from-upstream noise. `feat: add Spaces`, `feat: drop repos and worktrees onto Space tabs via HTML5 drag`, `fix: jump to workspace now switches Space and focuses agent's pane (#7)` are all original.

**What it means for SAUR packaging:** none directly. But: JCodesMore is the only fork in the network whose heat score, stars, and feature quality all co-vary. If `stablyai/orca` ever absorbs Spaces, this fork goes silent; if not, it remains a viable integration target.

### 3. `brennoaf/orca` — *integrations-and-themes surface*

**Live divergence:** ahead 42, behind 163, diverged.
**Stars:** 0.
**Last push:** 2026-08-17.
**Default branch:** `main`.
**Drifter archetype:** 18,712 lines added, 100 deleted.

Surface-level fork. Recent commits are:

- `feat(app): integrate communication and theme surfaces`
- `feat(themes): add interface theme gallery`
- `feat(spotify): add system media controls`
- `feat(discord): add compact communication hub`
- `feat(slack): add compact web fast-response`
- `feat(whatsapp): add compact web fast-response`
- `feat(communications): add detachable fast-response workspace`
- `refactor(integrations): remove API credential settings` (warning sign: a refactor that strips API credential plumbing is the kind of change that breaks sign-in flows upstream wouldn't accept)
- `build(electron): bundle sandboxed preloads`

**What it means for SAUR packaging:** none directly. **Skip.** The "remove API credential settings" commit is the opposite of what a packaging user wants; this fork trades security for surface breadth.

### 4. `andrewyatesai/orca-alab` — *Rust-terminal-engine wrapper*

**Live divergence:** ahead 1,633, behind 1,168, diverged.
**Stars:** 1.
**Last push:** 2026-08-19.

The fork wraps Orca around the [aterm](https://github.com/alabsystems/aterm) Rust terminal engine. The commit cadence is:

- `port(parity): +31 cadence sync — 8 upstream items (Aug 18) re-derived in spirit`
- `port(parity): +34 cadence sync — 14 upstream items (Aug 17-18) re-derived in spirit`
- `port(parity): +79 cadence sync — 38 upstream items (Aug 16-17) re-derived in spirit`
- `port(parity): +300-delta batch 4 — 20 upstream items re-derived in spirit`

The "re-derived in spirit" framing is honest: this is a parallel re-implementation, not a fork that lands cleanly upstream. 1,633 ahead commits make a real merge impossible.

**What it means for SAUR packaging:** none. Read once for the engineering approach (Rust terminal engine wrapper), then move on.

### 5. `inlineapps/orca` — *small, discrete features*

**Live divergence:** ahead 10, behind 295, diverged.
**Stars:** 0.
**Last push:** 2026-08-15.
**Lone wolf:** strength 94%.

Ten original commits. Three discrete features, each cherry-pickable in isolation:

- `feat(editor): add TypeScript language service` → diff-view extension
- `feat(editor): extend TypeScript language service to diff views`
- `feat(tasks): add Asana task provider`
- `feat(tasks): lazy-load Asana sections, add subtasks and caching`
- `feat(agents): narrow launch presets to curated Claude model/effort pairs`
- `feat(agents): pick a model when launching/continuing an agent`
- `fix(build): emit serve-mode-argv so the CLI require graph resolves`

**What it means for SAUR packaging:** none directly. But `fix(build): emit serve-mode-argv` is the kind of small build-flag fix that the SAUR PKGBUILD's `build()` step might rely on — worth checking whether the current upstream build still passes without this fix on a clean chroot.

### 6. `paidaxingyo666/Manta` — *self-hosted relay, renamed identity*

**Live divergence:** ahead 225, behind 205, diverged.
**Stars:** 0.
**Last push:** 2026-08-21.
**Default branch:** `main`.

The only fork in the top tier with a renamed identity. README states:

> **Manta is a self-hosted fork of [Orca](https://github.com/stablyai/orca)** (MIT, © Lovecast Inc.).
> Features: Self-host relay server · No mandatory cloud account · Internationalization · Enterprise deployment.
> No standalone Manta release is published yet — build from source.

The recent commits show the work: `feat(relay): publish versioned images to Docker Hub, Aliyun, and Tencent`, `fix(cloud): stop defaulting sign-in at a private relay`, `perf(relay): build each architecture on its own runner`, `fix(relay): commit the lockfile the image build needs`. Manta is its own product line; it does not push back to upstream.

**What it means for SAUR packaging:** none. But it's a useful **existence proof** that Orca's relay layer can be self-hosted without forking the desktop app wholesale. If you ever want a self-hosted `orca serve` story on SAUR, Manta's `relay-server/` is the closest existing reference.

### 7. `cyrus123456/orca` — *custom CLI agents + sync workflow*

**Live divergence:** ahead 136, behind 0, ahead only.
**Stars:** 0.
**Last push:** 2026-08-21.
**Default branch:** `main`.
**Drifter archetype:** strength 100%.

`ahead 136 / behind 0` is the cleanest possible divergence signal in the export: this fork re-bases on every upstream release and ships only its own additions.

The original commits cluster around one feature:

- `feat: add support for custom agents in terminal tabs`
- `feat: enhance custom agent management and icon handling`
- `feat: add prompt delivery options for custom agent launch in new tab`

Plus a discipline layer: `chore: add workflow to sync with upstream main branch`, `ci: build unsigned Windows release when upstream cuts a stable release`, `chore: update sync workflow to merge upstream main instead of fast-forward`, `fix(build): drop broken version-match gate, force-set version in build job`.

**What it means for SAUR packaging:** none directly, but **the sync-upstream workflow is the closest existing reference to what a SAUR `-git` recipe would look like.** The `force-set version in build job` commit maps directly onto the version-pin problem the SAUR PKGBUILD handles via `pkgver`. Worth reading if a future `-git` package ever ships.

### 8. `zpyoung/orca` — *terminal-composer UI + sandboxed test runner + AGENTS.md runbook*

**Live divergence:** ahead 207, behind 425, diverged.
**Stars:** 0.
**Last push:** 2026-08-21.

Recent commits:

- `feat: docked rich-input composer for terminal agent panes (#9)`
- `feat(fork): keep vitest on the remote sandbox host`
- `refactor(fork): make the sync-upstream skill the runbook`
- `docs(fork): declare the fork feature structure in AGENTS.md`
- `Add Docker-based sandboxed test-shard runner`
- `Declare fork ownership in a manifest and resolve syncs from it (#11)`
- `sync: drop upstream WSL-gate watch test the fork's tail reader omits`

**What it means for SAUR packaging:** none directly. But the **"sync-upstream skill as runbook"** pattern is the kind of operational document SAUR itself uses — the `docs/packaging-guide.md` and `README.md` of SAUR follow the same shape. Read for cross-pollination.

---

## Anti-port list

These forks look interesting at first read but are not worth integrating.

| Fork | Why skip |
|---|---|
| `ohmygaugh-crypto/orca`, `Nathishwar-prog/orca`, `ginjaninja78/orca`, `kent666/orca`, `soaded/orca`, `vkn129/orca`, `optrader8/orca`, `manuelapetsi/orca`, `ais1175/orca`, `tgmarinho/orca`, `chindris-mihai-alexandru/orca-fork`, `Bennyoooo/orca`, `MaTriXy/orca`, `cresl1/orca`, `wolfiesch/orca`, `rohitg00/orca`, `arndvs/orca`, `bennewell35/orca` | All `ahead 0 / behind 4000+`. Starved forks that never rebased. The export's "starved" signature is `ahead=0, files_changed=0, additions=0, deletions=0` — these forks are pristine clones that the upstream has since moved past. Drop. |
| `GhostFlying/orca`, `jae-heo/orca`, `WYK15/orca` | **Contradicted by the companion report - do not use this row.** It reads `ahead 0 / behind 1000+` and infers nothing to contribute, while `spoon-stablyai-orca-unique-forks-summary-2026-08-21.md` (same date) records ahead 11, 26 and 134 respectively and lists current fork-specific commits for two of them. Most likely a branch/ref mismatch (`GhostFlying/orca` sits on branch `fork`, not `main`). Treat all three as unclassified until re-probed. |
| `DevZonayed/orca` | `ahead 7,708 / behind 8,992` — fork-of-a-fork pattern; this is a fork that absorbed multiple other forks and now sits on a parallel universe. Not portable. |
| `Oxeegen/OxeeUI` | 14 ahead, 3 stars, but commits are mostly upstream merges + 1-2 thin features (`feat(relay): prefer closest available region`). Low signal. |
| `machamy/orca` | `ahead 72 / behind 54`. 45,219 lines added. Single contributor. Most of the volume is upstream churn, not novelty. |
| `lifefloating/orca` (9★) | `ahead 0`. Most-starred non-JCodesMore fork, but it has not diverged from upstream. Star signal only. |
| `nwparker/orca` (4★) | `ahead 0 / behind 7,230`. Same shape. |
| `mvanhorn/orca`, `tmchow/orca`, `rohitg00/orca` (2★ each) | All `ahead 0`. Star signal only. |
| `wintergl/orca`, `jborkowski/orca`, `646826/orca` | Drifter archetypes with `ahead 0–40 / behind 1500–2500`. Old forks that drifted then froze. |

---

## Heat-score vs novelty — the divergence that matters

The export's heat score conflates popularity with novelty. The actual novelty in this fork network — measured by "live ahead commits that are not upstream merges" — is concentrated in:

| Fork | Live ahead | Real novel commits (estimate) |
|---|---|---|
| `cyrus123456/orca` | 136 | ~10 (custom agent feature + sync workflow) |
| `JCodesMore/orca` | 20 | ~20 (Spaces feature) |
| `brennoaf/orca` | 42 | ~10 (themes + integrations) |
| `inlineapps/orca` | 10 | ~7 (Asana + TS service + agent presets) |
| `DarwinWDEWEN/rx-cli` | 15 | ~15 (collaboration feature) |
| `paidaxingyo666/Manta` | 225 | ~50 (relay + self-host plumbing; the rest is upstream churn) |
| `andrewyatesai/orca-alab` | 1,633 | ~? (parallel re-implementation; not cherry-pickable) |

The export's `why_distinct` strings often say "Single contributor · Feature Builder: strength 96%" — these are descriptive of commit *quality*, not novelty. Trust the diff, not the descriptor.

---

## What I did not verify

- I did not open 5-10 commits from every fork. For the 8 deep-dives above, I checked the README and the last 15–25 commits. The remaining 125 tier-3 forks have only been screened by signature and live divergence.
- The export is `degraded: true` with 507 forks unenriched. Most of those are brand-new forks (e.g. `CreatorComputeCompany/orca` pushed 2026-08-21 10:12 UTC) — they don't yet have enough history to compute divergence. They're real forks but contribute nothing to a packaging recommendation.
- I did not attempt to actually build any fork or diff their `package.json` against upstream — the comparison here is `compare` API + commit messages + README intent, not byte-level.

---

## Final recommendation for SAUR

**Do not adopt any fork wholesale.** None of these forks is a candidate for an Arch package — they all build from the same source (the upstream tag) with extra patches that are not generally useful to a packaging consumer. The SAUR `stably-orca` recipe should stay pinned to upstream `v1.4.187` as written.

If a future SAUR package needs the kind of feature work these forks represent (custom CLI agents, team collaboration, self-hosted relay), the right move is *upstreaming* those features, not forking. The forks worth reading are the ones with the most novel commits per star (`cyrus123456/orca`, `inlineapps/orca`, `DarwinWDEWEN/rx-cli`) — not the most-starred forks, which are mostly starved clones.
