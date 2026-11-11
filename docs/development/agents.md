# Spoon development agents

Nine project-scoped Codex agents live in `.codex/agents/*.toml`. They divide work
by Spoon's failure boundaries, rather than by generic language specialties.
Use one owner for a small task; add another specialist when a contract crosses
layers, and an independent verifier for substantial changes.

These native Codex definitions contain only `name`, `description`, and
`developer_instructions`. They introduce no model, permission, tool or resource
overrides; existing Codex agent defaults and explicit spawn settings still apply.
No global configuration change is needed. See the
[Codex custom-agent format](https://learn.chatgpt.com/docs/agent-configuration/subagents#custom-agents).
Start a new Codex session in this checkout after installing or changing the
roster; do not assume an already-running session reloads agent definitions.

## Design goals

Correctness is the primary objective across fixes and new features; delivery
speed and token cost are secondary. A specialist needs recurring work with
distinct domain judgment. Package boundaries alone do not justify a role;
improve documentation, tools and tests for navigation and mechanical checks.

The nine-role roster is provisional. Retention requires a distinct responsibility
and demonstrated benefit on representative tasks compared with an ordinary
agent. Merge or retire roles that do not justify their cost. The reporting and
review contracts below were agreed with the maintainer on 2026-11-11.

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
| `spoon-review-verifier` | Independent source review, fix verification and adjudication of assigned unhandled concerns | Always assign implementation to a different domain owner |

The short names in the last column refer to the corresponding `spoon-` agents.
Shared files such as `internal/tui/app.go`, `internal/forksops/stream.go` and `cmd/spn/forks.go` do not imply
shared write ownership: assign each file to one implementer per task.
Role entrypoints are a navigation map, not a requirement to read every listed
file for every task. Follow the assigned path. "Pair with" identifies a handoff
to report to the coordinator; it does not authorize spawning another agent.

## Working contract

The common starting instructions live in `AGENTS.md`; do not duplicate them in
each role or load `CLAUDE.md` for the same guidance. Read the relevant procedures
below, then verify current source. Historical evidence explains the checks; it
is not a frozen architecture specification or proof that a bug remains present.

1. **Establish scope.** Record cwd, branch, HEAD, dirty files and the requested
   outcome. Read affected callers, tests, fixtures, config and exports before
   editing. Preserve concurrent changes; use an isolated worktree when needed.
2. **Use a concrete handoff.** Give the child the revision, failure or feature,
   assigned files, read-only versus implementation mode, allowed side effects,
   acceptance checks, and known validation limits. Include the target PR for
   code review. Missing optional detail should not stop useful work. Source-edit
   assignments do not authorize unrelated external mutations; the reporting and
   review publication permissions below are explicit exceptions.
3. **Keep ownership bounded.** One implementer owns a file at a time. A specialist
   may always request a broader mandate; a request does not expand it by itself.
   Where the runtime supports mid-task messages, continue independent work while
   the coordinator decides within the user's existing authorization. Otherwise
   return the request and the remaining scope; never assume approval. Report work
   you cannot or will not handle through the reporting contract below. No
   recursive team fan-out or unrelated cleanup.
   The coordinator owns ordinary implementation acceptance, branch integration,
   commits, pushes and final reporting unless explicitly delegated.
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
   leaves source unchanged and follows the independent PR review contract below.
   Reporting or reviewing does not authorize implementation or merging. Never
   resolve merely because a thread is old.

For code changes the repository's broad checks are `go test ./...`,
`go vet ./...`, and `go build ./cmd/...`; run focused checks first. Go/CGO and
vendored dependency requirements come from current `go.mod` and packaging.
For agent/documentation-only changes, validate TOML, referenced paths/test names,
runtime discovery and representative delegation instead of downloading models.
Run `python3 scripts/check-agent-anchors.py` and
`python3 -m unittest discover -s scripts -p 'test_check_agent_anchors.py'` for the
static checks. They require Python 3.11+, not third-party packages, and do not
prove semantic coverage or enforce review dispatch. No CI job currently runs them.

## Reporting unhandled work

Use this path when an agent cannot or will not handle a concern itself, including
minor, tangential or out-of-scope work. It is not a second approval process for
ordinary work the agent completes within its assignment.

- **Code-review assignment:** put every review comment on its target PR, including
  tangential concerns. Search existing threads and PR comments, including resolved
  discussions, before posting. Reply with new evidence to an existing finding;
  do not open another thread for the same cause. Use inline threads for actionable
  defects and one comment per review pass for tangential observations/questions.
  Batching changes presentation, not which concerns are recorded. Do not create
  issues for those review comments. If the target PR is missing, request it from
  the coordinator.
- **Other assignments:** search existing repository issues, including closed ones,
  before opening a new issue or appending evidence to a matching one. Publish
  directly without parent approval. Include the reporting role, revision,
  observation/evidence, uncertainty, why it was not handled and any blocked task.
  Preserve original reports; add corrections and rebuttals as comments.

Use these labels on issues, alongside existing types such as `bug`, `question`
and `documentation`:

| Label | Meaning |
| --- | --- |
| `agent-reported` | An agent reported work it could not or would not handle |
| `needs-review` | Independent examination is pending |
| `blocks-task` | A named task cannot correctly complete until this is addressed |

Send the parent the issue or PR-comment URL and request independent review.
On receipt, the coordinator assigns a qualified reviewer who did not originate
the unhandled report or implement its fix. If no reviewer is available, notify
the maintainer and retain the URL and pending status in the final handoff. A
verifier's own new unhandled report follows the same rule: another qualified
specialist or the maintainer examines it. Adjudicating an existing report does
not create another report merely because the reviewer reaches a conclusion.
Labels do not dispatch a reviewer or grant implementation authority. Continue
unrelated work; pause only work whose correctness depends on the concern.
Agents should consult relevant issues and PR discussions when taking over work;
visibility does not depend on the parent forwarding a summary.

For these unhandled concerns, the independent reviewer decides validity and
resolution, records evidence at the original destination, and updates labels or
closes the issue/resolves the thread accordingly. Valid deferred concerns remain
open. Preserve disagreement and reopen on new evidence. The parent coordinates
implementation but cannot suppress, rewrite or override that adjudication.
**This allocation applies only to work an agent cannot or will not handle itself,
not to all work or ordinary implementation acceptance.**
Completing a normal PR review does not require another review of that review;
its reviewer may verify fixes to its ordinary findings. Separately reported
unhandled concerns follow the independent-adjudicator rule above.

Escalate unresolved material disputes, including in-scope PR defects, to the
maintainer with both positions and evidence at the original destination. The
maintainer is the final tie-breaker; the coordinator cannot declare a disputed
finding resolved on its own. Preserve the original finding and any maintainer
decision as separate public records.

Spoon's repository is public: exclude credentials and private evidence while
preserving the concern. If publication fails, return the unpublished report and
error explicitly so it can be retried; do not claim a shared record exists.

## Independent PR review

Require independent PR review for changes to persistence/migrations,
concurrency/lifecycle, security or mutation boundaries, numerical semantics,
or behavior shared by `spoon` and `spn`. The coordinator may request review for
other changes; routine low-impact work does not require an extra reviewer.
The implementer cannot serve as that change's independent reviewer. At handoff,
the coordinator records which trigger applies, the reviewer and reviewed SHA;
if none applies, state why. The review remains pending until a report covers the
relevant revision. This is an operating obligation, not a path-based CI gate or
a guarantee that a model will dispatch a reviewer.

Every specialist assigned code review publishes all review comments directly on
the target PR, without parent approval, rewriting, suppression or override. The
reviewer leaves source unchanged, pins the reviewed revision, and distinguishes
confirmed defects from questions and documented tradeoffs. Form initial findings
from the base/head diff, current callers, tests and requirements before reading
the coordinator's explanation; then consider rebuttals and missing context.
If prior narrative was already inherited, disclose it rather than claiming a
clean context. Each finding states severity (critical, major or minor) or marks
itself as a question, with its trigger, consequence, evidence and uncertainty.
Use the deduplication and batching rules above. The coordinator may
supply context and respond publicly with evidence; the reviewer may reconsider
on that evidence. This publication independence is separate from the narrower
adjudication rule for unhandled concerns above.

Return links, the reviewed SHA, verification outcomes and limitations to the
coordinator. A source/diagnostic exercise explicitly assigned as a private test
is not a live PR review; keep evaluation fixtures off GitHub. These are operating
rules using inherited tools and credentials, not technical isolation between
parent and child. Existing permission to publish reports does not authorize a
merge, paid services, downloads or unrelated account changes.

`spoon-review-verifier` never implements fixes, commits, pushes, merges or closes
PRs. A coordinator handling an authorized PR-fix workflow delegates implementation
to a domain owner and verification to a different reviewer. Resolving a verified
review thread under the rules above is distinct from closing or merging a PR.
Custom role files cannot override the parent's sandbox/approval settings in the
current Codex format. A different model also does not establish independent
judgment; clean context, external triggering and credential separation are
different controls, none demonstrated by the role definitions alone.

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
Keep existing effective model/effort/permission settings unless a user asks otherwise.

Validate each TOML file with Python's `tomllib`, check name/filename agreement,
source/test references and native Codex discovery. Exercise representative tasks
in a fresh session; listing a role proves discovery, not effective behavior.
Prompt-only read-only instructions are behavioral constraints, not a separate
filesystem sandbox; the parent session's permissions still apply.

### Behavioral evaluation

Start with comparison-to-persistence coherence, asynchronous TUI state and
review adjudication. Use a historical case and a new variant for each, comparing
ordinary agents with specialists on the same model, tools and comparable budgets.
For the next comparison, give the ordinary agent the same domain instructions
as documentation; otherwise the experiment confounds useful knowledge with role
packaging. Keep the evaluated revision fixed within each pair. Select new cases
independently of prompt design and freeze grading before runs; grade without
knowing the condition where practical. Reused history is a regression set, not
held-out evidence. Newly written variants of known failures are not independent
selection either. Compare model choice separately from external review triggering.
Freeze expected outcomes before trials. Judge missed bugs and false findings
first; report source diagnosis separately from patch quality or runtime proof.
Evaluate the remaining domains before deciding their roles' future. Discovery
and successful invocation alone do not establish a benefit over ordinary agents.

The [2026-11-11 diagnosis pilot](agent-evaluation.md) scored both conditions
6/6 across three defects and three false alarms. It established no specialist
advantage; retain the roster provisionally and keep the report's limits explicit.

The maintainer's next roster decision is due **2026-12-11**, or before adding
another role, whichever comes first. For each role, record recurring use and
paired held-out results. Recommend retention only with correctness benefits on
at least two independently selected tasks that the generic agent with the same
guidance missed, and no increase in false findings on the negative controls.
Recommend merging into shared guidance or retirement for unused roles or those
without that evidence. This is a practical retention threshold, not statistical
proof; the maintainer decides changes explicitly. No automatic deletion, new
model selection or evaluation spending is authorized by the date.

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
