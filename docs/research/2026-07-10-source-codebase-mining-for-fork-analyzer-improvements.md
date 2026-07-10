# Source-Codebase Mining for Spoon Fork-Analyzer Improvements

Date: 2026-07-10
Target repo: /home/svnbjrn/projects/spoon-4/spoon
Source corpus: /home/svnbjrn/projects/spoon-4/sources
Research agent: SpoonSourcesResearch
Artifact status: the research subagent returned the artifact in-band; it reported that it did not persist local://spoon-4-sources-fork-analyzer-research.md because its session was read-only.

## Purpose

This document preserves the complete source-mining research artifact and turns it into a grounded pseudo-implementation backlog for spoon, a fork analyzer. It is intentionally self-contained because docs/research/ was empty at the time of writing and downstream planning should not have to reconstruct context from chat history.

The implementation backlog below follows the research conclusion: do not replace spoon's existing heat / clustering / expected-rank shortlist core. The quickest high-value work is additive: expose clearer decision policy, add missing low-cost signals, and keep expensive local-git mining as an opt-in escalation.

## Fresh Codebase Grounding Notes

These notes re-ground the research against the current spoon tree before translating it into work items.

- spoon/internal/forksops/stream.go:24-91 defines the main streaming options. It already supports tiering, budget caps, shortlist mode, query reranking, rate-limit reserve handling, clustering, owner-profile caps, and cache TTL. New work should thread through this options surface rather than creating a parallel pipeline.
- spoon/internal/forksops/stream.go:153-208 defines the public per-fork Result envelope. It already has ExpectedRank, RankConfidence, QueryScore, ClusterSkip, T3Skip, BudgetSkip, OwnerProfileSkip, and SiblingSimSkip. This is close to a visibility/degradation layer, but it is still stage-skip oriented rather than a first-class per-fork visibility / demotion_reasons policy.
- spoon/cmd/spn/forks.go:542-624 serializes fork NDJSON. It emits heat, tier, query fields, category, t2/t3 summaries, and degradation booleans, but it does not currently emit a normalized visibility status, reason list, or per-network rank/percentile band.
- spoon/internal/heat/score.go:27-146 shows the additive scoring ladder: T1 surface signals, T2 divergence ownership, and T3 behavioral/novelty signals. New scoring should be narrowly justified and should not flatten this ladder into a single borrowed heuristic.
- spoon/internal/heat/scorer.go:56-70 confirms trust and penalties are already a post-raw-score finalization step; tiny fork sets skip percentile trust. This makes a separate visibility/demotion policy a natural extension rather than an architectural reset.
- spoon/internal/heat/percentile.go:24-78 already computes stars, sub-forks, and recency percentiles for scoring internals. The gap is mostly output/UX and possibly generic percentile-band helpers, not inventing percentile math from scratch.
- spoon/internal/topics/topics.go:36-83 confirms topic mode is currently stars + fork-network + recency, with archived halving. Multi-lane topic discovery would extend candidate generation; it should still feed SelectBest or an equivalent transparent selector.
- spoon/internal/github/owner_profile.go:33-40 caps owner-profile pagination at five pages, and spoon/internal/forksops/stream.go:75-86 caps distinct owner-history calls per run. This supports the research's caution that owner/farmer improvements must remain budget-aware.
- Fresh searches found no dedicated star/fork snapshot, velocity, or trend subsystem. The only delta hits were README-delta wording and score-delta comments, so momentum history is a real missing capability, not just an undocumented feature.

## Planning Principles From the Research

1. Keep the current core. The existing additive heat, clustering, query reranking, and expected-rank shortlist are the strongest fork-analysis mechanisms in the surveyed corpus.
2. Separate value from policy. Base heat should answer “how valuable does this fork look?” Visibility should answer “should this record be shown, demoted, or hidden, and why?”
3. Expose relative context. Fork networks differ too much for raw heat alone to be self-explanatory. Percentile/rank bands should accompany raw scores.
4. Prefer cheap persistent signals before deep local mining. Daily snapshots for stars/forks are cheap and API-friendly; local clone history is valuable but belongs behind an opt-in shortlist escalation.
5. Preserve NDJSON contracts. Agent-facing output must remain structured and stream-safe; browser-first ideas are optional skins, not core design constraints.
6. Use AGPL sources concept-only. repowise is AGPL-3.0-only, so any implementation inspired by it must be clean-room / concept-level unless the project deliberately changes license posture.

## ASAP / Quick-Win Pseudo-Implementation Backlog

The following tasks are intentionally phrased as implementation-shaped work items, but they are still pseudo-tasks for planning. They are ordered to produce useful output quickly while minimizing architectural risk.

### ASAP-1 — Add a first-class fork visibility envelope
**Status: DONE** — shipped as the per-record `visibility` envelope (visible/demoted/hidden) with deterministic reasons.
Implementation mapping: internal/forksops/annotations.go (VisibilityStatus/DeriveVisibility), cmd/spn/forks.go visibilityToJSON, internal/forksops/annotations_policy_test.go + testdata/visibility_policy_cases.json.


Goal: Make every emitted fork explain whether it is normally visible, demoted, hidden, or degraded, without overloading heat as the only decision surface.

Why now: Result already carries skip/degradation fields, and HeatResult.Penalties already records penalty names. The missing layer is an explicit output contract that combines those facts into a stable policy field.

Pseudo-implementation:

1. Add a small policy package or heat-adjacent helper, for example internal/forksops/visibility.go or internal/heat/visibility.go.
2. Define types along these lines:

        type VisibilityStatus string

        const (
            VisibilityVisible  VisibilityStatus = "visible"
            VisibilityDemoted  VisibilityStatus = "demoted"
            VisibilityHidden   VisibilityStatus = "hidden"
            VisibilityDegraded VisibilityStatus = "degraded"
        )

        type VisibilityDecision struct {
            Status  VisibilityStatus `json:"status"`
            Reasons []string         `json:"reasons,omitempty"`
            Stages  []string         `json:"stages,omitempty"`
        }

3. Build the decision from already-grounded inputs:
   - r.Heat.Penalties -> demotion reasons such as archived, low_recency, topic_tag, fork_farmer, no_ahead, upstreamed.
   - r.BudgetSkip, r.T3Skip, r.OwnerProfileSkip, r.SiblingSimSkip, r.ClusterSkip -> degraded reasons.
   - r.Err -> hidden/error depending on whether the fork still emits a useful record.
4. Emit visibility in forkToJSON and forkToJSONUpstream while keeping legacy booleans such as budget_skipped for compatibility.
5. Add unit tests for precedence:
   - no_ahead / upstreamed should produce hidden or zero-value demoted status, depending on current product semantics.
   - BudgetSkip with no penalties should be degraded, not low-quality.
   - archived + low_recency should report both reasons deterministically.
   - a normal enriched fork should be visible with no reasons.

Acceptance sketch: A sample NDJSON fork can be machine-read as:

        {
          "heat": 21,
          "visibility": {
            "status": "demoted",
            "reasons": ["archived", "low_recency"]
          }
        }

Risk: Low. This is mostly an output/envelope layer. The main risk is prematurely hiding records that current users expect to see. Default to demotion unless a fork is provably non-actionable.

Source inspiration: Issue Finder’s base-score-then-quality-policy split; current spoon stage skips and penalties.

### ASAP-2 — Expose per-network rank and percentile bands
**Status: DONE** — shipped as batch-mode `networkRank` (position/total/percentile/band, tiny_set under 10); `--no-cluster` streaming intentionally omits run-relative rank.
Implementation mapping: internal/forksops/annotations.go assignNetworkRanks/NetworkRank, cmd/spn/forks.go networkRank block, internal/forksops/stream_test.go TestStream_batchModeAssignsNetworkRank.


Goal: Make raw heat interpretable across fork networks by adding per-run rank context to output.

Why now: Percentile math already exists in internal/heat/percentile.go, and Scorer already uses percentile-derived trust internally. This is mostly a presentation and finalization gap.

Pseudo-implementation:

1. After collect-then-sort paths, compute per-run rank fields for emitted results:
   - heatRank or rank among emitted candidates.
   - heatPercentile or networkPercentile within the current fork network.
   - rankBand: for example top_1pct, top_5pct, top_10pct, middle, bottom.
2. Decide how to handle true streaming mode:
   - Option A: only emit percentile/rank fields in collect-then-emit modes (cluster, shortlist, query).
   - Option B: add a cheap precomputed T1 percentile band, then refine after enrichment only in batch modes.
3. Keep tiny-set behavior honest:
   - For forkCount < 10, emit rankBand: tiny_set or omit percentile fields with rankContext: tiny_set.
4. Add tests around ties and tiny sets.
5. Document that rank/percentile are run-local, not cross-network calibrated truth.

Acceptance sketch: NDJSON records include something like:

        {
          "heat": 72.4,
          "rank": 3,
          "rankTotal": 147,
          "heatPercentile": 0.986,
          "rankBand": "top_5pct"
        }

Risk: Low-medium. The only semantic risk is making approximate ranks look too authoritative. Use explicit field names like network_percentile rather than generic quality.

Source inspiration: repowise’s repo-relative risk normalization; current spoon percentile/trust logic.

### ASAP-3 — Add lightweight star/fork momentum snapshots
**Status: PARTIAL** — output-only snapshot persistence, deltas, statuses, and NDJSON are shipped; matching is keyed only by the mutable provider ID (`nameWithOwner`/`fullPath`), so the promised rename fallback is not implemented.
Implementation mapping: internal/forksops/momentum.go, cmd/spn/forks.go momentumToJSON, internal/forksops/stream_test.go momentum tests.


Goal: Distinguish emerging forks from historically popular but stale forks by persisting daily snapshots of simple GitHub surface counts.

Why now: Fresh search found no existing snapshot/trend subsystem. This is the most concrete missing signal in T1.

Pseudo-implementation:

1. Add a cache namespace under ~/.cache/spoon/, for example fork-snapshots/<provider>/<owner>/<repo>.json.
2. Store per-fork daily facts:
   - fork ID / full name
   - stars
   - sub-forks
   - pushed_at
   - observed_at
3. On each run:
   - Load the previous snapshot for the upstream network.
   - Match by stable fork ID first, full name second.
   - Compute starsDelta30d, subForksDelta30d, and maybe observedDays.
   - Save the current snapshot at the end of enumeration.
4. First implementation should not alter ordering by default. Emit momentum as explanatory fields first:
   - momentum.stars_30d
   - momentum.sub_forks_30d
   - momentum.observed_days
   - momentum.status: new | rising | flat | unknown
5. Add an opt-in heat weight later, after label/eval checks show value.
6. Tests:
   - First run emits unknown and writes snapshot.
   - Second run computes deltas.
   - Renamed fork still matches by ID.
   - Corrupt cache degrades to unknown without failing the scan.

Acceptance sketch: Existing scans still work with an empty cache; a second scan emits deterministic momentum fields for changed fixture data.

Risk: Medium. Snapshot persistence is straightforward, but using momentum in ranking can overfit hype. Keep ranking-neutral until evaluated.

Source inspiration: gitdeck’s daily star/fork snapshots and cached insight pattern.

### ASAP-4 — Promote existing stage skips into stable NDJSON warnings and per-record reasons
**Status: DONE** — shipped as the ordered per-record `degraded` stage array while preserving stderr warnings and legacy skip booleans.
Implementation mapping: internal/forksops/annotations.go collectDegradedStages, cmd/spn/forks.go degradedToJSON, testdata/visibility_policy_cases.json all_skips_ordered.


Goal: Make degraded states easier for agents to consume without scraping stderr warnings.

Why now: forks.go already emits structured warnings for skip fields, and records already contain some booleans. Visibility work should carry the same reasons inline.

Pseudo-implementation:

1. Extend forkToJSON to include a degraded object when any skip exists:

        "degraded": {
          "stages": ["compare", "contributors"],
          "reasons": {
            "compare": "rate-limit reserve reached",
            "contributors": "GitHub stats not ready"
          }
        }

2. Keep stderr warnings for human runs but make stdout records sufficient for agents.
3. Add fixture tests around BudgetSkip, T3Skip, OwnerProfileSkip, and SiblingSimSkip.

Acceptance sketch: A parser consuming only stdout can tell “not computed” from “computed empty/zero” for every enrichment stage.

Risk: Low. This formalizes existing semantics.

Source inspiration: current spoon stage-skip contract plus Issue Finder’s reasoned feed policy.

### ASAP-5 — Add topic-mode candidate-lane scaffolding without changing defaults
**Status: DONE** — shipped live (not just scaffolding) as opt-in `--topic-lanes`/`--topic-lane-budget` (default,stars,updated,forks) with dedup, lane provenance, and unchanged default selection.
Implementation mapping: internal/forge/types.go TopicLane, internal/topics/topics.go ParseLanes/SelectBestFromLanes, cmd/spn/forks.go flag parse, internal/topics/topics_test.go.


Goal: Prepare topic mode for multi-lane upstream discovery while preserving current stable behavior.

Why now: topics.SelectBest is clean and small. We can add extension points without changing default ranking.

Pseudo-implementation:

1. Introduce TopicLane / TopicCandidateSource concepts:

        type TopicLane string
        const (
            TopicLaneDefault TopicLane = "default"
            TopicLaneStars   TopicLane = "stars"
            TopicLaneUpdated TopicLane = "updated"
            TopicLaneReadme  TopicLane = "readme_query"
        )

2. Keep Resolve calling the current GitHub topic search by default.
3. Add a future-capable internal function:

        func SelectBestFromLanes(cands []LaneCandidate, k int, now time.Time) []Selection

4. Deduplicate by repo full name; preserve lane provenance in Selection.Components or a new field.
5. Add tests proving default lane output is unchanged.

Acceptance sketch: No behavior changes unless a future flag enables extra lanes; internal types make the later feature small and low-risk.

Risk: Low if kept as scaffolding. Medium if it immediately starts spending more GitHub search calls.

Source inspiration: GitDeepSearch’s multi-query/lane search pattern, constrained by spoon’s topic selector.

## Work To Tackle ASAP After Quick Wins

These are not as small as the quick wins, but they are the next most important once the output contract and basic missing signals are in place.

### ASAP-NEXT-1 — Evaluate whether momentum should affect ordering
**Status: DONE** — deterministic output-only/tie-breaker/weighted momentum evaluator shipped (library-only, no CLI verb); verdict remains output-only pending real labeled networks.
Implementation mapping: internal/eval/momentum.go, internal/eval/momentum_test.go + testdata/momentum_policy_cases.json.

Goal: Decide empirically whether snapshot deltas improve shortlist precision.

Pseudo-implementation:

1. Pick 3-5 fork-rich upstreams with known useful forks.
2. Capture current top-10 / top-25 output.
3. Simulate candidate momentum fields from cached or manually collected snapshots.
4. Try ranking variants:
   - output-only momentum badge
   - small T1 component, for example max +3 or +5
   - tie-breaker only
5. Label whether elevated forks are actually worth opening.
6. Commit to ranking-neutral, tie-breaker, or weighted mode based on evidence.

Acceptance sketch: A short eval report under docs/research/ or experiments/ says whether momentum graduates from facet to scoring component.

#### Evaluation implementation note

- Added a deterministic evaluator and synthetic fixture without changing production ranking.
- The fixture proves that a weighted momentum variant can improve a known synthetic failure mode, but that is not enough evidence to alter the default ranking.
- Verdict for this pass: keep momentum output-only in production; require real labeled fork-network runs before promoting `weighted_3`, `weighted_5`, or tie-breaker behavior into `spn forks list`.

### ASAP-NEXT-2 — Build a visibility-policy evaluation fixture
**Status: DONE** — fixture-backed visibility/degraded precedence gate shipped, including the rule that enrichment skips alone never demote or hide.
Implementation mapping: internal/forksops/testdata/visibility_policy_cases.json, internal/forksops/annotations_policy_test.go TestVisibilityPolicyFixture.

Goal: Avoid silently hiding valuable niche forks while adding policy gates.

Pseudo-implementation:

1. Create a fixture containing current spoon outputs for several noisy networks.
2. Manually label obvious junk, useful niche forks, already-upstreamed forks, archived-but-useful forks, and un-enriched/degraded forks.
3. Run proposed visibility policy over fixture.
4. Measure:
   - true junk demoted/hidden
   - useful forks incorrectly hidden
   - degraded forks correctly marked unknown rather than bad
5. Tighten policy precedence.

Acceptance sketch: Visibility rules have regression tests against known tricky cases.

#### Fixture implementation note

`internal/forksops/testdata/visibility_policy_cases.json` pins the current visibility/degraded precedence, including the rule that enrichment skips do not by themselves demote or hide a fork.

### ASAP-NEXT-3 — Expand sibling similarity from upstream README only to fork intent similarity
**Status: DONE** — opt-in `--sibling-sim-mode fork_intent` shipped alongside the default upstream_readme mode, with mutual-exclusion validation and per-fork digest scoring.
Implementation mapping: internal/cluster/sibling_search.go SiblingSimMode, internal/cluster/pipeline.go mode dispatch, internal/github/sibling_search.go SearchForkIntentSiblings, cmd/spn/forks.go flag validation.


Goal: Improve sibling/relation signal quality without exploding API cost.

Current state: Sibling search is optional, GitHub-only, and described as one repository search plus about 50 README fetches. Current research notes it compares upstream README to sibling candidates, not fork digests to likely sibling projects.

Pseudo-implementation:

1. Add a second sibling-sim mode behind a flag:
   - default: current upstream README similarity
   - experimental: compare fork feature digest against sibling repo README/descriptions
2. Reuse existing embed.ForkFeatures digest construction where possible.
3. Cap candidates hard; preserve SiblingSimSkip on budget or embed failure.
4. Evaluate only on top-N enriched forks.

Acceptance sketch: Experimental mode can show a higher sibling-sim score for a fork whose changed paths/commits match a non-fork sibling project better than the upstream README does.

### ASAP-NEXT-4 — Document and enforce clean-room handling for AGPL concept sources
**Status: DEFERRED** — not selected this pass; it is the provenance gate for AGPL-inspired local-history work (FUTURE-1), which is itself deferred.

Goal: Prevent accidental license contamination from repowise while retaining conceptual learning.

Pseudo-implementation:

1. Add a short docs/research/license-notes.md or a section in implementation PR notes.
2. State that repowise is AGPL-3.0-only and must not be copied directly unless the project accepts AGPL obligations.
3. For any repowise-inspired work, cite concepts and papers, not source code implementations.
4. Prefer Kamei/Hassan papers or fresh independent implementation notes as sources.

Acceptance sketch: Future PRs touching change-risk/history ideas include an explicit provenance note.

## Future Improvements / Larger Bets

### FUTURE-1 — Optional local-clone deep diligence mode
**Status: DEFERRED** — not selected this pass; report rates it High risk / High effort and it requires the ASAP-NEXT-4 clean-room note first.

Goal: Add a second-stage analyzer for shortlisted forks that clones locally and computes richer history signals.

Use case: A user has a top-10 shortlist and wants to know which forks are focused feature branches versus broad maintenance churn.

Pseudo-implementation:

1. Add a separate command or flag, for example spn forks diligence owner/repo --top 10 or spn forks list owner/repo --deep-local --shortlist 10.
2. For each shortlisted fork:
   - shallow/fetch relevant branches carefully
   - identify divergent commits/files relative to upstream
   - compute change entropy / scatter
   - compute co-change breadth
   - summarize risk/value facets
3. Keep local clones under a bounded cache with cleanup policy.
4. Emit a separate deep object in NDJSON rather than mutating base heat until the signal is validated.

Risk: High. It changes setup expectations and runtime cost. Keep opt-in.

Source inspiration: repowise change-risk/co-change concepts; git_bayesect priors; git-newspaper local-git summarization as presentation inspiration only.

### FUTURE-2 — Curated priors for subsystem-specific fork prospecting
**Status: PLANNED** — selected for this implementation set; ship curated priors as an opt-in, output-first `priorScore`/`priorReasons` facet with a matched-then-unmatched lane split, never hiding forks or mutating heat.

Goal: Let users express known interest areas that bias shortlist selection without replacing objective scoring.

Pseudo-implementation:

1. Accept a small priors config:
   - path globs
   - keywords
   - languages
   - owner allow/deny hints
2. Convert priors into query relevance or expected-rank adjustments.
3. Surface the adjustment explicitly as priorScore / priorReasons.
4. Never hide high-heat forks solely because they miss priors; demote or split into lanes.

Source inspiration: git_bayesect filename/text priors, mapped into spoon’s existing query/expected-rank machinery.

#### Implementation note

Priors ship as an opt-in `--priors PATH` output-first facet computed from already-fetched T1/T2 data (zero extra API); matched forks are listed before unmatched (heat order within each lane) only when neither `--query` nor `--shortlist` is active; deny demotes to score 0 but never hides; heat and default output are unchanged.

### FUTURE-3 — Topic-mode lane diversification
**Status: DEFERRED** — superseded in large part by the shipped ASAP-5 lanes (default,stars,updated,forks); only the unshipped readme/query lane remains, deferred.

Goal: Discover upstream seed repos that current topic mode misses because it relies on a single popularity/network/recency search pool.

Pseudo-implementation:

1. Add opt-in flags:
   - --topic-lanes=default,updated,stars,readme
   - --topic-lane-budget=N
2. Fetch separate candidate pools.
3. Deduplicate by full name.
4. Preserve lane provenance in stderr topic_repo_selected records.
5. Reuse or extend SelectBest to keep final scoring transparent.

Risk: Medium. It spends additional search calls and can surface noisy repos. Gate behind explicit flags first.

### FUTURE-4 — Browser/bookmarklet entry point for casual fork discovery
**Status: DEFERRED** — not selected; low leverage for the agent-first CLI.

Goal: Borrow Useful Forks’ convenience without importing its weak ranking model.

Pseudo-implementation:

1. Add docs or a tiny browser helper that maps GitHub repo pages to spn forks list owner/repo commands or a local web endpoint.
2. Keep spoon ranking as backend; do not reimplement Useful Forks ranking in JS.
3. Use this only as UX sugar.

Risk: Low if separated from core. Low priority for agent-first workflows.

### FUTURE-5 — Explainability profiles / narrative summaries
**Status: PLANNED** — selected for this implementation set; ship deterministic presentation-only `profile`/`profileReasons` derived from already-shipped facets plus the new prior signal.

Goal: Make fork results easier to skim for humans without changing scoring.

Pseudo-implementation:

1. Add optional summaries such as:
   - “focused security hardening fork”
   - “broad dependency maintenance fork”
   - “stale but historically popular fork”
   - “emerging active fork”
2. Generate from existing category, cluster, paths, momentum, and visibility reasons.
3. Keep the raw fields canonical; summaries are presentation only.

Source inspiration: git-newspaper archetypes, but toned down for engineering use.

#### Implementation note

Profiles are deterministic, presentation-only, derived from visibility / lone-wolf archetype / momentum / penalties / prior fields already on the record, with `standard` as the floor. These notes live inline; no separate docs file is created.

## Suggested Execution Order

1. Visibility envelope — fast, high leverage, minimal risk.
2. Inline degraded reasons — pairs naturally with visibility work.
3. Per-network rank/percentile bands — mostly output work, uses existing percentile machinery.
4. Momentum snapshots as output-only facet — first genuinely new persistence/signal work.
5. Momentum evaluation — decide whether to alter ordering.
6. Topic-lane scaffolding — no default behavior change.
7. Sibling-sim expansion experiment — useful but needs careful budget caps.
8. Local-clone diligence mode — high value, high effort, opt-in only.

## Do-Not-Do List

- Do not replace spoon’s ranking with Useful-Forks-style stars/activity sorting.
- Do not copy AGPL repowise code into spoon without an explicit license decision.
- Do not make local cloning part of the default fork-list path.
- Do not hide records aggressively before a visibility-policy fixture exists.
- Do not add new ranking weights without exposing their contribution and testing against real fork networks.
- Do not break NDJSON/agent output stability for browser-style UX.

## Complete Research Artifact Verbatim

**Question**: Evaluate the Git/GitHub/version-control codebases under `sources/` for ideas that materially improve `spoon` as a fork analyzer — not by cargo-culting shiny features, but by identifying mechanisms that increase shortlist precision, ranking honesty, GitHub-signal quality, and operator UX for fork prospecting. The real target is not “make spoon look like other tools”; it is “improve the probability that spoon surfaces the forks worth integrating, per API dollar and per human minute.”

**Framing pushback**: “Mine Git/GitHub/version-control codebases for features” is only partly the right route. The best fork-analysis improvements in this corpus did **not** come from other fork explorers; they came from tools that learned to separate **base ranking** from **trust/quality gating**, **repo-relative normalization**, and **budget-aware enrichment**. The dangerous assumption in the original framing is that fork-analysis improvement will mainly come from feature parity with other GitHub UIs; in reality, direct ports from browser-first tools like Useful Forks would regress spoon’s already more sophisticated scoring. The useful reframe is: mine adjacent tools for **decision-quality mechanisms**, not for superficial feature checklists. Does that reframe match your intent?

**Versions pinned**:
- **Target: `spoon`** — branch `improvements-4`, HEAD `abb56e7ebaf6e66af7e1880dae1dbd807d7972d6`; Go `1.26.2` in `go.mod`.[S1][S3]
- **`sources/useful-forks.github.io`** — HEAD `c497d3792ceacc1a3c1a67244fd0fedc6bbbedac`; website package `1.0.0`; MIT.[S1][S22]
- **`sources/AjmalShajahan-useful-forks.github.io`** — HEAD `d2d0e7ef389c91c80bc3d5b237b0ec19cc372675`; plugin manifest `2.2.3`; MIT family variant.[S1][S24]
- **`sources/GitDeepSearch`** — HEAD `5f2fd744a2b46f8b882b3c15e2a5d8e79fae3ebc`; package `1.0.0`; React 18 / Vite 5; MIT.[S1][S28][S27]
- **`sources/gitdeck`** — HEAD `ba9dd48514d134f91d9d9c6fb4d9c4fb9e3ae5ca`; package `1.0.4`; React 19 / Vite 8; MIT.[S1][S33]
- **`sources/repowise`** — HEAD `2ce46d8d5fa19ffdb3ca9f7ea532557a1f9d69ca`; Python `>=3.11`; package `0.29.0`; **AGPL-3.0-only**.[S1][S35]
- **`sources/issue-finder`** — HEAD `7faa39dd667131cd6c97b3f5863cf7cf732b4605`; Rust edition `2021`; version `0.3.0`; MIT.[S1][S45]
- **`sources/git_bayesect`** — HEAD `8254d60e85867c8f9d377e8cf48a03edea9faccf`; Python `>=3.10`; version `1.2`; MIT.[S1][S57]
- **`sources/git-newspaper`** — HEAD `2e5027e3ad997de4cf16632b22ac30c7998ece49`; Node `>=18`; version `0.1.2`; MIT.[S1][S60]

**Scope**:
- **In**
  - `spoon`’s current fork-ranking, enrichment, clustering, topic mode, query mode, caching, and agent-CLI surfaces.[S2][S4][S8][S10][S18]
  - Source repos that concretely touch ranking, GitHub API use, local git mining, caching, or UX patterns relevant to fork analysis: Useful Forks family, GitDeepSearch, gitdeck, repowise, issue-finder, git_bayesect, git-newspaper.[S19][S25][S29][S34][S45][S57][S60]
  - License/attribution and portability constraints.[S22][S27][S33][S35][S45][S57][S60]
- **Out**
  - Implementing any changes.
  - Generic agent-orchestration systems whose Git concepts are incidental rather than fork-analysis-relevant: `flock`, `no-mistakes`.[S62][S63]
  - Alternative VCS infrastructure not directly about GitHub fork prospecting: `lore`, `rift`.[S64][S65]
  - Profile gamification (`gitfut`) and prompt libraries (`claude-code-prompts`) as core fork-analysis inputs.[S66]

**Context**:
`spoon` is already **well past** the “stars plus ahead/behind” stage. It has two user faces: a human-facing TUI and an agent-shaped `spn` CLI with JSON/NDJSON surfaces.[S2] Its fork evaluation pipeline is tiered: T1 surface signals (`recency`, `stars`, `sub_forks`, `releases`), T2 divergence signals (`mna`, `sync_ratio`, `feature_ratio`), and T3 behavior-ish signals (`lone_wolf`, `span`, `novelty`), all under a capped additive scoring model.[S4] Finalization then applies percentile-derived trust, hard zeroing for forks with no ahead commits, upstreamed/archived penalties, low-recency dampening, topic-tag penalty, and an owner “fork farmer” penalty.[S4][S5][S19]

`spoon` also already contains features that many source repos do **not**: 
- **uncertainty-aware shortlisting** via Robbins-style expected-rank rather than raw top-score picking; this is designed for “give me the top-k worth integrating” under partial evidence, not just “pick the single max.”[S8][S9]
- **query-aware reranking** over change digests, with lexical fallback or OpenVINO reranker.[S8][S18]
- **post-T2 clustering** with novelty bonus, sibling-similarity bonus, optional zero-shot categorization, and MDG/directory centrality plumbing.[S8][S10][S11][S18]
- **topic mode** for selecting representative upstreams from a GitHub topic using stars, fork-network size, and recency before prospecting their forks.[S2][S17]
- **rate-budget-aware degradation**: `BudgetSkip`, `ClusterSkip`, owner-profile caps, and reserve headroom semantics.[S8][S18]

Relevant seams where new work would attach:
1. **Momentum is missing.** T1 uses current stars, sub-forks, releases, and push recency, but not time-series deltas for stars/forks/watchers.[S4]
2. **Absolute heat is visible; repo-relative rank is not.** `spoon` exposes component scores and trust, but not an explicit percentile / “typical for this fork network” surface.[S4][S5][S8]
3. **Visibility policy is mostly encoded as penalties/skips, not as a first-class feed layer.** There are degraded-state fields (`BudgetSkip`, `ClusterSkip`, `T3Skip`), but no generalized `visible/demoted/hidden because X` policy envelope around fork records.[S8]
4. **Sibling similarity is narrow.** The current search picks the **first upstream topic** and compares upstream README against non-fork siblings; it does not multi-topic search or compare per-fork digests to sibling candidates.[S16]
5. **Topic mode is popularity-heavy.** It chooses upstream representatives with only stars, fork-network size, and recency; there is no explicit anomaly/noise screen for topic picks.[S17]
6. **Owner-profile trust is intentionally cheap and approximate.** It caps at 30 owners/run and 5 repo pages/owner, good for budget safety but shallow for nuanced farm detection.[S8][S15]
7. **Category anchors are coarse and fixed.** `feature`, `bugfix`, `security`, `ci-build`, `docs`, etc. are useful facets, but not enough to explain “integration-worthiness” alone.[S11]

**Constraints**:
- **Agent/streaming contract matters.** `spn forks list` is NDJSON-first and must preserve structured degraded states; any UX import that assumes browser interactivity or batch-only HTML must not break that contract.[S2][S8][S18]
- **GitHub API cost is load-bearing.** Useful Forks explicitly warns that scanning large fork trees can exhaust API limits, and spoon already carries reserve-floor, owner-profile caps, and short-retry behavior to avoid eating the rate window alive.[S19][S21][S13][S15]
- **Batch clustering is already a semantic tradeoff.** In `spoon`, enabling clustering collapses true streaming into collect-then-emit semantics; any enrichment that needs the whole candidate set has to justify that cost.[S8][S10]
- **License risk blocks direct reuse from repowise.** `repowise` is AGPL-3.0-only, so its code is concept inspiration unless you consciously accept copyleft obligations.[S35]
- **`spoon` is already better than most direct analogues.** Useful Forks is still fundamentally star-ordered plus branch-activity filter, and GitDeepSearch is general repo search, not fork ranking. That means the research goal is additive refinement, not replacement.[S19][S21][S25][S26]
- **Local-history depth and API-only convenience pull in opposite directions.** repowise, git-newspaper, and git_bayesect get signal by reading local git history; Useful Forks and GitDeepSearch stay browser/API-only. That constraint decides whether a signal can live in default spoon or only in an escalation path.[S36][S40][S57][S60][S61]

**Mechanisms (not slogans)**:
1. **`spoon`’s current ranking is an additive evidence ladder with post-hoc honesty corrections, not a monolith.** T1/T2/T3 components are summed under caps, then trust and penalties are applied afterward; tiny fork sets skip percentile trust because percentiles below 10 samples are noise.[S4][S5] **Confidence: high.**  
   **Falsifier:** find a code path where tiny sets still apply percentile trust or where archived/upstreamed/no-ahead handling is embedded directly into raw tier scoring.

2. **A direct Useful Forks port would regress ranking quality because it collapses “worth integrating” into “starred and has branch activity.”** Useful Forks filters out forks with no default-branch activity since creation, sorts by stars, recursively scans sub-forks, and decorates results with ahead/behind badges and simple user filters.[S19][S20][S21] That is a good discovery affordance, but much weaker than spoon’s current divergence ownership, MNA weighting, lone-wolf detection, clustering, and owner-farmer penalty.[S4][S6][S19] **Confidence: high.**  
   **Falsifier:** find code in Useful Forks that computes anything equivalent to `sync_ratio`, `feature_ratio`, lone-wolf strength, or owner-profile trust.

3. **Repo-relative normalization is the right way to present any new “risk” or “behavior” signal when networks differ wildly in scale.** repowise’s `change_risk` docs explicitly state that absolute calibrated bands skew on repos with larger typical commits, while the **percentile within the repo’s own distribution** remains the honest triage signal; its runtime therefore computes a local baseline and derives percentile/priority from that distribution.[S39][S43] Fork networks have the same problem: a raw signal that is meaningful in a 20-fork niche network is not directly comparable to a 5,000-fork megarepo network. **Confidence: high.**  
   **Falsifier:** show that spoon’s raw heat distributions are already stable and comparable across networks of radically different sizes without percentile context.

4. **A separate visibility/quality policy layer reduces false-positive shortlist pollution better than stuffing every concern into one scalar score.** Issue Finder first computes base issue value, then applies freshness caps, competition suppression, low-trust hiding, scope hiding, and feed visibility as a second stage with explicit reasons.[S47][S48][S49][S50][S51] `spoon` already has the beginnings of this idea (`BudgetSkip`, `ClusterSkip`, owner penalties), but they are not yet a unified “show / demote / hide, and why” layer for fork records.[S8][S19] **Confidence: high.**  
   **Falsifier:** show that all current spoon output modes already carry explicit visibility semantics and demotion reasons independent of heat.

5. **Low-cost time-series history is an underused signal in fork analysis.** gitdeck persists daily star/fork snapshots for 90 days, attaches recent history to repos, and exposes sorted fork/stargazer views plus cached insight computation with bounded concurrency.[S29][S30][S31][S32][S33] That mechanism can give spoon “is this fork emerging or merely accumulated?” without inventing a giant new scoring theory. **Confidence: high.**  
   **Falsifier:** demonstrate that current spoon already persists per-fork historical deltas or that GitHub’s live API already returns all needed trend data with no local snapshotting.

6. **Deep git-history signals are promising but belong behind a shortlist/escalation boundary, not in default API-only prospecting.** repowise’s co-change/entropy machinery depends on walking substantial commit history and computing decay-weighted co-change partners and Hassan-style history complexity metrics.[S40][S41][S44] git_bayesect likewise gets leverage from local history and user-supplied priors over commits/files/text.[S57][S58][S59] Those ideas are valuable, but they are expensive and operationally different from spoon’s current forge-API-first pass. **Confidence: high.**  
   **Falsifier:** find a cheap, one-request-per-fork API path that yields equivalently rich co-change or entropy history.

7. **`spoon` already owns the strongest uncertainty-aware shortlist primitive in the corpus.** The Robbins expected-rank shortlist models utility uncertainty from evidence depth and minimizes expected rank under uncertainty rather than naively trusting the current best score.[S8][S9] That means the best external borrow from git_bayesect is **not** “add Bayesian math somewhere”; it is “if you add priors or extra signals, feed them into the existing uncertainty-aware shortlist rather than replacing it.” **Confidence: high.**  
   **Falsifier:** find a source repo in this corpus with a better explicit uncertainty-aware top-k selection mechanism than spoon’s current expected-rank pass.

**Canonical sources**:
- **Just-in-time quality assurance / change-level defect prediction** — Yasutaka Kamei et al., *A Large-Scale Empirical Study of Just-in-Time Quality Assurance*, IEEE Transactions on Software Engineering, 2013, DOI `10.1109/TSE.2012.70`.[S67]
- **History complexity / change entropy as a fault predictor** — Ahmed E. Hassan, *Predicting Faults Using the Complexity of Code Changes*, ICSE 2009, DOI `10.1109/ICSE.2009.5070510`.[S68]
- **Expected-rank optimal stopping / Robbins problem** — *Minimizing the Expected Rank with Full Information* (verified public PDF source during this run); primary-source trail exists, but authorship/bibliographic normalization was not fully resolved in-run. **unverifiable — search lead:** `https://www.math.ucla.edu/~tom/papers/exprank.pdf`.[S69]

**Disagreements in the wild**:
- **Popularity-first vs behavior-first ranking**
  - **Camp A:** rank by stars / visible popularity / simple recency because it is cheap, legible, and robust enough for casual discovery. Useful Forks and GitDeepSearch both end up there in practice.[S19][S21][S25][S26]
  - **Camp B:** popularity is necessary but insufficient; trust, novelty, divergence ownership, and evidence quality need separate treatment. spoon, issue-finder, and repowise all embody this more layered stance.[S4][S8][S47][S48][S49]
  - **Constraint that flips the answer:** if the goal is “a browser helper for casual fork browsing,” Camp A wins; if the goal is “top forks worth integrating,” Camp B wins.

- **Absolute calibrated scores vs repo-relative percentiles**
  - **Camp A:** show absolute scores/bands because they are compact and intuitive.
  - **Camp B:** absolute bands skew across repos/network shapes, so lead with repo-relative percentile/rank and keep raw score secondary. repowise argues this explicitly for change risk.[S39][S43]
  - **Constraint that flips the answer:** if you need cross-network comparability, percentiles or rank bands matter more; if you only triage inside one network, absolute scores are tolerable.

- **API-only convenience vs local-history depth**
  - **Camp A:** stay API-only for zero-setup UX and lower operational cost (Useful Forks, GitDeepSearch).[S19][S25][S26]
  - **Camp B:** accept local git mining because real history signals live there (repowise, git-newspaper, git_bayesect).[S36][S40][S57][S60][S61]
  - **Constraint that flips the answer:** whether spoon is allowed to offer an opt-in local-clone escalation path rather than a pure hosted/API path.

**Decision table**:

| Scenario / constraint | Simple API filter-sort (Useful Forks style) | Current spoon | Augmented spoon: visibility + momentum + percentile *(recommended default)* | Deep local-history escalation |
|---|---|---|---|---|
| Small fork network (<30 forks) | Fast, understandable; often “good enough” | Good; tiny-set trust exemption already helps [S5] | Best explanation value; percentile + demotion reasons avoid over-reading raw heat | Overkill |
| Large noisy network (1000+ forks) | Drowns in stars/farms and API pain [S19][S21] | Better, but static-count bias remains | Best default; gates + trend history + reserve-aware degradation | Useful only on shortlist |
| Topic prospecting | Weak; no upstream representative logic | Good stars/network/recency picker [S17] | Better if you add lane diversity and anomaly screens | Only after upstream shortlist chosen |
| Agent / NDJSON workflow | Poor fit; browser-first | Native fit [S2][S8] | Native fit, richer explanations | Native fit if opt-in |
| Casual browser entry | Best | Weaker today | Could add shortcut affordances later | Bad |
| “Which fork should I integrate?” | Weak | Strong | Stronger | Strongest, but only if human tolerates extra setup |

**Recommended commitments**:
- **Fork**: Should spoon replace its core ranking with another tool’s simpler heuristic?
  - **Camps**: current spoon core vs Useful-Forks-style activity+stars
  - **Each commitment forces**:
    - **Replace with simple heuristic** → cheaper explanation, worse precision, regression in divergence/trust reasoning.
    - **Keep spoon core** → preserves strongest differentiator, constrains imported ideas to additive layers.
  - **Recommendation**: **Keep spoon’s core heat/cluster/shortlist architecture.** The corpus does not contain a better fork-ranking core than spoon already has.[S4][S8][S9] What would change my mind: a source repo here with demonstrated fork-specific ranking signals that predict integration-worthiness better than spoon’s current additive/divergence model.

- **Fork**: How should new trust/noise heuristics land?
  - **Camps**: bake everything into heat vs add a post-rank visibility/quality layer
  - **Each commitment forces**:
    - **All-in-heat** → one number, opaque demotions, harder debugging.
    - **Visibility layer** → more fields, but honest explanations and reversible policy tuning.
  - **Recommendation**: **Add a first-class visibility / demotion layer** modeled after Issue Finder’s feed/quality-policy split, while keeping heat as the base value score.[S47][S48][S49] Expected impact: high; risk: low; effort: medium. What would change my mind: evidence that spoon users only consume a single scalar and ignore explicit reasons.

- **Fork**: How should spoon capture “emerging fork” momentum?
  - **Camps**: static counts only vs persisted time-series snapshots
  - **Each commitment forces**:
    - **Static only** → simpler code, blind to breakout forks.
    - **Snapshots** → small persistence layer, better trend signal, more UX surfaces.
  - **Recommendation**: **Add lightweight daily snapshot history for stars/forks and expose deltas/trend badges.** gitdeck shows a cheap implementation pattern; issue-finder already reasons over star/fork velocity in enriched repo facts.[S30][S31][S49] Expected impact: high; risk: low-medium; effort: medium. What would change my mind: proof that recent star/fork deltas do not improve top-k quality on real fork sets.

- **Fork**: How should spoon improve upstream/topic discovery?
  - **Camps**: keep stars/fork-network/recency only vs add multi-lane search
  - **Each commitment forces**:
    - **Current selector only** → stable, cheap, may miss non-obvious but active upstreams.
    - **Lane diversification** → slightly more complexity and API cost, potentially better seed repos.
  - **Recommendation**: **Add limited multi-lane upstream discovery only in topic mode** — e.g. stars lane, updated lane, README/query lane — deduped and then rescored by spoon’s own selector. GitDeepSearch provides the lane pattern; Issue Finder provides quota discipline and merge semantics.[S25][S26][S52] Expected impact: medium; risk: medium; effort: medium. What would change my mind: evidence that current topic mode already finds all materially interesting upstreams in practice.

- **Fork**: Should spoon import repowise-style git-history intelligence directly into default ranking?
  - **Camps**: default ranker gets deep local-history signals vs optional shortlist-only escalation
  - **Each commitment forces**:
    - **Default** → heavy setup, harder API-only UX, more compute budget.
    - **Escalation** → keeps default cheap while enabling deeper diligence on finalists.
  - **Recommendation**: **Make deep git-history signals an opt-in escalation on shortlisted forks only.** Borrow the idea of co-change scatter / change entropy / localized risk explanation, not the AGPL code.[S35][S40][S41][S42][S43][S44] Expected impact: medium-high; risk: medium-high; effort: high. What would change my mind: a proven low-cost API-only equivalent.

- **Fork**: Should spoon copy code from repowise?
  - **Camps**: direct code reuse vs concept-only transfer
  - **Each commitment forces**:
    - **Direct reuse** → AGPL inheritance questions.
    - **Concept-only** → more implementation work, clean licensing.
  - **Recommendation**: **Concept-only transfer. Do not port AGPL code.**[S35] What would change my mind: explicit willingness to absorb AGPL obligations.

**Implementation strategies (tiered)**:
- **Cheap / lossy**: Port Useful Forks ideas directly — “only show forks with default-branch activity, sort by stars, add ahead/behind badges, maybe a browser button.” Tempting because the code is MIT and simple.[S19][S20][S21][S22] **Do not ship** as the main improvement path: it would mostly recreate functionality spoon already surpasses while weakening ranking honesty.
- **Default / workhorse**: Keep spoon’s current heat/cluster/shortlist core; add **(a)** repo-relative percentile presentation, **(b)** explicit visibility/demotion reasons, and **(c)** low-cost snapshot-based momentum fields. This compounds well with existing NDJSON/TUI contracts and does not require local clones.[S4][S8][S29][S30][S31][S39]
- **Expensive / escalation**: Add an opt-in “deep diligence” mode on top-N forks that clones locally and computes richer history signals (co-change scatter, entropy, maybe curated priors on subsystems or filenames). This is where repowise/git_bayesect ideas belong if you want higher-confidence integration triage.[S36][S40][S41][S57][S58][S59]

**Testable hypotheses**:
1. `Hypothesis:` Recent star/fork momentum improves top-10 fork precision beyond static counts alone. `| Test:` On 3 known fork-rich upstreams, compare today’s spoon top-10 vs top-10 after adding 30-day star/fork delta as a secondary signal; manually label “worth opening” forks. `| Predicted outcome:` At least one emerging fork rises into top-10 without displacing obviously strong incumbents.[S29][S30][S31][S49]
2. `Hypothesis:` Repo-relative percentile bands reduce misreads of raw heat across differently sized fork networks. `| Test:` Run spoon on a tiny, medium, and huge fork network; compare raw heat distributions against percentile-banded outputs. `| Predicted outcome:` percentile bands remain interpretable where raw 0–100 heat does not.[S4][S5][S39][S43]
3. `Hypothesis:` A visibility/demotion layer improves shortlist precision more than additional raw-score tweaks. `| Test:` Take one noisy topic/upstream, run current spoon, then simulate Issue-Finder-style hide/demote policies for archived/no-ahead/farmer/anomaly/degraded results; compare top-15 precision. `| Predicted outcome:` fewer obvious junk candidates survive, with minimal loss of true positives.[S47][S48][S49][S51]
4. `Hypothesis:` Topic-mode lane diversification surfaces better upstream seeds than stars/network/recency alone. `| Test:` For one topic, collect stars, updated, and README/query lanes à la GitDeepSearch, dedupe, then rescore with current topic selector. `| Predicted outcome:` at least one non-obvious but fork-rich upstream enters the final representative set.[S17][S25][S26]
5. `Hypothesis:` Deep local-history signals distinguish focused feature forks from broad maintenance churn better than MNA alone. `| Test:` Clone 5–10 shortlisted forks for one upstream and compute change entropy / co-change partner breadth from their divergent histories. `| Predicted outcome:` broad, scattered maintenance forks separate from targeted feature branches.[S40][S41][S44]

**Failure modes**:
- **Wrong path — over-penalizing niche but valuable forks.** A visibility layer can hide legitimate small-community forks if anomaly gates are too blunt. Detect via “hidden but manually selected” rate and override counts.
- **Wrong path — momentum hype.** Trend deltas can over-elevate short-lived spikes. Detect by measuring how often momentum-elevated forks later fall back or never receive human follow-up.
- **Slow path — enrichment budget blowups.** Snapshot, topic lanes, or sibling/readme enhancements can increase GitHub calls. Detect via per-stage request counters, reserve-floor hits, `BudgetSkip`, and owner-profile skip rates.[S8][S15][S53]
- **Unsafe path — AGPL contamination.** Copying repowise code rather than ideas would change licensing posture. Detect operationally by enforcing provenance notes on any borrowed implementation and forbidding direct code carryover from AGPL paths.[S35]
- **Explanatory drift.** More signals without better explanation will make spoon feel “smarter” while actually being less debuggable. Detect by requiring every demotion to carry machine-readable reasons and source stage.

**Adjacent / cross-domain leads**:
- **Issue triage feed policy → fork shortlist policy.** Issue Finder’s “value score, then quality gates, then visibility” translates well to fork prospecting.[S47][S48][S49] **Disanalogy:** issues have explicit social signals (claims, PRs, maintainer replies); forks are artifacts, so social-competition heuristics do not transfer literally.
- **Just-in-time change risk → fork integration risk.** repowise’s Kamei/Hassan-inspired diff risk shows how to normalize volatile signals within a repo.[S36][S37][S38][S39][S43][S67][S68] **Disanalogy:** change-risk predicts defect proneness of a code change, not strategic value of adopting a fork.
- **Bayesian bisection priors → curated fork priors.** git_bayesect’s filename/text prior hooks suggest a future curated-prior mechanism for known good subsystem forks.[S59] **Disanalogy:** bisection is sequential experiment design with pass/fail observations; fork prospecting usually lacks that feedback loop.
- **Narrative archetypes → explainability skin, not scoring.** git-newspaper shows how local git facts can be turned into archetypes.[S60][S61] **Disanalogy:** archetypes are communication aids; they are too theatrical to drive ranking.

**Open questions for the human**:
1. Is `spoon` allowed to gain an **optional local-clone escalation mode**, or must the main product stay forge-API-only?
2. Do you want to preserve `spoon`’s current **agent-CLI ethos** strictly, or is a later browser/URL shortcut surface acceptable for casual discovery?
3. Are you willing to accept **concept-only** inspiration from AGPL sources like repowise, with a hard ban on code carryover?
4. For topic mode, do you care more about **discovering underrated upstreams** or keeping **API costs minimal**?
5. Should “momentum” be used only as an **explanatory facet**, or can it affect ordering?

**Critical files**:
1. `spoon/internal/forksops/stream.go` — the fork-enrichment contract, degraded-state semantics, clustering, budgeting, and NDJSON output shape.[S8]
2. `spoon/internal/heat/score.go` — base scoring, penalties, confidence tiers, and heat structure.[S4]
3. `spoon/internal/heat/scorer.go` — percentile trust and tiny-set behavior.[S5]
4. `spoon/cmd/spn/forks.go` — CLI flags, topic-mode toggles, query scorer setup, sibling-sim wiring, and NDJSON emission policy.[S18]
5. `spoon/internal/cluster/pipeline.go` — batch clustering, centrality plumbing, sibling-sim lifecycle, and score mutation rules.[S10]
6. `spoon/internal/github/owner_profile.go` — current owner-farmer trust approximation and cache policy.[S15]
7. `spoon/internal/github/sibling_search.go` — current distant-relation search scope and its “first topic + README cosine” narrowness.[S16]
8. `spoon/internal/topics/topics.go` — current upstream topic representative selection logic.[S17]
9. `sources/useful-forks.github.io/website/src/queries-logic.js` — the canonical “simple fork explorer” baseline spoon should not regress to.[S21]
10. `sources/issue-finder/src/recommendation/feed_ranker.rs` and `quality_policy.rs` — the clearest transfer target for a separate visibility/demotion layer.[S47][S48]
11. `sources/repowise/packages/core/src/repowise/core/analysis/change_risk/normalize.py` and `baseline.py` — the cleanest statement of repo-relative normalization.[S38][S39]
12. `sources/gitdeck/src/server/snapshots.ts` — the simplest momentum-persistence pattern in the corpus.[S30]

**Sources**:
- **[S1]** Command observations from this run: `git rev-parse HEAD` for `spoon` (`abb56e7ebaf6e66af7e1880dae1dbd807d7972d6`) and selected source repos (`useful-forks.github.io` `c497d3792ceacc1a3c1a67244fd0fedc6bbbedac`; `AjmalShajahan-useful-forks.github.io` `d2d0e7ef389c91c80bc3d5b237b0ec19cc372675`; `GitDeepSearch` `5f2fd744a2b46f8b882b3c15e2a5d8e79fae3ebc`; `gitdeck` `ba9dd48514d134f91d9d9c6fb4d9c4fb9e3ae5ca`; `repowise` `2ce46d8d5fa19ffdb3ca9f7ea532557a1f9d69ca`; `issue-finder` `7faa39dd667131cd6c97b3f5863cf7cf732b4605`; `git_bayesect` `8254d60e85867c8f9d377e8cf48a03edea9faccf`; `git-newspaper` `2e5027e3ad997de4cf16632b22ac30c7998ece49`).
- **[S2]** `spoon/README.md:1-223`
- **[S3]** `spoon/go.mod:1-38`
- **[S4]** `spoon/internal/heat/score.go:1-283`
- **[S5]** `spoon/internal/heat/scorer.go:1-68`
- **[S6]** `spoon/internal/heat/filter.go:1-104`
- **[S7]** `spoon/internal/heat/lonewolf.go:1-143`
- **[S8]** `spoon/internal/forksops/stream.go:1-205`
- **[S9]** `spoon/internal/forksops/rank.go:1-66`
- **[S10]** `spoon/internal/cluster/pipeline.go:1-263`
- **[S11]** `spoon/internal/cluster/classify.go:1-81`
- **[S12]** `spoon/internal/github/forks.go:1-29`
- **[S13]** `spoon/internal/github/retry.go:1-26`
- **[S14]** `spoon/internal/github/cache.go:1-136`
- **[S15]** `spoon/internal/github/owner_profile.go:1-218`
- **[S16]** `spoon/internal/github/sibling_search.go:1-154`
- **[S17]** `spoon/internal/topics/topics.go:1-105`
- **[S18]** `spoon/cmd/spn/forks.go:1-283` and `spoon/cmd/spn/forks.go:359-573`
- **[S19]** `sources/useful-forks.github.io/README.md:1-101`
- **[S20]** `sources/useful-forks.github.io/website/src/queries-init.js:1-77`
- **[S21]** `sources/useful-forks.github.io/website/src/queries-logic.js:1-586`
- **[S22]** `sources/useful-forks.github.io/website/package.json:1-21`; `sources/useful-forks.github.io/LICENSE:1-18`; `sources/useful-forks.github.io/plugin/manifest.json:1-24`
- **[S23]** `sources/AjmalShajahan-useful-forks.github.io/plugin/useful-forks.js:1-80`
- **[S24]** `sources/AjmalShajahan-useful-forks.github.io/README.md:31-95`; `sources/AjmalShajahan-useful-forks.github.io/plugin/manifest.json:2-26`
- **[S25]** `sources/GitDeepSearch/README.md:1-223`
- **[S26]** `sources/GitDeepSearch/src/lib/github.js:1-183`
- **[S27]** `sources/GitDeepSearch/package.json:1-17`; `sources/GitDeepSearch/LICENSE:1-18`
- **[S28]** `sources/gitdeck/src/server/githubClient.ts:1-121`
- **[S29]** `sources/gitdeck/src/server/repoInsights.ts:1-93`
- **[S30]** `sources/gitdeck/src/server/snapshots.ts:1-64`
- **[S31]** `sources/gitdeck/src/api/cache.ts:1-74`
- **[S32]** `sources/gitdeck/src/components/modals/RepositoryMetricModal.tsx:1-130`; `sources/gitdeck/src/api/github.ts:177-283`
- **[S33]** `sources/gitdeck/package.json:1-38`; `sources/gitdeck/LICENSE:1-18`; `sources/gitdeck/src/utils/dashboard.ts:1-223`
- **[S34]** `sources/repowise/README.md:39-113`
- **[S35]** `sources/repowise/pyproject.toml:1-163`
- **[S36]** `sources/repowise/packages/core/src/repowise/core/analysis/change_risk/features.py:1-220`
- **[S37]** `sources/repowise/packages/core/src/repowise/core/analysis/change_risk/model.py:1-113`
- **[S38]** `sources/repowise/packages/core/src/repowise/core/analysis/change_risk/baseline.py:1-49`
- **[S39]** `sources/repowise/packages/core/src/repowise/core/analysis/change_risk/normalize.py:1-70`
- **[S40]** `sources/repowise/packages/core/src/repowise/core/ingestion/git_indexer/indexer.py:1-263`
- **[S41]** `sources/repowise/packages/core/src/repowise/core/ingestion/git_indexer/co_change.py:1-171`
- **[S42]** `sources/repowise/packages/core/src/repowise/core/analysis/health/scoring.py:79-133`
- **[S43]** `sources/repowise/docs/CHANGE_RISK.md:1-113`
- **[S44]** `sources/repowise/docs/CODE_HEALTH.md:1-123`
- **[S45]** `sources/issue-finder/Cargo.toml:1-28`; `sources/issue-finder/src/cli.rs:1-143`
- **[S46]** `sources/issue-finder/src/recommendation/engine.rs:1-263`
- **[S47]** `sources/issue-finder/src/recommendation/feed_ranker.rs:1-103`
- **[S48]** `sources/issue-finder/src/recommendation/quality_policy.rs:1-523`
- **[S49]** `sources/issue-finder/src/value_scoring.rs:1-303`
- **[S50]** `sources/issue-finder/src/value_scores.rs:1-263`
- **[S51]** `sources/issue-finder/src/value_gates.rs:1-223`
- **[S52]** `sources/issue-finder/src/discovery.rs:1-523`
- **[S53]** `sources/issue-finder/src/github_budget.rs:1-183`
- **[S54]** `sources/issue-finder/src/github_enrichment.rs:1-263`
- **[S55]** `sources/issue-finder/src/context_pack.rs:1-183`
- **[S56]** `sources/issue-finder/src/competition.rs:1-263`
- **[S57]** `sources/git_bayesect/README.md:1-69`; `sources/git_bayesect/pyproject.toml:1-28`
- **[S58]** `sources/git_bayesect/git_bayesect.py:38-191`
- **[S59]** `sources/git_bayesect/git_bayesect.py:384-501` and `sources/git_bayesect/git_bayesect.py:716-773`
- **[S60]** `sources/git-newspaper/README.md:1-57`; `sources/git-newspaper/package.json:1-26`
- **[S61]** `sources/git-newspaper/src/git.js:1-160`; `sources/git-newspaper/src/archetype.js:1-64`
- **[S62]** `sources/flock/README.md:1-180`
- **[S63]** `sources/no-mistakes/README.md:1-180`
- **[S64]** `sources/rift/README.md:1-183`
- **[S65]** `sources/lore/README.md:1-180`
- **[S66]** `sources/gitfut/README.md:1-60`
- **[S67]** Web search verified canonical source: Yasutaka Kamei et al., *A Large-Scale Empirical Study of Just-in-Time Quality Assurance*, IEEE Transactions on Software Engineering, 2013, DOI `10.1109/TSE.2012.70`
- **[S68]** Web search verified canonical source: Ahmed E. Hassan, *Predicting Faults Using the Complexity of Code Changes*, ICSE 2009, DOI `10.1109/ICSE.2009.5070510`
- **[S69]** Web search verified public source for Robbins expected-rank literature: `https://www.math.ucla.edu/~tom/papers/exprank.pdf` (*Minimizing the Expected Rank with Full Information*; bibliographic normalization not completed in-run)

**Verification leads**:
- Verify whether the **other Useful Forks family snapshots** (`websterwh`, `devnoname120`, `940smiley`, `awesome-stars`) contain any non-trivial divergence beyond packaging/plugin modernization. Search lead: compare `plugin/manifest.json`, `plugin/useful-forks.js`, and `website/src/queries-logic.js` across those repos.
- Verify whether **momentum** materially improves fork ranking by testing on real labeled fork networks; current evidence here is mechanism-level, not benchmarked inside spoon.[S29][S30][S31]
- Verify whether a **topic-mode lane merge** outperforms current `topics.SelectBest` on your actual use cases; the GitDeepSearch transfer is conceptual, not fork-specific.[S17][S25][S26]
- Verify whether **local-history escalation** is worth the setup cost for spoon users before investing in repowise-like co-change/entropy analysis.[S40][S41][S43][S44]
- **[UNVERIFIED]** This artifact was **not** persisted to `local://spoon-4-sources-fork-analyzer-research.md` in this run because the session was constrained read-only and no write tool was available.

**Meta-observation**: The surprise is that the highest-leverage ideas did **not** come from the most obviously similar fork tools. Useful Forks, GitDeepSearch, and other GitHub surfaces are mostly about *getting a list on screen*; spoon is already beyond that. The strongest upgrades came from systems that learned three hard lessons: separate base score from visibility policy, normalize volatile signals relative to the local population, and account for enrichment budget explicitly. The LLM failure mode here is obvious: it wants to fetishize novelty and over-praise other tools’ visible features, when the real leverage is in quiet ranking discipline. The design guard against that bias is to prefer **additive, explainable, falsifiable layers** over wholesale score rewrites; keep spoon’s current core, and force every imported idea to justify itself either as a cheaper explanation, a better gate, or a genuinely new signal. **Overall confidence level: medium-high.** High on spoon’s current architecture and on the portability/risk assessment of Useful Forks, issue-finder, repowise, gitdeck, and git_bayesect; medium on the practical uplift of momentum and topic-lane diversification until spoon-specific benchmarks exist.

**Parking lot**:
- Browser-extension or bookmarklet entry points inspired by Useful Forks.[S19][S23]
- Full local-clone/graph mining in default spoon runs instead of a shortlist-only escalation.[S40][S41]
- Alternative-VCS ideas from Lore or copy-on-write worktree tooling from Rift — interesting, but not central to fork ranking.[S64][S65]
- Agent-gating / PR automation ideas from `flock` and `no-mistakes`; useful for developer workflow, not the fork-analyzer core.[S62][S63]
- Profile-gamification/UI theatrics from `gitfut`; entertaining, but low leverage for integration-focused fork prospecting.[S66]

