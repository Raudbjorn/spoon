# Native agent diagnosis pilot - 2026-11-11

**Both conditions answered all six cases correctly: no missed defects and no false findings.** This demonstrates bounded diagnosis correctness in this pilot, not a specialist advantage. Keep the roster provisional; this small, ceiling-limited result does not justify pruning roles or claiming productivity improvements.

## Protocol and effective settings

Six scenarios each received two fresh native children: `default` and the assigned specialist. One Codex CLI orchestrator ran at most two children concurrently, finishing each pair before starting the next. Spawn order alternated between control-first and specialist-first. All spawns used `fork_turns="none"`; native role discovery and actual invocation succeeded without fallback or retry.

The role baseline was `40db84fbaaf0c172cdd46c5a384bde809df47a23`. All nine TOML hashes still matched the frozen manifest after the run. A later review cleanup expanded abbreviated navigation paths in four roles; this pilot predates that wording-only change. C1/C2/T1 children observed checkout `1e63f7c`; T2/R1/R2 observed `4f5e325`. These intervening commits changed the coordinator navigation/guide wording only. Production source, case files and native role definitions were unchanged; both conditions within each pair observed the same checkout revision. The checkout was clean for every child.

Native rollout metadata verified **`gpt-6-astra`, `xhigh`, read-only sandbox** for all 12 children. No model or effort override was supplied. Both conditions received the same case, instructions and permitted shell access. Each read `CLAUDE.md`, the working contract and its case; no child read the answer key or another trial. Each actually used one shell call through `exec` and returned 135-192 words, within the common two-read-call/220-word limit. These were cooperative task limits, not identical token usage or a hard reasoning-token cap. Unused connector capabilities were not exercised.

The task explicitly prohibited implementation, tests, downloads, paid provider work, subagents and external publication. These were private controlled diagnosis exercises, **not formal reviews of a live PR**.

## Cases and frozen expectations

Each child had to return `case_id`, a boolean `defect`, mechanism, consequence, correction, regression and proof limit. `defect=true` meant a real defect in the supplied scenario, even when the reviewer's claim asserted safety. Expected labels and reasoning were recorded before launching any child. Historical snippets were diagnosed at their named revision; new variants were explicitly hypothetical and were not reported as present-day production bugs.

| Case | Kind and primary role | Reproducible input and expected diagnosis | Default | Specialist |
| --- | --- | --- | --- | --- |
| C1 | Historical; `spoon-comparison-integrity` | `e74dcfe:internal/forksops/stream.go`: caller-owned cached T2 has head `old`, ahead 3, behind 1, old diffs/commits. Complete selection has head `new`, ahead 3, behind 8. Reuse condition is equal head **OR** equal ahead; REST runs only when T2 is nil. **Defect:** equal counts retain old material, mix in new behind and mutate the shared cache. Invalidate/refetch changed identity; copy before a valid scalar refresh. | Correct | Correct |
| C2 | New variant; `spoon-comparison-integrity` | Adapted from `498c953:internal/store/store.go`: stale scalar refresh expects `(fork=f,pushed=p,head=H1,branch=main,base=B1,ahead=3)`, behind 9. A newer transaction stored H2/B2/ahead 4 and new material. UPDATE changes only JSON BehindCount and matches every old tuple field; no other tables/timestamps are written and full replacement is unreachable. **No defect:** zero affected rows preserve the newer comparison and material. | Correct | Correct |
| T1 | Historical; `spoon-terminal` | Pre-`ca00cb8`, `internal/tui/app.go` and `embedrun.go`: batched start announcement and worker may complete out of order. Started sets running true and schedules a pump; Progress schedules another; Done clears running/note. **Defect:** Done -> Started -> Progress resurrects finished work. Activate synchronously before launching; guard obsolete lifecycle messages while accepting a genuine new start. | Correct | Correct |
| T2 | New variant; `spoon-terminal` | Hypothetical coverage handler inspired by `498c953:internal/tui/app.go`: selecting a repo clears coverage and launches a captured-repo request; replies carry `repo` and values, but Update assigns values unconditionally. Select A, select B, receive B `{7:100}`, then A `{7:0}`. **Defect:** B displays A's coverage despite serialized writes. Reject a reply whose repo differs from the selection; assert B remains at 100. | Correct | Correct |
| R1 | Historical; `spoon-review-verifier` | PR106, `discussion_r3769795549`; `c83b8fc^:internal/embed/{multimodal,fastembed}.go`. Expected-dimension zero vector passes finite checks; normalization returns immediately when sum of squares is zero; validation subsequently returns `errZero`. **No defect:** the alleged divide-by-zero/NaN bypass never occurs. Reordering validation is optional clarity, not a required fix. | Correct | Correct |
| R2 | New variant; `spoon-review-verifier` | Self-contained same-package Go fixture: `classify(404)` returns `"missing"`, status >=500 returns `"retry"`, otherwise `"ok"`. `request_test.go` covers 200. Untagged `request_regression_test.go` also runs and asserts `classify(404)=="missing"`. **No defect:** the claimed missing 404 regression exists in the sibling file; no duplicate test is needed. Presence is source evidence, not an observed passing test run. | Correct | Correct |

Classification was scored against the frozen key. Manual source-grounded checking also found the explanations consistent with the supplied triggers and outcomes; both conditions proposed discriminating assertions and respected the false-alarm cases. This was not blinded grading.

| Observed measure across six trials | Default | Specialists |
| --- | ---: | ---: |
| Correct classifications | 6/6 | 6/6 |
| Missed defects | 0 | 0 |
| False findings | 0 | 0 |
| Shell calls | 6 | 6 |
| Reported input tokens | 433,323 | 442,959 |
| Reported cached input tokens | 372,096 | 359,552 |
| Reported output tokens, including reasoning | 3,638 | 4,391 |

Token totals come from child rollout usage, exclude the orchestrator and include ambient context and repeated input. Cache reuse differed. They are observations from one run, not normalized cost or speed estimates; no latency or monetary advantage is claimed.

## Replay material and limits

Local artifacts are in `/tmp/spoon-agent-eval-20261111/`: `manifest.json`, six case files, `expected.json`, `orchestrator-prompt.txt`, `events.jsonl`, `result.json`, `observed-trials.json` and `grades.json`. The frozen expected-key SHA256, rechecked after trials, is `1d3deddc0f5c22a6d99663faab4899787aca036dded433b3a9272aa54db13d51`. Exact child IDs, effective settings, read commands, answers and usage are retained in the observed-trials artifact. Temporary artifacts are not a durable repository fixture; the table preserves the logical scenarios, while byte-identical replay requires retaining that bundle.

The launch used the installed Codex CLI 0.167.0 and existing account configuration, without selecting a model:

```sh
codex exec -C /home/svnbjrn/dev/functionality-overhaul -s read-only \
  -c agents.max_threads=3 --json --color never \
  -o /tmp/spoon-agent-eval-20261111/result.json - \
  < /tmp/spoon-agent-eval-20261111/orchestrator-prompt.txt \
  > /tmp/spoon-agent-eval-20261111/events.jsonl \
  2> /tmp/spoon-agent-eval-20261111/stderr.log
```

Handpicked small snippets, strong task boundaries, known historical patterns and one sample per condition limit generalization. New variants test transfer beyond the exact history anchors, but remain deliberately small fixtures. The pilot tests neither broad independent discovery nor large-codebase navigation. The specialist instructions contain related invariants, and the default condition also receives project guidance.

No production code was changed or tests run. Patch quality, runtime/TUI behavior, populated database upgrades, native inference, live APIs, integration work, review publication and long-running resource behavior remain unproven. `spoon-forge-acquisition`, `spoon-store-integrity`, `spoon-embedding-runtime`, `spoon-ranking-science`, `spoon-cli-workspace` and `spoon-config-release` were not evaluated as primary specialists. No broader rerun was performed.
