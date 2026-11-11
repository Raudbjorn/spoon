# Unified System Architecture & Redesign

**`spoon` (`spn`)** is an offline-first, deterministic Go CLI and TUI utility designed to triage, score, cluster, and inspect large fork ecosystems and pull request threads across code forges. Reading across the specifications — spanning May 2026 to September 2026 — reveals a system that has evolved rapidly, leaving behind architectural drift between older v1 specs (`~/.cache/spoon/*.json`, Ollama-only embedders) and newer production reality (`spoon.db` SQLite storage, in-process FastEmbed/lexical embedders, batched GraphQL divergence passes, and shipped `internal/mdg`).

This document separates historical specifications from proposed implementation work. Source baseline: PR #146, initially `a849a75`, with the review corrections described below. The package map and algorithms are targets unless explicitly marked implemented.

| Area | Implemented baseline | Remaining target work |
| --- | --- | --- |
| Embedding | `internal/embed/backend.go` selects FastEmbed (semantic default) or builtin lexical; `openvino` is rejected. `Embedder` exposes `Embed` and `Dim`. | Geometry fingerprints and safe incremental reuse; OpenVINO is historical/experimental, not a shipped backend. |
| Storage | Migration v6 in `internal/store/store.go`, plus `clusters.go` and `units.go`, provides clustering, lineage, and unit tables/APIs. | Wire the clustering pipeline to these tables; the existing cluster cache still uses JSON. |
| Cached divergence | Cached forks participate in batching. Review corrections gate requests on reserve and refresh only matching comparison evidence; changed comparisons replace cached T2. | Durable refresh policy and batching last-touch lookups. |
| Workspace discovery | `resolveGitDir` already handles worktrees/submodules; origin/upstream discovery exists. Review corrections preserve host/provider, report failures, encode branch filters, and scan siblings from checkout subdirectories. | Further discovery features require separate scope. |
| Clustering/attribution/summaries | Storage foundations do not implement `Reassign`, cross-repo prototype matching, or the proposed attribution/summarization pipeline. | Implement and validate the algorithms below. |

---

## Part 1: Architectural Audit & Critical Fixes

### 1. Storage Split-Brain: JSON Cache Files vs. `spoon.db` SQLite

* **The Flaw:** The earlier May 2026 specs (`continual-cluster-updates`, `cross-repo-lineage`, `full-mdg-centrality`) design persistence around flat JSON files in `~/.cache/spoon/clusters/<provider>/<owner>__<repo>.json`, `~/.cache/spoon/lineage/global.json`, and `~/.cache/spoon/mdg/...`. However, the September 2026 spec (`touching-unit-attribution`) reveals that `spoon` has migrated to a relational SQLite database (`spoon.db`) containing tables like `compare_files`, `commit_files`, and `T2Present` replacement rules. Storing float32 centroid vectors in JSON files while relational fork metadata lives in SQLite creates cache invalidation race conditions, broken foreign keys, and unnecessary JSON serialization overhead.

* **The Fix:** Consolidate **all** state into `spoon.db` (or a dedicated `lineage.db` for cross-repo state) using SQLite in WAL mode. Store vectors as raw little-endian `FLOAT32` byte blobs (`[]byte`), eliminating JSON float formatting drift and guaranteeing byte-identical centroid persistence.

### 2. Fatal Logic Bug in `normalize`: Per-Fork Salted Identifier Hashing

* **The Flaw:** Even though `future-work-semantic-preserving-normalization.md` was marked `ABANDONED` in June 2026, it contains a severe conceptual bug worth calling out so it never creeps into `builtin-lexical-v1` or future tokenizers:
  * In the code comment: *"different forks producing the same identifier get different hashes (preventing accidental cross-fork merging)"*.
  * In the Risks section: *"Salt = SHA256(upstream_repo_name) is a reasonable choice"*.
  * Not only do those two sections contradict each other, the code comment's design **destroys clustering**. The entire point of fork clustering is to group forks that make semantically equivalent changes. If Fork A and Fork B both write `userId := 1`, and you salt the identifier hash with the *fork's* name (`ID_3f4a` in Fork A, `ID_9c01` in Fork B), their normalized diffs will diverge in token space and fail to cluster together.

* **The Fix:** If token normalization or canonicalization is ever applied in `internal/embed/local.go`, identifier replacement must be **positional per basic block/hunk** (`VAR_1`, `VAR_2` in order of appearance within the function/hunk) or keyed strictly to `SHA256(upstream_repo)`. Never salt embeddings with fork-specific identifiers.

### 3. Math & Algorithmic Flaws in Continual Category Discovery (`Reassign`)

* **The Flaw 1 (Spherical Geometry):** `future-work-continual-cluster-updates.md` updates centroids using a weighted linear average (`CentroidNudgeAlpha`) in Euclidean space, while `cross-repo-lineage` and `behavioral-embeddings` measure similarity using **Cosine Distance**. A linear average of unit vectors does not lie on the unit hypersphere; over repeated `Reassign` passes, unnormalized centroids shrink toward the origin, distorting cosine thresholds (`epsilon`).
* **The Flaw 2 (Order-Dependent Alpha Nudging):** Using a fixed exponential moving average (`CentroidNudgeAlpha = 0.3`) per new point means the final centroid depends on the *iteration order* of `newPoints`, violating the deterministic guarantee of `spoon`. Furthermore, the code comment says *"weighted average with member-count weight"* while the struct defines a fixed `CentroidNudgeAlpha = 0.3` — those are two incompatible formulas.
* **The Flaw 3 (Chaining Effect in Orphan Clustering):** Step 4 specifies *"single-link agglomerative at epsilon"* over orphans. Single-linkage clustering is notorious for the chaining effect (two unrelated clusters bridged by a single intermediate noise fork).

* **The Fix:**
  1. Preserve the unnormalized sum $S = \sum_{v \in members} v$, or reconstruct it from the stored member embeddings in stable fork-key order. A unit centroid plus member count cannot recover $S$: for `(1,0)` and `(0,1)`, the sum is `(1,1)`, while twice the unit centroid is `(sqrt(2),sqrt(2))`. Apply removals and additions to the sum:
     $$S_{\text{new}} = S_{\text{old}} - \sum_{v \in removed} v + \sum_{v \in added} v,\qquad \mu_{\text{new}} = \frac{S_{\text{new}}}{\|S_{\text{new}}\|}$$
     Do not multiply a normalized centroid by member count. Reconstruction from `cluster_members.embedding` avoids adding a sum column to migration v6. Empty clusters dissolve; a zero/near-zero sum has no direction and triggers full clustering/noise handling. Use stable accumulation order for floating-point reproducibility; mathematical commutativity alone does not guarantee identical floating-point results.
  2. Measure `MaxDrift` as cosine distance $1 - (\mu_{\text{old}} \cdot \mu_{\text{new}})$ against the *original* `stableSince` anchor centroid (`anchor_centroid`), not just per-run delta, preventing slow "boiling-the-frog" drift over 20 consecutive small runs.
  3. Replace single-link agglomerative clustering on orphans with **complete-linkage** or **average-linkage (UPGMA)** at $\epsilon$, matching spherical cluster geometry.

### 4. Flawed FCG `CallImpact` Aggregation & Missing Patch Context

* **The Flaw 1 (Dilution Bias):** `future-work-fcg-fork-analysis.md` defines `CallImpact` as the *"mean score over all touched lines, weighted by line count in each function"*. Under this formula, a fork that makes a critical 2-line security fix in a core function (`CallCount = 80`) AND adds a 200-line unit test or helper function (`CallCount = 0`) has its `CallImpact` diluted by $100\times$, scoring near zero!
* **The Flaw 2 (Upstream-Only Graph Blindness):** `fcg.Build` only parses the *upstream* repo at `BaseSHA`. If a fork adds a brand-new function or modifies lines that shift line numbers relative to upstream, mapping fork-touched lines directly to `FunctionAtLine(file, line)` using post-image line numbers will index into the wrong upstream function.
* **The Flaw 3 (Missing Hunk Data Assumption):** The FCG doc states *"Spoon's existing `T2Data.Files` includes files but not specific line ranges of changes"*, whereas `future-work-touching-unit-attribution.md` clarifies that `compare_files.patch` is **already stored in `spoon.db`** for every live-fetched fork.

* **The Fix:**
  1. Parse the patch body with separate old/new line cursors. Map only removed (`-`) lines to upstream declarations; unchanged context advances the cursors but is not an edit. Insertions (`+`) require post-image spans or an explicitly qualified insertion-boundary candidate; a whole hunk range is not a touched-declaration set.
  2. Change `CallImpact` aggregation from a line-weighted mean to a **soft-max / top-$K$ decayed sum** (e.g., $\max_{f \in \text{Touched}} \text{Score}(f) + 0.25 \sum_{f \neq f_{\max}} \text{Score}(f)$ clamp to $[0, 1]$). Adding tests or docs alongside a core fix must *never* reduce the impact score of the core fix.

### 5. Embedder Architecture Drift across Specs

* **Historical specification drift:** Older documents mention Ollama and OpenVINO. The implemented selector in `internal/embed/backend.go` supports `fastembed` and `builtin`/`lexical`, and rejects `openvino`. `internal/embed/fastembed.go` uses ONNX Runtime; no production `openvino.go` backend exists.

* **Target:** Add a deterministic geometry identity covering backend/model revision, normalization, modality weights, and feature construction. For `builtin-lexical-v1`, `internal/embed/local.go` computes IDF within each `Embed` call: its geometry identity must also include the complete input corpus and resulting IDF state. Embed the complete lexical corpus together after any addition, removal, or changed input, then rebuild assignments and anchors. Never mix vectors from different calls/corpora. Incremental per-fork reuse and cross-repo lineage require a corpus-independent embedding space; disable cross-repo lineage for the current lexical backend.

### 6. Git Worktree / Submodule Parsing Bug in `threadsops.ScanWorkspace`

* **The Flaw:** `future-work-multi-repo-pr-discovery.md` states: *"Treat the `core.worktree`-pointing forms (worktrees, submodules) the same as a normal repo — `.git/HEAD` and `.git/config` still resolve"*. In Git worktrees and submodules, `.git` is a **plain text file** containing `gitdir: /path/to/main/.git/worktrees/feature-a`, NOT a directory. Calling `os.ReadFile(filepath.Join(dir, ".git", "HEAD"))` will fail with `ENOTDIR`. Furthermore, in a linked worktree, `HEAD` lives in the worktree's `gitdir`, while `remote.origin.url` lives in `commondir/config` (`../..`), not the worktree dir!

* **Already implemented:** `resolveGitDir` in `internal/threadsops/workspace.go` checks `os.Stat(dotGit)` and returns the Git and common directories:
  * If directory: `headFile = .git/HEAD`, `configFile = .git/config`.
  * If regular file: parse `gitdir: <path>` (resolving relative paths against `repoDir`), set `headFile = <gitdir>/HEAD`, and check `<gitdir>/commondir` to locate the shared `config` file.

---

## Part 2: Unified System Architecture

### Target System Topology & Package Map (not an inventory of shipped files)

```
cmd/
  spoon/ (alias: spn)
    main.go               # CLI entrypoint & subcommand router
    forks.go              # `spn forks list [--touching] [--touching-units] [--full-mdg]`
    threads.go            # `spn threads [--interactive] [--scan-depth]`
    lineage.go            # `spn lineage list | show <L-id>`
    report.go             # `spn report <repo> [--summary-top N]`
internal/
  forge/                  # URL parsing (`ParseRepoURL`), provider abstraction (GitHub/GitLab)
  github/
    batch_compare.go      # GraphQL Phase A `{aheadBy behindBy}` + history(first:1, path:)
    compare_diff.go       # REST compare + unbounded `.diff` fallback (>300 files)
    last_touch.go         # Batched GraphQL history + tree-commit-info fallback
    pulls.go              # ListOpenPRs, FindOpenPRForHead
  store/
    store.go              # `spoon.db` (WAL mode): forks, T2, compare_files, clusters, lineage, units
  unidiff/                # Streaming unified diff parser & hunk-header extractor
  forksops/
    stream.go             # Pre-dispatch GraphQL batch, cache hydration, T2 pipeline
    lasttouch.go          # Containment gate & last-touch skip evaluator
    touching.go           # Literal, glob, and `re:<regex>` path matcher + Tier 0/1 unit attribution
  threadsops/
    workspace.go          # Worktree-aware `ScanWorkspace` + concurrent `DiscoverPRsInWorkspace`
  ast/                    # Shared AST & Tree-Sitter Layer (Go stdlib, Python tree-sitter, JS/TS esbuild)
    mdg.go                # Module Dependency Graph + EntryPoint-biased Personalized PageRank
    fcg.go                # Function Call Graph + interval-tree `FunctionAtLine` + `CallImpact`
    units.go              # Top-level declaration & exported registry key extractor (for --touching-units)
  embed/
    embed.go              # `Embedder` interface + deterministic `Fingerprint()`
    local.go              # `builtin-lexical-v1` (zero-dependency fast default)
    fastembed.go          # Implemented in-process FastEmbed / ONNX Runtime backend
  cluster/
    cluster.go            # Spherical clustering + `WeakSignals` (ChangeImpact, CallImpact)
    reassign.go           # Continual Category Discovery (order-independent spherical updates)
    lineage.go            # Cross-repo prototype matching (`MatchOrCreate`)
    summarize.go          # 3-stage Hierarchical Summarizer (Fork -> Cluster -> Repo) + Hallucination Gate
  tui/
    forks_view.go         # Heat table, touched units, touched functions, `s`/`o` summary views
    cluster_view.go       # Cluster view + CCD delta banner + `↻ L0042` lineage chips
    threads/picker.go     # Multi-repo aware PR picker
```

### End-to-End Execution Pipeline (`spn forks`)

```
[1. Discovery & Pre-Dispatch Batch (internal/forksops + internal/github)]
   ├─ Fetch Fork List (T1 metadata)
   ├─ Load CachedT2 from `spoon.db`
   ├─ Run GraphQL batches for eligible forks (including CachedT2), checking reserve before each request
   │    ├─ Refresh matching branch/head/ahead/provenance only; replace zero-ahead T2 or refetch changed comparisons
   │    ├─ If `--touching <literal>`: batch `history(first:1, path:)` in same GraphQL query
   │    └─ Prune forks failing containment gate or matching upstream path SHA
   └─ Fetch T2 Compare for cache-misses / invalidated forks
        └─ If files == 300 (truncated) AND (`--touching` or `--touching re:` active):
             └─ Stream raw `.diff` via `compare_diff.go` + `internal/unidiff`

[2. Structural Code Analysis (internal/ast — Shared Single-Pass AST)]
   ├─ Keyed by `(repo, upstream_head_sha)` in `spoon.db`
   ├─ Parse repo once per language (Go `go/ast`, Python `tree-sitter`, JS/TS `esbuild`)
   │    ├─ Emit Module Imports ──► Build MDG ──► Personalized PageRank (`ChangeImpact`)
   │    └─ Emit Func Spans/Calls ──► Build FCG (Interval Tree) ──► Softmax `CallImpact`
   └─ If `--touching` active:
        ├─ Tier 0 (0 API calls): Extract hunk headers (` @@ ... @@ <ctx>`) + FCG enclosing funcs
        └─ Tier 1 (`--touching-units`, budgeted): Fetch BaseSHA/HeadSHA blobs, diff declarations

[3. Multimodal Embedding & Continual Clustering (internal/embed + internal/cluster)]
   ├─ Build ForkFeatures (paths, commits, README delta, diff chunk, WeakSignals)
   ├─ Fixed geometry: reuse unchanged feature vectors; lexical: embed the full corpus after any input change
   ├─ Compare fork-set diff against `clusters` table in `spoon.db`:
   │    ├─ If |new| + |vanished| < 20% and age < 30d and Drift <= MaxDrift:
   │    │    └─ Run `cluster.Reassign` (spherical batch update + complete-link orphan step)
   │    └─ Else:
   │         └─ Run full `cluster.Cluster`
   └─ Match centroids only within the same corpus-independent geometry (`MatchOrCreate` at ε = 0.20)

[4. Hierarchical Summarization (internal/cluster/summarize.go — Opt-In)]
   ├─ Stage A: Top-N forks (`--summary-top 20`) ──► 1-sentence grounded blurb
   ├─ Stage B: Per-cluster aggregation ──► 2-3 sentence archetype summary
   ├─ Stage C: Repo-level aggregation ──► 4-6 sentence ecosystem narrative
   └─ Post-Output Deterministic Validator: Strip/reject hallucinated fork handles or SHAs
```

---

## Part 3: Detailed Component Designs & Upgraded Specifications

### 1. Unified SQLite Schema (`internal/store/store.go`)

Migration v6 already provides the tables below, except the proposed `feature_fp` column. Add that column in a future append-only migration when wiring incremental embeddings; do not rewrite v6. Transactional pipeline integration and invalidation rules remain required:

```sql
-- 1. Continual Clustering & Provenance (Replaces ~/.cache/spoon/clusters/*.json)
CREATE TABLE IF NOT EXISTS cluster_runs (
    repo_key            TEXT PRIMARY KEY, -- e.g. "github.com/IBM/mcp-context-forge"
    embedder_fp         TEXT NOT NULL,    -- Geometry identity; lexical also includes corpus + IDF state
    computed_at         TEXT NOT NULL,
    last_reassigned_at  TEXT NOT NULL,
    next_cluster_seq    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS clusters (
    repo_key            TEXT NOT NULL,
    cluster_id          TEXT NOT NULL,    -- Stable ID: "c0", "c1", ...
    centroid            BLOB NOT NULL,    -- Normalized float32[] little-endian bytes
    anchor_centroid     BLOB NOT NULL,    -- Centroid at stable_since (for cumulative drift check)
    member_count        INTEGER NOT NULL,
    label               TEXT NOT NULL,
    summary             TEXT,             -- Populated by Hierarchical Summarizer Stage B
    lineage_id          TEXT,             -- Foreign key to global_lineage.lineage_id
    stable_since        TEXT NOT NULL,
    PRIMARY KEY (repo_key, cluster_id)
);

CREATE TABLE IF NOT EXISTS cluster_members (
    repo_key            TEXT NOT NULL,
    fork_key            TEXT NOT NULL,
    head_sha            TEXT NOT NULL,
    cluster_id          TEXT,             -- NULL = noise (novelty 1.0)
    feature_fp          TEXT NOT NULL,    -- Proposed: hash of every embedding input, not head SHA alone
    embedding           BLOB NOT NULL,    -- Reuse only under matching feature_fp and run embedder_fp
    PRIMARY KEY (repo_key, fork_key)
);

-- 2. Cross-Repo Lineage (Replaces ~/.cache/spoon/lineage/global.json)
CREATE TABLE IF NOT EXISTS global_lineage (
    lineage_id          TEXT PRIMARY KEY, -- "L0001", "L0002"
    embedder_fp         TEXT NOT NULL,
    centroid            BLOB NOT NULL,
    label               TEXT NOT NULL,
    origin_repo         TEXT NOT NULL,
    origin_cluster_id   TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    last_seen_at        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS lineage_occurrences (
    lineage_id          TEXT NOT NULL,
    repo_key            TEXT NOT NULL,
    cluster_id          TEXT NOT NULL,
    member_count        INTEGER NOT NULL,
    first_seen_at       TEXT NOT NULL,
    last_seen_at        TEXT NOT NULL,
    PRIMARY KEY (lineage_id, repo_key, cluster_id)
);

-- 3. Unit-Level Attribution (Survives T2Present replacement rules)
CREATE TABLE IF NOT EXISTS touched_units (
    fork_key            TEXT NOT NULL,
    path                TEXT NOT NULL,
    base_sha            TEXT NOT NULL,
    head_sha            TEXT NOT NULL,
    tier                INTEGER NOT NULL, -- 0 = hunk-context, 1 = AST declaration diff
    units_json          TEXT NOT NULL,    -- [{"name":"antipatterns","kind":"modified","added":18,"removed":0}]
    skipped_reason      TEXT,
    PRIMARY KEY (fork_key, path, base_sha, head_sha, tier)
);
```

**Embedding reuse contract:** For a corpus-independent backend, reuse a member vector only when `(fork_key, head_sha, feature_fp, embedder_fp)` matches. `feature_fp` covers all actual embedding inputs, including upstream-dependent diffs, README/features, and weak signals; an unchanged head alone is insufficient. Cached vectors can avoid repeated model inference during full clustering, but clustering still has a cost and no latency bound is established here.

For builtin lexical embeddings, the run's `embedder_fp` additionally identifies the whole corpus and its IDF geometry. Any corpus change invalidates every vector and centroid/anchor in that run: re-embed the complete corpus in one consistent geometry and fully cluster it. Do not use lexical centroids for cross-repository lineage. Migration v6 is only storage groundwork; it does not enforce this contract by itself.

---

### 2. Continual Category Discovery & Lineage (`internal/cluster/reassign.go`)

Here is the corrected, order-independent spherical `Reassign` algorithm:

```go
package cluster

// Reassign performs Continual Category Discovery over unit-normalized embeddings.
func Reassign(existing []Cluster, currentForks []ForkPoint, opts ReassignOptions) (Result, bool) {
    // 0. Require matching geometry; a changed lexical corpus triggers full embedding/clustering.
    // 1. Reconstruct each unnormalized sum from stored embeddings in stable fork-key order.
    // Subtract removed/changed members' old vectors, then normalize surviving sums
    // for assignment. Never reconstruct a sum as Centroid * MemberCount.
    
    // 2. Assign new/updated forks to nearest existing centroid (Cosine Distance < Epsilon)
    // Collect batch additions per cluster without mutating centroids mid-loop (order-independent).
    for _, pt := range newForks {
        bestCluster, minCosineDist := findNearestCentroid(existing, pt.Vec)
        if minCosineDist < opts.Epsilon {
            pendingAdditions[bestCluster] = append(pendingAdditions[bestCluster], pt)
        } else {
            orphans = append(orphans, pt)
        }
    }

    // 3. Apply spherical batch update and check cumulative drift against AnchorCentroid
    for i := range existing {
        if membershipChanged[i] { // Includes removal-only updates.
            sums[i] = addVectors(sums[i], pendingAdditions[i])
            // Empty clusters dissolve; near-zero sums trigger full clustering.
            existing[i].Centroid = normalize(sums[i])
            // Check drift against AnchorCentroid (at stable_since), not just last run!
            if cosineDistance(existing[i].AnchorCentroid, existing[i].Centroid) > opts.MaxDrift {
                return Result{}, true // Full clustering; reuse only geometry-compatible embeddings
            }
        }
    }

    // 4. Dissolve clusters whose member count dropped below MinClusterSize (from upstream deletions)
    // Re-route their surviving members into the orphans pool.

    // 5. Run Complete-Linkage Agglomerative Clustering at Epsilon over orphans
    // Spawns new clusters with monotonically increasing IDs (cN+1, cN+2...).
    // Remaining singletons become Noise (Novelty = 1.0).
}
```

**Lineage Matching Upgrade (`internal/cluster/lineage.go`):**

* **Demotion/Promotion Gate:** In `future-work-cross-repo-lineage.md`, `DemoteIfOnlyOnce` is proposed as an option. We make this a two-tier state in `global_lineage`: every local cluster with $\ge 2$ members is recorded in `global_lineage` as a *candidate* prototype, but the TUI chip (`↻ L0042`) and `spn lineage list` only surface lineages where `COUNT(DISTINCT repo_key) >= 2`. This allows the first repo's clusters to seed the table without polluting the user's global archetype list with one-off repo-specific clusters.

---

### 3. Shared Single-Pass Structural Engine (`internal/ast`)

Notice how three separate specs ask to parse source files in Go, Python, and JS/TS:
1. `future-work-full-mdg-centrality.md` parses files for `import` statements.
2. `future-work-fcg-fork-analysis.md` parses files for function definitions and call sites.
3. `future-work-touching-unit-attribution.md` parses files for top-level declarations and object-literal registries.

**Architectural Improvement:** Instead of three separate packages (`internal/mdg`, `internal/fcg`, and a third parser in `forksops`) cloning or reading files three times, unify them into `internal/ast`.

* **Zero-Clone Tarball Extraction:** `future-work-full-mdg-centrality.md` proposes running `git clone --depth 1` to a temp directory (5–60s, up to 1 GB disk peak) or making 1 Contents API call per file. Neither is optimal! Fetching `GET /repos/{owner}/{repo}/tarball/{sha}` streams the entire repository at `BaseSHA` in **one single HTTP request** (~1–3 seconds for a typical repo), and Go's `archive/tar` + `compress/gzip` can stream-parse `.go`, `.py`, and `.js/.ts` files **purely in memory** while skipping `vendor/`, `node_modules/`, binaries, and assets! Disk peak drops from 1 GB to **0 bytes**, and `git` doesn't even need to be installed.
* **Interval-Tree Function Lookup & Enclosing Unit Attribution:** When `internal/ast` streams the upstream tarball at `BaseSHA`, it builds both the MDG (`PageRank`) and the FCG (`Graph` + sorted interval slices per file) in a single pass.
* **Bridging FCG and `--touching-units`:** When the upstream AST at the comparison's `BaseSHA` is already available, parse patch bodies with old/new line cursors. Only removed lines intersect upstream declaration spans; hunk context is unchanged and must not count as an edit. Added lines have no old-side span: an insertion boundary can identify a candidate enclosing declaration, but must be labeled provisional without post-image evidence. Upstream-only analysis reports affected pre-image declarations; it cannot prove whether a declaration was modified, replaced, renamed, or deleted. Fetch/parse the post-image at `HeadSHA` for those classifications and for new declarations, including insertion-only changes. Missing/truncated patches remain unknown. Reusing the base AST may save a base-blob request, but does not eliminate the post-image request or establish a fixed percentage saving.


---

### 4. Request Efficiency, Regex Discovery & `BehindCount` (`internal/forksops` & `internal/github`)

1. **Unified Pre-Dispatch GraphQL Batch (`internal/github/batch_compare.go`):**
   * **Implemented:** Eligible cached forks already participate in the batch. Review corrections enforce the reserve before dispatch and inside Phase A/B chunks. At low headroom, cached comparisons remain usable without live refresh.
   * **Coherence rule:** A cached divergent comparison is reusable only with the same nonempty head SHA, ahead count, selected branch, and upstreamed provenance. Refresh behind count on a copy; otherwise replace the comparison through the normal enrichment path. A zero-ahead batch result replaces the entire old T2, including stale branch/diff evidence. If a changed comparison cannot be fetched under the reserve, report that gap rather than mixing old evidence with new counts.
   * **Remaining:** Define durable refresh integration with the existing snapshot/T2 replacement contract; an in-memory refresh is not proof of a database update. Batch last-touch lookup integration is also future work.

2. **Batched `history(first:1, path:)` vs. `tree-commit-info` (`internal/github/last_touch.go`):**
   * Embed `history(first: 1, path: $touchingPath) { nodes { oid } }` **directly inside the Phase A GraphQL batch** whenever `--touching <literal-path>` is used! This folds N separate last-touch HTTP requests into the *exact same* GraphQL batch call we are already making for divergence counts.

3. **Two-Tier `--touching re:<regex>` Discovery Mode (`internal/forksops/touching.go`):**
   * Under `--touching re:<regex>`, any fork whose compare file list is truncated at 300 files triggers the raw `.diff` fallback.
   * **Improvement (Streaming Header-Only Diff Scan):** When fetching `GET /repos/{owner}/{repo}/compare/{base}...{head}.diff` for a 300+ file fork under `re:<regex>`, do not buffer or parse the full multi-megabyte diff body if paths don't match. Wrap the HTTP response body in a streaming scanner in `internal/unidiff` that inspects `diff --git a/<path> b/<path>` lines on the fly.

---

### 5. Grounded Hierarchical Summarization (`internal/cluster/summarize.go`)

* **Structured JSON Output Schema:** Enforce a strict JSON schema (`format: "json"` in Ollama):
  ```json
  {
    "summary": "Adds OAuth2 PKCE authentication providers and token refresh middleware.",
    "cited_forks": ["alice/mcp-context-forge", "bob/mcp-context-forge"],
    "is_noise": false
  }
  ```
* **Deterministic Citation Gate:** Validate against the evidence actually supplied to each stage. Stage B (`SummarizeCluster`) allows citations only from that cluster's supplied members. Stage C (`SummarizeRepo`) allows the union of provenance from all cluster summaries supplied to the repository prompt, plus any explicitly supplied fork/noise evidence. Omitted clusters/forks are outside the scope. Require `cited_forks` to be a subset (equality is valid), and validate cited SHAs against the corresponding supplied fork evidence. Reject or scrub unsupported citations without erasing valid citations across multiple input clusters.
* **Prompt-Injection Sanitization:** Before interpolating `ForkFeatures` into prompt templates, strip control tokens (`<|im_start|>`, `<|eot_id|>`, `[INST]`), cap per-fork diff/README snippets to 1,000 characters, and wrap untrusted fork strings in XML data tags (`<fork_data>...</fork_data>`).

---

### 6. Worktree-Aware Multi-Repo PR Discovery (`internal/threadsops/workspace.go`)

`resolveGitDir` already implements standard `.git` directories, pointer files, relative/absolute `gitdir`, and `commondir` handling. Reuse it; do not add a duplicate path resolver.

The discovery path parses both origin and upstream, preserving provider and host. Only origins served by the GitHub client may supply head identities; only compatible upstreams may be queried. Upstream is tried first, then origin when needed, and lookup failures remain visible even if fallback finds a PR. A scan started inside a checkout or its subdirectories uses the checkout's parent; a workspace-parent invocation scans that directory directly. `FindOpenPRForHead` encodes the complete head filter as a query value.

---

## Part 4: Recommended Implementation Roadmap

1. **Phase 1: Core Storage & Transport Unification**
   * Storage tables/APIs are present in migration v6; integrate pipeline transactions and add future schema extensions through new migrations.
   * Cached-fork batching and workspace discovery are implemented, including the review corrections above. Remaining transport work includes batching `history(first:1, path:)` and durable coherent refreshes.

2. **Phase 2: In-Memory Tarball AST Engine — MDG, FCG & Touching Units**
   * Upgrade `internal/mdg` into the unified `internal/ast` package using single-request tarball streaming (`GET /repos/{owner}/{repo}/tarball/{sha}`).
   * Add FCG extraction and Tier 0.5/Tier 1 declaration diffing for `--touching-units`.
   * Add streaming header-only regex filtering to `internal/unidiff`.

3. **Phase 3: Spherical Continual Clustering & Cross-Repo Lineage**
   * Implement geometry identity for `local.go` and `fastembed.go`, including lexical corpus/IDF identity and full-corpus invalidation. Enable incremental reuse and cross-repo lineage only for compatible corpus-independent embeddings.
   * Implement `cluster.Reassign` (order-independent spherical mean, anchor-centroid drift guard, complete-linkage orphan clustering).

4. **Phase 4: Grounded Hierarchical Summarization**
   * Implement Stage A/B/C structured JSON summarization in `internal/cluster/summarize.go` with XML data sandboxing and the deterministic citation validator.
