# Future work: unit-level attribution for `--touching` matches

Status (2026-09-03): PROPOSED. Not started. Depends on `spn forks list --touching`
(docs/superpowers/plans/2026-09-03-touching-path-filter.md).

## Problem

`--touching` says *which forks* changed a file and *how much* (+a/-d, centrality).
The next question a maintainer asks is *what inside the file*: which exported
rule, function or class the fork added, removed or rewrote — e.g. "which
antipattern rules in `cli/engine/registry/antipatterns.mjs` did fork C add?".
Today that needs the patch view and a human.

## Non-goals

- Agentic repository QA (DeepRepoQA, arXiv 2608.24221) or RAG over chunks
  (ai-codebase-analyzer): spoon stays deterministic and offline-first.
- Function-call graphs (RepoMaster FCG, arXiv 2505.21577 §3.2.1) and the
  abandoned AST normalizer (docs/superpowers/specs/shipped/
  future-work-semantic-preserving-normalization.md, status ABANDONED).
- The empty `code_units` / `unit_alignments` / `intent_runs` tables present in
  some local `spoon.db` files: created by an unrelated dirty build, no source
  in this repo, not a base to build on.

## Tier 0 (zero API calls): hunk-header context

GitHub compare patches carry git's funcname context on every hunk header
(`@@ -12,7 +12,9 @@ export const antipatterns = [`). `compare_files.patch`
already stores them for every live-fetched fork (NULL only when GitHub omitted
the patch: binary, >~1 MB, or >300 files). Parse `@@ … @@ <context>` per matched
file, normalise the context line per language (strip `export`, `function`,
`const`, trailing `{`/`[`/`(`), and emit:

    "touching": {"files": [{"path": "...", "units": [
        {"name": "antipatterns", "kind": "hunk-context", "hunks": 2, "added": 18, "removed": 0}]}]}

Cost: none. Precision: the enclosing declaration git found, which for a rule
table is the table itself — good enough to distinguish "edited the registry"
from "edited the test fixture", not to name the rule.

## Tier 1 (2 Contents-API calls per matched file): declaration diff

Fetch the file at `T2.BaseSHA` and `T2.HeadSHA` (both already on `T2Data`),
parse both with the tree-sitter grammars `internal/mdg` already vendors for
Python (and stdlib `go/parser` for Go; add JS/TS grammars when needed), list
top-level and exported declarations with byte spans, and align by name:
`added` / `removed` / `modified` (span hash differs) / `renamed` (same body
hash, different name). For object-literal registries (the impeccable case)
treat each top-level array element / object key as a unit.

Budget: opt-in flag `--touching-units`, capped like `--commit-file-budget`
(default 50 file fetches per run, best-Impact first). Persist per
`(fork_key, path, base_sha, head_sha)` in a new `touched_units` table so
re-runs are free; the store's `T2Present` replace-rule must leave it alone
(same contract as `commit_files`).

## Output and ranking

Units are presentation only: they never feed heat, rank or visibility. The
TUI detail section lists them under each touched file; the patch view jumps
to the unit's hunk.

## Open questions

- Whether GitHub's Contents API base64 payloads for >1 MB files are worth
  handling or should be reported as `units_skipped_reason`.
- Language coverage order: JS/TS first (impeccable), then Python, Go.
- Whether Tier 0 alone answers enough real questions to defer Tier 1.
