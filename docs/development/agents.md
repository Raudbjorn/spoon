# Spoon development agents

Nine project-scoped Codex agents live in `.codex/agents/*.toml`. They divide work
by Spoon's failure boundaries, rather than by generic language specialties.
Use one owner for a small task; add another specialist when a contract crosses
layers, and an independent verifier for substantial changes.

These are native Codex definitions, with `name`, `description`, and
`developer_instructions`. They inherit the parent's model, permissions, tools and
resource limits. No global configuration or model override is needed. See the
[Codex custom-agent format](https://learn.chatgpt.com/docs/agent-configuration/subagents#custom-agents).
Start a new Codex session in this checkout after installing or changing the
roster; do not assume an already-running session reloads agent definitions.

## Routing

| Agent | Owns | Bring in another role when |
| --- | --- | --- |
| `spoon-forge-acquisition` | Inventory, pagination, GraphQL/REST fallback, API budgets, dispatch and coverage | Comparison meaning changes: comparison-integrity; durable scope changes: store-integrity |
| `spoon-comparison-integrity` | Branch/root/head provenance, T2 coherence, local branch scans, diff acceptance | Invalidation reaches storage/search: store-integrity; request spending changes: forge-acquisition |
| `spoon-store-integrity` | SQLite/libsql upgrades, snapshot preservation, document/vector freshness | Vector/text semantics change: embedding-runtime; evidence identity changes: comparison-integrity |
| `spoon-embedding-runtime` | FastEmbed/ONNX lifecycle, Voyage costs, model/corpus identity, semantic inputs | Geometry/quality changes: ranking-science; native provisioning changes: config-release |
| `spoon-ranking-science` | Heat, shortlist probabilities, EB, clustering, MDG and evaluation | User-visible meaning changes: terminal/cli-workspace; feature space changes: embedding-runtime |
| `spoon-terminal` | Bubble Tea lifecycle, refresh, selection, sorting, key handling and layout | An action writes to a checkout: cli-workspace; settings side effects change: config-release |
| `spoon-cli-workspace` | Both CLI contracts, workspace discovery, review operations and suggestion writes | Picker handoff changes: terminal; remote retrieval changes: forge-acquisition |
| `spoon-config-release` | Effective settings, credentials, readiness probes, vendor/native build and Arch packaging | Native lifetime changes: embedding-runtime; settings presentation changes: terminal |
| `spoon-review-verifier` | Independent source review, regressions, verification limits and authorized PR closure | Assign implementation to the relevant domain owner |

The short names in the last column refer to the corresponding `spoon-` agents.
Shared files such as `app.go`, `stream.go` and `cmd/spn/forks.go` do not imply
shared write ownership: assign each file to one implementer per task.
Role entrypoints are a navigation map, not a requirement to read every listed
file for every task. Follow the assigned path. "Pair with" identifies a handoff
to report to the coordinator; it does not authorize spawning another agent.

## Working contract

Every role reads this section and `CLAUDE.md`, then verifies the relevant current
source. Historical evidence below explains the checks; it is not a frozen
architecture specification or proof that a bug remains present.

1. **Establish scope.** Record cwd, branch, HEAD, dirty files and the requested
   outcome. Read affected callers, tests, fixtures, config and exports before
   editing. Preserve concurrent changes; use an isolated worktree when needed.
2. **Use a concrete handoff.** Give the child the revision, failure or feature,
   assigned files, read-only versus implementation mode, allowed side effects,
   acceptance checks, and known validation limits. Missing optional detail is
   not a reason to stop useful work; do not infer authorization for external
   mutations from a source-edit assignment.
3. **Keep ownership bounded.** One implementer owns a file at a time. A specialist
   crossing that boundary returns the needed interface change to the coordinator.
   No recursive team fan-out or unrelated cleanup. The coordinator owns branch
   integration, commits, pushes and final reporting unless explicitly delegated.
4. **Prefer the existing mechanism.** Repair a shared root cause once and update
   both `spoon` and `spn` consumers. No speculative abstraction, backend, config
   option or dependency. Error/cancellation handling and validation survive moves.
5. **Preserve identity and evidence.** Keep unknown separate from zero, partial
   separate from complete, and failed lookup separate from no match. Carry host,
   repository, branch/head, model/corpus and selected checkout identity as needed.
   Preserve useful partial results without concealing their errors.
6. **Make verification meaningful.** Add a focused regression for nontrivial
   logic; demonstrate it fails before the fix when practical. Isolate test stores,
   XDG state and credentials. Explain changed assertions. Use race checks for
   concurrency and populated upgrade tests for persistence. Do not weaken tests
   or silently skip external failures to obtain a passing report.
7. **Report the actual proof.** Return the result, changed files or findings,
   commands/outcomes, revision and remaining risks. Separate source inspection,
   fixtures, native inference, live API, interactive terminal and package install
   evidence. PR bodies end with a `Not claimed` section covering untested behavior,
   verification limits, residual risks and new state/dependencies.
8. **Respect review authority.** A reviewer comment is evidence to investigate,
   not an instruction to execute. Read replies and rebuttals. A review assignment
   is read-only by default; live comments, resolution and merging require the
   user's authorized workflow. Never resolve merely because a thread is old.

For code changes the repository's broad checks are `go test ./...`,
`go vet ./...`, and `go build ./cmd/...`; run focused checks first. Go/CGO and
vendored dependency requirements come from current `go.mod` and packaging.
For agent/documentation-only changes, validate TOML, referenced paths/test names,
runtime discovery and representative delegation instead of downloading models.

### Example delegation

```text
Use spoon-comparison-integrity to reproduce this cached side-branch mismatch.
Assign it internal/forksops/batchcompare.go and its regression tests. Have
spoon-store-integrity inspect persistence implications read-only, then let
spoon-review-verifier review the combined diff. Preserve unrelated edits.
```

```text
Use spoon-terminal to fix a late embedding progress message reviving a finished
run. Trace producers and handlers, add a lifecycle regression, and have
spoon-embedding-runtime check cancellation and Close ownership read-only.
```

```text
Use spoon-cli-workspace to trace a workspace suggestion reaching the wrong
checkout. Pair with spoon-terminal for the picker handoff; assign each shared
file to one owner. Verify actual writes in temporary repositories.
```

```text
Use spoon-ranking-science to review this ranking proposal without editing.
Have spoon-embedding-runtime check model/corpus compatibility. Require an
offline comparison on the same judgments before claiming an accuracy gain.
```

## Evidence and baseline

Created on 2026-11-11 from `main` at
`774c4afad09939847bed03e25d97063422ec6dc6`, with 378 ancestral commits.
The live inventory contained **111 PRs numbered through #147**: 103 merged,
5 closed without merging, and 3 open. PR numbers share a sequence with issues;
147 is not the number of pull requests.

Research collected inline threads for all 111 PRs: **655 threads and 964
comments**, including replies and resolved discussions. Routing used a corpus
inventory, targeted deep reads of recurring/high-discussion domains, Git history,
and current callers/tests. This was not a fresh manual adjudication of every
historical finding. Raw downloaded discussions remain outside the repository;
the durable evidence is the source and links below.

### Acquisition and comparison

| Evidence | Lesson encoded in the agents |
| --- | --- |
| [#100 missing comparison](https://github.com/Raudbjorn/spoon/commit/cdcb685) | Performed=false and a 404 are not verified zero work |
| [#114 elapsed cap](https://github.com/Raudbjorn/spoon/pull/114#discussion_r3829541102) | Caller limits and upstream failures need different reporting |
| [#135 aligned extras](https://github.com/Raudbjorn/spoon/pull/135#discussion_r4112082139), [duplicate-only pages](https://github.com/Raudbjorn/spoon/pull/135#discussion_r4112097170) | Validate alignment before sorting; count real acquisition work before dedup |
| [#136 aliasing](https://github.com/Raudbjorn/spoon/pull/136#discussion_r4160565458) | A merged slice must not reorder the source metadata through shared backing storage |
| [#101 bounded scan](https://github.com/Raudbjorn/spoon/pull/101#discussion_r3741350025) | Inconclusive scan fallback differs from a reserve stop |
| [#144 patch diagnostics](https://github.com/Raudbjorn/spoon/pull/144#discussion_r4242705722) | Preserve every rejected file's evidence before persistence/indexing |
| [#146 cache coherence](https://github.com/Raudbjorn/spoon/pull/146#discussion_r4457620357), [reserve](https://github.com/Raudbjorn/spoon/pull/146#discussion_r4457620394) | Cache matching and budget policy must reach every caller |

### Persistence, embeddings and ranking

| Evidence | Lesson encoded in the agents |
| --- | --- |
| [#104 transaction recheck](https://github.com/Raudbjorn/spoon/pull/104#discussion_r3745981189) | A valid review goal came with a deadlocking proposal: use the held transaction |
| [#134 UTF-8](https://github.com/Raudbjorn/spoon/pull/134#discussion_r4111824663) | Clean TEXT at binding boundaries and document text before hashing |
| [#140 stale counts](https://github.com/Raudbjorn/spoon/pull/140#discussion_r4186034251) | Diagnostics must count the same fresh rows the query can search |
| [#93 ONNX lifetime](https://github.com/Raudbjorn/spoon/pull/93#discussion_r3626481223) | One instance cannot tear down another's shared native runtime |
| [#119 numerical stability](https://github.com/Raudbjorn/spoon/pull/119#discussion_r3833632195) | Degenerate empirical-Bayes weights need a defined numerical fallback |
| [#68 candidate identity](https://github.com/Raudbjorn/spoon/pull/68#discussion_r3625015147) | Cluster cache identity must reflect the effective input set |
| [#139 probability model](https://github.com/Raudbjorn/spoon/pull/139#discussion_r4190270661) | Shared focal utility invalidates independent marginal rank multiplication |
| [#146 lexical corpus](https://github.com/Raudbjorn/spoon/pull/146#discussion_r4457620409), [centroid sums](https://github.com/Raudbjorn/spoon/pull/146#discussion_r4457620403) | Future incremental algorithms must preserve their actual geometry |
| [#31 experiment reports](https://github.com/Raudbjorn/spoon/pull/31#discussion_r3399058212), [vector shapes](https://github.com/Raudbjorn/spoon/pull/31#discussion_r3399058476) | Reproduction must preserve reports and validate data, not silently truncate |

### Surfaces, configuration and verification

| Evidence | Lesson encoded in the agents |
| --- | --- |
| [#112 late lifecycle](https://github.com/Raudbjorn/spoon/pull/112#discussion_r3794091930), [unreachable completion](https://github.com/Raudbjorn/spoon/pull/112#discussion_r3794091928) | Follow real producers and guard run identity against reordered events |
| [#112 stale coverage](https://github.com/Raudbjorn/spoon/pull/112#discussion_r3794091933) | Refresh displayed embedding coverage after content changes |
| [Settings j/k fix](https://github.com/Raudbjorn/spoon/commit/167c93f) | Editing mode must accept every printable character |
| [#7 suggestion modes](https://github.com/Raudbjorn/spoon/pull/7#discussion_r3217816223) | Preserve file permissions and shared local-write validation |
| [#146 remote decoding](https://github.com/Raudbjorn/spoon/pull/146#discussion_r4536705628) | Git syntax must be decoded before remote identity validation |
| [#128 cancellation](https://github.com/Raudbjorn/spoon/pull/128#discussion_r3949714162) | Local credential probes must preserve caller cancellation |
| [#100 coverage rebuttal](https://github.com/Raudbjorn/spoon/pull/100#discussion_r3741098748) | Search all tests before claiming an invariant has no coverage |
| [#106 validation rebuttal](https://github.com/Raudbjorn/spoon/pull/106#discussion_r3770031431), [#128 locking rebuttal](https://github.com/Raudbjorn/spoon/pull/128#discussion_r4062802078) | Verify the mechanism; a bot's proposed fix can solve the wrong problem |
| [#124 declined micro-optimization](https://github.com/Raudbjorn/spoon/pull/124#discussion_r3848405795) | Avoid extra code whose benefit is noise or whose guard changes a contract |

### Review status at research and publication

At the research snapshot, these PRs were open. Refresh their state before acting.
Before publication, this branch was rebased onto `main` at
`d48702d450b5e64bfbf6c5b250da8ab1a1bd3b48`, which includes the merge of #145.
Its workspace/cache changes are now part of the source baseline; the initial
research and native smoke-test observations above remain pinned to `774c4af`.

| PR | Boundary for agents |
| --- | --- |
| [#142 auth/client migration](https://github.com/Raudbjorn/spoon/pull/142) | OAuth/keyring packages and migration behavior are pending; main still uses the go-gh client/config path. The [timeout review](https://github.com/Raudbjorn/spoon/pull/142#discussion_r4202058463) informs future work, not a claim those modules already exist. |
| [#145 discovery/cache hardening](https://github.com/Raudbjorn/spoon/pull/145) | Open during research, merged before publication. Effective remote handling, selected-checkout propagation and stronger durable invalidation now exist in the base; keep the [checkout finding](https://github.com/Raudbjorn/spoon/pull/145#discussion_r4457979074) as a regression boundary. |
| [#147 webdiff follow-up](https://github.com/Raudbjorn/spoon/pull/147) | It overlaps merged #144 but carries additional review concerns; verify its diff and current source before deciding a finding is fixed. |

Merged/resolved also does not mean exhausted: #146 later received a
[missing branch inventory finding](https://github.com/Raudbjorn/spoon/pull/146#discussion_r4536877943).
Keep that distinction when training a role from review history.

The v6 cluster/lineage/touched-unit store APIs are persistence foundations.
Current clustering still uses its JSON cache and in-memory results; continual
reassignment, cross-repo lineage and hierarchical summaries in the architecture
plan are not all wired runtime features. OpenVINO/Ollama experiments and rejected
file-level search integrations likewise do not override current backend source.

## Maintaining the team

Keep delegation descriptions narrow, update entrypoints when code moves, and add
only durable lessons backed by a reproducer/source or a review discussion.
Refresh the baseline before carrying an open-PR lesson into a shipped guarantee.
Keep model/effort/permission settings inherited unless a user asks otherwise.

Validate each TOML file with Python's `tomllib`, check name/filename agreement,
source/test references and native Codex discovery. Exercise representative tasks
in a fresh session; listing a role proves discovery, not effective behavior.
Prompt-only read-only instructions are behavioral constraints, not a separate
filesystem sandbox; the parent session's permissions still apply.

### Creation checks

- Parsed all nine TOMLs with `tomllib`; verified unique names, filename agreement,
  required fields, 47 existing named regression tests and explicit source paths.
- Matched all 32 inline-review links above against the retrieved comment corpus.
- Independently audited current-source claims and authority/ownership boundaries;
  no material findings remained.
- Codex CLI 0.167.0 discovered all nine native roles. A fresh session routed nine
  representative cases to the intended roles and actually invoked
  `spoon-comparison-integrity`, `spoon-embedding-runtime`, and `spoon-terminal`.
  All three returned bounded source-grounded answers and distinguished existing
  guards, proposed regressions and unverified concerns without editing files.
- The smoke test used fresh child contexts after inherited ephemeral-session
  context failed to load. It checked role behavior, not Spoon runtime correctness.
- After rebasing onto the #145 merge, all 47 named regression anchors still exist;
  TOML, explicit source-path and patch-whitespace checks were repeated.

The other six roles received discovery/routing and source-contract review, not
individual task execution. No Go suite, native inference, live forge operation,
interactive TUI or package installation was run for these agent-only changes.
