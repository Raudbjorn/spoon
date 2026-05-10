# Future Work: Hierarchical Fork Summarization

**Status:** Deferred from v1; depends on the LLM `Labeler` hook landing first.
**Parent plan:** `plan-integrating-this-research-declarative-puddle.md`
**Estimated effort:** 1–2 weeks (mostly prompt engineering + integration; LLM client reuses the `Labeler` hook).

## Context

V1 of spoon's clustering produces cluster *labels* via heuristic TF-IDF over directories and commit-message tokens, with an optional LLM polishing pass. That gives the user a short phrase per cluster ("plugins/  ·  oauth, provider, scope") but no narrative.

When a user runs `spoon` on a 600-fork repo, they currently get: a heat-ranked table + an optional cluster grouping. They lack what a colleague would actually say if asked "so what's happening in this repo's fork ecosystem?" — a paragraph-level summary that explains the landscape, calls out interesting clusters by intent, and surfaces forks worth examining.

Hierarchical Fork Summarization fills that gap with three levels: per-fork blurb → per-cluster summary → per-repo fork-landscape summary.

## Origin of the idea

The user provided Dhulshette et al. (TCS 2025), "Hierarchical Repository-Level Code Summarization for Business Applications Using Local LLMs" as research input. The paper makes two contributions that map cleanly onto spoon's needs:

1. **Decompose-then-aggregate** beats trying to summarize a huge artifact in one shot. They decompose Java files via AST into class/function units, summarize each via a small local LLM, then aggregate to file → package → repo. The key observation: local LLMs (Llama3.2 128K) drop information when summarizing entire files, but handle smaller units faithfully.
2. **Business-context grounding** in the prompt: include the *domain* of the project (e.g., "telecommunications BSS") and the *problem context* so the summary reflects intent, not just mechanics.

The spoon analogy: instead of one giant prompt asking "describe these 600 forks," decompose into fork-level summaries, then cluster-level, then repo-level. Each level grounded in upstream context.

## Research basis

**Dhulshette, Shah & Kulkarni, "Hierarchical Repository-Level Code Summarization for Business Applications Using Local LLMs" (arXiv:2501.07857v1, 2025).** Direct precedent. Two-step pipeline: AST parser splits each file into segments; per-segment summarizer (local LLM with custom prompts per segment type); aggregator generates file → package summaries. Empirically improves coverage and relevance over flat one-shot summarization. The paper specifically uses Llama3.2 via local serving — same model class spoon can hit via Ollama.

**Generalized Category Discovery with Large Language Models in the Loop (arXiv:2312.10897v1, 2023).** The "Loop" framework uses LLMs to provide semantic labels for discovered clusters. Demonstrates that LLM-in-the-loop labeling reduces human-labeling cost and improves interpretability. Spoon's per-cluster summary is the same act at a higher altitude.

**RepoMaster (Wang et al., NeurIPS 2025).** Builds a hierarchical code tree (HCT), function call graph (FCG), and module dependency graph (MDG), then uses an LLM to explore. The hierarchical context approach is a strong design pattern: each level of the hierarchy gets context-windowed inputs that fit.

## Implementation sketch

Three new summarizers in `internal/cluster/summarize.go`, all calling through the existing `Labeler` interface (extend it to a richer `Summarizer`):

```go
package cluster

// Summarizer is the extended Labeler interface for hierarchical summarization.
// Same transport (HTTP to Ollama chat endpoint); different prompts.
type Summarizer interface {
    SummarizeFork(ctx context.Context, fc ForkContext) (string, error)
    SummarizeCluster(ctx context.Context, cc ClusterContext) (string, error)
    SummarizeRepo(ctx context.Context, rc RepoContext) (string, error)
}

type ForkContext struct {
    Fork         string                  // "alice/myfork"
    Features     embed.ForkFeatures      // paths, commits, readme delta, diff chunk
    Heat         heat.HeatResult         // for context: score, components
    NoveltyScore float64
    ClusterLabel string                  // heuristic; for grounding
}

type ClusterContext struct {
    ClusterID         string
    Label             string                  // heuristic label
    ForkSummaries     []string                // per-fork summaries from earlier stage
    UpstreamRepo      string
    UpstreamCoreDirs  []string
}

type RepoContext struct {
    Upstream         string
    UpstreamDesc     string
    UpstreamReadme   string                   // truncated
    ClusterSummaries []ClusterSummary
    Stats            RepoStats                // counts: total forks, novel, mirrors, etc.
}
```

### Pipeline placement

Optional pass after clustering:

1. **Stage A (per-fork).** Loop over top-N forks (N = `--summary-top`, default 20). Each gets a 1-sentence summary. Skip if no `Summarizer` configured or if `--no-summarize` is set.
2. **Stage B (per-cluster).** Aggregate Stage A outputs by cluster. Pass to `SummarizeCluster` with upstream context. Each cluster gets a 2-3 sentence paragraph.
3. **Stage C (per-repo).** Aggregate Stage B + repo stats. Single 4-6 sentence paragraph describing the fork landscape.

### Surfacing

- **TUI.** New `s` keybinding toggles summary mode. Detail view shows per-fork summary at top. Cluster header row shows per-cluster paragraph. New "Overview" screen (key `o`) shows the repo-level paragraph + key stats.
- **`spn forks list`.** Adds `summary` field per fork record when summarization is enabled.
- **New JSON output mode.** `spn report <repo>` produces a structured summary blob: `{upstream: ..., summary: "...", clusters: [{id, label, summary, ...}], stats: {...}}`. Useful for agents that want the narrative without all the per-fork detail.

### Prompts (per Dhulshette et al.)

Each prompt template carries upstream context for grounding:

> "You are reviewing a fork of `{UpstreamRepo}`. Upstream description: `{UpstreamDesc}`. Upstream's core directories include `{UpstreamCoreDirs}`.
>
> The fork made these changes: `{features}`. Its heat score is `{heat}` (T1: ..., T2: ..., T3: ...). The fork was clustered as: `{clusterLabel}`.
>
> Write one sentence describing what this fork is doing and why it might be interesting. Be concrete. Avoid filler words. If the fork looks like noise (sync mirror, fork farm, trivial tweak), say so."

Per-cluster and per-repo prompts follow the same pattern: ground in upstream, decompose into specifics, ask for narrative.

## Cost analysis

For a 600-fork repo with default settings:
- 20 fork summaries × ~300 tokens prompt + 50 tokens response = ~7K tokens
- 6 cluster summaries × ~400 tokens prompt + 100 tokens response = ~3K tokens
- 1 repo summary × ~1500 tokens prompt + 200 tokens response = ~1.7K tokens
- **Total ~12K tokens** through a local Llama 3.2 — about 30-60 seconds on a modest GPU, longer on CPU.

Optional gated to top-N forks by default (`--summary-top`) so cost doesn't scale with total fork count.

## Acceptance criteria

1. **Stages run in order.** Per-fork summaries available before per-cluster. Per-cluster before per-repo. Any stage can fail independently without poisoning others (output `summary=null` and continue).
2. **Faithful grounding.** Manual review of 10 cluster summaries on IBM/mcp-context-forge — at least 8/10 should correctly identify the cluster's actual archetype (judged by a human comparing against the underlying member forks).
3. **No hallucinated forks.** Per-cluster summaries should only name forks that are actually in the cluster. (LLMs sometimes invent contributors / repo names; test for this explicitly with a name-presence checker.)
4. **Graceful degradation.** If the chat model is unreachable, the run still completes; summaries are simply absent. Heuristic labels (from v1) remain.
5. **Token budget respected.** No single prompt exceeds 4K tokens. Truncate aggressively: per-fork summaries are 1 sentence; cluster summaries include at most 5 representative fork summaries.

## Risks and open questions

- **LLM hallucinations.** Especially around fork ownership, commit attribution, and license claims. Mitigations: prompt explicitly forbids these claims, post-output validator checks for forks/handles not in the cluster.
- **Stale model.** Local LLMs lag the world. If a user analyzes a brand-new framework, the model may give a poor cluster summary. Acceptable — the heuristic label always shows alongside.
- **Local LLM availability.** Many users will have an embedder set up (for the v1 path) but not a chat model. Document the separate `--summarizer URL` and `--summarizer-model M` flags; auto-detect both at bootstrap.
- **Adversarial prompt injection** via fork READMEs/commit messages embedded in the prompt. Mitigation: strict input sanitation (strip prompt-control-token sequences, length cap, content quoted as data not instruction).

## How to complete

1. **Validate Llama-class models locally.** Pull `llama3.2:3b` and `qwen2.5-coder:7b`. Hand-eval on a few clusters from IBM/mcp-context-forge: which produces more faithful summaries?
2. **Implement `Summarizer` interface** as an extension of `Labeler`. Reuse the HTTP client.
3. **Build prompt templates** in `internal/cluster/prompts/`. One file per stage. Templates with `{{}}` placeholders for clarity.
4. **Add Stage A → B → C orchestration** in `internal/cluster/summarize.go`. Stage failures don't propagate (each stage tries independently with empty inputs for missing pieces).
5. **Add the `spn report` subcommand** for the agent-shaped narrative output.
6. **Add TUI summary view** with `s` (per-fork) and `o` (overview) bindings.
7. **Add hallucination check** as a post-output validator: every named fork must appear in cluster membership; every commit hash must appear in the fork's commit set.
8. **Hand-eval gate.** Run on three real repos (different sizes / domains). At least 80% of cluster summaries must be judged "faithful and useful" by a human reviewer before merging.

## References

- Dhulshette, N., Shah, S., & Kulkarni, V. (2025). *Hierarchical Repository-Level Code Summarization for Business Applications Using Local LLMs.* arXiv:2501.07857v1.
- *Generalized Category Discovery with Large Language Models in the Loop.* arXiv:2312.10897v1, 2023.
- Wang, H., Ni, Z., Zhang, S., et al. (NeurIPS 2025). *RepoMaster: Autonomous Exploration and Understanding of GitHub Repositories for Complex Task Solving.*
