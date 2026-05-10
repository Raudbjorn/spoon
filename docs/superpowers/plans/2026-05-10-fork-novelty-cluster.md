# Plan: Integrate GCD research into a fork novelty + cluster subsystem

**Date:** 2026-05-10
**Status:** plan (pending approval) — supersedes parts of `docs/superpowers/plans/2026-04-28-fork-quality-signals.md`

## Context

Spoon currently scores forks individually via a tiered heat pipeline (T1/T2/T3 → additive components → trust multiplier → penalties). Each fork is evaluated in isolation: no cross-fork comparison beyond the planned (but unimplemented) fork-farm statistical-fingerprint grouping and regex-based content-archetype classification.

Field analysis (IBM/mcp-context-forge, 623 forks) showed the limit of per-fork scoring: ~25% are fork farms posting identical diffs, ~30% are upstream mirrors, and a long tail of small genuine-work forks falls into recognizable archetypes (plugin, security, docs, migration, etc.). The original plan tackles this with hand-rolled detectors. The Generalized Category Discovery (GCD) research the user provided argues for a unified treatment: cluster the fork set and score each fork's novelty relative to its peers — using multi-modal signals (paths, messages, readmes, diff text) and embeddings to capture semantic structure that regex and statistical fingerprints miss.

The intended outcome: a single "fork novelty + cluster" subsystem that (a) replaces the planned fork-farm and content-archetype detectors with embedding-driven clustering, (b) emits a per-fork novelty score that flows into the existing heat pipeline as a new T3 component, and (c) surfaces cluster groupings to humans (TUI) and agents (`spn forks list` NDJSON). The system stays self-contained: a pluggable local-embedder interface (Ollama by default), heuristic cluster labels, with an optional LLM-labeling hook for users who want polished phrases.

## Scope decisions (from brainstorming)

| Decision | Choice |
| --- | --- |
| Subsystem shape | Combined: per-fork novelty score + cluster-discovery layer |
| Cost / dependency budget | Local embedding model; no API key required at runtime |
| Embedder deployment | Pluggable `Embedder` interface; Ollama HTTP client is the default |
| Embedding inputs | All four modalities: file paths/dirs, commit messages, README/description, chunked diff text |
| Output surfaces | Heat-score Component (T3); TUI cluster view; `spn forks list` NDJSON fields |
| Relation to fork-quality-signals plan | Supersedes the planned `forkfarm.go` and `content_archetype.go`; keeps `upstream_mirror.go` |
| Cluster labels | Heuristic (TF-IDF over dirs + commit tokens) by default; optional `Labeler` hook for an LLM phrase |

## Architecture

Three new packages and a small set of changes to existing ones. Existing `internal/forge` and provider adapters are untouched.

```
internal/
  embed/          NEW — Embedder interface, Ollama client, multi-modal feature builder
  cluster/        NEW — density-based clustering, novelty scoring, label generator
  heat/
    novelty.go    NEW — wires cluster results into HeatResult.Components
    upstream_mirror.go      from forkfarm-signals plan, retained
  tui/
    cluster_view.go         NEW — group-by-cluster toggle and rendering
  dump/
    dump.go       MODIFIED — orchestrates embed → cluster → label after T2
  github/
    readme.go     NEW — README fetch (gated to top-N)
```

`internal/heat` stays pure — it does no IO. Embedding and clustering live in their own packages so the pipeline can be tested without touching networks. `internal/dump` is the orchestrator (same role it has today).

### Critical files to modify
- `internal/heat/types.go` — add `ClusterID string`, `ClusterLabel string`, `NoveltyScore float64`, `ClusterMemberCount int` to `HeatResult`. Add `novelty` to allowed `Component.Name` values for T3.
- `internal/heat/score.go` — extend `Tier3ParamsV2` with `NoveltyScore` (0..1); `ComputeTier3V2` adds a `novelty` component capped at 5 pts (rebalances `lone_wolf` 10 → 7, `span` 10 → 8, `novelty` new 5 — keeps T3 max at 20).
- `internal/dump/dump.go` — invoke embed → cluster → label after T2 enrichment, gated to top-N (`--cluster-top`, default 50). Feed `NoveltyScore` into T3 params.
- `internal/tui/cache_bridge.go` — same orchestration when T2 results arrive incrementally.
- `internal/tui/model.go` + `internal/tui/table.go` — add `groupByCluster` view state, `g` keybinding to toggle.
- `internal/tui/detail.go` — show cluster label, sibling fork list, novelty score.
- `internal/tui/export.go` — new fields in `ExportFork`.
- `cmd/spoon/main.go` — `--cluster-top N`, `--no-cluster`, `--embedder URL`, `--labeler URL` flags.
- `cmd/spn/forks.go` (from spn bifurcation work) — emit `clusterId`, `clusterLabel`, `noveltyScore` on each NDJSON line.

### `internal/embed`

```go
package embed

type Vector []float32

type Embedder interface {
    Embed(ctx context.Context, texts []string) ([]Vector, error)
    Dim() int
}

// OllamaClient hits POST /api/embeddings on a local Ollama instance.
// Default model: "nomic-embed-text" (good general-purpose, 768-dim).
type OllamaClient struct {
    Endpoint string // default http://localhost:11434
    Model    string // default "nomic-embed-text"
    HTTP     *http.Client
}

// NewFromEnv returns OllamaClient configured from SPOON_EMBEDDER_URL and
// SPOON_EMBEDDER_MODEL, falling back to localhost defaults.
func NewFromEnv() Embedder

// Detect probes the default Ollama endpoint (or SPOON_EMBEDDER_URL).
// Returns (running=true, endpoint, installedModels) if /api/tags responds.
func Detect(ctx context.Context) (running bool, endpoint string, installed []string)

// PreferredEmbeddingModels is the ranked list of known-good embedding models on Ollama,
// best-quality-first. Used to pick from installed and to suggest a pull.
// Default chain is general-purpose; code-specialized models are listed as opt-in
// (selectable via --embedder-model) since they're not all available on Ollama yet.
var PreferredEmbeddingModels = []ModelSuggestion{
    // General-purpose (Ollama-native, used for auto-pull on first run)
    {Name: "nomic-embed-text",         SizeMB: 274,  Dim: 768,  Default: true, OnOllama: true},
    {Name: "mxbai-embed-large",        SizeMB: 670,  Dim: 1024, OnOllama: true},
    {Name: "bge-m3",                   SizeMB: 1200, Dim: 1024, OnOllama: true},
    {Name: "snowflake-arctic-embed",   SizeMB: 670,  Dim: 1024, OnOllama: true},

    // Code-specialized (better intra-language fork separation; require a non-Ollama
    // endpoint via --embedder URL, e.g., an OpenAI-compatible HF inference endpoint
    // or a local Python sidecar). All 768-dim so they're drop-in compatible.
    {Name: "jina-embeddings-v2-base-code", Dim: 768, CodeAware: true},
    {Name: "codebert-base",                Dim: 768, CodeAware: true}, // via sidecar
    {Name: "unixcoder-base",               Dim: 768, CodeAware: true}, // via sidecar
}

// PickInstalled returns the highest-ranked preferred model already installed, or "".
func PickInstalled(installed []string) string

// Pull streams Ollama's POST /api/pull and reports progress via the callback.
// progress is invoked with phase ("downloading", "verifying", "done") and 0..1 fraction.
func Pull(ctx context.Context, endpoint, model string, progress func(phase string, pct float64)) error

// ForkFeatures holds the four per-fork modality blobs.
type ForkFeatures struct {
    Paths     string // newline-joined sorted file paths
    Commits   string // newline-joined non-merge commit messages, deduped
    ReadmeDoc string // README delta vs upstream + repo description
    DiffChunk string // truncated diff text (token-budgeted)
}

// BuildFeatures composes the four modality strings from T2Data + (optional) README fetch.
// DiffChunk is built by selecting the largest hunks first up to MaxDiffChars (default 4000).
// Before embedding, diff text is normalized cheaply (collapse runs of whitespace, strip
// "+/-" hunk-header line numbers, lowercase identifiers in identifier-only tokens).
// Per Bui et al. (Corder, 2021), this reduces lexical variance without AST tooling.
func BuildFeatures(t2 forge.T2Data, readme string, maxDiffChars int) ForkFeatures

// NormalizeDiff applies the cheap text-level normalizations described above.
// Exported for testing.
func NormalizeDiff(diff string) string

// MultiModalEmbed embeds each modality separately and returns a concatenated vector
// weighted [paths 0.3, commits 0.3, readme 0.2, diff 0.2]. Missing modalities → zero block.
// The result is L2-normalized.
func MultiModalEmbed(ctx context.Context, e Embedder, fs []ForkFeatures) ([]Vector, error)
```

Why four separate embeddings then concat (rather than one big concat-string): isolates modality contributions, makes the modality weighting deterministic, and lets us gracefully skip a modality (e.g., empty README) without polluting the others.

**Alternative for bimodal-trained embedders.** Perera et al. (CodeBERT-Based Embeddings for Detecting Vulnerable Smart Contracts, IEEE 2026) report that CodeBERT-family models pre-trained on a *shared* natural-language + programming-language vector space achieve strong separation when fed mixed NL/PL input in a single pass. When the configured embedder reports `CodeAware: true` (set on `codebert-base`, `unixcoder-base`, `jina-embeddings-v2-base-code`), `MultiModalEmbed` switches to a single-call variant: it concatenates `paths + commits + readme + diff` with structural separators (`<paths>...</paths><commits>...</commits>...`) and embeds once per fork — saving ~4× embedding calls and exploiting the model's pre-trained NL+PL coupling. The four-modality independent path stays as the fallback for general-purpose embedders.

**Model-choice insight from the same paper.** Across the CodeBERT series (CodeBERT, GraphCodeBERT, UniXcoder, CodeReviewer, CodeExecutor), all produce 768-dim vectors — so swapping is a config concern, not architectural. CodeExecutor (trained to *simulate* code execution) achieved the highest AUC (0.9963) and outperformed GraphCodeBERT's explicit data-flow awareness, suggesting behavioral / execution-pattern embeddings separate code variants better than syntactic or graph-structural ones. CodeExecutor is not on Ollama at time of writing — recorded as a future-work upgrade once it's available via a local serving path.

### `internal/cluster`

```go
package cluster

type Point struct {
    ForkID string  // owner/repo for the fork
    Vec    []float32
}

type Cluster struct {
    ID       string  // "c0", "c1", ... or "noise"
    Members  []string // fork IDs
    Centroid []float32
    Label    string  // heuristic-generated; can be overridden by Labeler hook
}

type Assignment struct {
    ForkID  string
    Cluster string  // cluster ID, or "noise" for outliers
    Novelty float64 // 0..1; distance to nearest cluster centroid, normalized
}

// Cluster runs density-based clustering over a batch of fork embeddings.
// v1 algorithm: single-link agglomerative with cosine distance cutoff (epsilon = 0.35,
// minClusterSize = 3). Anything below minClusterSize collapses to "noise" → novelty = 1.0.
// epsilon and minClusterSize are tunable via opts.
type Options struct {
    Epsilon        float64 // cosine distance cutoff, default 0.35
    MinClusterSize int     // default 3
}

func Cluster(points []Point, opts Options) ([]Cluster, []Assignment)

// HeuristicLabel builds a label from cluster-member features.
// Two parts: (1) most-common directory prefix in member paths (e.g. "plugins/"), and
// (2) top-3 TF-IDF-discriminative tokens from member commit messages versus the corpus.
// Result: "plugins/  ·  oauth, provider, scope"
func HeuristicLabel(members []embed.ForkFeatures, corpus []embed.ForkFeatures) string

// Optional polishing hook. If non-nil, called once per cluster after HeuristicLabel.
// The LLM gets cluster discriminative features AND upstream repo context (description
// + truncated README) so labels are grounded in what the parent project is about,
// per Dhulshette et al. (TCS 2025) on business-context-grounded code summarization.
// Example: instead of "auth-related modifications", the model produces
// "OAuth provider plugins for the MCP gateway".
type Labeler interface {
    Polish(ctx context.Context, ctx_in LabelerContext) (string, error)
}

type LabelerContext struct {
    Heuristic        string             // the deterministic label
    Members          []embed.ForkFeatures // up to 5 representative members
    UpstreamRepo     string             // "IBM/mcp-context-forge"
    UpstreamDesc     string             // upstream description
    UpstreamReadme   string             // truncated upstream README (first ~2 KB)
    UpstreamCoreDirs []string           // top-K central dirs from repo.DirectoryCentrality
                                        // — per RepoMaster (NeurIPS 2025), gives the LLM a
                                        // sense of what "core" looks like in the parent project
}
```

**Why agglomerative + density cutoff rather than HDBSCAN.** No production-quality HDBSCAN exists in Go, and reimplementing it is its own project. Single-link agglomerative with a distance cutoff captures most of HDBSCAN's behavior on the "many small clusters + a long tail of noise" profile typical of fork sets, in <200 LOC. We get noise labels (→ novelty 1.0) directly from membership counts. If quality is inadequate in v1, the `Cluster` function signature is stable enough to swap implementations.

**Novelty score definition.** For each fork:
- If assigned to a real cluster: `novelty = clip(d_centroid / epsilon, 0, 1)` — how far on the cluster's edge it sits.
- If labeled "noise": `novelty = 1.0`.
- For repos with <10 enriched forks (TinySet path): novelty defaults to 0.5, no clustering attempted.

### `internal/heat/novelty.go`

```go
package heat

// NoveltyComponent converts a 0..1 novelty score to T3 points (max 5).
// Linear: points = novelty * 5.
func NoveltyComponent(novelty float64) Component
```

Bound deliberately tight (5 pts of 20 in T3) so novelty enhances, not dominates, the heat score. A novel fork still needs activity, recency, and meaningful diffs to score high overall.

### Embedder bootstrap (auto-detect Ollama + optional pull)

The first run of any command that would invoke clustering goes through a startup probe so users don't have to read docs or set env vars:

```
1. embed.Detect(ctx) — GET <endpoint>/api/tags with 500ms timeout.
   - If unreachable → silently disable clustering for this run. No prompt.
     (Most users without Ollama shouldn't see a question they can't answer.)
2. If reachable → embed.PickInstalled(installed):
   - Hit → use that model. Single one-line stderr log: "spoon: using <model> via Ollama".
   - Miss → branch by binary and TTY:
       a. spoon + interactive TTY:
          Prompt on stderr:
            "No embedding model found on Ollama. Pull nomic-embed-text (274 MB)? [Y/n]"
          Yes (default) → embed.Pull with a single-line progress indicator on stderr
                          (carriage-return updates; cleared on completion).
          No            → skip clustering for this run, print one-liner explaining
                          how to re-enable: "spoon: clustering disabled — run
                          `ollama pull nomic-embed-text` to enable next time".
       b. spoon + non-TTY (piped --json/--csv) OR --no-prompt set:
          Skip clustering, write a warning to stderr identifying the missing model.
       c. spn (always non-interactive):
          Never prompt. If clustering is requested or implicit, emit a non-fatal
          warning line to stderr in the structured-error shape:
            {"warning": {"code": "embedder_model_missing",
                         "message": "no embedding model installed on Ollama at <endpoint>",
                         "remediation": "ollama pull nomic-embed-text",
                         "details": {"endpoint": "...", "preferred": ["nomic-embed-text", "mxbai-embed-large"]}}}
          Continue without clustering; NDJSON records omit cluster fields.
3. --embedder-model M, when set, skips detection-based picking and uses M.
   If M is not installed, behavior depends on --auto-pull:
     - default: same prompt logic as 2.b (interactive) or 2.c (spn);
     - --auto-pull / SPOON_AUTO_PULL=1: pull without prompting (spn-friendly).
4. --no-cluster overrides everything: no detect, no pull, no clustering.
```

This makes the happy path zero-config: a user with Ollama already running gets clustering "for free" the first time. A user without Ollama gets a clean run that doesn't ask irrelevant questions. Agents using `spn` get a structured signal they can act on.

`internal/embed/bootstrap.go` owns this flow:

```go
// SelectEmbedder runs the detect → pick → (optionally prompt-pull) sequence.
// Returns (Embedder, modelName, nil) on success; (nil, "", reason) when clustering
// should be skipped for this run. The reason is descriptive, not an error.
func SelectEmbedder(ctx context.Context, opts SelectOptions, prompt Prompter) (Embedder, string, SkipReason)

type SelectOptions struct {
    Endpoint        string  // from --embedder / SPOON_EMBEDDER_URL
    ExplicitModel   string  // from --embedder-model / env; "" → auto-pick
    AutoPull        bool    // from --auto-pull / SPOON_AUTO_PULL=1
    NoPrompt        bool    // from --no-prompt or non-TTY stderr
    NonInteractive  bool    // true for spn; never prompts, never pulls without AutoPull
}

// Prompter is satisfied by the TUI bootstrap screen (returns the answer via a tea.Msg)
// and by a stdin TTY helper for spoon's non-TUI paths.
type Prompter interface {
    AskPull(model string, sizeMB int) (bool, error)
    ProgressFunc() func(phase string, pct float64)
}
```

The TUI uses a small bootstrap screen (visible only when a pull is needed) before transitioning to the existing input/table flow. Non-TUI spoon paths use a stdin helper; spn never instantiates a `Prompter`.

### Pipeline placement

In `internal/dump/dump.go::Run` (and mirror in `internal/tui/cache_bridge.go`):

```
1.  Fetch parent + forks list (existing).
2.  T1 score all forks (existing).
3.  Pick top-N for T2 (existing; --top N).
4.  Fetch T2 for top-N (existing).
5.  Run upstream_mirror detector (from retained part of fork-quality-signals plan).
6.  Pick top-M of T2-enriched forks for the embedding pass (--cluster-top, default 50).
    Skip forks already flagged as upstream mirrors. Skip ghost forks.
7.  For each of the M forks:
    a. Fetch README (gated by Headroom() > 0.10).
    b. Build ForkFeatures.
8.  Batch-embed all M forks via the configured Embedder.
9.  Cluster the M vectors.
10. For each cluster: heuristic label, optionally Labeler.Polish.
11. For each of the M forks: compute novelty, write back into HeatResult.
12. T3 score (existing) — now reads NoveltyScore from the assignment and adds the
    novelty Component.
13. ApplyTrust + ApplyPenalties (existing).
```

Stage 6's filter (skip mirrors and ghosts) keeps embedder calls focused on forks that might contain actual work. With `--cluster-top 50`, a typical run does ≤50 README fetches and 4 × 50 = 200 embeddings — well within Ollama's local throughput.

### Caching

Cluster results are deterministic given the same fork set and embedder. Cache at `~/.cache/spoon/clusters/<provider>/<owner>__<repo>.json`:

```json
{
  "computedAt": "2026-05-10T...",
  "embedderModel": "nomic-embed-text",
  "embedderEndpoint": "http://localhost:11434",
  "topM": 50,
  "epsilon": 0.35,
  "clusters": [...],
  "assignments": [...]
}
```

TTL: 24h (same as compare cache). `--refresh` invalidates. If the embedder model name changes, cache is treated as miss (model field mismatch).

### Configuration & flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--no-cluster` | — | Skip the embedding pass entirely. Falls back to legacy T3 (no novelty component). |
| `--cluster-top N` | 50 | Cap forks fed to embedder. |
| `--embedder URL` | `http://localhost:11434` | Ollama-compatible endpoint. |
| `--embedder-model M` | `nomic-embed-text` | Model name passed in the embedding request. |
| `--labeler URL` | (unset) | If set, POST cluster labels for polishing. Unset → heuristic only. |
| `--cluster-epsilon F` | 0.35 | Distance cutoff. |
| `--cluster-min-size N` | 3 | Min cluster size. |
| `--auto-pull` | false | Pull a missing embedding model without prompting (`spn`-friendly). |
| `--no-prompt` | auto (from TTY) | Skip the pull prompt and disable clustering when no model is installed. |

Equivalent env vars: `SPOON_EMBEDDER_URL`, `SPOON_EMBEDDER_MODEL`, `SPOON_LABELER_URL`. CLI flags win.

### TUI integration

- New view-state field `groupByCluster bool` on `tea.Model`.
- Keybinding `g` toggles cluster grouping in the table view. When on, table rows are grouped by `ClusterID`; each group renders a header row with the cluster label and member count.
- Detail view gains a "Cluster" block: label, sibling fork list (linked), novelty score with sparkline.
- `?` help screen lists the new binding.

### `spn` integration

The spn bifurcation spec already defines NDJSON output for `spn forks list`. The novelty work adds three new fields per record (no schema break — additive):

```json
{
  "fork": "alice/myfork",
  "heat": 67.4,
  "clusterId": "c3",
  "clusterLabel": "plugins/  ·  oauth, provider, scope",
  "noveltyScore": 0.42,
  "components": [{"name":"novelty","points":2.1,"max":5,"raw":0.42}, ...]
}
```

If `--no-cluster` is set or the embedder is unreachable, the three fields are omitted (not null) so downstream agents can distinguish "not computed" from "computed and zero."

### Errors and fallback

The pipeline gracefully degrades:
- Ollama unreachable → silent skip (no question prompted); clustering omitted.
- Ollama reachable but no embedding model installed → interactive prompt (spoon TTY), `--auto-pull` pull (any binary with the flag), or structured warning + skip (spn / non-TTY).
- Pull fails mid-download → stderr warning, clustering skipped this run, cache untouched so a later retry is clean.
- Partial embedder failure (some forks succeed at embedding time) → cluster the subset; failed forks get cluster `""`, novelty `0.0`.
- Labeler unreachable → fall back to heuristic labels.

No fatal errors from the GCD pass — it's strictly additive enrichment.

## What this supersedes (revised)

From `docs/superpowers/plans/2026-04-28-fork-quality-signals.md`. Note: re-framed from the earlier "drop entirely" stance after reviewing Soll & Vosgerau (ClassifyHub, KI 2017) — their ensemble of weak heuristic classifiers achieved ~60% precision/recall over 7 GitHub classes without any embeddings, showing that cheap diverse signals are individually useful as ensemble members.

- **Drops** `internal/heat/forkfarm.go` as a top-level "type of fork" — identical-diff fork farms collapse into a single very-tight cluster naturally and don't need a separate classification primitive. The *fingerprint* function itself (sha of `(filesChanged, totalAdds, totalDels, aheadBy)`) is retained as a cheap deduplication step *before* embedding: identical fingerprints share a single embedding pass, cutting cost on fork-farm-heavy repos.
- **Repurposes** `internal/heat/content_archetype.go` as a *weak classifier* feeding the cluster system, not as a top-level label. The regex archetype detector (Plugin/Security/Docs/Test/Migration/Refactor/ConfigTweak/General) runs on every enriched fork and contributes its category as a discriminative feature (a one-hot vector concatenated to the embedded representation, weight 0.05 — small enough to nudge clustering without dominating it). Heuristic archetype tags also surface in JSON output alongside cluster labels for users who want both views.
- **Keeps** `internal/heat/upstream_mirror.go` and its tests — orthogonal heuristic; runs before clustering to exclude mirrors from the embedder budget.
- **Keeps** the Phase-5/6/7/8/9 enrichment endpoints (tree SHA, contributors, recursive tree, pulls) — they remain useful for other planned signals.

### `internal/cluster/signals.go` — weak signal ensemble

Light layer that produces per-fork weak-classifier vectors fed into the clustering step:

```go
package cluster

// WeakSignals captures cheap deterministic per-fork signals derived without
// embeddings. Inspired by ClassifyHub (Soll & Vosgerau, KI 2017). Each signal
// is a small one-hot/numeric vector; they're concatenated and weighted.
type WeakSignals struct {
    ArchetypeOneHot []float32 // from content_archetype regex output
    LangOneHot      []float32 // primary language one-hot
    FileExtDist     []float32 // distribution over top-K most-common ext buckets
    NameHash        []float32 // mini-hash of repo name tokens
    ChangeImpact    float32   // 0..1 centrality of touched dirs in the upstream
}

// SignalsToFeatureVec returns the concatenated, normalized signal vector.
// This is appended to the embedding vector at clustering time (weight tunable
// via SignalWeight option on the Cluster call, default 0.05).
func SignalsToFeatureVec(s WeakSignals) []float32
```

This makes the system gracefully degrade: if Ollama is unreachable and clustering falls back, the weak signals alone can still drive a coarse rule-based grouping (single-link agglomerative over the signal vectors only). Output quality drops but the feature still produces *something*.

### `internal/repo/centrality.go` — directory-centrality for ChangeImpact

Inspired by RepoMaster (Wang et al., NeurIPS 2025), which scores module centrality via personalized PageRank on a module-dependency graph to identify the "core" of a repository. A full MDG needs per-language import parsing — out of scope for v1. The cheap proxy: **directory-centrality**, computed once per upstream repo.

```go
package repo

// DirectoryCentrality holds per-directory scores reflecting how "core" each
// directory is in the upstream. Score = combined weight from:
//   (a) file-count share (more files → more central)
//   (b) upstream commit-message keyword frequency (commit msgs mentioning
//       this directory's name token, normalized)
// Both signals come from data we already fetch in T1/T2.
type DirectoryCentrality struct {
    DirScore    map[string]float64 // "src/api/" → 0.87
    CoreDirs    []string           // top-K by score, K=10
    ComputedAt  time.Time
}

// Compute builds the centrality table from upstream's recursive tree (Phase 7
// of the fork-quality-signals plan) and a sample of upstream commit messages.
// Cached at ~/.cache/spoon/centrality/<provider>/<owner>__<repo>.json (24h TTL).
func Compute(ctx context.Context, c *github.Client, owner, repo string) (DirectoryCentrality, error)

// ScoreFork returns a 0..1 ChangeImpact score for a fork:
//   mean(DirScore[d]) for d in dirs-touched-by-fork
// Falls back to 0.0 if no centrality data is available.
func (dc DirectoryCentrality) ScoreFork(touchedDirs []string) float64
```

Wired into `internal/dump/dump.go`:
1. After parent fetch, kick off `repo.Compute` for the upstream (gated by `Headroom() > 0.10`; cache reused across reruns).
2. For each fork's `T2Data`, derive `touchedDirs` from the file list.
3. Set `WeakSignals.ChangeImpact = centrality.ScoreFork(touchedDirs)`.
4. Pass into clustering AND surface in JSON output as `changeImpact` (0..1).

This gives every fork a "did they touch the core or the periphery?" signal at near-zero marginal API cost — the recursive-tree fetch was already planned for Phase 7 of fork-quality-signals.

## Testing & verification

### Unit tests
- `internal/embed/`: `BuildFeatures` covers empty README, empty commits, oversized diffs (truncation). `MultiModalEmbed` with a stub `Embedder` that returns deterministic vectors; verify modality-weight blending. Ollama client tested against a `httptest.Server` returning fixture responses.
- `internal/cluster/`: feed synthetic vector batches with known geometry (3 tight blobs + 2 noise points) and assert the expected clusters + noise labels. Test `HeuristicLabel` on fixture features.
- `internal/heat/novelty.go`: trivial — verifies the component conversion math and that `ComputeTier3V2` integrates novelty without breaking the 20-pt cap.

### Integration tests
- `internal/dump`: full pipeline against a fake `forge.Forge` and a stub `Embedder` returning predetermined vectors. Verify cluster assignments and JSON output shape.
- `cmd/spoon`: golden-file test on `--json` output with stub embedder; one with `--no-cluster` to confirm fields are omitted.

### End-to-end verification
Run against IBM/mcp-context-forge with Ollama running locally:

```sh
ollama pull nomic-embed-text
ollama serve &
go build -o spoon ./cmd/spoon
./spoon --json --cluster-top 50 IBM/mcp-context-forge > out.json
jq '[.[] | {fork, clusterId, clusterLabel, noveltyScore}] | group_by(.clusterId)' out.json
```

Expected outcomes (based on field-analysis priors):
- ~5-10 clusters; the largest contains the ~27 identical fork-farm forks (cluster size ≥10, average novelty ≤0.2).
- Upstream mirrors are absent from clusters (filtered before embedding).
- Plugin / security / docs forks fall into distinct small clusters with labels like `mcpgateway/plugins/  ·  jwt, scope, oauth`.
- Novelty histogram is bimodal: a tight low-novelty cluster (fork farms, similar plugins) and a high-novelty tail (genuine one-offs).

### TUI verification
```sh
./spoon IBM/mcp-context-forge
# Press 'g' → table groups by cluster
# Highlight a cluster header → detail view shows label, siblings, novelty histogram
```

### Manual sanity checks
- `--no-cluster` produces identical output to current `spoon` (modulo unrelated changes) — confirms zero-impact when feature is off.
- Killing Ollama mid-run produces a stderr warning and a valid (no-cluster-fields) output — confirms graceful degradation.
- `--embedder http://localhost:11434 --embedder-model bge-small` swaps the model and re-clusters when paired with `--refresh`.

## Implementation order

1. `internal/embed` package: `Embedder` interface, Ollama client, `Detect`, `PickInstalled`, `Pull`, `BuildFeatures`, `MultiModalEmbed`. Tests with a stub embedder + `httptest.Server` for the Ollama probe and pull endpoints.
2. `internal/embed/bootstrap.go`: `SelectEmbedder` + `Prompter` interface; stdin-TTY prompter implementation for non-TUI spoon paths.
3. `internal/cluster` package: agglomerative clustering, novelty scoring, heuristic labels. Tests with synthetic vectors.
4. `internal/heat/novelty.go` + extend `Tier3ParamsV2` / `ComputeTier3V2`. Rebalance T3 component weights.
5. Wire into `internal/dump/dump.go` behind a `--no-cluster` flag (default off, i.e. clustering on by default).
6. `internal/github/readme.go` for README fetch.
7. Mirror wiring in `internal/tui/cache_bridge.go` + TUI bootstrap screen for the pull prompt.
8. TUI cluster view (`g` keybinding, grouped table, detail panel).
9. `spn forks list` field additions + structured `embedder_model_missing` warning emission.
10. Cache layer at `~/.cache/spoon/clusters/`.
11. Optional `Labeler` interface + HTTP client (separate, last).

Each step ships with tests and is independently mergeable.

## Open questions deferred to implementation

- Exact rebalance of T3 component caps (lone_wolf 10→7, span 10→8, novelty 0→5) — the specific numbers may need empirical tuning against the IBM dataset; the constraint is total ≤ 20.
- Whether to ship a default `--labeler` config pointing at Ollama's chat endpoint for users with a generative model also installed.
- Schema-versioning of the cluster cache file (assume forward-compatible for v1; add `$schema_version` if cluster fields evolve).

## Future work

Each item below has a dedicated design document covering: how the idea was formed, the research basis (with citations), implementation sketch, cost analysis, acceptance criteria, risks/open questions, and step-by-step completion guidance.

| Item | Origin | Document |
| --- | --- | --- |
| Behavioral / execution-pattern embeddings | Perera et al. 2026 — CodeExecutor outperformed graph-structural and general code embedders (AUC 0.9963 vs. 0.9770) | [`../specs/future/future-work-behavioral-embeddings.md`](../specs/future/future-work-behavioral-embeddings.md) |
| Continual cluster updates (CCD) | GCD survey + ProtoGCD; eliminates re-cluster churn on warm-cache runs | [`../specs/future/future-work-continual-cluster-updates.md`](../specs/future/future-work-continual-cluster-updates.md) |
| Cross-repo cluster lineage | GCD survey on FCD; lets users see the same archetype across different upstream repos | [`../specs/future/future-work-cross-repo-lineage.md`](../specs/future/future-work-cross-repo-lineage.md) |
| Hierarchical fork summarization | Dhulshette et al. 2025 — decompose-then-aggregate beats one-shot LLM summarization | [`../specs/future/future-work-hierarchical-summarization.md`](../specs/future/future-work-hierarchical-summarization.md) |
| AST-based semantic-preserving normalization | Corder (Bui et al. 2021) — apply the 5 transformations *in reverse* at inference time to canonicalize diffs | [`../specs/future/future-work-semantic-preserving-normalization.md`](../specs/future/future-work-semantic-preserving-normalization.md) |
| Full Module-Dependency-Graph centrality | RepoMaster (Wang et al. NeurIPS 2025) — replace v1's directory-centrality proxy with real per-language import parsing + personalized PageRank | [`../specs/future/future-work-full-mdg-centrality.md`](../specs/future/future-work-full-mdg-centrality.md) |
| Function-Call-Graph fork analysis | RepoMaster — function-level call-fan-in as a complement to MDG ChangeImpact, sharpens forks-touching-widely-used-code signal | [`../specs/future/future-work-fcg-fork-analysis.md`](../specs/future/future-work-fcg-fork-analysis.md) |

## Verification command summary

```sh
# Build and run unit tests
go test ./internal/embed/... ./internal/cluster/... ./internal/heat/...

# Full suite
go test ./...

# End-to-end against a real repo (requires Ollama)
ollama pull nomic-embed-text
ollama serve &
./spoon --json IBM/mcp-context-forge | jq '.[0]'

# Confirm graceful degradation
SPOON_EMBEDDER_URL=http://localhost:1 ./spoon --json IBM/mcp-context-forge | jq '.[0] | has("clusterId")'  # → false

# TUI smoke test
./spoon IBM/mcp-context-forge   # then press 'g'
```
