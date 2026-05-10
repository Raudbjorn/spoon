# Future Work: Full Module Dependency Graph Centrality

**Status:** Deferred from v1, which ships the cheap directory-centrality proxy in `internal/repo/centrality.go`.
**Parent plan:** `plan-integrating-this-research-declarative-puddle.md`
**Estimated effort:** 3–4 weeks per supported language; staged rollout.

## Context

V1 of the `ChangeImpact` signal uses a coarse heuristic: directory-level centrality computed from file counts and upstream commit-message keyword frequency. This is cheap and language-agnostic but misses the actual structural importance of code. Two directories with the same file count can have very different roles in the dependency graph: one might be the lone entry point everyone imports from, the other a leaf-level utility no one references.

RepoMaster (Wang et al., NeurIPS 2025) defines the principled answer: build a **Module Dependency Graph (MDG)** — nodes are modules, edges are explicit imports — and score module importance via personalized PageRank. The resulting centrality scores are dramatically more accurate than the file-count proxy at identifying "core" modules.

This document describes upgrading spoon to a real MDG. The win: forks touching genuinely-central modules score correctly even when those modules live in small, unassuming directories; forks touching peripheral files don't inherit centrality from sharing a directory with central ones.

## Origin of the idea

RepoMaster's empirical evaluation (Table in §3.2.2 of the paper) shows that on real GitHub repositories, importance scores from MDG personalized PageRank correlate well with human judgment of "what is this project really about?" — and that this signal is what their agent uses to focus exploration. The same principle applies to fork triage: a fork modifying the project's core gets the user's attention; a fork modifying a forgotten leaf utility doesn't.

V1's directory-centrality proxy emerged from the constraint that spoon currently has no per-language import parser. Building one — or rather, several — is real work but bounded.

## Research basis

**Wang et al., "RepoMaster: Autonomous Exploration and Understanding of GitHub Repositories for Complex Task Solving" (NeurIPS 2025).** §3.2.1 defines the MDG: `Gm = (Vm, Em, wm)`, where `Vm = M` is the set of modules, `Em ⊆ M × M` captures explicit dependencies from import statements, and `wm` measures coupling strength (typically import count). §3.2.2 scores module importance via Personalized PageRank over the MDG, combined linearly with five other features (complexity, usage, semantic keywords, doc richness, git activity). Equal weighting empirically validated.

**Page, Brin, Motwani, & Winograd, "The PageRank Citation Ranking: Bringing Order to the Web" (Stanford 1998).** Original PageRank. Personalized PageRank biases the random-walk teleport distribution toward a chosen subset (e.g., entry-point modules); useful when we want centrality measured "from the user's perspective."

**GraphCode2Vec (Yefet et al., ORBilu 2022).** Uses Soot's Jimple intermediate representation to flatten Java bytecode into a uniform graph substrate before running GNNs. The same insight applies: a language-neutral intermediate representation makes downstream graph algorithms (like PageRank) language-agnostic.

**Source Code Vulnerability Detection (arXiv:2404.14719v1, 2024).** Combines code language models with code property graphs (CPGs), which are a superset of MDGs (CPG = AST + CFG + DDG + PDG merged). Confirms the broad applicability of graph-based code analysis.

## Implementation sketch

```
internal/
  mdg/
    mdg.go            NEW — Graph type, PageRank, public API
    mdg_test.go
    parse_go.go       NEW — Go import parser using go/parser + go/build
    parse_python.go   NEW — Python import parser using a bundled tree-sitter grammar
    parse_js.go       NEW — JS/TS via esbuild's Go-native API
    parse_test.go
```

```go
package mdg

type Module struct {
    Path string  // canonical, e.g., "github.com/owner/repo/internal/auth"
    Lang string
}

type Graph struct {
    Nodes []Module
    Edges map[string][]string  // imports
    Index map[string]int       // path → node index
}

// Build constructs the MDG for a repo at the given clone path.
// Uses one parser per detected language; combines into a single graph.
// For files outside the supported language set, treats them as opaque
// non-importing leaves (visible in the graph but contributing zero edges).
func Build(ctx context.Context, repoPath string) (*Graph, error)

// PageRank computes personalized PageRank scores. teleport is a node-index
// distribution; for the unbiased case pass nil and PageRank uses a uniform
// teleport. Returns a map from module path to score in [0, 1].
func (g *Graph) PageRank(teleport []float64, damping float64, iterations int) map[string]float64

// EntryPointTeleport heuristically constructs a teleport distribution biased
// toward likely entry points (cmd/* in Go, src/index.* in JS, __main__.py in
// Python, top-level Go files declaring `package main`). This matches the
// "from-the-user's-perspective" view of importance.
func (g *Graph) EntryPointTeleport() []float64
```

### Pipeline placement

Replaces the `internal/repo/centrality.go` directory-centrality computation when `--full-mdg` is enabled (or when the repo is in a language with an MDG parser). The same `ScoreFork(touchedFiles []string) → float64` API stays: aggregate PageRank scores for each touched module, normalize.

The MDG is cached at `~/.cache/spoon/mdg/<provider>/<owner>__<repo>.json` for the same 24h TTL as the centrality proxy. If the upstream is unchanged (HEAD SHA matches the cache), don't rebuild.

### Building the MDG requires a clone

This is the key infrastructure shift: v1's centrality proxy works from the recursive tree + commit message API responses. A real MDG needs source code. Either:
- **Shallow clone** the upstream to a temp directory, parse, discard. ~10–60 seconds for typical repos; 1 GB disk peak.
- **Fetch raw file contents** via the GitHub Contents API for source files only. Slower (one API call per file), but doesn't need git locally.

The cleaner path is shallow-clone for users who have `git` installed; fall back to per-file fetch when git is unavailable.

## Cost analysis

| Resource | First-time | Cached |
| --- | --- | --- |
| Clone time | 5–60 s | 0 (cache hit) |
| Parser CPU | 1–10 s | 0 |
| Disk peak during build | ~50 MB–1 GB | 50 KB cache file |
| Build memory | < 500 MB | < 5 MB |
| API calls | 0–1 (clone via git proto) | 0 |

PageRank itself is cheap: linear in edges, typically <100ms on graphs up to a few thousand nodes.

## Acceptance criteria

1. **Better than the directory proxy.** On a hand-curated set of 5 real repos, the top-10 modules by MDG PageRank should overlap the human-judged "core modules" at ≥ 70% — significantly above the directory-proxy's baseline (typically ~40%).
2. **Language coverage.** The first phase ships Go support (in-process via stdlib). Subsequent phases add Python (tree-sitter) and JS/TS (esbuild).
3. **Robustness.** Repositories with syntax errors, missing files, or unusual layouts must not crash the MDG builder. Parse failures fall through to "no-edges-for-this-file" semantics with a stderr warning.
4. **Cache integrity.** Repeated runs must produce byte-identical MDG cache files when the upstream HEAD SHA is unchanged. Otherwise the cache is invalidated.
5. **Performance budget.** End-to-end run on IBM/mcp-context-forge (Python, ~400 files) including MDG build and clustering: ≤ 90 seconds first-run, ≤ 15 seconds cached.

## Risks and open questions

- **Cross-language imports.** Polyglot repos (e.g., Go service with embedded Python scripts) have implicit dependencies that aren't expressed via imports. Out of scope; document as a limitation.
- **Dynamic imports.** Python `__import__()` and JS `require(variable)` defeat static parsing. Out of scope; PageRank gives those modules low edge counts and they're treated as peripheral. Usually fine.
- **Vendor directories.** `vendor/`, `node_modules/`, `venv/`, `third_party/`. These produce huge graphs of dependencies that aren't part of the project's own code. Exclude by convention (configurable list); document defaults.
- **Disk usage.** Cloning hundreds of upstream repos accumulates. The clone is removed after parsing; the cache file is small. Document that `spoon` may briefly use up to ~1 GB during analysis.
- **Auth for private repos.** Cloning requires the user's git credentials; spoon currently uses `gh auth`'s token via the API. Add a thin shim that runs `gh repo clone --depth 1` (which handles auth correctly) when git operations are needed.

## How to complete

1. **Land v1 with the directory-centrality proxy.** The proxy validates that ChangeImpact is a useful signal before we invest in real MDG infrastructure.
2. **Phase A: Go-only MDG.** Implement `internal/mdg/parse_go.go` using `go/parser`. Validate against a hand-curated set of Go repos.
3. **Validation experiment.** For 5 well-known Go repos, hand-pick the top-10 "core modules" by expert judgment. Compare against the directory proxy's top-10 and the MDG PageRank top-10. Measure overlap.
4. **Phase B: Python via tree-sitter.** Bundle the tree-sitter Python grammar (~1 MB) into spoon. Implement import parsing.
5. **Phase C: JS/TS via esbuild.** esbuild's Go API exposes module resolution; use it.
6. **Shallow-clone subsystem.** Implement clone-to-tempdir with cleanup, plus the per-file API fallback. Add a `--no-clone` flag for users in restricted environments.
7. **Cache schema** for MDG: nodes, edges, build options (parser versions, language list), upstream HEAD SHA, timestamp.
8. **Wire `mdg.PageRank` into `internal/repo/centrality.go`** as the primary backend; keep the directory proxy as fallback when MDG is unavailable for the repo's languages.
9. **Document** the new `--full-mdg`/`--no-mdg` flags, language coverage, and known limitations.

## References

- Wang, H., Ni, Z., Zhang, S., Hu, S., Lu, S., He, Z., Lin, J., Hu, C., Guo, Y., Lyu, P., & Du, Y. (2025). *RepoMaster: Autonomous Exploration and Understanding of GitHub Repositories for Complex Task Solving.* NeurIPS 2025.
- Page, L., Brin, S., Motwani, R., & Winograd, T. (1998). *The PageRank Citation Ranking: Bringing Order to the Web.* Stanford InfoLab.
- Yefet, N., Alon, U., & Yahav, E. (2022). *GraphCode2Vec: Generic Code Embedding via Lexical and Program Dependence Analyses.* ORBilu.
- *Source Code Vulnerability Detection: Combining Code Language Models and Code Property Graphs.* arXiv:2404.14719v1.
