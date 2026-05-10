# Future Work: Function-Call-Graph Fork Analysis

**Status:** Deferred from v1; depends on full-MDG-centrality landing first (shares the clone + per-language parser infrastructure).
**Parent plan:** `plan-integrating-this-research-declarative-puddle.md`
**Estimated effort:** 4–6 weeks per language, on top of MDG work.

## Context

Once spoon has a Module Dependency Graph (the next-tier infrastructure described in `future-work-full-mdg-centrality.md`), the natural extension is a Function Call Graph (FCG) — nodes are individual functions/methods, edges are call relationships. RepoMaster (NeurIPS 2025) uses FCGs alongside MDGs as a structural artifact for repository understanding.

The relevance for fork novelty: a fork that modifies a *widely-called function* — one used by many other functions across the codebase — has dramatically higher impact than a fork modifying a never-called helper or a CLI entrypoint. The MDG-level signal already approximates this at the module level; FCG refines it to function granularity, which is where most fork diffs actually live.

## Origin of the idea

Spoon's existing T2 enrichment captures *which files* a fork touched and *how many lines* changed. It does not capture *which functions in those files* were touched, nor *how widely those functions are used*. Anecdotally, this missing signal is why the MNA metric (Meaningful Net Additions) can mislead: a 200-line change to an internal helper that's only called once is far less impactful than a 2-line change to a function called by 80 other functions.

FCG fork analysis closes this gap: parse the upstream's source to build the call graph; for each fork's touched lines, identify which functions they fall within; sum the call-fan-in of those functions to produce a `CallImpact` score that complements `ChangeImpact` (which lives at the module level).

## Research basis

**Wang et al., "RepoMaster: Autonomous Exploration and Understanding of GitHub Repositories for Complex Task Solving" (NeurIPS 2025).** §3.2.1 defines the FCG: `Gf = (Vf = F, Ef, wf)`, where `Vf` is the set of functions, `(fi, fj) ∈ Ef` exists when `fi` invokes `fj`, and `wf` encodes call frequency. The FCG complements the MDG by exposing function-level rather than module-level dependencies.

**Yefet et al., "GraphCode2Vec: Generic Code Embedding via Lexical and Program Dependence Analyses" (ORBilu 2022).** Builds program-dependence graphs (a generalization of FCGs) from Soot's Jimple IR. Demonstrates that combining lexical embeddings with graph-structural features improves downstream code understanding tasks. Validates the value of explicit call relationships for code embedding.

**GNN-Coder: Boosting Semantic Code Retrieval with Combined GNN and Transformer (arXiv:2502.15202v1).** Uses AST-guided GNNs over function-level structure. Introduces ASTGPool, a topological pooling layer that assigns importance scores to nodes based on connectivity. Confirms the broad value of treating central-by-connectivity functions as semantically central too.

**Source Code Vulnerability Detection (arXiv:2404.14719v1, 2024).** Uses code property graphs (which subsume FCG) to find vulnerabilities. Practical evidence that FCG-derived features capture meaningful signal.

## Implementation sketch

Build on the MDG infrastructure. Each language parser already produces an AST; extending it to extract function definitions + call sites is a contained delta.

```
internal/
  fcg/
    fcg.go             NEW — Graph type, CallImpact computation
    parse_go.go        NEW — extracts functions + calls via go/ast inspection
    parse_python.go    NEW — tree-sitter Python with function/method/call queries
    parse_js.go        NEW — esbuild AST traversal
```

```go
package fcg

type Function struct {
    QualifiedName string  // e.g., "internal/auth.User.IsAdmin"
    File          string
    StartLine     int
    EndLine       int
    Lang          string
}

type Graph struct {
    Funcs []Function
    // Edges[i] = list of indexes that function i calls
    Edges     map[int][]int
    // CallCount[i] = total times function i is called from other functions
    CallCount map[int]int
}

// Build walks the parsed source and produces the function-level call graph.
// Cross-language calls (e.g., FFI) are ignored — out of scope.
func Build(ctx context.Context, repoPath string) (*Graph, error)

// FunctionAtLine returns the function whose body contains the given file:line.
// Used to map fork-touched lines to functions.
func (g *Graph) FunctionAtLine(file string, line int) (*Function, bool)

// CallImpact computes a 0..1 score for a fork given its touched lines.
//
// For each touched line:
//   1. Look up the containing function (if any).
//   2. Score = log1p(CallCount[func]) / log1p(maxCallCount in graph).
// Aggregate: mean score over all touched lines, weighted by line count in
// each function. Lines outside any function (e.g., top-level package
// declarations) contribute 0.
func (g *Graph) CallImpact(touchedLines map[string][]int) float64
```

### Pipeline placement

After MDG (which produces `ChangeImpact`), the FCG pass produces `CallImpact`. Both feed into `WeakSignals`:

```go
type WeakSignals struct {
    // ... existing fields ...
    ChangeImpact float32  // module-level (from MDG or directory proxy)
    CallImpact   float32  // function-level (from FCG)
}
```

The two signals are correlated but not redundant: a fork can touch many lines in a peripheral module that happens to contain a widely-called function, or vice versa. Both surface in JSON output and in the TUI detail view.

### Surfacing

- **TUI detail view**: a small "Functions touched" block lists the top-3 functions by call-fan-in that the fork modified. Helpful for understanding *what* the fork actually changed in semantic terms.
- **`spn forks list`** NDJSON: adds `callImpact` (0..1) and `touchedFunctions` (array of qualified function names).
- **Cluster labels** can incorporate touched-function names. A cluster where every member modifies `auth.OAuthHandler` will produce a much sharper label.

## Cost analysis

| Resource | First-time | Cached |
| --- | --- | --- |
| Parse pass | 2–20 s (depends on repo size + language) | 0 |
| FCG build | 1–5 s | 0 |
| Per-fork line→function lookup | < 1 ms per fork | < 1 ms |
| Cache size on 1000-function repo | ~200 KB | — |

The line→function lookup is O(log n) with a sorted-by-line index — cheap enough to be on the per-fork hot path.

## Acceptance criteria

1. **Function attribution correctness.** For a hand-curated set of 100 (file, line) tuples in 3 different repos, the `FunctionAtLine` lookup should return the correct enclosing function in ≥ 95% of cases. False matches (returning a sibling function or no function) accounted and documented.
2. **CallImpact signal quality.** On IBM/mcp-context-forge, sort forks by `CallImpact` descending. Manually review the top 10: at least 7 should be judged "modifying significant functions" by a human. Same review on the bottom 10: at least 7 should be "modifying peripheral or trivial functions."
3. **Independence from ChangeImpact.** Compute Pearson correlation between `ChangeImpact` and `CallImpact` across all forks. Correlation should be < 0.7 (otherwise we're just measuring the same thing twice and one of the signals can be dropped).
4. **Clustering improvement.** Adding `CallImpact` as a `WeakSignal` should improve cluster purity (where evaluable against hand labels) by ≥ 5% over the MDG-only setup.
5. **Cluster labels mention function names** when the cluster has a tight function focus (e.g., all members touch `auth.OAuthHandler`). Heuristic label generator updated accordingly.

## Risks and open questions

- **Dynamic dispatch.** Method calls through interfaces/abstract classes can't be statically resolved to a single target. Solution: count edges to *all possible* targets. Bias toward over-counting, which is fine for the "call fan-in" metric.
- **Reflection and dynamic invocation.** Code that calls functions by name at runtime (Go's `reflect.Call`, Python's `getattr`, JS's `obj[name]()`) defeats static analysis. These functions appear less-called than they actually are. Document as a limitation.
- **Generated code.** Many repos have generated files (protobuf bindings, mock implementations, etc.) that inflate call counts. Apply standard heuristics (`//go:generate` markers, common generated-file paths) to exclude these from the graph. Configurable.
- **Diff-to-line mapping.** Spoon's existing `T2Data.Files` includes files but not specific line ranges of changes. The compare API returns hunk ranges; need to consume those. This may require either a richer T2 fetch or a separate patch-fetch step.
- **Large repos.** Massive codebases (millions of lines) produce huge FCGs. Limit by `--fcg-max-functions` (default 50000) and skip the FCG pass for repos that exceed it (falling back to MDG-only ChangeImpact).

## How to complete

1. **Land MDG infrastructure first.** FCG reuses the cloning, parser dispatch, and cache layer.
2. **Extend the Go parser** to emit function definitions and call sites. Tests against hand-curated fixtures.
3. **Build the FCG type** (`internal/fcg/fcg.go`) with the line→function index. Tests with synthetic graphs.
4. **Extend T2 fetch** to retain per-file hunk line ranges (rather than only file-level counts).
5. **Wire `CallImpact`** into `WeakSignals` and pipeline orchestration.
6. **Validation.** Hand-eval on 3 real repos covering 3 languages.
7. **TUI integration.** "Functions touched" block in the detail view, with the function name + its call-fan-in count rendered.
8. **Update cluster labeling** to mention top touched-function names when concentrated.
9. **Document** language coverage, the dynamic-dispatch limitation, and the configurable graph-size cap.

## References

- Wang, H., Ni, Z., Zhang, S., et al. (2025). *RepoMaster: Autonomous Exploration and Understanding of GitHub Repositories for Complex Task Solving.* NeurIPS 2025.
- Yefet, N., Alon, U., & Yahav, E. (2022). *GraphCode2Vec: Generic Code Embedding via Lexical and Program Dependence Analyses.* ORBilu, University of Luxembourg.
- *GNN-Coder: Boosting Semantic Code Retrieval with Combined GNN and Transformer.* arXiv:2502.15202v1.
- *Source Code Vulnerability Detection: Combining Code Language Models and Code Property Graphs.* arXiv:2404.14719v1.
