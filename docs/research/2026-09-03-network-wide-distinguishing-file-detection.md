# Network-Wide Distinguishing-File Detection for Spoon

Date: 2026-09-03
Subject: Spoon, a GitHub fork-analyzer CLI/TUI (Go). Its private source repo
is `Raudbjorn/spoon`.
Motivating example: pbakaus/impeccable, a public repo with ~3691 forks.
Research agent: (unassigned)

**Access note: assume this agent has no access to the requester's local
filesystem, no access to the private Spoon repo, and no ability to run live
GitHub API calls against the impeccable network.** Every code excerpt and
every number this document relies on is quoted or stated inline below —
nothing here should require the agent to go fetch anything to do useful
design work. If the agent *is* separately given repo or network access,
treat the file:line citations below as a bonus shortcut, not a requirement;
otherwise, work from the quoted excerpts and treat the reported numbers as
given facts from the requester, not something to re-derive. If a question
below turns out to hinge on something only visible in the real source tree
(a control-flow detail this document didn't quote), say so explicitly as an
open question rather than guessing at spoon's internals.

## Purpose

A recurring manual task: given an upstream repo and one file known to be
the load-bearing implementation of a specific capability (here,
`cli/engine/registry/antipatterns.mjs` in pbakaus/impeccable — the rule
table that encodes how that tool detects AI-generated design "slop"), find
every fork in the network that has **genuinely modified that file in its
own commits**, as opposed to forks that merely look different because they
are stale and missed later upstream edits to the same file.

Doing this by hand today means: fetch or clone candidate forks one at a
time, diff a specific file against upstream's current tip, and manually
reason about which differences are fork-authored vs. drift. That process
produced two confirmed false positives before the mistake was caught (see
"The bug this must not reproduce" below). Spoon's own source, quoted below,
already contains almost every piece needed to do this correctly and cheaply
at network scale — this document is the grounding for a design that
connects them.

## What actually happened (reported by the requester, not independently
verifiable by this agent — treat as given)

1. Started by diffing `cli/engine/registry/antipatterns.mjs` between a
   local clone of upstream (pbakaus/impeccable, current tip) and 7
   locally-cloned forks, using a plain two-file diff. This flagged two
   forks, call them Fork A and Fork B, as having added or removed
   antipattern rules.
2. Re-scoped to the full 3691-fork network instead of 7. A cache Spoon
   itself had built from earlier `spn forks list --commit-files` runs
   already held ahead-commit file diffs for 62 of the 3691 forks.
   Filtering that cache for antipattern-related paths surfaced a real hit
   — a third fork, call it Fork C, genuinely adding a new rule — that the
   7-fork sample had missed entirely.
3. While building a correct full-network sweep (GitHub's REST `compare`
   endpoint, three-dot semantics — merge-base-relative, not tip-vs-tip),
   re-ran the two earlier findings through it and both evaporated:
   - Fork A: zero file hits in its own ~70 ahead-commits. The rule it
     appeared to have "added" turned out to be an **original upstream
     rule that upstream itself later retired** (visible in upstream's own
     git history as a "retire this rule" commit). Fork A is hundreds of
     commits behind and simply predates the retirement — it never touched
     the file.
   - Fork B: zero file hits in its own ~34 ahead-commits. The rules it
     appeared to have "removed" were rules **upstream added after Fork
     B's fork point**; Fork B is hundreds of commits behind and, again,
     never touched the file.
   - Fork C re-confirmed as real: a genuine ahead-commit touching the
     file, patch content matching a hand-authored new rule with its own
     test fixture.

### The bug this must not reproduce

**A plain two-tree diff (fork tip vs. upstream's current tip) cannot tell
fork-authored changes apart from upstream drift.** If a fork is N commits
behind, every upstream change in those N commits shows up as a "diff"
indistinguishable from something the fork's owner did. The only correct
signal is a **three-dot / merge-base-relative diff**:
`merge-base(base, head)...head`, which shows only the files touched by the
fork's own ahead-commits and is immune to how far behind the fork is. This
is exactly what GitHub's REST `compare` endpoint computes, and — per the
quoted source below — exactly what Spoon's own compare code already calls.
Any design for "did fork X touch file Y" must go through that three-dot
path, never a raw tip-vs-tip diff. This is worth a named guard or comment
in the implementation: it is exactly the kind of mistake that looks
correct on a handful of examples and is wrong on most of a real network,
since most forks in a typical network are stale, not actively synced.

## Spoon source, quoted (this is what the design must build on)

The following excerpts are quoted directly from Spoon's Go source as it
stood when this document was written. Treat them as ground truth for what
already exists; do not assume anything not shown here without flagging it
as an open question.

**`internal/github/compare.go`** — the three-dot compare call already
exists and already has correct 404 handling:

```go
// FetchCompare fetches the comparison between a parent branch and a fork branch.
// Returns an empty result (not error) if the compare 404s (e.g., deleted fork).
func (c *Client) FetchCompare(ctx context.Context, parentOwner, parentRepo, parentBranch, forkOwner, forkBranch string) (CompareResult, error) {
	path := fmt.Sprintf("repos/%s/%s/compare/%s...%s:%s",
		parentOwner, parentRepo, parentBranch, forkOwner, forkBranch)

	var result CompareResult
	err := c.Get(ctx, path, &result)
	if err != nil {
		if isNotFound(err) {
			return CompareResult{}, nil
		}
		return CompareResult{}, err
	}
	result.Performed = true
	return result, nil
}
```

**`internal/github/branches.go`** — a second function,
`FetchCompareWithBranchScan`, goes further: it tries the fork's default
branch first, and if that shows no genuine (not-already-merged-upstream)
work, scans side branches for the most recent one with real work, falling
back to the default-branch result otherwise. Its doc comment:

```
// FetchCompareWithBranchScan returns the divergence to attribute to a fork. It
// tries the default branch first; if the default has genuine (not-upstreamed)
// work it wins. Otherwise it scans side branches for the most-recent branch
// with real work, falling back to the default-branch result.
```

This is exactly the "the fork's real work isn't on its default branch"
problem — the requester hit two real forks in the impeccable network whose
actual work lived on a non-`main` branch, and had to find that manually.
**Open question**: confirm (once real repo access exists) that this
branch-scan function is wired into the default fork-list divergence path
for every fork, not just a separate command — if so, this problem is
already solved and the design should build on it rather than re-solving
it.

**`internal/github/types.go`** — the compare response already carries a
file list:

```go
type CompareResult struct {
	Performed       bool
	Status          string       // "ahead", "behind", "diverged", "identical"
	AheadBy         int
	BehindBy        int
	TotalCommits    int
	Files           []FileChange
	Commits         []Commit
	BaseCommit      Commit
	MergeBaseCommit Commit
	HTMLURL         string
}
```

**`internal/github/adapter.go`**, function `compareToT2` — this file list
is already mapped into Spoon's internal divergence type on every compare
call:

```go
func compareToT2(r CompareResult) forge.T2Data {
	diffs := make([]forge.FileDiff, 0, len(r.Files))
	var totalAdd, totalDel int
	for _, f := range r.Files {
		totalAdd += f.Additions
		totalDel += f.Deletions
		diffs = append(diffs, forge.FileDiff{
			Path:         f.Filename,
			PreviousPath: f.PreviousFilename,
			Status:       f.Status,
			Additions:    f.Additions,
			Deletions:    f.Deletions,
			Patch:        f.Patch,
			PatchSource:  "compare_rest",
		})
	}
	// ... (ahead-commit list, MNA, feature-commit-ratio, merge-base/head
	// SHA resolution follow; omitted here, not relevant to this question)
}
```

The type this feeds, `forge.T2Data`, includes:

```go
type T2Data struct {
	Performed          bool
	AheadCount         int
	BehindCount        int
	MNA                int
	TotalAdditions     int
	TotalDeletions     int
	FeatureCommitRatio float64
	IsBranchWork       bool
	ActiveBranch       string
	Upstreamed         bool
	UpstreamedPR       int
	BaseSHA            string
	HeadSHA            string
	Diffs              []FileDiff   // <-- the file list, already populated
	Commits            []AheadCommit
	PatchSkipReason    string
}
```

**This mapping (`Files` → `Diffs`) happens on every T2/compare fetch,
regardless of any flag.** The open question is what happens to `Diffs`
afterward — see below.

**`internal/store/store.go`**, the `Snapshot` type's doc comment states
the intended design directly:

```go
type Snapshot struct {
	Repo         RepoRecord
	Fork         ForkRecord
	CompareFiles []FileRecord
	Commits      []CommitRecord
	Document     DocumentRecord
	T2Present bool
	T1 *forge.T1Data
	// T2 carries the compare scalars (ahead/behind, MNA, upstreamed, branch
	// work …). Its Diffs and Commits are NOT serialised into t2_json — they
	// already live relationally in compare_files/commits and would double the
	// row size (patches included). Written only when T2Present.
	T2 *forge.T2Data
}
```

So the design intent is clear: `T2.Diffs` is meant to be persisted
relationally into a `compare_files` table, separately from the scalar
`ahead`/`behind`/`mna` fields that go into `t2_json`. **Open question for
this research**: trace exactly which code path decides *whether*
`T2.Diffs` actually gets copied into `Snapshot.CompareFiles` before a
given `Snapshot` is written, and what gates that decision. The requester's
report above (62 of 3691 forks having `compare_files` rows, but effectively
all 3691 having `ahead`/`behind` scalars) suggests that gate is currently
narrower than "every fork whose T2 was fetched at all" — i.e. that the
`Diffs` list is often computed, then discarded, even though it cost
nothing extra to obtain (same API response as the scalars). If that's
correct, persisting it by default (path/status/size only — patch can stay
lazy, see the store-size comment above) would be close to a free win: no
new API calls, no new fetch, just stop discarding data already in memory.

**`internal/forksops/stream.go`**, function `enrichCommitFiles`, is a
**separate and much more expensive** path: one API call **per
ahead-commit**, not one call per fork:

```go
func enrichCommitFiles(ctx context.Context, provider forge.Forge, collected []Result, opts Options) {
	// ...
	budget := opts.CommitFileBudget
	if budget <= 0 {
		budget = 100
	}
	// ... claim() consumes one unit of a shared budget per call ...
	for _, index := range order {
		result := &collected[index]
		// ...
		for commitIndex := range result.T2.Commits {
			if !claim() {
				result.CommitFilesSkip = &StageSkip{Reason: fmt.Sprintf("global commit-file budget (%d) exhausted", budget)}
				break
			}
			files, err := fileProvider.CommitFiles(ctx, result.Fork, result.T2.Commits[commitIndex].SHA)
			// ...
		}
	}
}
```

This feeds a `commit_files` table keyed by `(fork_key, sha, path)` — i.e.
per-commit attribution ("which commit touched this file"). A single fork
with 70 ahead-commits can burn most of a default 100-call budget by
itself. This is almost certainly why only a small fraction of a large
network ever gets this level of enrichment in practice, while the *scalar*
divergence data (ahead/behind/MNA, one compare call per fork) covers
everyone.

**The key distinction the design should make explicit**: per-commit
attribution (`enrichCommitFiles`, expensive, budget-capped, answers "which
commit changed this file") is a different question from aggregate
divergence (`compareToT2`'s `Diffs`, already fetched for every fork,
answers "did this fork's own work touch this file at all, and how"). The
motivating use case in this document only needs the second, cheaper
question answered — it does not need per-commit attribution.

## Planning principles to inherit

(These match principles the requester says an earlier Spoon research
document already established, and remain sound advice for this design
regardless of that document's exact contents, which this agent cannot
read.)

1. Do not replace or fork Spoon's existing heat/clustering/expected-rank
   core. This is additive: a new lens on data mostly already flowing
   through the existing compare/T2 stage, not a parallel pipeline.
2. Thread new behavior through Spoon's existing options/result surface
   (the `Options` struct feeding `stream.go`'s pipeline, per the quoted
   code above) rather than inventing a side path.
3. Preserve whatever streaming/output contract Spoon's CLI already uses
   for fork records (the quoted code suggests one JSON record per fork,
   emitted incrementally).
4. Prefer cheap, already-fetched, persistent signals over new expensive
   API calls — this document's central finding is that the signal needed
   here (the aggregate file list) is already fetched by the existing
   compare call and only needs to stop being discarded before storage.
5. Patch content (`FileDiff.Patch`) should stay optional/lazy — do not
   make full patch text mandatory for a full-network path/status list, in
   keeping with the store-size concern already noted in `Snapshot`'s own
   comment.

## The research question

Design a **cheap, network-wide "which forks touch path X" capability**
for Spoon, working only from the quoted source and reported numbers
above. Answer:

1. **Where, in the code paths shown above, is `T2Data.Diffs` most likely
   getting dropped** before it reaches storage, and what is the minimal
   change to persist path/status/additions/deletions (not necessarily
   patch) for every fork whose T2/compare step already ran — not just the
   subset currently reached via the expensive per-commit
   (`enrichCommitFiles`) path? Is this plausibly free (data already in
   memory, just needs a store write), or can you think of a real reason
   it might have been deliberately deferred (cost, correctness,
   cache-scope) that the requester should check for before assuming it's
   simply an oversight?
2. **What's the right CLI/API surface** for "given an upstream repo and a
   path pattern (a literal path, a glob, or a small set of paths), report
   every fork whose own ahead-commits touch it, with status
   (added/modified/removed) and size"? Options to weigh: a new flag on
   the existing fork-listing command, a new subcommand, or a query
   against already-persisted data with a live compare call only for
   forks not yet scanned. Prefer designs where a re-run against
   already-collected data costs zero new API calls.
3. **Is "zero ahead-commits" a valid zero-cost pre-filter** — i.e., can a
   fork with `AheadCount == 0` be skipped before even asking the
   file-list question, since a three-dot diff against a fork with no
   ahead-commits is definitionally empty? (The requester reports Spoon
   already has some notion of a "no-ahead" fork being low-value for
   other purposes — confirm this reasoning holds for this specific
   question too, since it seems close to definitionally true rather than
   something needing new investigation.)
4. **Batching**: the requester's manual sweep used ad hoc GraphQL
   alias-batching (tens of repos per query) purely to fetch each fork's
   default branch name cheaply, and reports that GraphQL failed an
   *entire batch* when one repo in it had been renamed or deleted (no
   partial results for that batch without a REST fallback per repo).
   Given Spoon's compare path is REST-based (one call, or a few with
   side-branch scanning, per fork), is a GraphQL batching tier worth
   adding ahead of the REST compare (e.g. batch-fetching a target path's
   blob hash across many repos in one query, as a cheap "definitely
   unchanged" filter before spending a REST compare call on a fork)?
   Weigh this against GraphQL's per-query point-cost model, and note
   that some upstream repos have far more than a few thousand forks, so
   the design should reason about network sizes an order of magnitude
   larger than the 3691-fork example.
5. **Output shape**: should a match against the target path become a new
   field on Spoon's existing per-fork record output, a separate filtered
   query/command, or both? How should it interact with any existing
   visibility/ranking concept Spoon has for a fork (should a fork that
   touches the target path but scores poorly on Spoon's usual signals
   still be surfaced here, or suppressed consistently with the rest of
   the tool)? This document cannot confirm the exact shape of that
   existing concept — treat it as an open question to resolve once real
   repo access exists.

## Deliverable expected back

A concrete, pseudo-implementation-shaped backlog: numbered items, each
with a stated goal, the design decision it makes, and — once this agent
or a follow-up session has real access to the Spoon source and can cite
exact file:line locations — grounding citations. Where a question above
cannot be answered from the quoted excerpts alone, say so explicitly
rather than guessing at unseen code. Where the requester's reported
numbers (3691 forks, 62 previously enriched, etc.) would benefit from
being re-verified or refreshed, say what query or command would produce
that verification rather than assuming access to run it.

## Errata after source verification (2026-09-03)

- No gate discards `T2.Diffs`: `cmd/spn/forks.go:956-966` and
  `store.SnapshotFromForge` persist `compare_files` on every live fetch.
- 62 of 69 ahead>0 forks have file rows; the other 7 have net-empty three-dot
  diffs (`TotalAdditions=0`). Zero-ahead forks have no rows by definition.
- 1572 forks were never compared because the rate-reserve floor stopped the
  sweep, not because data was dropped.
- GitHub's 300-file cap is real: 10 impeccable forks sit at exactly 300 rows.
  Now flagged as `T2Data.FilesTruncated`.
- Rank override (`Expected-Rank = 9999`) rejected; matches are pinned through
  `VisibilityDecision` instead.
- `pushed_at <= created_at` is not a hard zero-ahead proof (1/1855 exception
  observed); such forks are compared last, not skipped.
- Citations above are search-grounding redirects; API specifics unverified.
- The "GraphQL pre-filter — Rejected" row in the `--touching` implementation
  plan's verified-against-source table
  (`docs/superpowers/plans/2026-09-03-touching-path-filter.md:32`, reasoning:
  "REST compare is already mandatory and returns the file list") no longer
  holds. A later branch makes the REST compare non-mandatory for zero-ahead
  forks: a pre-dispatch GraphQL batch resolves an entire network's
  ahead/behind status in a handful of queries, and a fork the batch finds
  with nothing ahead of upstream gets a synthesised T2 with no compare call
  at all. See `docs/research/2026-09-03-github-request-efficiency.md`.
- See `docs/research/2026-09-03-github-request-efficiency.md` for the
  follow-up work that reduces `spn forks list`'s GitHub request volume: the
  GraphQL batch above, an unbounded `.diff` fallback for the 300-file
  compare cap this document first flagged, and a `tree-commit-info`-backed
  skip for `--touching` on literal paths.
