# Future Work: Cross-Repo Cluster Lineage

**Status:** Deferred from v1; depends on continual-cluster-updates landing first.
**Parent plan:** `plan-integrating-this-research-declarative-puddle.md`
**Estimated effort:** 3–4 weeks (substantial new infrastructure).

## Context

V1 of spoon clusters forks *within a single upstream repository*. The same archetype — say, "OAuth provider plugins" — almost certainly recurs across multiple parent projects: MCP gateways, LangChain integrations, SaaS auth wrappers, etc. A user analyzing one repo never sees this. Cross-repo cluster lineage would let spoon recognize "this fork's archetype is the same as cluster c3 from repo X you analyzed last week."

## Origin of the idea

The user's research input — the *Structural Intelligence in the Open World* survey — has a section on Federated Category Discovery (FCD): "FCD allows multiple clients to collaboratively discover new categories without sharing their raw data. This decentralized approach focuses on identifying 'global' novel categories that appear across multiple sites, providing a broader view of emerging trends while respecting data regulatory practices."

That's an industrial setting (multiple companies' private repos). Translated to spoon's single-user-multi-repo setting: the user *is* multiple "clients" across time, analyzing different upstream repos. The same archetype should be detectable across these analyses.

## Research basis

**Category Discovery: An Open-World Perspective (arXiv:2509.22542v1).** Section on FCD frames cross-source discovery as identifying global categories that appear across multiple sites. The federated formulation is overkill for our single-user case, but the underlying mechanic — comparing cluster centroids across runs/sites to detect lineage — translates directly.

**ProtoGCD (IEEE Computer Society, 2025).** Prototype-based representation enables direct similarity comparison: two clusters from different repos with cosine-close centroids represent the same archetype, given the same embedder produced both.

**Immersion in the GitHub Universe: Scaling Coding Agents to Mastery (ResearchGate, 2025).** Makes the case for "cross-repository understanding" as a foundational capability for code agents, observing that the same patterns recur across repositories and that surfacing this recurrence is high-value.

## Implementation sketch

A new top-level cache at `~/.cache/spoon/lineage/global.json` accumulates cluster centroids from every cluster pass spoon has run, tagged with provenance:

```json
{
  "schemaVersion": 1,
  "embedderModel": "nomic-embed-text",  // lineage is invalid across model swaps
  "clusters": [
    {
      "lineageId": "L0042",
      "centroid": [0.12, -0.04, ...],
      "label": "OAuth provider plugins",
      "originRepo": "IBM/mcp-context-forge",
      "originClusterId": "c3",
      "occurrences": [
        {"repo": "IBM/mcp-context-forge", "clusterId": "c3", "memberCount": 5, "firstSeen": "2026-05-10T..."},
        {"repo": "anthropics/mcp-servers", "clusterId": "c1", "memberCount": 3, "firstSeen": "2026-05-15T..."}
      ]
    }
  ]
}
```

### Algorithm

After per-repo clustering completes (v1 or CCD), each local cluster centroid is matched against the global lineage table:

```go
package lineage

// MatchOrCreate compares the cluster's centroid against the global lineage table.
// Returns the lineage ID if a close match exists (cosine < epsilon_lineage, default
// 0.20 — tighter than the in-repo clustering epsilon since we're matching across
// noisier conditions). Otherwise creates a new lineage entry.
func MatchOrCreate(local cluster.Cluster, repo string) (lineageId string, isNew bool)

// LineageOptions tunes the matching.
type LineageOptions struct {
    EpsilonLineage      float64  // default 0.20
    MinMemberCount      int      // skip lineage matching for very small local clusters (default 2)
    DemoteIfOnlyOnce    bool     // local clusters with no global match remain "private" unless
                                 // they show up again in another repo within OccurrenceTTL
    OccurrenceTTL       time.Duration  // default 90 days
}
```

### Surfacing

- **TUI cluster view**: under each cluster label, if a lineage ID exists with ≥ 2 occurrences, render a chip: `↻ L0042 — also in IBM/mcp-context-forge, anthropics/mcp-servers (2 repos)`.
- **JSON output**: add `lineage` block per cluster: `{"lineageId":"L0042","occurrences":[...]}`.
- **New subcommand** `spoon lineage list` shows all known archetypes globally, ranked by occurrence count. `spoon lineage show <L-id>` opens a detail view.
- **New flag** `--no-lineage` disables global cache writes/reads.

### Privacy

The lineage cache is local to the user's machine. No network calls. Users who don't want their analyses correlated should use `--no-lineage` or delete the cache. This is documented in README + `spoon lineage --help`.

## Cost analysis

| Resource | Estimate |
| --- | --- |
| Per-run overhead | ~50 ms (centroid matching against global table) |
| Global cache size after 100 repos analyzed | ~2 MB |
| Disk IO | One read, one write per spoon invocation |

Embedding-time cost: zero (we already have centroids from the local clustering step).

## Acceptance criteria

1. **Recurrence detection.** Analyze two repos known to share an archetype (e.g., two MCP server projects with OAuth plugin forks). Both runs should produce a cluster, and on the second run, that cluster's `lineageId` should match the first run's.
2. **No false matches.** Analyze two repos with non-overlapping archetypes (e.g., a web framework and a data-science library). Their clusters should NOT share lineage IDs.
3. **Model-swap invalidation.** Change `--embedder-model` and re-run. Lineage entries from the old model should be marked invalid and not matched against. New entries should accumulate under the new model.
4. **TTL eviction.** Lineage entries last touched > 90 days ago should be evicted unless they have ≥ 3 occurrences (i.e., established archetypes survive; one-offs decay).
5. **Lineage subcommand parity.** `spoon lineage list` returns the same data shown in the TUI lineage chips.

## Risks and open questions

- **Centroid drift across versions.** Embedder updates, modifications to multimodal weighting, or seed changes shift the latent space. Solution: store the full set of `Options` used to produce each centroid (embedder model, modality weights, normalization version) and only match centroids produced under identical options. Mismatched options trigger a re-embed (expensive) or a skip (cheap, lossy).
- **Privacy implication.** The lineage cache reveals what repos the user has analyzed and when. Even local-only, this is a meaningful surface. Make it opt-out (default-on for the feature itself, but with very visible `--no-lineage` documentation).
- **Naming churn.** If two runs produce slightly different heuristic labels for the same lineage, which wins? Solution: lineage's label is the *most recent* one; the older labels are kept in occurrence history.
- **Scale.** A user analyzing thousands of repos accumulates a large lineage table. Eventually a vector index (faiss or HNSW) would be needed. Out of scope for v1 of this feature.

## How to complete

1. **Land continual-cluster-updates first.** Lineage matching only makes sense once cluster centroids are stable across runs.
2. **Implement `internal/lineage/`** with: matcher (`MatchOrCreate`), cache loader/saver (`Load`, `Save` to `~/.cache/spoon/lineage/global.json` with atomic-write semantics).
3. **Add provenance tracking** to centroid generation: every centroid carries the embedder model + options hash. The matcher rejects mismatches.
4. **Wire into the post-clustering step.** After `cluster.Cluster` or `cluster.Reassign`, iterate clusters, call `MatchOrCreate`, attach lineage IDs.
5. **Surface in TUI.** Add a lineage chip renderer in `internal/tui/cluster_view.go`.
6. **Surface in JSON / spn output.** Add `lineage` field; document in spn schema docs.
7. **Build `spoon lineage list/show` subcommand.** Simple operations over the global cache.
8. **Privacy doc.** README section explaining what the lineage cache stores, where it lives, and how to opt out.
9. **Validation.** Curate 3 pairs of repos known to share archetypes; verify lineage matching works end-to-end.

## References

- *Category Discovery: An Open-World Perspective.* arXiv:2509.22542v1.
- *ProtoGCD: Unified and Unbiased Prototype Learning for Generalized Category Discovery.* IEEE Computer Society, 2025.
- *Immersion in the GitHub Universe: Scaling Coding Agents to Mastery.* ResearchGate, 2025.
