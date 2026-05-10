# Future Work: AST-Based Semantic-Preserving Diff Normalization

**Status:** Deferred from v1 (which uses cheap text-level normalization).
**Parent plan:** `plan-integrating-this-research-declarative-puddle.md`
**Estimated effort:** 4–6 weeks per supported language pair; substantial new dependency.

## Context

V1 of spoon's fork clustering normalizes diff text cheaply before embedding: collapse runs of whitespace, strip hunk header line numbers, lowercase identifier-only tokens. This buys some lexical invariance but is far from full *semantic equivalence*. Two forks doing the same refactor — say, renaming all instances of `userID` to `userId` — produce different diffs that v1 will embed differently and likely cluster apart.

AST-based semantic-preserving normalization closes this gap by transforming each fork's diff into a canonical form before embedding. Identifiers get hashed to stable placeholders. Independent statements get sorted. Equivalent control-flow constructs (for-vs-while, if-vs-switch) get rewritten to one canonical form. The resulting embedding represents the *intent* of the change, not its surface syntax.

## Origin of the idea

Bui et al. (Corder, 2021) demonstrated that contrastive learning over semantically-equivalent program transformations produces code embeddings where functionally equivalent code clusters tightly. Their training-time technique transforms code in five ways:
1. **Variable Renaming (VN)** — random identifier replacement.
2. **Unused Statement (US)** — dead-code insertion.
3. **Permute Statement (PS)** — swap independent statements.
4. **Loop Exchange (LX)** — replace `for` with `while` or vice versa.
5. **Switch to If (SF)** — convert `switch` to `if/else if` chains.

The training-time observation: a model exposed to these as positive pairs learns invariance to them. The *inference-time* implication for spoon: if we apply these transformations **in reverse** (toward a canonical form) before embedding, even a general-purpose embedder produces invariant outputs without needing a special model.

In short, instead of teaching the model to ignore surface variations, we *remove* the surface variations before the model sees them.

## Research basis

**Bui, Yu & Jiang, "Self-Supervised Learning for Code Retrieval and Summarization through Semantic-Preserving Program Transformations" (Corder, arXiv:2009.02731v4, 2021).** Defines the five transformation operators above. Demonstrates that models trained with these transformations as positive pairs achieve significantly better code-to-code retrieval than non-contrastive baselines. The transformations preserve semantics by construction (AST-level).

**RepoMaster (Wang et al., NeurIPS 2025).** Uses AST walks to extract module/class/function structure for code understanding. The AST-walk pattern is the same primitive we'd build: one parser per language, applied repository-wide.

**GraphCode2Vec (Yefet et al., ORBilu 2022).** Uses Soot's Jimple intermediate representation (a three-address-code form of Java bytecode) as a normalized substrate for code embedding. Jimple itself is a semantic-preserving normalization for JVM languages: it strips syntactic sugar and reduces multiple Java constructs to a common form. The lesson: language-specific intermediate representations are pre-built normalizations we can use rather than implementing transformations ourselves.

**Perera et al. (IEEE 2026).** Indirectly supports this: their best-performing model (CodeExecutor) operates on syntactically diverse but semantically equivalent inputs effectively. Suggests that input normalization is one valid path; alternative model selection is another.

## Implementation sketch

Per-language. Start with the languages most common in spoon's target audience: Go, Python, JavaScript/TypeScript.

```
internal/
  normalize/
    normalize.go       NEW — Normalizer interface, language-detection dispatch
    go.go              NEW — Go normalizer using golang.org/x/tools/go/ast
    python.go          NEW — Python normalizer (subprocess to a Python script using `ast`)
    js.go              NEW — JavaScript/TypeScript normalizer using esbuild's parser
    fallback.go        NEW — text-only normalization for unsupported languages
```

```go
package normalize

type Normalizer interface {
    // Normalize takes a diff hunk (in unified-diff format) and returns the same
    // diff with semantic-preserving transformations applied to the changed lines.
    // Failure to parse falls through to a text-only normalizer.
    Normalize(ctx context.Context, language string, diff string) (string, error)
}

// Transformations applied (per Corder):
//   - Variable renaming: hash each identifier deterministically.
//     "userId" → "ID_3f4a", "name" → "ID_8b91", etc.
//     Hash is stable within a single diff (so "userId" appears twice with the
//     same placeholder), but different forks producing the same identifier
//     get different hashes (preventing accidental cross-fork merging).
//   - Statement permutation: in basic blocks, sort independent statements
//     lexically. Requires data-flow analysis to detect independence.
//   - Loop canonicalization: rewrite "for(init;cond;upd){body}" as
//     "while(cond){body; upd}" with init lifted. Equivalent on the AST.
//   - Switch-to-if: convert switch statements with no fallthroughs to
//     if/else-if chains.
//
// Dead-code removal is NOT applied — distinguishing "intentional debug aid"
// from "unused statement" is unreliable.
```

For Go we get an AST for free via stdlib. Python and JS require subprocess invocations (or embed a JS parser via WASM, or a Python parser via something like `tree-sitter`).

### Pipeline placement

Insert into `internal/embed/BuildFeatures`:

```
BuildFeatures(t2, readme, maxDiffChars) → ForkFeatures:
  detect primary language from t2.PrimaryLanguage
  if normalizer for language exists:
    diffChunk = normalizer.Normalize(language, raw_diff)
  else:
    diffChunk = textOnlyNormalize(raw_diff)
  …
```

The normalized diff replaces the raw diff in the `DiffChunk` modality of the embedding. Other modalities (paths, commits, README) are unaffected.

## Cost analysis

| Metric | Per-fork |
| --- | --- |
| Go normalizer (in-process) | 5–20 ms per ~4 KB diff |
| Python normalizer (subprocess) | 200–500 ms (subprocess startup dominates) |
| JS normalizer (esbuild Go API) | 10–50 ms |
| Total normalization on 50 forks | 0.5–25 s depending on language mix |

Subprocess cost for Python is unfortunate but unavoidable without bundling a Python AST parser into the Go binary. Mitigate by batching: one subprocess invocation handles all Python forks in a single call.

## Acceptance criteria

1. **Variable-rename invariance.** Take a fork that renames `userID → userId` across 20 sites. Embed before and after normalization. The two embeddings should be much closer after (cosine > 0.95) than before (likely cosine 0.6–0.8).
2. **Statement-permutation invariance.** Synthetic fork: take an existing fork's diff, permute independent statements. Embed both. Cosine similarity > 0.92 after normalization.
3. **Loop-form invariance.** Synthetic: rewrite a `for` loop as a `while`. After normalization, the two should embed within cosine 0.95.
4. **No semantic drift.** A fork that genuinely changes behavior (adds an `if` check that wasn't there) should NOT be normalized to look like its no-op variant. Sanity test: 20 hand-picked "real change" forks; their normalized diffs should still embed substantially apart from the un-normalized parent.
5. **Cluster quality improvement.** On IBM/mcp-context-forge, intra-cluster cosine distance should decrease by ≥ 15% (compared to v1 text-only normalization) without inflating cluster count by more than 10%.
6. **Robustness.** Malformed code (e.g., partial diffs that don't parse) falls through to text-only normalization without crashing the pipeline.

## Risks and open questions

- **Per-language coverage.** Even covering top 5 languages by GitHub volume leaves 30%+ of forks normalized only by text rules. The CHANGELOG should document supported languages clearly.
- **Parser hangups on adversarial inputs.** A maliciously-crafted diff could trigger pathological parsing. Mitigation: hard 5-second timeout per normalization call; fall back to text-only on timeout.
- **Hash collisions in identifier renaming.** Two distinct identifiers hashing to the same placeholder collapses information. Use a long hash (8 hex chars from a salt-keyed SHA) to make collisions negligible.
- **Salt management.** The salt that keys identifier hashing should be stable across runs of the same repo (so cached centroids remain valid) but different across repos (so identifier patterns don't leak between repos). Salt = SHA256(upstream_repo_name) is a reasonable choice.
- **Statement-permutation correctness.** Detecting independent statements requires data-flow analysis. Getting this wrong (declaring dependent statements independent) breaks semantic preservation. Lean conservative: only permute when both statements are local assignments to non-overlapping variables with no side effects.

## How to complete

1. **Start with Go.** Use `go/ast` and `go/types`. Build the four transformations in `internal/normalize/go.go`. Tests on hand-curated diff fixtures covering each transformation.
2. **Validation experiment.** Hand-curate 20 pairs of "semantically equivalent" Go forks (real or synthetic). Verify embeddings cluster tighter after normalization.
3. **Add JavaScript via esbuild's Go API.** esbuild already exposes its parser; transformations adapt naturally to its AST shape.
4. **Add Python via a small bundled Python helper script** invoked as a single batched subprocess. Document the Python dependency.
5. **Add language fallback layer** that tries text-only normalization for everything else. The same `Normalizer` interface contract.
6. **Wire into `BuildFeatures`** as the first transformation step on `DiffChunk`.
7. **Benchmark.** Measure embedding cost + normalization cost end-to-end against v1. The normalization should add < 30% wall-clock; if it adds more, optimization is needed before merging.
8. **Document supported languages and known limitations** in the README.

## References

- Bui, N. D. Q., Yu, Y., & Jiang, L. (2021). *Self-Supervised Learning for Code Retrieval and Summarization through Semantic-Preserving Program Transformations.* arXiv:2009.02731v4.
- Yefet, N., Alon, U., & Yahav, E. (2022). *GraphCode2Vec: Generic Code Embedding via Lexical and Program Dependence Analyses.* ORBilu, University of Luxembourg.
- Wang, H., Ni, Z., Zhang, S., et al. (NeurIPS 2025). *RepoMaster: Autonomous Exploration and Understanding of GitHub Repositories for Complex Task Solving.*
- Perera, A., Pillai, B., Tharani, J. S., Rao, A. S., & Muthukkumarasamy, V. (2026). *CodeBERT-Based Embeddings for Detecting Vulnerable Smart Contracts.* IEEE.
