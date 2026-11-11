# GitHub reconnaissance: research follow-up and implementation handoff

Date: 2026-10-11. Scope: defensive, authorized analysis. This is a research
continuation, not a claim that the proposed feature is shipped.

## Provenance and tracking

The earlier research belongs to merged [PR #138](https://github.com/Raudbjorn/spoon/pull/138),
branch `research-recon-tools`, tip `ea567f206d387e009af32af1f5eecc699b4694dd`.
The issue search `recon in:title,body` across all issue states returned no match;
the complete issue-title list also contained no reconnaissance issue. This is
evidence of no matching issue found, not proof that no differently named issue exists.
Issue #83 and the initially checked-out branch concern web-diff correctness.

This follow-up is on `research/recon-followup-20261011` in a separate worktree.
The original web-diff edits and the separate implementation worktree were not changed.

The pasted October 1 conversation predates the final research document: that
document already records 420 sampled events, 49 retrievable rewrite candidates,
30 live-fork cross-network reads, and the correction to scan discarded commits.
Those remain **historical reported observations**, not tests rerun here.

Maintainer-local evidence is archived at
`~/.docs/spoon/recon-followup-2026-10-11/`, with pinned upstream source,
repository metadata, HTTP/SQL results, synthetic checks and `FILES.sha256`.
This local archive is not part of the repository or required for checkout.
No discovered credentials or scanned third-party commit contents are in the archive.

## Findings that change the implementation plan

### 1. Gitleaks baseline matching is not secret-safe identity

Source audited: `gitleaks/gitleaks` at
`b58d3f102cf3a2c84cb7f923d05c25c9b1aed84b`.

`LoadBaseline` accepts a JSON findings array. `IsNew` compares rule, description,
location, file, commit and author metadata, and entropy. It deliberately ignores
the fingerprint field. When the redaction parameter is positive, it also skips
the equality checks on the matched text and secret. Consequently, a replacement
value at the same location with the same entropy and other metadata can be
suppressed. Changing the commit still makes the finding new.

An extracted-source Go check reproduced this with two synthetic four-character
values, with redaction disabled, partial redaction, full redaction, and a changed
commit. Only the `report.Finding` qualifier was replaced with a minimal local
type; the predicate itself was retained. This is not a full Gitleaks execution.
[Source: baseline.go](https://github.com/gitleaks/gitleaks/blob/b58d3f102cf3a2c84cb7f923d05c25c9b1aed84b/detect/baseline.go).

Gitleaks ignore-file fingerprints identify file/rule/line, optionally with a
commit prefix. They do not identify secret content.
[Source: AddFinding](https://github.com/gitleaks/gitleaks/blob/b58d3f102cf3a2c84cb7f923d05c25c9b1aed84b/detect/detect.go).

**Spoon consequence:** keep run-local duplicate suppression distinct from a
persistent baseline. The existing Spoon fingerprint hashes provenance and
coordinates, including snapshot ID; it contains no matched value. That is
appropriate for identifying occurrences in a snapshot, but must not silently
become a cross-snapshot suppression rule. A persistent baseline needs a separate,
explicit design and synthetic replacement-value regression tests.

### 2. Gitleaks SARIF is a format example, not Spoon's redaction policy

The pinned SARIF writer emits SARIF 2.1.0 with rule descriptors, file locations,
line/column spans and a region snippet populated from `Finding.Secret`. Its
`partialFingerprints` contain commit metadata, including author/email/message;
the writer does not perform redaction itself. Upstream `Finding.Redact` changes
the secret and occurrences in the match/line, but does not scrub every metadata
field. Calling the writer on an unredacted finding exposes its secret.
[Writer](https://github.com/gitleaks/gitleaks/blob/b58d3f102cf3a2c84cb7f923d05c25c9b1aed84b/report/sarif.go),
[finding redaction](https://github.com/gitleaks/gitleaks/blob/b58d3f102cf3a2c84cb7f923d05c25c9b1aed84b/report/finding.go).

**Spoon consequence:** retain the existing finding type that has no matched-text
field. Omit snippets and commit-message/author metadata from reports. Do not add
raw-secret retention merely to imitate another scanner's report schema.

GitHub documents repository-relative file mapping, stable rule IDs/paths and
fingerprints for alert matching. Direct API uploads without fingerprints can
produce duplicates. Its documented limits include 10 MB gzip, 20 runs per file,
25,000 results per run, and display of the top 5,000 results. These are different
limits, not a single 5,000-result format limit.
[GitHub SARIF requirements](https://docs.github.com/en/code-security/reference/code-scanning/sarif-files/sarif-support).

The local draft uses `spoon://repository/...?...snapshot=...` artifact URIs and
snapshot-dependent fingerprints. **Inference:** these support local provenance,
but cannot be presented as verified GitHub source mapping or stable identity
across scans. A GitHub-upload design must bind results to the correct repository
and revision, use meaningful file paths, and distinguish discarded-history
evidence from current-branch findings. No upload was performed.

### 3. Gitleaks TOML support would be a compatibility project

The pinned configuration supports rule/global allowlists, different regex
targets and conditions, plus required auxiliary rules with proximity bounds.
Loading only regex, keywords and entropy would not preserve those semantics.
[Configuration](https://github.com/gitleaks/gitleaks/blob/b58d3f102cf3a2c84cb7f923d05c25c9b1aed84b/config/config.go),
[rule types](https://github.com/gitleaks/gitleaks/blob/b58d3f102cf3a2c84cb7f923d05c25c9b1aed84b/config/rule.go).

**Recommendation:** keep the strict JSON v1 format already implemented in the
separate Spoon worktree for the first release. Do not label it Gitleaks-compatible.
If import is later needed, define a supported subset, reject unsupported
constructs, pin the source corpus and preserve licensing. No external signature
corpus was imported during this work.

### 4. ClickHouse is reachable, but its public table lacks rewrite inputs

The documented HTTPS endpoint is `https://play.clickhouse.com/`, with public
users `play` or `explorer` and an empty password.
[Connection documentation](https://clickhouse.com/docs/get-started/sample-datasets/playground).

Observed in this session:

| Probe | Result | What it establishes |
| --- | --- | --- |
| Initial queries attempting `max_execution_time=5` | HTTP 500 | Not evidence of an unavailable service |
| `play`, `SELECT 1 FORMAT JSON`, no setting overrides | HTTP 200, one row | Query endpoint works |
| `system.tables` | `default.github_events` plus repository/user history tables | Concrete table discovery |
| `system.columns` for `github_events` | 54 columns, no `before` or raw `payload` | Required rewrite input is absent from this table |
| Spoon PushEvent sample with setting overrides | HTTP 500, error 164: read-only setting cannot be modified | Explains this request failure |
| Same sample without overrides, `LIMIT 3` | HTTP 200, zero rows | Does not establish current coverage/freshness |

The table has `head_sha`, `base_sha`, `push_size` and `push_distinct_size`, but
their existence is not evidence that current PushEvents populate them. No event
content was selected. **Recommendation:** do not use this table as the primary
`before...head` candidate source. Another provider must demonstrate that it
preserves the actual event fields before an adapter is justified.

### 5. Archive availability needs narrower claims

GH Archive's site documents hourly archives and a BigQuery public dataset.
This is a published interface, not a fresh ingestion test.
[GH Archive](https://www.gharchive.org/).

An October 10 hourly archive returned HTTP 403 to both HEAD and a bounded range
GET; the latter returned `error code: 1010`. This environment did not retrieve
the file. Do not infer deletion, global outage or a new archive schema from that
response. BigQuery was not queried, and no billing project was configured.

GHTorrent's repository explicitly says public database shell access ended in
July 2019. Its website repository remains unarchived; the API reported a 2024
last-push date. A direct website request returned HTTP 403 here. These facts do
not prove that every data dump or project component is dead, but provide no
verified 2026 source for this feature.
[GHTorrent website repository](https://github.com/ghtorrent/ghtorrent.org/tree/51890965af72da85bdd0954a50ef1a71603fb4e7).

### 6. Alert API permissions must be checked per endpoint

The October 1 403/404 observations remain valid as reported, but the explanation
that only repo/org admins can read all alerts was too broad. GitHub documents
code-scanning visibility for users with write access and additional security/custom
roles. Its repository API lists the fine-grained token permission
`Code scanning alerts: read`.
[Security overview permissions](https://docs.github.com/en/code-security/reference/permissions/security-overview),
[code-scanning API](https://docs.github.com/en/rest/code-scanning/code-scanning#list-code-scanning-alerts-for-a-repository).

For the repository-level secret-scanning list/get endpoints, the REST documentation
explicitly requires the authenticated user to administer the repository or its
owning organization. The listed fine-grained token permission,
`Secret scanning alerts: read`, must not be treated as a substitute for that
user authority. These are endpoint-specific documented requirements, not newly
tested access grants.
[secret-scanning API](https://docs.github.com/en/rest/secret-scanning/secret-scanning#list-secret-scanning-alerts-for-a-repository).

The product conclusion stands: public repository visibility does not grant
access to its security alerts. Do not use those APIs as general third-party
fork-prospecting signals. An authorized defender integration is a different scope.

GitHub custom patterns use Hyperscan's supported regex syntax, not arbitrary Go
RE2 syntax. Their dry-run workflow can test a pattern without creating alerts.
These are useful evaluation ideas, not a portable rule format or third-party
query capability.
[Pattern reference](https://docs.github.com/en/code-security/reference/secret-security/custom-patterns),
[dry-run documentation](https://docs.github.com/en/code-security/how-tos/secure-your-secrets/customize-leak-detection/define-custom-patterns).

### 7. Remaining tool survey

`auth0/repo-supervisor` returned 404. The discoverable MIT project is
`radekk/repo-supervisor`, pinned at `37abbdf58c40734934f116d7b73be98031ded465`,
with a reported last push in January 2018. Its source tokenizes JS/JSON, filters
common non-secret strings before entropy scoring, and supports a PR webhook
workflow with human acknowledge/reject feedback. Its scanner produces raw
strings; do not import its reporting model into Spoon. Borrow the evaluation
idea: record false-positive judgments without retaining candidate values.
[Repository and README](https://github.com/radekk/repo-supervisor/tree/37abbdf58c40734934f116d7b73be98031ded465),
[entropy filter](https://github.com/radekk/repo-supervisor/blob/37abbdf58c40734934f116d7b73be98031ded465/src/filters/entropy.meter/index.js).

Repository metadata confirms `praetorian-inc/gato` is archived. `AdnaneKhan/gato-x`
is unarchived and reported a push on October 2, 2026; its maintenance status is
therefore no longer wholly unknown, although that does not establish quality or
support. No attack capabilities were tested or adopted.
[Gato](https://github.com/praetorian-inc/gato), [Gato-X](https://github.com/AdnaneKhan/gato-x).

## Existing Spoon implementation: reuse it, with explicit limits

Inspected `~/dev/spoon-owner-sampling-recon`, branch
`feature/github-recon`, HEAD `3dd58e16682011c9a7a0f85adc4fe709e46034fd`.
The archive records hashes of inspected files, including the uncommitted files.

Review follow-up, 2026-10-11: the complete draft is now preserved at
[commit `3c3aa20c8785d14408174c15d3491b05cb9a86d7`](https://github.com/Raudbjorn/spoon/commit/3c3aa20c8785d14408174c15d3491b05cb9a86d7)
on `research/recon-draft-snapshot-20261011`. This snapshot retains the inspected
base and copies the 11 modified/untracked files byte for byte, including tests
and fixtures. The original worktree remains unchanged. The snapshot is a draft
implementation base, not a merged feature or part of this documentation PR's diff.

| Surface | State at initial inspection | Consequence |
| --- | --- | --- |
| Dependency-noise filtering | Commit `c9d58a2` exists | Do not implement it again; its ranking quality was not evaluated here |
| `internal/secretscan` | Committed bounded scanner, strict JSON rules, metadata-only findings, bounded LRU | Use as the implementation base |
| Patch parser | Requires real unified hunks; 64 KiB patch cap | Headerless web scrapes cannot supply trustworthy finding coordinates |
| [`internal/recon`](https://github.com/Raudbjorn/spoon/tree/3c3aa20c8785d14408174c15d3491b05cb9a86d7/internal/recon) | Then-untracked NDJSON ingestion, summary and SARIF files; now in the snapshot | Existing draft, not merged/shipped behavior |
| [`cmd/spn/recon.go`](https://github.com/Raudbjorn/spoon/blob/3c3aa20c8785d14408174c15d3491b05cb9a86d7/cmd/spn/recon.go) | Then-untracked local `scan` command; now in the snapshot; `rewrites` explicitly unavailable | Do not claim dangling-commit enumeration exists |
| Rules | Operator-supplied format and synthetic fixture corpus | Detection precision/recall remains unmeasured |

To work from the published snapshot in a separate checkout:

```sh
git fetch origin research/recon-draft-snapshot-20261011
git worktree add --detach ../spoon-recon-draft 3c3aa20c8785d14408174c15d3491b05cb9a86d7
```

The initial research pass tested only `internal/secretscan` and `internal/recon`.
During snapshot publication, these checks passed in its isolated worktree:

```sh
go test -count=1 ./internal/secretscan ./internal/recon
go test -count=1 ./cmd/spn -run 'TestRecon|TestProductionWritesUseTypedOutput'
go build ./cmd/...
```

The focused CLI dispatch tests cover synthetic NDJSON, redacted findings/SARIF,
incomplete summaries, routing, and output failures. No test assertions or draft
source were changed during preservation. Executing the built CLI as a subprocess,
the full suite, race detector, native embeddings and live integrations were not
exercised in this review follow-up.

The original shhgit export was independently recounted: 455 rows, 455 unique
names, 62 with `ahead > 0`, expected count 472 and zero repeats reported. The
17-fork discrepancy is real in that artifact; it does not identify 17 particular
missing forks or establish the cause. A later live enumeration cannot reconstruct
an October 1 list without contemporaneous page/cursor evidence.

## Recommended next implementation slice

1. Finish the existing local scan CLI and document strict JSON rule support,
   incomplete-input behavior and local SARIF provenance. Verify the real CLI
   with synthetic NDJSON and output-failure tests before presenting it as usable.
2. Keep GitHub-upload compatibility and persistent baselines out of that slice.
   They need identity/location decisions that the current drafts do not settle.
3. Build rewrite **metadata enumeration** separately, using explicit repository
   and time bounds, candidate/page/byte/request caps, cancellation, and a source
   that actually supplies nonzero `before` and `head`. Record incomplete results.
4. For history with a common ancestor, evaluate paginated reverse compare
   `head...before` to identify discarded commits, then scan additions in each
   discarded commit. A net diff can miss a value introduced and subsequently
   removed within that discarded history. No-common-ancestor cases need bounded
   traversal or an explicit unsupported/incomplete result, never a clean scan.
5. Reuse the HTTP stack of the eventual target branch. Neither inspected local
   branch has `go-github`, but open [PR #142](https://github.com/Raudbjorn/spoon/pull/142)
   proposes it. The old dependency decision is branch-dependent; this research
   adds no dependency and does not decide that separate PR.

The compare API documents default commit and file-list limits; paginating
commits does not make the file list complete. The enumeration plan must account
for both. The reverse-compare plan above is an inference from the API's commit
range semantics, not a newly implemented or live-tested feature.
[Compare API](https://docs.github.com/en/rest/commits/commits#compare-two-commits).

The October 1 GraphQL observation concerns existence-check cost only. For N
candidates and survivor fraction p, the proposed preflight uses approximately
ceil(N/20) GraphQL queries plus pN REST compares under that observed batching
behavior. These consume separate quotas. It is not a demonstrated 20x saving
for the whole pipeline, and if every candidate survives it saves no compare calls.

## Not claimed

- Deleted-fork retrieval, garbage-collection policy, retention guarantees,
  GraphQL batches above 20, or secondary-rate-limit behavior were not retested.
- No third-party commit contents were scanned, no credentials validated, no
  security alerts retrieved, and no SARIF uploaded.
- The archive failures are environment-specific observations. BigQuery freshness
  and billing behavior, GH Archive freshness, and ClickHouse ingestion completeness
  remain unverified.
- Source audit and synthetic checks do not measure detection quality. Passing
  the focused suites and compiling both binaries do not establish production
  readiness, full CLI integration, or GitHub SARIF compatibility.
- New state includes isolated research/snapshot worktrees, local evidence, and
  the published draft snapshot branch. This PR changes documentation only; it
  adds no application dependency, production configuration, or cache changes.
