# Future Work: Continual Category Discovery for Cached Fork Clusters

**Status:** Deferred from v1.
**Parent plan:** `plan-integrating-this-research-declarative-puddle.md`
**Estimated effort:** 1–2 weeks.

## Context

V1 of spoon's fork clustering re-clusters the entire enriched fork batch every time a user invokes `spoon` on a repo (modulo the 24h JSON cluster cache, which is invalidated wholesale on `--refresh`). This works at small scale but wastes work when the user is iterating: they fetch yesterday's clusters again, and a few new forks have appeared. Re-clustering everything from scratch is expensive — the embedding pass is the dominant cost.

Continual Category Discovery (CCD) — a setting in the GCD literature — addresses exactly this case: existing categories should persist, only new data should be processed, and the model should detect when genuinely new categories emerge.

## Origin of the idea

The original Structural Intelligence in the Open World survey provided as research input names CCD explicitly: "A learning agent in a real-world software repository will be exposed to a continuous sequence of tasks, each potentially containing new frameworks or code patterns. C-GCD systems must be able to identify novel classes from unlabeled data while retaining knowledge of known classes over time, without the luxury of retraining on the entire historical dataset."

The same paragraph nails the failure mode our cache currently has: "representation drift" — when you retrain (re-cluster) from scratch, the cluster IDs and centroids shift even for forks that didn't change. Users watching the same repo over weeks see arbitrary cluster-ID churn, which is alarming and unhelpful.

CCD says: keep the centroids, assign new forks to existing clusters when they fit, only spawn a new cluster when they genuinely don't.

## Research basis

**Category Discovery: An Open-World Perspective (arXiv:2509.22542v1).** Survey article that defines CCD and contrasts it with one-shot GCD. The key shift: CCD systems must dynamically update classifier weights and prototypes while maintaining consistent feature alignment to prevent representation drift.

**ProtoGCD: Unified and Unbiased Prototype Learning for Generalized Category Discovery (Computer Society, 2025).** Argues for parametric prototypical classifiers that handle known + novel classes in a unified feature space. The prototype-based formulation is directly portable: persist cluster centroids; new forks get assigned to the nearest centroid above a threshold, or spawn a new cluster.

**Deep Aligned Clustering (DAC) and DAPL** (cited in the same survey). Filtering mechanisms remove inconsistent or low-confidence pseudo-labels during continual updates, mitigating the confirmation bias that plagues self-trained clustering.

## Implementation sketch

The pivot: v1's `~/.cache/spoon/clusters/<provider>/<owner>__<repo>.json` cache becomes a richer artifact carrying not just per-fork assignments but per-cluster centroids (already partially there in our v1 schema). Re-clustering becomes a soft operation:

```go
package cluster

// Reassign takes existing clusters + new fork embeddings and produces an
// updated cluster set. Existing centroids stay put unless their composition
// changes. Members of a vanished fork (deleted upstream) are removed.
//
// Algorithm (per ProtoGCD / DAC):
//   1. For each new fork embedding, compute nearest centroid distance.
//   2. If distance < epsilon, assign to that cluster; nudge centroid toward
//      the new point (weighted average with member-count weight).
//   3. If distance >= epsilon for all centroids, mark as orphan.
//   4. Run a density step over orphans: if >= minClusterSize orphans cluster
//      together (single-link agglomerative at epsilon), spawn a new cluster.
//   5. Remaining orphans below minClusterSize → noise (novelty 1.0).
//
// Cluster IDs are stable: existing clusters keep their IDs. New clusters get
// monotonically-increasing IDs (c0, c1, ... cN, with N tracked in the cache).
func Reassign(existing []Cluster, newPoints []Point, opts ReassignOptions) ([]Cluster, []Assignment)

type ReassignOptions struct {
    Options                    // same epsilon, minClusterSize as full Cluster
    CentroidNudgeAlpha float64 // 0.0 = freeze centroids, 1.0 = full update. Default 0.3.
    MaxDrift           float64 // re-cluster from scratch if any centroid moves > MaxDrift
                               // across a single Reassign call. Default 0.2.
}
```

`MaxDrift` is the safety valve: if too many forks "fit but only barely", the centroids drift far, the cache becomes lies, and we trigger a full re-cluster.

### Cache schema bump

```json
{
  "schemaVersion": 2,
  "computedAt": "2026-05-10T...",
  "lastReassignedAt": "2026-05-15T...",
  "embedderModel": "nomic-embed-text",
  "clusters": [
    {"id": "c0", "centroid": [...], "memberFingerprints": [...], "stableSince": "2026-05-10T..."}
  ],
  "noiseFingerprints": [...]
}
```

`memberFingerprints` lets us detect "vanished" members on the next run without storing full embeddings.

### Trigger points

- On every `spoon` invocation: load cache, fetch current fork list, compute fingerprint set, compute set diff. If `|new| + |vanished|` < 20% of `|total|`, run `Reassign`. Else full re-cluster.
- `--refresh` always full re-clusters.
- New flag `--no-reassign` forces full re-cluster without invalidating other caches.

## Cost analysis

| Operation | V1 | V2 (CCD) |
| --- | --- | --- |
| Cluster pass on 50 forks, all new | ~25 s | ~25 s (same) |
| Cluster pass on 50 forks, 47 cached | ~25 s | ~2 s (3 new embeddings) |
| Cache size on 600-fork repo | ~50 KB | ~80 KB (+ centroids) |

Big win is on warm runs against active repos, where the user reruns `spoon` daily and only a handful of new forks appear.

## Acceptance criteria

1. **Stability.** Run `spoon <repo>` twice in a row with no upstream activity. Cluster IDs must be identical. Centroids must be byte-identical (no drift from numeric noise).
2. **Continuity.** Add one new fork to a stub repo (or simulate via fixture). Re-run. The new fork should join the nearest existing cluster, the cluster ID should not change, the rest of the assignments should be byte-identical.
3. **Spawn correctness.** Add three new forks that are far from any existing centroid. Re-run. A new cluster `cN+1` should appear; existing IDs unchanged.
4. **Drift guard.** Inject 30% new forks that all fall near a single centroid. The centroid nudge should not push past `MaxDrift`; otherwise the system should auto-trigger a full re-cluster and emit a stderr note.
5. **Correctness under deletion.** Delete forks from the upstream. Re-run. The vanished forks should drop from `memberFingerprints`. Clusters that fall below `minClusterSize` should dissolve (members reclassified as noise or absorbed into the nearest surviving cluster).

## Risks and open questions

- **Centroid drift over months.** Even tiny per-run nudges accumulate. Solution: time-bounded reassign — once a cluster's `stableSince` is > 30 days old, trigger a full re-cluster on that cluster's neighborhood.
- **Cache poisoning.** A corrupted cache (interrupted write, schema mismatch) would propagate forever. Mitigate with a checksum field and graceful fallback to full re-cluster on validation failure.
- **Multi-machine sync.** Two devs running spoon on the same repo with different cache states will diverge. Not solving in this iteration; cache stays per-machine.
- **Adversarial forks.** A coordinated set of similar fork-farm forks added daily could keep nudging a centroid into a corner. The drift guard catches the worst case.

## How to complete

1. **Bump cache schema** to v2 with backward-compat loader that recomputes centroids from the v1 assignment file the first time.
2. **Implement `Reassign`** in `internal/cluster/reassign.go`. Pure function, no IO. Tests with synthetic embeddings covering: all-new, all-cached, mixed, deletion, drift-trigger.
3. **Wire decision logic into `internal/dump/dump.go`.** Compute fork-set diff before deciding `Cluster` vs `Reassign`.
4. **Add stability tests.** Two-run determinism on a fixture repo.
5. **Add user-facing notes:** in TUI cluster-view header, show "5 new forks since last run" when `Reassign` is the path taken.
6. **Document the new flag** `--no-reassign` and the auto-fallback to full re-cluster.

## References

- *Category Discovery: An Open-World Perspective.* arXiv:2509.22542v1.
- *ProtoGCD: Unified and Unbiased Prototype Learning for Generalized Category Discovery.* IEEE Computer Society, 2025.
- *Deep Aligned Clustering (DAC) and DAPL,* as cited in the GCD survey above.
