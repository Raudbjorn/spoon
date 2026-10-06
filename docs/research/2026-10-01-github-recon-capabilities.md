# GitHub recon techniques: what spoon should adopt

Status: proposed. Date: 2026-10-01. Scope: defensive / authorized use only.

## Context

Two deep-research passes (102 + 104 agents, adversarial 3-vote verification) and
a survey of local reference clones (shhgit, git-wild-hunt, Treasure, codeql,
code-scanning-javascript-demo) looked for recon techniques worth adding to
spoon. A third pass ran read-only live tests against public GitHub data,
because web research could not settle whether the dangling-commit approach
still works. Live-test numbers below were observed once, on 2026-10-01; they
are not guarantees.

## Decisions

1. **Dangling-commit enumeration is built natively in Go.** No shell-out to
   TruffleHog, no dependency on its alpha `--object-discovery`.
2. **Detection starts from GH Archive PushEvents, but force-push detection is
   a compare step, not an event filter** (see Finding 1).
3. **Existence checks use GraphQL batching; REST `compare` only on survivors**
   (Finding 3).
4. **A `internal/secretscan` package scans only what a fork adds** (diff blobs
   from forks with `ahead > 0`), never whole clones or the firehose.
5. **`google/go-github` was approved for addition, but is re-opened here**:
   see "Open decision".
6. **SARIF 2.1.0 writer is optional output**, for repos the user controls.
7. **Out of scope:** Events-API live monitoring, GitHub secret-scanning /
   push-protection / code-scanning alert reads on third-party repos,
   Gato-style attack features, email harvesting beyond an opt-in low-confidence
   signal.

## Findings

### 1. PushEvent payloads lost commit metadata (live-observed)

GH Archive PushEvents carried `size`, `distinct_size` and `commits` through the
2025-09-15 sample hour and carry only `before`, `head`, `ref`, `push_id`,
`repository_id` from the 2025-10-15 sample hour on. The Truffle
`force-push-scanner` technique ("zero-commit PushEvents") cannot be reproduced
from the events alone. ForkEvents also no longer identify the parent
(`repo` is the fork), so a deleted fork cannot be mapped to its parent from the
archive.

Force pushes are now detected by comparing `before...head`:
`diverged`, `behind`, or "no common ancestor" mark a history rewrite
(or a new root). `ahead` is a normal push.

### 2. Orphaned commits are still served by SHA (live-observed)

Across six sample hours spanning 2025-11-15 to 2026-09-30 (70 randomly
sampled non-creation pushes per hour), 49 of 49 rewrite candidates still
returned the `before` commit via `GET /repos/{r}/commits/{sha}`, including
the oldest hour (about 10.5 months old). No garbage collection was observed in
that window.

Split of the 420 sampled events: ahead 231, not found 140, no-common-ancestor
40, diverged 9. "Not found" (repo deleted or private since) is large, 6% to 63% per hour
with no clear age trend (17%, 19%, 63%, 47%, 49%, 6% from oldest to newest
hour; the 2026-07-15 hour is the 63% one). Those commits are only reachable
if some other repository in the network is still alive.

### 3. Rate-limit cost

- REST `commits/{sha}` and `compare`: 1 point per call.
- GraphQL `object(oid:)` aliases: 20 lookups cost 1 point, both inside one
  repository and across 20 different repositories. A missing repository returns
  a per-alias `NOT_FOUND` error with partial data for the rest.
- Resulting design: batch about 20 `before` SHAs per GraphQL query to test
  existence, then call REST `compare` only for survivors. Roughly a 20x saving
  on the existence step. Batch sizes above 20 and secondary rate limits were
  not tested.

### 4. Cross-fork reads work on live forks (live-observed)

For the 62 forks of `eth0izzle/shhgit` with `ahead > 0` in
`spoon-export-eth0izzle-shhgit-2026-10-01.json`, 30 of 30 tested fork-only
`head_sha` values were served by `GET /repos/eth0izzle/shhgit/commits/{sha}`,
that is, through the parent. This confirms objects in a fork are readable via
the network's other repositories. The *deleted-fork* case was not tested
(no deleted fork was available; the export is from the same day).

### 5. Alert APIs are not a prospecting surface (live-observed)

With a token holding `repo` (accepted by the endpoint) the code-scanning
alerts endpoint returned 403 on `github/codeql` and `cli/cli`, and secret
scanning returned 404. Secret-scanning, push-protection and delegated-bypass
APIs are role-gated (repo or org admin). None can enumerate third-party forks.

### 6. Detection design worth borrowing

- gitleaks: generated per-provider rules; regex + keyword prefilter + entropy +
  allowlists. Parse its TOML rather than inventing rule syntax. (Baseline and
  SARIF behavior of v8 were not verified.)
- shhgit: signatures carry a `part` (path / filename / extension / contents);
  extension, path and string denylists; entropy on lines of 7 to 99 chars.
  Its entropy implementation is slow (use a byte-frequency map) and its dedup
  map is unbounded (use a bounded LRU).
- git-wild-hunt: 937-regex corpus, with duplicates and `[a|A]` character-class
  bugs (`|` is literal inside a class; use `(?i)`). Dedupe and convert to RE2.
- git-secrets: only the allowlist and hook design is of interest; regex-only,
  reachable-history only.
- codeql: named suites and `@precision` / `@security-severity` metadata as a
  model for signature bundles.

### 7. Cheap quality win found in spoon's own output

In the shhgit export, cluster `c1` is labelled from `gopkg.in/yaml.v3...`
because dependency-bump / lockfile noise dominates its centroid. Filter
lockfiles and dependency bumps from cluster labelling.

Also: that export lists 455 forks against 472 expected, with
`repeats_dropped: 0`. 17 forks are unaccounted for.

## Proposed design

Pipeline, cheapest tier first (matches the existing tiered API pipeline):

1. Candidate source: GH Archive hourly files (`data.gharchive.org/YYYY-MM-DD-H.json.gz`,
   hour not zero-padded, about 16 MB each) filtered to PushEvents whose `before`
   **and** `head` are both non-zero, scoped to a repository or fork network of
   interest. The `head` half is not optional: branch and tag deletions arrive as
   push events with a real `before` and a null SHA for `head`, so a non-zero-`before`
   filter alone admits every ref deletion in the archive. Its `before` commit
   survives the step-2 existence check, and the pipeline then spends a REST
   `before...null` compare on it before failing.
2. Existence: GraphQL batches of about 20 `object(oid: before)`.
3. Rewrite check: REST `compare before...head` on survivors; keep `diverged`,
   `behind`, no-common-ancestor.
4. Content: fetch the diff blobs via the existing contents / `unidiff` path
   and run `internal/secretscan` over added lines only.
   For a rewrite, that head-side diff is the wrong side to read. The compare
   path is merge-base relative (`compare/{before}...{head}`), so when a force push
   removes a commit — `before=B` where `B` descends from `head=A` — the result is
   `behind` with no head-side additions, and a divergent rewrite yields the
   replacement commits rather than the discarded ones. The content stage must
   therefore walk the commits reachable from `before` but not from `head` and
   scan the additions those discarded commits introduced. That side is the whole
   point of the feature: it is where a secret-bearing commit lives after it has
   been rewritten away.
5. Output: NDJSON via `agentio`; optional SARIF.

`internal/secretscan`: signature struct `{Name, Part, Match|Regex, Keywords,
Entropy, Allowlist}`, loadable from gitleaks-style TOML; map-based Shannon
entropy; denylists for extensions, paths and strings; bounded LRU for dedup.

## Open decision: go-github

The earlier answer was "add go-github". The live tests weaken the case: the
valuable calls (batched `object(oid:)` aliases, `compare`, `commits/{sha}`)
are either GraphQL string-building or single REST calls that `cli/go-gh`
already provides, and the code-scanning surface it would type is unusable on
third-party repos (Finding 5). go-github v58 is also old (Jan 2024). Recommend
deferring the dependency until a typed API is needed, and re-confirming.

## Not claimed

- Live tests are one-day, small samples (70 events per hour, 6 hours; 30 forks
  of one network). They show behavior was present on 2026-10-01, not that it is
  stable or at scale. "49 of 49" has a wide confidence interval.
- "No common ancestor" may include new root branches, not only force pushes.
  The sample was not manually classified.
- Deleted-fork (CFOR) retrieval was not tested; only live-fork cross reads.
- Garbage collection beyond about 10.5 months, and any GitHub policy change,
  were not tested. One external report of unreachable-commit GC exists
  (isaacs/github issue 997, unverified here).
- Secondary rate limits, GraphQL batches above 20, and sustained-load behavior
  were not tested.
- GH Archive availability was observed for single files only; ClickHouse and
  BigQuery availability were not verified. GHTorrent status unverified.
- gitleaks v8 baseline and SARIF behavior, repo-supervisor, and GitHub custom
  patterns are unverified.
- Detection quality (precision/recall) of any proposed signature set is
  untested. Several research claims came from vendor-authored sources.
- No commit contents were read or scanned during these tests; only status
  codes and OIDs were recorded.
- New state and dependencies this would introduce: a GH Archive download cache
  (about 16 MB per hour), a signature corpus to maintain, and (if approved) a
  GraphQL query builder.
- Dual use: dangling-commit retrieval and any secret scanning must be framed
  as authorized or defensive, with findings handled by the owner-notification
  path, and no automated validation of discovered credentials.
