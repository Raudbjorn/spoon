# `spn forks list --touching` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Answer "which forks of X changed path P in their own commits" network-wide, at zero API cost against a warm cache, from the CLI and the TUI, without a tip-vs-tip diff ever being consulted.

**Architecture:** A new `--touching <pattern>` flag annotates every `forksops.Result` with a `TouchMatch` derived solely from `T2.Diffs` (GitHub's merge-base-relative compare, already fetched and persisted per fork). Each matched file is weighted by the upstream centrality backend spoon already runs for `changeImpact`, so forks that edited load-bearing code list first. Matched forks are pinned via the existing `VisibilityDecision` envelope, never via a fake rank score. The CLI filters at emit time (after persistence) so paid compares are never discarded. The TUI gets a `path:` filter, a "touched files" detail section, and an on-demand patch view. A follow-up spec covers unit-level attribution inside a touched file.

**Tech Stack:** Go 1.26.2, stdlib `path.Match`, libsql store, Bubble Tea TUI. No new dependencies.

**Spec:** `docs/research/2026-09-03-network-wide-distinguishing-file-detection.md` (research brief) plus the errata in Context below. Task 12 copies this plan to `docs/superpowers/plans/2026-09-03-touching-path-filter.md` per repo convention.

---

## Context

### Why

Finding forks that genuinely modified one load-bearing file (e.g. `cli/engine/registry/antipatterns.mjs` in pbakaus/impeccable, 3692 forks) is a recurring manual task. Two false positives came from tip-vs-tip diffs that could not separate fork-authored edits from upstream drift. Spoon's compare stage already computes the correct three-dot answer and stores the file list; nothing exposes it as a query.

### Verified against source and the live store (2026-09-03)

The research brief was written without repo access. Its central diagnosis is wrong and this plan does not implement backlog items 1–3 or the rank override:

| Brief claimed | Reality (verified) |
|---|---|
| `T2.Diffs` discarded by a gate before store | No gate. `cmd/spn/forks.go:956-966` and `store.SnapshotFromForge` (`internal/store/store.go:1121-1124`) write `compare_files` unconditionally on every live fetch. Patch text stored verbatim. |
| 62/3691 forks have file rows, "almost all" have scalars | 3692 forks. 2120 have T2. 69 have `AheadCount>0`. 62 have `compare_files` rows. The 7 missing all have `TotalAdditions=0` (net-empty three-dot diff, sync-merge commits). Zero-ahead forks have empty file lists by definition, so coverage of the interesting set is complete. |
| Remaining forks unscanned because discarded | 1572 forks never got a compare: the rate-reserve floor (`stream.go:695-703`) stopped the sweep. 1476 of them have `pushed_at <= created_at`. Re-runs backfill from cache. |
| 300-file truncation "plausible" | Real and present: 10 impeccable forks sit at exactly 300 `compare_files` rows. No code handles it (`FetchCompare` is a single GET, `compare.go:14-32`). |
| Set `Expected-Rank = 9999` to surface matches | Rejected. `Heat.Score` feeds `assignNetworkRanks` and the Robbins rank pool; corrupting it poisons downstream math. Use `VisibilityDecision` (`internal/forksops/annotations.go:13-16`) with a new `pinned` status. |
| GraphQL pre-filter | Rejected, brief's own reasoning holds: REST compare is already mandatory and returns the file list. |

Also verified: cached T2 (`RepoSnapshot.ValidT2`, `store.go:172`) rehydrates `Diffs` without patch text, so matching over cache costs zero API calls. Branch scan is unconditional inside `GHProvider.Compare` (`internal/github/adapter.go:336-344`), so side-branch work flows into `T2.Diffs` already. `internal/priors/priors.go:169-185` already has a path matcher (single-segment `path.Match`, no `**`). Brief citations are Vertex grounding redirects: treat GitHub-API specifics as unverified.

One counterexample to "never pushed means zero ahead": `watcharin101ac/impeccable` has `pushed_at < created_at` yet 70 ahead commits (1 of 1855). So never-pushed forks are **deprioritised, not skipped** (Task 6).

### Reference material in `.do-not-commit/` (never committed)

- **RepoMaster** (arXiv 2505.21577, §3.2): core-component identification = personalised PageRank over the module dependency graph plus complexity, usage, semantic, doc and git-activity features. Spoon already ships this idea as `repo.Centrality` (`internal/repo/centrality.go:31-35`): a directory proxy by default, `--full-mdg` PageRank (`internal/mdg`), both cached as JSON under `$XDG_CACHE_HOME/spoon/{centrality,mdg}/`, resolved by `cluster.loadOrComputeCentrality` (`internal/cluster/pipeline.go:546-571`) and folded into `Heat.ChangeImpact` as the mean over all touched paths. This plan reuses that backend to score each **touched file individually** (Task 3, 5, 6).
- **DeepRepoQA** (arXiv 2608.24221): MCTS agent QA over repository structure. Out of scope for code; informs the follow-up spec (Task 12).
- **ai-codebase-analyzer** (Yadu080): FAISS RAG over chunked source. Its `docs/knowledge-base/09-database-and-storage.md` is the source of the `source_files`/`code_units`/`unit_alignments` schema. **Those tables exist in `~/.config/spoon/spoon.db` (all empty, 0 rows) but not in this checkout's `store.go`**; they were created by the system `/usr/bin/spn` built from a dirty tree at 1a74fa2. Nothing in this plan reads or writes them.
- `greptile.com.txt`, `githubnext.com.txt`: empty placeholders; nothing to act on.

### User decisions (asked 2026-09-03)

- Non-matching forks are **dropped** from stdout (still persisted).
- `--touching` **widens coverage**: pushed-after-fork forks compared first, never-pushed last.
- Glob semantics gain **`**`** via a small stdlib-only matcher.
- **TUI tasks included**: `path:` filter, detail section, on-demand patch view.
- **Centrality weight on touched files**: each matched file gets the upstream centrality of its directory/module; matched forks sort by the most load-bearing file they touched.
- **Unit-level attribution** ("which functions inside the file changed") is a follow-up **spec**, not tasks (Task 12).

## Global Constraints

- Go `1.26.2` (`go.mod:3`); repo vendors; no new modules.
- Every path match sources paths from `forge.T2Data.Diffs` only. Never from a tree/tip diff. Comment this guard at the matcher call site.
- `Heat.Score`, `ExpectedRank`, `RankStats` are never mutated by touching logic.
- `forksops.Stream` never drops a `Result` (priors contract, `stream.go:842-845`); filtering is the CLI's job after `persistSnapshotBestEffort`.
- NDJSON additions are omit-when-not-computed (`cmd/spn/forks.go:1289-1295` pattern). CSV schema (24 fixed columns) untouched; `--touching` with `--csv` is rejected.
- Verification for every task: `go test ./<pkg>/... && go vet ./...`. Final: `go build ./cmd/... && go test ./...`.
- Commit per task. Message style: `feat(forks): ...`, `feat(tui): ...`, `docs: ...`; end with the session trailer given in the environment.

---

### Task 1: `internal/pathmatch` — glob matcher with `**`

**Files:**
- Create: `internal/pathmatch/pathmatch.go`
- Create: `internal/pathmatch/pathmatch_test.go`
- Modify: `internal/priors/priors.go:80-81,169-185`
- Modify: `internal/priors/priors_test.go` (add `**` case)

**Interfaces:**
- Produces:
  ```go
  package pathmatch
  func Normalize(pattern string) (string, error)          // trims "./" and trailing "/", rejects "", "/", "..", absolute
  func Match(pattern, name string) (bool, error)           // see semantics below
  type Matcher struct{ patterns []string }
  func Compile(patterns []string) (Matcher, error)         // Normalize + syntax check each
  func (m Matcher) First(name string) (pattern string, ok bool)
  func (m Matcher) Patterns() []string
  ```
- Semantics: no wildcard → `name == pattern || strings.HasPrefix(name, pattern+"/")`. Wildcard, no `**` → `path.Match` (single segment, `*` does not cross `/`). `**` segment → zero or more path segments.

- [ ] **Step 1: Write the failing tests**

```go
package pathmatch

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"cli/engine/registry/antipatterns.mjs", "cli/engine/registry/antipatterns.mjs", true},
		{"cli/engine", "cli/engine/registry/antipatterns.mjs", true}, // dir prefix
		{"cli/eng", "cli/engine/x", false},                           // not a segment prefix
		{"*.mjs", "a.mjs", true},
		{"*.mjs", "cli/a.mjs", false}, // single segment
		{"**/*.mjs", "cli/a.mjs", true},
		{"**/*.mjs", "a.mjs", true}, // ** matches zero segments
		{"**/registry/antipatterns.mjs", ".claude/skills/impeccable/scripts/detector/registry/antipatterns.mjs", true},
		{"cli/**", "cli/engine/x.js", true},
		{"cli/**/x.js", "cli/x.js", true},
		{"cli/**/x.js", "lib/x.js", false},
		{"tests/**/*.html", "tests/fixtures/antipatterns/label.html", true},
	}
	for _, tc := range cases {
		got, err := Match(tc.pattern, tc.name)
		if err != nil {
			t.Fatalf("Match(%q,%q): %v", tc.pattern, tc.name, err)
		}
		if got != tc.want {
			t.Errorf("Match(%q,%q)=%v want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestMatchBadPattern(t *testing.T) {
	if _, err := Match("[", "x"); err == nil {
		t.Fatal("expected ErrBadPattern")
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"./src/": "src", "src/a.go": "src/a.go"} {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "/", "/abs", "../x", "a/../b"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q): expected error", bad)
		}
	}
}

func TestCompileFirst(t *testing.T) {
	m, err := Compile([]string{"docs/", "**/*.mjs"})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := m.First("cli/a.mjs"); !ok || p != "**/*.mjs" {
		t.Errorf("First = %q,%v", p, ok)
	}
	if p, ok := m.First("docs/x.md"); !ok || p != "docs" {
		t.Errorf("First = %q,%v", p, ok)
	}
	if _, ok := m.First("README"); ok {
		t.Error("unexpected match")
	}
	if _, err := Compile([]string{"["}); err == nil {
		t.Error("expected compile error")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/pathmatch/ -run . -v`
Expected: FAIL, package does not exist / undefined symbols.

- [ ] **Step 3: Implement**

```go
// Package pathmatch matches repository-relative file paths against user
// patterns. It exists so `spn forks list --touching`, `--priors` and the TUI
// path filter agree on one rule set.
//
// Semantics:
//   - no wildcard: exact path, or directory prefix (pattern or pattern+"/").
//   - wildcard without "**": path.Match, so "*" never crosses "/".
//   - a "**" segment matches zero or more whole segments.
package pathmatch

import (
	"errors"
	"path"
	"strings"
)

var ErrBadPattern = errors.New("pathmatch: bad pattern")

// Normalize strips a leading "./" and a trailing "/", and rejects empty,
// absolute, or dot-dot patterns. Patterns are repo-relative by definition.
func Normalize(pattern string) (string, error) {
	p := strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	p = strings.TrimSuffix(p, "/")
	if p == "" || strings.HasPrefix(p, "/") {
		return "", ErrBadPattern
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" {
			return "", ErrBadPattern
		}
	}
	return p, nil
}

func hasWildcard(s string) bool { return strings.ContainsAny(s, "*?[") }

// Match reports whether name matches pattern. The only error is ErrBadPattern.
func Match(pattern, name string) (bool, error) {
	if !hasWildcard(pattern) {
		return name == pattern || strings.HasPrefix(name, pattern+"/"), nil
	}
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, name)
		if err != nil {
			return false, ErrBadPattern
		}
		return ok, nil
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, segs []string) (bool, error) {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 0 && pat[0] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true, nil
			}
			for i := 0; i <= len(segs); i++ {
				ok, err := matchSegments(pat, segs[i:])
				if err != nil || ok {
					return ok, err
				}
			}
			return false, nil
		}
		if len(segs) == 0 {
			return false, nil
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil {
			return false, ErrBadPattern
		}
		if !ok {
			return false, nil
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0, nil
}

// Matcher is a compiled, validated pattern list.
type Matcher struct{ patterns []string }

// Compile normalizes and syntax-checks every pattern. An empty list compiles
// to a Matcher that matches nothing.
func Compile(patterns []string) (Matcher, error) {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		n, err := Normalize(p)
		if err != nil {
			return Matcher{}, err
		}
		for _, seg := range strings.Split(n, "/") {
			if seg == "**" {
				continue
			}
			if _, err := path.Match(seg, ""); err != nil {
				return Matcher{}, ErrBadPattern
			}
		}
		out = append(out, n)
	}
	return Matcher{patterns: out}, nil
}

// First returns the first pattern matching name.
func (m Matcher) First(name string) (string, bool) {
	for _, p := range m.patterns {
		if ok, _ := Match(p, name); ok {
			return p, true
		}
	}
	return "", false
}

func (m Matcher) Patterns() []string { return append([]string(nil), m.patterns...) }
func (m Matcher) Empty() bool         { return len(m.patterns) == 0 }
```

- [ ] **Step 4: Delegate priors**

In `internal/priors/priors.go` replace the body of `matchPath` (`:171-185`) with:

```go
func matchPath(p string, changedPaths []string) bool {
	for _, c := range changedPaths {
		if ok, _ := pathmatch.Match(p, c); ok {
			return true
		}
	}
	return false
}
```

Update the two doc comments at `:80-81` and `:169-170` to say `**` is supported (delegates to `internal/pathmatch`). Add to `priors_test.go` a case with `Paths: []string{"**/registry/*.mjs"}` matching `a/b/registry/x.mjs`.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/pathmatch/ ./internal/priors/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/pathmatch internal/priors
git commit -m "feat(pathmatch): shared path matcher with ** support; priors delegates"
```

---

### Task 2: `FilesTruncated` on `T2Data`

**Files:**
- Modify: `internal/forge/types.go:257-282` (T2Data), add const
- Modify: `internal/github/adapter.go:507-565` (`compareToT2`)
- Modify: `internal/gitea/compare.go:52-88,109-110` (set from local `capped`)
- Modify: `internal/gitlab/compare.go:48-50` (comment only)
- Modify: `cmd/spn/forks.go:1300-1312` (NDJSON `t2.files_truncated`)
- Test: `internal/github/compare_to_t2_test.go`, `cmd/spn/forks_detail_test.go`

**Interfaces:**
- Produces: `forge.CompareFilesCap = 300`; `forge.T2Data.FilesTruncated bool`. Survives into `t2_json` automatically (store strips only `Diffs`/`Commits`, `store.go:1035-1048`). Old rows decode as `false`; consumers must also test `len(Diffs) >= CompareFilesCap`.

- [ ] **Step 1: Failing test** (`internal/github/compare_to_t2_test.go`)

```go
func TestCompareToT2FlagsTruncatedFileList(t *testing.T) {
	files := make([]FileChange, forge.CompareFilesCap)
	for i := range files {
		files[i] = FileChange{Filename: fmt.Sprintf("f%d", i), Status: "modified"}
	}
	got := compareToT2(CompareResult{Performed: true, AheadBy: 1, Files: files})
	if !got.FilesTruncated {
		t.Fatal("300 files must set FilesTruncated")
	}
	got = compareToT2(CompareResult{Performed: true, AheadBy: 1, Files: files[:299]})
	if got.FilesTruncated {
		t.Fatal("299 files must not set FilesTruncated")
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/github/ -run TestCompareToT2FlagsTruncatedFileList` → FAIL (undefined field).

- [ ] **Step 3: Implement**

`internal/forge/types.go`, above `type T2Data`:

```go
// CompareFilesCap is the largest file list a single GitHub compare response
// carries (300). A T2 whose Diffs hit it may be missing files; see
// T2Data.FilesTruncated.
const CompareFilesCap = 300
```

In `T2Data` after `Diffs`:

```go
	// FilesTruncated is true when the provider capped the file list (GitHub:
	// 300 entries per compare, unpaginated here). Diffs, TotalAdditions,
	// TotalDeletions and MNA are then lower bounds. Rows stored before this
	// field existed decode as false; readers must also treat
	// len(Diffs) >= CompareFilesCap as truncated.
	FilesTruncated bool
```

`compareToT2`: add `FilesTruncated: len(r.Files) >= forge.CompareFilesCap,` to the returned literal.

`internal/gitea/compare.go`: where the local `capped` is known and the T2 is built, set `FilesTruncated: capped`. `internal/gitlab/compare.go:48-50`: add comment `// GitLab diff caps are not detected; FilesTruncated stays false.`

`cmd/spn/forks.go` inside `if r.T2 != nil {` after `"mna"`: `t2["files_truncated"] = r.T2.FilesTruncated || len(r.T2.Diffs) >= forge.CompareFilesCap`.

Extend `cmd/spn/forks_detail_test.go` to assert `t2["files_truncated"] == false` on the existing fixture.

- [ ] **Step 4: Run** `go test ./internal/github/ ./internal/gitea/ ./internal/gitlab/ ./cmd/spn/ ./internal/store/` → PASS (round-trip test in store must still pass; new bool marshals fine).

- [ ] **Step 5: Commit** `git commit -m "feat(compare): flag 300-file truncation on T2Data and NDJSON"`

---

### Task 3: `forksops` touching types and scorer

**Files:**
- Create: `internal/forksops/touching.go`
- Create: `internal/forksops/touching_test.go`
- Modify: `internal/forksops/stream.go:250-334` (Result gains `Touching *TouchMatch`)

**Interfaces:**
- Produces:
  ```go
  type TouchStatus string // "matched" | "unmatched" | "unknown" | "never_pushed"
  type TouchedFile struct { Path, PreviousPath, Status string; Additions, Deletions int; Pattern string; Centrality float64 }
  type TouchMatch struct { Status TouchStatus; Reason string; Files []TouchedFile; Partial bool; Impact float64; CentralityMethod string }
  type TouchSummary struct { Matched, Partial, Unmatched, Unknown, NeverPushed int; CentralityMethod string }
  func NeverPushed(f forge.T1Data) bool
  func touchOne(m pathmatch.Matcher, c repo.Centrality, r Result) TouchMatch   // c may be nil
  func scoreTouching(m pathmatch.Matcher, c repo.Centrality, results []Result) TouchSummary   // sets results[i].Touching
  func centralityMethod(c repo.Centrality) string   // "" | "directory" | "mdg"
  func (t *TouchMatch) Emit() bool   // matched, or unmatched-but-partial
  ```
- Consumes: `forge.CompareFilesCap`, `T2Data.FilesTruncated` (Task 2), `pathmatch.Matcher` (Task 1), `repo.Centrality` (`internal/repo/centrality.go:31-35`, `ScoreFork([]string) float64` in [0,1]).
- Centrality semantics: `Centrality = c.ScoreFork([]string{path})` per file (the directory proxy scores the file's directory; MDG scores its module). `Impact = max(files.Centrality)`. Zero when `c == nil`. Never touches `Heat`.

- [ ] **Step 1: Failing tests** (`touching_test.go`)

```go
package forksops

import (
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/pathmatch"
	"github.com/svnbjrn/spoon/internal/repo"
)

func mustMatcher(t *testing.T, pats ...string) pathmatch.Matcher {
	t.Helper()
	m, err := pathmatch.Compile(pats)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTouchOne(t *testing.T) {
	m := mustMatcher(t, "**/registry/antipatterns.mjs")
	diff := forge.FileDiff{Path: "cli/engine/registry/antipatterns.mjs", Status: "modified", Additions: 7}
	cases := []struct {
		name string
		r    Result
		want TouchStatus
		emit bool
	}{
		{"matched", Result{T2: &forge.T2Data{Performed: true, AheadCount: 2, Diffs: []forge.FileDiff{diff}}}, TouchMatched, true},
		{"unmatched", Result{T2: &forge.T2Data{Performed: true, AheadCount: 2, Diffs: []forge.FileDiff{{Path: "README.md"}}}}, TouchUnmatched, false},
		{"zero_ahead_is_unmatched_without_scanning", Result{T2: &forge.T2Data{Performed: true, AheadCount: 0, Diffs: []forge.FileDiff{diff}}}, TouchUnmatched, false},
		{"compare_404", Result{T2: &forge.T2Data{Performed: false}}, TouchUnknown, false},
		{"no_t2_reserve", Result{BudgetSkip: &StageSkip{Stage: "compare"}}, TouchUnknown, false},
		{"no_t2_never_pushed", Result{Fork: forge.T1Data{CreatedAt: time.Unix(10, 0), PushedAt: time.Unix(10, 0)}}, TouchNeverPushed, false},
		{"cache_rows_missing", Result{T2FromCache: true, T2: &forge.T2Data{Performed: true, AheadCount: 3, TotalAdditions: 9}}, TouchUnknown, false},
		{"cache_net_empty", Result{T2FromCache: true, T2: &forge.T2Data{Performed: true, AheadCount: 3}}, TouchUnmatched, false},
		{"renamed_from_matches_previous_path", Result{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "x/new.mjs", PreviousPath: "a/registry/antipatterns.mjs", Status: "renamed"}}}}, TouchMatched, true},
	}
	for _, tc := range cases {
		got := touchOne(m, nil, tc.r)
		if got.Status != tc.want {
			t.Errorf("%s: status=%s want %s (reason %q)", tc.name, got.Status, tc.want, got.Reason)
		}
		if got.Emit() != tc.emit {
			t.Errorf("%s: Emit=%v want %v", tc.name, got.Emit(), tc.emit)
		}
	}
}

func TestTouchOnePartial(t *testing.T) {
	m := mustMatcher(t, "never/matches")
	diffs := make([]forge.FileDiff, forge.CompareFilesCap)
	for i := range diffs {
		diffs[i].Path = "f" + string(rune('a'+i%26))
	}
	got := touchOne(m, nil, Result{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: diffs}})
	if got.Status != TouchUnmatched || !got.Partial || !got.Emit() {
		t.Fatalf("300-file unmatched must be partial and emittable: %+v", got)
	}
	got = touchOne(m, nil, Result{T2: &forge.T2Data{Performed: true, AheadCount: 1, FilesTruncated: true, Diffs: diffs[:5]}})
	if !got.Partial {
		t.Fatal("FilesTruncated flag must mark partial")
	}
}

func TestScoreTouchingSummary(t *testing.T) {
	m := mustMatcher(t, "a.go")
	rs := []Result{
		{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "a.go"}}}},
		{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "b.go"}}}},
		{BudgetSkip: &StageSkip{Stage: "compare"}},
	}
	s := scoreTouching(m, nil, rs)
	if s.Matched != 1 || s.Unmatched != 1 || s.Unknown != 1 {
		t.Fatalf("summary %+v", s)
	}
	if rs[0].Touching == nil || rs[0].Touching.Files[0].Pattern != "a.go" {
		t.Fatalf("pattern not recorded: %+v", rs[0].Touching)
	}
	if s.CentralityMethod != "" || rs[0].Touching.Impact != 0 {
		t.Fatalf("no centrality backend must yield zero impact: %+v", rs[0].Touching)
	}
}

func TestTouchOneCentrality(t *testing.T) {
	m := mustMatcher(t, "**/*.go")
	// repo.DirectoryCentrality keys directories with a trailing slash
	// (internal/repo/centrality.go:205-215) and ScoreFork averages them.
	c := repo.DirectoryCentrality{DirScore: map[string]float64{"core/": 1.0, "docs/": 0.1}}
	r := Result{T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{
		{Path: "docs/x.go"}, {Path: "core/y.go"},
	}}}
	got := touchOne(m, c, r)
	if got.CentralityMethod != "directory" {
		t.Fatalf("method %q", got.CentralityMethod)
	}
	if got.Files[0].Centrality >= got.Files[1].Centrality {
		t.Fatalf("per-file centrality not applied: %+v", got.Files)
	}
	if got.Impact != got.Files[1].Centrality || got.Impact <= 0.9 {
		t.Fatalf("Impact must be the max file centrality: %+v", got)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/forksops/ -run 'TestTouch|TestScoreTouching'` → FAIL.

- [ ] **Step 3: Implement** (`touching.go`)

```go
package forksops

import (
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/pathmatch"
	"github.com/svnbjrn/spoon/internal/repo"
)

// Touching answers "did this fork's OWN ahead commits touch path P".
//
// The only admissible source is T2.Diffs: GitHub's compare endpoint computes
// it merge-base-relative (base...head), so upstream commits the fork is
// behind never appear. A tip-vs-tip diff would report all of them as fork
// changes; that exact bug produced two confirmed false positives on
// pbakaus/impeccable (docs/research/2026-09-03-network-wide-distinguishing-
// file-detection.md). Nothing in this file may consult a tree or tip diff.

type TouchStatus string

const (
	TouchMatched     TouchStatus = "matched"
	TouchUnmatched   TouchStatus = "unmatched"
	TouchUnknown     TouchStatus = "unknown"      // no usable compare; see Reason
	TouchNeverPushed TouchStatus = "never_pushed" // no compare and pushed_at <= created_at
)

// TouchedFile is one T2 diff entry that matched a --touching pattern.
// Centrality is the upstream repo.Centrality score of the file's
// directory/module in [0,1]; 0 when no backend was available.
type TouchedFile struct {
	Path, PreviousPath, Status string
	Additions, Deletions       int
	Pattern                    string
	Centrality                 float64
}

// TouchMatch is the per-fork verdict. Partial means the provider capped the
// file list, so an unmatched verdict may be a false negative. Impact is the
// highest upstream centrality among the matched files (0 when no backend);
// CentralityMethod names the backend that produced it.
type TouchMatch struct {
	Status           TouchStatus
	Reason           string // unknown only: compare_unavailable | cache_no_files | reserve_skipped
	Files            []TouchedFile
	Partial          bool
	Impact           float64
	CentralityMethod string // "" | "directory" | "mdg"
}

// centralityMethod names the backend for output. The MDG backend lives in
// internal/mdg and only shares the interface, so anything that is not the
// directory proxy is reported as "mdg".
func centralityMethod(c repo.Centrality) string {
	switch c.(type) {
	case nil:
		return ""
	case repo.DirectoryCentrality, *repo.DirectoryCentrality:
		return "directory"
	default:
		return "mdg"
	}
}

// Emit reports whether the CLI should print this fork under --touching:
// matches always, and unmatched forks whose file list was truncated (the
// user must be told the answer is incomplete for them).
func (t *TouchMatch) Emit() bool {
	if t == nil {
		return false
	}
	return t.Status == TouchMatched || (t.Status == TouchUnmatched && t.Partial)
}

// TouchSummary is the run-level tally, written to Options.TouchReport.
type TouchSummary struct {
	Matched, Partial, Unmatched, Unknown, NeverPushed int
	CentralityMethod                                  string
}

// NeverPushed reports a fork that has not been pushed since it was created.
// Such forks are zero-ahead in all but pathological cases (1 of 1855 observed
// on impeccable), so callers deprioritise them; they are never skipped.
func NeverPushed(f forge.T1Data) bool {
	return !f.CreatedAt.IsZero() && !f.PushedAt.After(f.CreatedAt)
}

func touchOne(m pathmatch.Matcher, c repo.Centrality, r Result) TouchMatch {
	if r.T2 == nil {
		if r.BudgetSkip != nil {
			return TouchMatch{Status: TouchUnknown, Reason: "reserve_skipped"}
		}
		if NeverPushed(r.Fork) {
			return TouchMatch{Status: TouchNeverPushed}
		}
		return TouchMatch{Status: TouchUnknown, Reason: "compare_unavailable"}
	}
	t2 := r.T2
	if !t2.Performed {
		return TouchMatch{Status: TouchUnknown, Reason: "compare_unavailable"}
	}
	// Zero ahead commits: the three-dot diff is empty by definition. Do not
	// scan Diffs (they may be stale or absent).
	if t2.AheadCount == 0 {
		return TouchMatch{Status: TouchUnmatched}
	}
	// A cached compare rehydrates Diffs from compare_files. Missing rows for a
	// compare that had a non-empty diff are "unknown", not "unmatched".
	if r.T2FromCache && len(t2.Diffs) == 0 && t2.TotalAdditions+t2.TotalDeletions > 0 {
		return TouchMatch{Status: TouchUnknown, Reason: "cache_no_files"}
	}
	partial := t2.FilesTruncated || len(t2.Diffs) >= forge.CompareFilesCap
	var files []TouchedFile
	for _, d := range t2.Diffs {
		pat, ok := m.First(d.Path)
		if !ok && d.PreviousPath != "" {
			pat, ok = m.First(d.PreviousPath)
		}
		if !ok {
			continue
		}
		tf := TouchedFile{
			Path: d.Path, PreviousPath: d.PreviousPath, Status: d.Status,
			Additions: d.Additions, Deletions: d.Deletions, Pattern: pat,
		}
		if c != nil {
			// Per-file, not per-fork: ScoreFork over one path is that
			// path's own directory/module score, which is what "how
			// load-bearing is this file" asks. Heat.ChangeImpact keeps its
			// mean-over-all-paths meaning untouched.
			tf.Centrality = c.ScoreFork([]string{d.Path})
		}
		files = append(files, tf)
	}
	if len(files) == 0 {
		return TouchMatch{Status: TouchUnmatched, Partial: partial}
	}
	out := TouchMatch{Status: TouchMatched, Files: files, Partial: partial, CentralityMethod: centralityMethod(c)}
	for _, f := range files {
		if f.Centrality > out.Impact {
			out.Impact = f.Centrality
		}
	}
	return out
}

// scoreTouching annotates every result in place and returns the tally.
func scoreTouching(m pathmatch.Matcher, c repo.Centrality, results []Result) TouchSummary {
	s := TouchSummary{CentralityMethod: centralityMethod(c)}
	for i := range results {
		t := touchOne(m, c, results[i])
		results[i].Touching = &t
		switch t.Status {
		case TouchMatched:
			s.Matched++
		case TouchUnmatched:
			s.Unmatched++
		case TouchUnknown:
			s.Unknown++
		case TouchNeverPushed:
			s.NeverPushed++
		}
		if t.Partial {
			s.Partial++
		}
	}
	return s
}
```

Add to `Result` in `stream.go` after `PriorReasons`:

```go
	// Touching is the --touching verdict; nil when the option was not set.
	// Presentation and CLI filtering only: never feeds heat or rank.
	Touching *TouchMatch
```

- [ ] **Step 4: Run** `go test ./internal/forksops/ -run 'TestTouch|TestScoreTouching'` → PASS.

- [ ] **Step 5: Commit** `git commit -m "feat(forksops): touching verdict derived from merge-base-relative diffs"`

---

### Task 4: `pinned` visibility and profile label

**Files:**
- Modify: `internal/forksops/annotations.go:5-16,67-78`
- Modify: `internal/forksops/profile.go:15-17`
- Modify: `internal/forksops/annotations_policy_test.go:20-42`
- Modify: `internal/forksops/testdata/visibility_policy_cases.json`
- Test: `internal/forksops/profile_test.go` (exists? if not, add case to whichever file tests `DeriveProfile`)

**Interfaces:**
- Produces: `VisibilityPinned VisibilityStatus = "pinned"`; reasons carry `"touching:<pattern>"` first, then existing penalties. `DeriveProfile` returns `"touches_target"` for pinned forks.

- [ ] **Step 1: Failing fixture + test**

Append to `visibility_policy_cases.json` `cases`:

```json
    {
      "name": "touching_match_pins_even_upstreamed",
      "penalties": ["upstreamed"],
      "skips": {},
      "touching": {"status": "matched", "files": [{"path": "a/b.mjs", "pattern": "**/*.mjs"}]},
      "wantVisibility": {"status": "pinned", "reasons": ["touching:**/*.mjs", "upstreamed"]},
      "wantDegraded": []
    },
    {
      "name": "touching_unmatched_does_not_pin",
      "penalties": ["no_ahead"],
      "skips": {},
      "touching": {"status": "unmatched"},
      "wantVisibility": {"status": "hidden", "reasons": ["no_ahead"]},
      "wantDegraded": []
    }
```

In `annotations_policy_test.go` extend the case struct with

```go
			Touching *struct {
				Status string `json:"status"`
				Files  []struct {
					Path    string `json:"path"`
					Pattern string `json:"pattern"`
				} `json:"files"`
			} `json:"touching"`
```

and after `result := Result{...}` build `result.Touching` from it when non-nil (`TouchMatch{Status: TouchStatus(tc.Touching.Status)}` with files mapped to `TouchedFile{Path, Pattern}`).

Profile test:

```go
func TestDeriveProfilePinned(t *testing.T) {
	r := Result{Visibility: VisibilityDecision{Status: VisibilityPinned, Reasons: []string{"touching:a.go", "upstreamed"}}}
	label, facts := DeriveProfile(r)
	if label != "touches_target" || len(facts) != 2 {
		t.Fatalf("got %q %v", label, facts)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/forksops/ -run 'TestVisibilityPolicyFixture|TestDeriveProfilePinned'` → FAIL.

- [ ] **Step 3: Implement**

`annotations.go` const block: add `VisibilityPinned VisibilityStatus = "pinned"` with comment `// pinned: an explicit --touching match; overrides hidden/demoted for display only.`

`deriveVisibility`:

```go
func deriveVisibility(r Result) VisibilityDecision {
	if r.Touching != nil && r.Touching.Status == TouchMatched {
		reasons := make([]string, 0, len(r.Touching.Files)+len(r.Heat.Penalties))
		seen := map[string]bool{}
		for _, f := range r.Touching.Files {
			if !seen[f.Pattern] {
				seen[f.Pattern] = true
				reasons = append(reasons, "touching:"+f.Pattern)
			}
		}
		reasons = append(reasons, r.Heat.Penalties...)
		return VisibilityDecision{Status: VisibilityPinned, Reasons: reasons}
	}
	// ...existing body unchanged
```

`profile.go` before rule 1:

```go
	// 0. Pinned wins: the user asked for forks touching a path; say so.
	if r.Visibility.Status == VisibilityPinned {
		return "touches_target", sortedCopy(r.Visibility.Reasons)
	}
```

(Keep `sortedCopy` semantics consistent with rule 1. If sorting would move `touching:` after penalties, that is fine: reasons are facts, not ordered.) Adjust the fixture's expected `reasons` order only if `visibilityToJSON` sorts; it does not (`forks.go:1451-1454`), so keep as written.

- [ ] **Step 4: Run** `go test ./internal/forksops/` → PASS. Also `grep -rn "VisibilityHidden\|VisibilityDemoted" internal/tui cmd` to confirm no exhaustive switch needs a `pinned` arm (agent-verified: only `profile.go:16` and `forks.go:1448`).

- [ ] **Step 5: Commit** `git commit -m "feat(forksops): pinned visibility for --touching matches"`

---

### Task 5: Stream integration (options, ordering, rank pool, centrality)

**Files:**
- Modify: `internal/forksops/stream.go:27-171` (Options), `:372` (Stream entry), `:549-567` (dispatch), `:574` (batchMode), `:794-858` (tail), `:952-1010` (`runForksClusterPipeline` → extract `clusterEnv`)
- Modify: `internal/cluster/pipeline.go:546` (export `LoadOrComputeCentrality`)
- Test: `internal/forksops/stream_touching_test.go` (new), `internal/forksops/touching_test.go` (lane sort)

**Interfaces:**
- Produces:
  ```go
  // Options
  Touching    []string       // patterns; empty = off
  TouchReport *TouchSummary  // filled when Touching set

  // cluster (exported rename of loadOrComputeCentrality, body unchanged)
  func LoadOrComputeCentrality(ctx context.Context, opts PipelineOptions, inputs PipelineInputs, logger io.Writer) (repo.Centrality, bool, error)

  // forksops: the input/option construction currently inlined in
  // runForksClusterPipeline (:970-1003), shared with the touching pass
  func clusterEnv(ctx context.Context, provider forge.Forge, parent *forge.ParentData, owner, repoName string, opts ClusterOptions) (cluster.PipelineInputs, cluster.PipelineOptions)
  func sortTouchingLanes(rs []Result)   // stable: lane, then Impact desc within the matched lane
  ```
- Behaviour: `Stream` returns an error for an invalid pattern before any network. `batchMode` includes touching. Dispatch priority for never-pushed forks is lowered under touching. After the cluster pass and before `deriveVisibility`: resolve centrality through `cluster.LoadOrComputeCentrality` with the same env the cluster pass used (cache hit when clustering ran; the 24 h directory cache or the HeadSHA-pinned MDG cache otherwise; a miss costs the same 1–3 API calls the cluster pass would have paid), then `scoreTouching`. When `ShortlistN > 0`, the rank pool is the matched subset; unmatched results are appended after it (still emitted by Stream, filtered by the CLI). Final stable sort: matched (by `Impact` desc, ties keep prior order), then partial-unmatched, then the rest. Logger line summarises and names the centrality backend.

- [ ] **Step 1: Failing test** (`stream_touching_test.go`)

```go
package forksops

import (
	"context"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestStreamTouchingOrdersAndAnnotates(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	fk := func(id string, pushedAfter bool) forge.T1Data {
		f := forge.T1Data{ID: id, Owner: "o", Name: id, DefaultBranch: "main", CreatedAt: now.Add(-48 * time.Hour), PushedAt: now.Add(-48 * time.Hour)}
		if pushedAfter {
			f.PushedAt = now.Add(-time.Hour)
		}
		return f
	}
	prov := &fakeForge{
		parent: forge.ParentData{FullName: "up/repo", DefaultBranch: "main", PushedAt: now},
		forks:  []forge.T1Data{fk("hit", true), fk("miss", true), fk("stale", false)},
		t2: map[string]forge.T2Data{
			"hit":   {Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "src/a.go", Status: "modified", Additions: 1}}},
			"miss":  {Performed: true, AheadCount: 9, Diffs: []forge.FileDiff{{Path: "README.md"}}},
			"stale": {Performed: true, AheadCount: 0},
		},
	}
	var report TouchSummary
	opts := Options{Touching: []string{"src/**"}, TouchReport: &report, ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false}}
	ch, err := Stream(context.Background(), prov, "up", "repo", opts)
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for r := range ch {
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("Stream must never drop results, got %d", len(got))
	}
	if got[0].Fork.ID != "hit" || got[0].Touching.Status != TouchMatched || got[0].Visibility.Status != VisibilityPinned {
		t.Fatalf("first result must be the pinned match: %+v", got[0])
	}
	if report.Matched != 1 || report.Unmatched != 2 {
		t.Fatalf("report %+v", report)
	}
}

func TestStreamTouchingRejectsBadPattern(t *testing.T) {
	prov := &fakeForge{parent: forge.ParentData{FullName: "up/repo", DefaultBranch: "main"}}
	if _, err := Stream(context.Background(), prov, "up", "repo", Options{Touching: []string{"["}}); err == nil {
		t.Fatal("expected pattern error before any fetch")
	}
}

func TestTouchingDispatchDemotesNeverPushed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	var order []string
	prov := &fakeForge{
		parent: forge.ParentData{FullName: "up/repo", DefaultBranch: "main", PushedAt: now},
		forks: []forge.T1Data{
			{ID: "never", Owner: "o", Name: "never", DefaultBranch: "main", Stars: 500, CreatedAt: now.Add(-time.Hour), PushedAt: now.Add(-time.Hour)},
			{ID: "pushed", Owner: "o", Name: "pushed", DefaultBranch: "main", CreatedAt: now.Add(-48 * time.Hour), PushedAt: now.Add(-time.Minute)},
		},
		t2:           map[string]forge.T2Data{"never": {Performed: true}, "pushed": {Performed: true}},
		concurrency:  1,
		compareOrder: &order,
	}
	ch, err := Stream(context.Background(), prov, "up", "repo", Options{Touching: []string{"x"}, ReserveDisabled: true, Cluster: ClusterOptions{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if len(order) != 2 || order[0] != "pushed" {
		t.Fatalf("pushed-after-fork fork must be compared first, got %v", order)
	}
}
```

Lane-sort test (append to `touching_test.go`):

```go
func TestSortTouchingLanesByImpact(t *testing.T) {
	rs := []Result{
		{Fork: forge.T1Data{ID: "rest"}},
		{Fork: forge.T1Data{ID: "partial"}, Touching: &TouchMatch{Status: TouchUnmatched, Partial: true}},
		{Fork: forge.T1Data{ID: "low"}, Touching: &TouchMatch{Status: TouchMatched, Impact: 0.2}},
		{Fork: forge.T1Data{ID: "high"}, Touching: &TouchMatch{Status: TouchMatched, Impact: 0.9}},
	}
	sortTouchingLanes(rs)
	want := []string{"high", "low", "partial", "rest"}
	for i, w := range want {
		if rs[i].Fork.ID != w {
			t.Fatalf("pos %d = %s want %s", i, rs[i].Fork.ID, w)
		}
	}
}
```

(Check `fakeForge` field names against `stream_test.go:21-77` before running; adjust literal keys, not semantics. `fakeForge` is not a `*gh.GHProvider`, so `clusterEnv` yields no `TreeSource` and centrality resolves to nil in these tests; that is the intended degrade path.)

- [ ] **Step 2: Run** `go test ./internal/forksops/ -run 'TestStreamTouching|TestTouchingDispatch|TestSortTouchingLanes'` → FAIL.

- [ ] **Step 3: Implement**

`Options` (after `Priors`):

```go
	// Touching, when non-empty, annotates every result with a TouchMatch
	// (see touching.go) computed from T2.Diffs — merge-base-relative by
	// construction, never tip-vs-tip. Forces collect-then-emit. Stream never
	// drops a result; the CLI decides what to print. Under Touching the
	// dispatch order compares pushed-after-fork forks before never-pushed
	// ones so the rate reserve is spent where a match is possible.
	Touching []string
	// TouchReport, when non-nil, receives the run tally.
	TouchReport *TouchSummary
```

At the top of `Stream` (before any provider call):

```go
	touching := len(opts.Touching) > 0
	var touchMatcher pathmatch.Matcher
	if touching {
		m, err := pathmatch.Compile(opts.Touching)
		if err != nil {
			return nil, fmt.Errorf("--touching: %w", err)
		}
		touchMatcher = m
	}
```

Dispatch (`:557-559`): after computing `priorities[i]`, add

```go
			if touching && NeverPushed(s.fork) {
				priorities[i] -= touchingNeverPushedDemotion
			}
```

with `const touchingNeverPushedDemotion = 1e6 // pushes never-pushed forks behind every pushed one; they are still compared if headroom remains`.

`batchMode` (`:574`): append `|| len(opts.Touching) > 0`.

`internal/cluster/pipeline.go:546`: rename `loadOrComputeCentrality` → `LoadOrComputeCentrality` (update its two callers in the same package; add a doc line: "Exported so forksops can score --touching files with the same backend and caches the cluster pass uses.").

`stream.go` `runForksClusterPipeline`: move the construction of `inputs` (`:970-984`) and `pipelineOpts` (`:993-1003`, the `pin` resolution included) into

```go
func clusterEnv(ctx context.Context, provider forge.Forge, parent *forge.ParentData, owner, repoName string, opts ClusterOptions) (cluster.PipelineInputs, cluster.PipelineOptions) {
	// body = the moved lines, verbatim; Forks/Upstream fields are set by the caller
}
```

and have `runForksClusterPipeline` call it, then set `inputs.Forks = enriched` and `inputs.Upstream = *parent` as before. Behaviour identical; existing cluster tests must stay green.

Tail: immediately before the `deriveVisibility` loop at `:806`:

```go
		var touchSummary TouchSummary
		if touching {
			// Same backend and caches as the cluster pass: a hit is free, a
			// miss costs what clustering would have paid. Failure degrades
			// to zero centrality, never to a missing verdict.
			// NB: Stream's parameter named `repo` shadows the internal/repo
			// package here, so do not name the repo.Centrality type in this
			// scope; take the interface value straight from the call.
			envInputs, envOpts := clusterEnv(ctx, provider, &parent, owner, repo, opts.Cluster)
			centrality, ok, cerr := cluster.LoadOrComputeCentrality(ctx, envOpts, envInputs, logger)
			if !ok {
				centrality = nil
				if cerr != nil {
					fmt.Fprintf(logger, "[touching] centrality unavailable (%v); files reported without impact\n", cerr)
				}
			}
			touchSummary = scoreTouching(touchMatcher, centrality, collected)
			if opts.TouchReport != nil {
				*opts.TouchReport = touchSummary
			}
			fmt.Fprintf(logger, "[touching] matched %d (%d partial: file list capped at %d) · unmatched %d · unknown %d · never_pushed %d · centrality=%q\n",
				touchSummary.Matched, touchSummary.Partial, forge.CompareFilesCap, touchSummary.Unmatched, touchSummary.Unknown, touchSummary.NeverPushed, touchSummary.CentralityMethod)
			if touchSummary.Unknown > 0 {
				fmt.Fprintf(logger, "[touching] %d forks have no usable compare; re-run after the rate window resets to backfill\n", touchSummary.Unknown)
			}
		}
```

Shortlist (`:826-833`): wrap so the pool is the matched subset under touching:

```go
		if opts.ShortlistN > 0 {
			var report RankReport
			if touching {
				matched, rest := splitTouching(collected)
				matched, report = RankResults(matched, opts)
				collected = append(matched, rest...)
			} else {
				collected, report = RankResults(collected, opts)
			}
			// ...existing report handling
```

Add helper in `touching.go`:

```go
// splitTouching partitions results into matched and everything else,
// preserving order within each half.
func splitTouching(rs []Result) (matched, rest []Result) {
	for _, r := range rs {
		if r.Touching != nil && r.Touching.Status == TouchMatched {
			matched = append(matched, r)
		} else {
			rest = append(rest, r)
		}
	}
	return matched, rest
}

func touchLane(r Result) int {
	switch {
	case r.Touching == nil:
		return 2
	case r.Touching.Status == TouchMatched:
		return 0
	case r.Touching.Status == TouchUnmatched && r.Touching.Partial:
		return 1
	default:
		return 2
	}
}
```

After the existing sort chain (`:858`), before the emit loop:

```go
		if touching {
			sortTouchingLanes(collected)
		}
```

with, in `touching.go`:

```go
// sortTouchingLanes orders results for --touching output: matches first
// (most load-bearing file first, then whatever order the earlier passes
// produced), then capped-unmatched forks, then the rest. Stable, so
// query/priors/heat order survives inside each lane.
func sortTouchingLanes(rs []Result) {
	sort.SliceStable(rs, func(i, j int) bool {
		li, lj := touchLane(rs[i]), touchLane(rs[j])
		if li != lj {
			return li < lj
		}
		if li == 0 {
			return rs[i].Touching.Impact > rs[j].Touching.Impact
		}
		return false
	})
}
```

- [ ] **Step 4: Run** `go test ./internal/forksops/ ./internal/cluster/` → PASS (including existing stream and pipeline tests).

- [ ] **Step 5: Commit** `git commit -m "feat(forksops): --touching option: ordering, rank pool, centrality, report"`

---

### Task 6: CLI flag, pairing rules, emit filter, NDJSON block

**Files:**
- Modify: `cmd/spn/forks.go:189-518` (parse), `:531-550` (pairing), `:1232-1428` (JSON), `:1730-1822` (emit loop), plus a new `emitTouchReport`
- Modify: `cmd/spn/main.go:160-235` (usage text)
- Test: `cmd/spn/cli_contract_test.go:131-153`, `cmd/spn/forks_detail_test.go`, `cmd/spn/forks_test.go`

**Interfaces:**
- Consumes: `forksops.Options.Touching`, `TouchReport`, `Result.Touching.Emit()`.
- Produces NDJSON on stdout per emitted fork:
  ```json
  "touching": {"status": "matched", "partial": false, "impact": 0.83, "centrality_method": "directory",
               "files": [{"path": "...", "previousPath": "", "status": "modified", "additions": 7, "deletions": 0, "pattern": "**/registry/antipatterns.mjs", "centrality": 0.83}]}
  ```
  and on stderr after the run: `{"touching": {"matched": 9, "partial": 1, "unmatched": 2050, "unknown": 96, "never_pushed": 1476, "centrality_method": "directory"}}`. `impact`/`centrality` are `0` and `centrality_method` is `""` when no backend resolved (omit-vs-zero is not needed here: the method string carries the "not computed" state).

- [ ] **Step 1: Failing tests**

`cli_contract_test.go` `TestForksListParseErrors`: add cases

```go
	{args: []string{"o/r", "--touching"}, wantSubstr: "--touching requires"},
	{args: []string{"o/r", "--touching", "["}, wantSubstr: "--touching: "},
	{args: []string{"o/r", "--touching", "a.go", "--tier", "1"}, wantSubstr: "--touching needs compare data"},
	{args: []string{"o/r", "--touching", "a.go", "--csv"}, wantSubstr: "--touching emits NDJSON only"},
```

`forks_detail_test.go`: add

```go
func TestForkToJSONTouchingBlock(t *testing.T) {
	r := forksops.Result{Touching: &forksops.TouchMatch{Status: forksops.TouchMatched, Files: []forksops.TouchedFile{{Path: "a.go", Status: "added", Additions: 3, Pattern: "*.go"}}}}
	out := forkToJSON(r)
	tb, ok := out["touching"].(map[string]any)
	if !ok || tb["status"] != "matched" {
		t.Fatalf("touching block missing: %v", out["touching"])
	}
	if _, has := forkToJSON(forksops.Result{})["touching"]; has {
		t.Fatal("touching must be omitted when the option was not set")
	}
}
```

Emit-loop test (in `forks_test.go`, modelled on whichever existing test drives `streamAndEmit` with a fake provider; if none exists at that level, test `shouldEmitFork(opts, r)` as a pure helper — see Step 3):

```go
func TestShouldEmitForkUnderTouching(t *testing.T) {
	opts := forksops.Options{Touching: []string{"a.go"}}
	if shouldEmitFork(opts, forksops.Result{Touching: &forksops.TouchMatch{Status: forksops.TouchUnmatched}}) {
		t.Fatal("unmatched must be suppressed")
	}
	if !shouldEmitFork(opts, forksops.Result{Touching: &forksops.TouchMatch{Status: forksops.TouchUnmatched, Partial: true}}) {
		t.Fatal("partial unmatched must be emitted")
	}
	if !shouldEmitFork(forksops.Options{}, forksops.Result{}) {
		t.Fatal("no touching option: everything emits")
	}
}
```

- [ ] **Step 2: Run** `go test ./cmd/spn/ -run 'TestForksListParseErrors|TestForkToJSONTouchingBlock|TestShouldEmitFork'` → FAIL.

- [ ] **Step 3: Implement**

Parse arm (next to `--priors`):

```go
		case "--touching":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--touching requires a path or glob (repeatable)", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			opts.Touching = append(opts.Touching, args[i])
```

Pairing block additions:

```go
	if len(opts.Touching) > 0 {
		if _, err := pathmatch.Compile(opts.Touching); err != nil {
			return agentio.NewError(agentio.CodeBadInput, "--touching: "+err.Error()+" (repo-relative path or glob; ** matches directories)", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
		}
		if opts.Tier == 1 {
			return agentio.NewError(agentio.CodeBadInput, "--touching needs compare data; drop --tier 1", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
		}
		if csvMode {
			return agentio.NewError(agentio.CodeBadInput, "--touching emits NDJSON only; drop --csv", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
		}
		opts.TouchReport = &forksops.TouchSummary{}
	}
```

Helper + emit loop (`streamAndEmit`), after `persistSnapshotBestEffort(...)`:

```go
		if !shouldEmitFork(opts, r) {
			continue
		}
```

```go
// shouldEmitFork applies the --touching output filter. It runs after the
// snapshot is persisted so a live compare for an unmatched fork is never
// thrown away: the next --touching run with another pattern reads it from
// the store.
func shouldEmitFork(opts forksops.Options, r forksops.Result) bool {
	if len(opts.Touching) == 0 {
		return true
	}
	return r.Touching.Emit()
}
```

After `emitRankReport(stderr, opts.RankReport)` add `emitTouchReport(stderr, opts.TouchReport)`:

```go
func emitTouchReport(stderr io.Writer, s *forksops.TouchSummary) {
	if s == nil {
		return
	}
	_ = agentio.WriteNDJSON(stderr, map[string]any{"touching": map[string]any{
		"matched": s.Matched, "partial": s.Partial, "unmatched": s.Unmatched,
		"unknown": s.Unknown, "never_pushed": s.NeverPushed, "centrality_method": s.CentralityMethod,
		"note": "unknown = no usable compare (rate reserve, 404, or cached rows missing); re-run to backfill. never_pushed = pushed_at <= created_at, compared last.",
	}})
}
```

`forkToJSONDetailed`, after the priors block:

```go
	if r.Touching != nil {
		out["touching"] = touchingToJSON(*r.Touching)
	}
```

```go
func touchingToJSON(t forksops.TouchMatch) map[string]any {
	out := map[string]any{"status": string(t.Status), "partial": t.Partial}
	if t.Reason != "" {
		out["reason"] = t.Reason
	}
	if t.Status == forksops.TouchMatched {
		out["impact"] = t.Impact
		out["centrality_method"] = t.CentralityMethod
	}
	if len(t.Files) > 0 {
		files := make([]map[string]any, 0, len(t.Files))
		for _, f := range t.Files {
			files = append(files, map[string]any{
				"path": f.Path, "previousPath": f.PreviousPath, "status": f.Status,
				"additions": f.Additions, "deletions": f.Deletions, "pattern": f.Pattern,
				"centrality": f.Centrality,
			})
		}
		out["files"] = files
	}
	return out
}
```

Usage text in `cmd/spn/main.go` (next to `--priors` prose):

```
  --touching <path|glob>   Only forks whose own ahead commits touched the path
                           (repeatable; ** matches directories). Uses the
                           merge-base-relative compare already cached per fork,
                           so re-runs cost no API calls. Adds a "touching" block
                           to each record; unmatched forks are omitted. NDJSON only.
```

- [ ] **Step 4: Run** `go test ./cmd/spn/ && go vet ./cmd/...` → PASS.

- [ ] **Step 5: Commit** `git commit -m "feat(spn): forks list --touching <path|glob>"`

---

### Task 7: Docs, errata, plan copy

**Files:**
- Modify: `README.md:82-85` (example), `:176-178` (synopsis), new section after `:243-268` ("Touched paths (`--touching`)"), `:346-360` profile table (`touches_target`), priors section note on `**`
- Modify: `docs/superpowers/skills/using-spn-forks/SKILL.md` (add `--touching` and `--priors` paragraphs near `:57-59`)
- Modify: `docs/research/2026-09-03-network-wide-distinguishing-file-detection.md` (append errata)
- Create: `docs/superpowers/plans/2026-09-03-touching-path-filter.md` (copy of this plan)

- [ ] **Step 1: README section** (model on the priors section):

```markdown
### Touched paths (`--touching`)

    spn forks list pbakaus/impeccable --touching '**/registry/antipatterns.mjs'

Reports only forks whose **own ahead commits** changed a matching path, with
the file's status and line counts under `touching.files`. Matching reads the
merge-base-relative compare (`base...head`) spoon already caches per fork, so
a fork that is merely behind upstream never matches, and a re-run against a
scanned network costs no API calls. Patterns are repo-relative; `**` spans
directories, `*` does not. Repeat the flag for several patterns.

Records carry `visibility.status: "pinned"` and `profile: "touches_target"`.
Each matched file also carries `centrality`, the upstream importance of its
directory (or module with `--full-mdg`) from the same backend that feeds
`changeImpact`; `touching.impact` is the highest of them and orders the
output, so a fork that edited a core module lists before one that edited docs.
`touching.partial: true` marks forks whose file list hit GitHub's 300-file
compare cap; an unmatched fork in that state is still printed so the gap is
visible. A stderr summary tallies matched / unmatched / unknown / never_pushed;
`unknown` forks were not compared (rate reserve) — re-run to backfill.
NDJSON only; `--csv` is rejected.
```

- [ ] **Step 2: Errata** appended to the research doc:

```markdown
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
```

- [ ] **Step 3: Copy plan**: `cp ~/.claude/plans/agile-dreaming-rabbit.md docs/superpowers/plans/2026-09-03-touching-path-filter.md`.

- [ ] **Step 4: Commit** `git commit -m "docs: --touching usage, research errata, plan"`

(The follow-up spec is Task 12, committed separately so it can be reviewed on its own.)

---

### Task 8: TUI `path:` filter

**Files:**
- Modify: `internal/tui/filter.go` (parse, matcher, prompt text)
- Modify: `internal/tui/app.go` Model (add `pathFilter *pathmatch.Matcher`), `applyFilter`
- Modify: `internal/tui/table.go:476-480` (status shows `path:` filter as is)
- Test: `internal/tui/filter_test.go` (existing or new), regenerate `internal/tui/testdata/main_*_filter.golden`

**Interfaces:**
- A query beginning with `path:` filters rows to forks whose `T2.Diffs` match (`touchesPath(sf.T2, matcher)`); forks without T2 are hidden. Anything else keeps substring semantics. `matchesFilter(sf, q)` keeps its signature for tests; the compiled matcher lives on the Model to avoid recompiling per row.

- [ ] **Step 1: Failing test**

```go
func TestPathFilterMatchesTouchedFiles(t *testing.T) {
	hit := ScoredFork{Fork: forge.T1Data{ID: "o/hit"}, T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "cli/registry/antipatterns.mjs"}}}}
	miss := ScoredFork{Fork: forge.T1Data{ID: "o/miss"}, T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "README.md"}}}}
	none := ScoredFork{Fork: forge.T1Data{ID: "o/none"}}
	m := Model{forks: []ScoredFork{hit, miss, none}}
	m.applyFilter("path:**/antipatterns.mjs")
	if got := m.visibleIdx(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("visible = %v", got)
	}
	m.applyFilter("o/m")
	if got := m.visibleIdx(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("substring filter regressed: %v", got)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/tui/ -run TestPathFilterMatchesTouchedFiles` → FAIL.

- [ ] **Step 3: Implement**

`filter.go`:

```go
const pathFilterPrefix = "path:"

// matchesFilter: substring on owner/name, or, for a "path:" query, a match
// against the fork's own ahead-commit diffs (T2.Diffs, merge-base-relative;
// never a tip diff). m is nil for substring queries.
func matchesFilter(sf ScoredFork, q string, m *pathmatch.Matcher) bool {
	if q == "" {
		return true
	}
	if strings.HasPrefix(q, pathFilterPrefix) {
		return m != nil && touchesPath(sf.T2, *m)
	}
	return strings.Contains(strings.ToLower(sf.Fork.ID), strings.ToLower(q))
}

func touchesPath(t2 *forge.T2Data, m pathmatch.Matcher) bool {
	if t2 == nil || !t2.Performed || t2.AheadCount == 0 {
		return false
	}
	for _, d := range t2.Diffs {
		if _, ok := m.First(d.Path); ok {
			return true
		}
		if d.PreviousPath != "" {
			if _, ok := m.First(d.PreviousPath); ok {
				return true
			}
		}
	}
	return false
}
```

`applyFilter`: after `m.filter = q`, compile when prefixed:

```go
	m.pathFilter = nil
	if strings.HasPrefix(q, pathFilterPrefix) {
		if pm, err := pathmatch.Compile([]string{strings.TrimPrefix(q, pathFilterPrefix)}); err == nil {
			m.pathFilter = &pm
		} else {
			m.errMsg = "bad path pattern: " + err.Error()
		}
	}
```

Update `visibleIdx`/`visibleCount` and every `matchesFilter(...)` call to pass `m.pathFilter`. Prompt text: `"  Filter %d forks by owner/name, or path:<glob> for touched files\n\n"`.

- [ ] **Step 4: Goldens** `UPDATE_GOLDEN=1 go test ./internal/tui/ -run TestGoldenMainViewStates`, then `git diff --stat internal/tui/testdata` must show only the three `*_filter.golden` files; inspect one.

- [ ] **Step 5: Run** `go test ./internal/tui/...` → PASS. Commit `git commit -m "feat(tui): path: filter over fork-authored diffs"`

---

### Task 9: TUI detail section for touched files

**Files:**
- Modify: `internal/tui/detail.go:184-190`
- Test: `internal/tui/detail_test.go` (new or existing)

**Interfaces:** When `m.pathFilter != nil` and the fork matches, the detail card gains a divider and a `Touches <pattern>:` block listing up to 20 files as ` <status> <path> (+a/-d)`, plus `... and N more`. The `Files:` line appends ` (capped at 300)` when `T2.FilesTruncated || len(Diffs) >= forge.CompareFilesCap`.

- [ ] **Step 1: Failing test**

```go
func TestDetailShowsTouchedFiles(t *testing.T) {
	m := newTestModel() // internal/tui/cluster_bridge_test.go:14, returns *Model
	m.forks = []ScoredFork{{Fork: forge.T1Data{ID: "o/hit"}, T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "cli/registry/antipatterns.mjs", Status: "modified", Additions: 7, Deletions: 1}}}}}
	m.cursor = 0
	m.applyFilter("path:**/antipatterns.mjs")
	body := m.detailBody()
	for _, want := range []string{"Touches **/antipatterns.mjs", "modified cli/registry/antipatterns.mjs (+7/-1)"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q:\n%s", want, body)
		}
	}
}
```

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** in `detailBody` after the `Ahead:` line:

```go
		if sf.T2.FilesTruncated || len(sf.T2.Diffs) >= forge.CompareFilesCap {
			writeLine(styles.warn.Render(fmt.Sprintf(" file list capped at %d by the provider; counts are lower bounds", forge.CompareFilesCap)))
		}
		if m.pathFilter != nil {
			var touched []forge.FileDiff
			for _, d := range sf.T2.Diffs {
				if _, ok := m.pathFilter.First(d.Path); ok {
					touched = append(touched, d)
				}
			}
			if len(touched) > 0 {
				divider()
				writeLine(styles.warn.Render("Touches " + strings.Join(m.pathFilter.Patterns(), ", ") + ":"))
				const maxShown = 20
				for i, d := range touched {
					if i == maxShown {
						writeLine(fmt.Sprintf("   ... and %d more", len(touched)-maxShown))
						break
					}
					writeLine(fmt.Sprintf(" %s %s (+%d/-%d)", d.Status, d.Path, d.Additions, d.Deletions))
				}
				writeLine("   [p] View patch for these files")
			}
		}
```

(No per-file centrality in the TUI section: the TUI's cluster bridge keeps its centrality private and `ChangeImpact` already shows at `detail.go:157-158`. Adding it is a follow-up once `clusterEnv` has a TUI twin.)

- [ ] **Step 4: Run** `go test ./internal/tui/...` → PASS (detail goldens have no path filter, unchanged). Commit `git commit -m "feat(tui): touched-files section in fork detail"`

---

### Task 10: TUI on-demand patch view

**Files:**
- Modify: `internal/tui/keymap/keymap.go` (new scope `MainPatch`, action `ViewPatch`, bindings)
- Modify: `internal/tui/messages.go` (`patchResultMsg`)
- Create: `internal/tui/patch.go` (fetch cmd, render, key handling)
- Modify: `internal/tui/app.go` (view const `viewPatch`, dispatch in `handleDetailKey`, `Update` case, `View` case)
- Modify: `internal/tui/help.go` (include `MainPatch` scope if scopes are enumerated)
- Modify: `docs/keymap.md`
- Test: `internal/tui/patch_test.go`; regenerate `main_*_help.golden`

**Interfaces:**
- `p` in `MainDetail` → `fetchPatchCmd(fork)` → **live** `m.provider.Compare(ctx, fork, fork.DefaultBranch)` (cached T2 carries no patch text, `store.go:1195-1202`) → `patchResultMsg{forkID, t2, err}`. Render only files matching `m.pathFilter` when set, else all files, total capped at `maxPatchChars = 200_000`. Scroll keys mirror `MainDetail`. Result is not persisted (view-only; persistence stays with the enrichment sweep).

- [ ] **Step 1: Failing tests**

```go
func TestRenderPatchFiltersAndColours(t *testing.T) {
	t2 := forge.T2Data{Diffs: []forge.FileDiff{
		{Path: "a/registry/antipatterns.mjs", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
		{Path: "README.md", Status: "modified", Patch: "@@ -1 +1 @@\n-x\n+y\n"},
	}}
	m, _ := pathmatch.Compile([]string{"**/antipatterns.mjs"})
	out := renderPatch(theme.Context{}, t2, &m, 200_000)
	if !strings.Contains(out, "a/registry/antipatterns.mjs") || strings.Contains(out, "README.md") {
		t.Fatalf("filter not applied:\n%s", out)
	}
	if !strings.Contains(out, "+new") {
		t.Fatal("patch body missing")
	}
	if out2 := renderPatch(theme.Context{}, t2, nil, 10); !strings.Contains(out2, "truncated") {
		t.Fatal("cap must announce truncation")
	}
}

func TestPatchKeymap(t *testing.T) {
	if keymap.Dispatch(keymap.MainDetail, "p") != keymap.ViewPatch {
		t.Fatal("p must open the patch view from detail")
	}
	if keymap.Dispatch(keymap.MainPatch, "esc") != keymap.Back {
		t.Fatal("esc must close the patch view")
	}
}
```

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement**

`keymap.go`: add `MainPatch Scope = "main-patch"`, `ViewPatch Action = "view-patch"`, and registry rows:

```go
	{MainDetail, []string{"p"}, ViewPatch, "View patch of touched files", "No upstream counterpart"},
	{MainPatch, []string{"esc", "b", "q"}, Back, "Return to fork details", ""},
	{MainPatch, []string{"up", "k"}, Up, "Scroll up", "Spoon adds vi navigation"},
	{MainPatch, []string{"down", "j"}, Down, "Scroll down", "Spoon adds vi navigation"},
	{MainPatch, []string{"pgup"}, PageUp, "Page up", ""},
	{MainPatch, []string{"pgdown"}, PageDown, "Page down", ""},
	{MainPatch, []string{"home"}, Home, "Go to top", ""},
	{MainPatch, []string{"G", "end"}, End, "Go to bottom", "Spoon adds vi navigation"},
```

`messages.go`: `type patchResultMsg struct { forkID string; t2 forge.T2Data; err error }`.

`patch.go`:

```go
package tui

const maxPatchChars = 200_000

func (m *Model) fetchPatchCmd() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.forks) || m.provider == nil {
		return nil
	}
	fork := m.forks[m.cursor].Fork
	provider := m.provider
	m.patchLoading = true
	return func() tea.Msg {
		// Live call on purpose: cached compares carry no patch text.
		t2, err := provider.Compare(context.Background(), fork, fork.DefaultBranch)
		return patchResultMsg{forkID: fork.ID, t2: t2, err: err}
	}
}

// renderPatch renders unified-diff hunks for the files selected by pm (nil =
// every file), stopping after limit characters with a visible marker.
func renderPatch(ctx theme.Context, t2 forge.T2Data, pm *pathmatch.Matcher, limit int) string {
	var b strings.Builder
	add := lipgloss.NewStyle().Foreground(ctx.Palette.Success)
	del := lipgloss.NewStyle().Foreground(ctx.Palette.Error)
	hunk := lipgloss.NewStyle().Foreground(ctx.Palette.Info)
	shown := 0
	for _, d := range t2.Diffs {
		if pm != nil {
			if _, ok := pm.First(d.Path); !ok {
				continue
			}
		}
		shown++
		b.WriteString(lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("=== %s %s (+%d/-%d)", d.Status, d.Path, d.Additions, d.Deletions)) + "\n")
		if d.Patch == "" {
			b.WriteString("  (no patch text from provider — binary, too large, or omitted)\n\n")
			continue
		}
		for _, line := range strings.Split(d.Patch, "\n") {
			if b.Len() > limit {
				b.WriteString("\n... patch truncated at " + strconv.Itoa(limit) + " characters\n")
				return b.String()
			}
			switch {
			case strings.HasPrefix(line, "+"):
				b.WriteString(add.Render(line))
			case strings.HasPrefix(line, "-"):
				b.WriteString(del.Render(line))
			case strings.HasPrefix(line, "@@"):
				b.WriteString(hunk.Render(line))
			default:
				b.WriteString(line)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if shown == 0 {
		return "No files match the active path filter in this fork's own commits.\n"
	}
	return b.String()
}

func (m *Model) handlePatchKey(key string) (tea.Model, tea.Cmd) {
	switch keymap.Dispatch(keymap.MainPatch, key) {
	case keymap.Back:
		m.patchOffset = 0
		m.view = viewDetail
	case keymap.Up:
		m.patchOffset = max(0, m.patchOffset-1)
	case keymap.Down:
		m.patchOffset = min(maxScrollOffset(m.patchBody, m.detailViewHeight()), m.patchOffset+1)
	case keymap.PageUp:
		m.patchOffset = max(0, m.patchOffset-m.detailViewHeight())
	case keymap.PageDown:
		m.patchOffset = min(maxScrollOffset(m.patchBody, m.detailViewHeight()), m.patchOffset+m.detailViewHeight())
	case keymap.Home:
		m.patchOffset = 0
	case keymap.End:
		m.patchOffset = maxScrollOffset(m.patchBody, m.detailViewHeight())
	}
	return m, nil
}

func (m Model) viewPatch() string {
	if m.patchLoading {
		return "\n  Fetching patch (one live compare call)...\n"
	}
	body := scrollLines(m.patchBody, m.patchOffset, m.detailViewHeight())
	return body + "\n" + ui.KeyLegend(m.themeContext(), ui.ContentWidth(m.width)-2, keymap.MainPatch)
}
```

(`scrollLines`, `maxScrollOffset` live in `internal/tui/viewport.go:206,226`; reuse them. Palette fields verified: `Success`, `Error`, `Warning`, `Info` on `theme.Palette`.)

`app.go`: add `viewPatch` to the view enum; Model fields `patchBody string; patchOffset int; patchLoading bool`; in `handleDetailKey` add `case keymap.ViewPatch: m.view = viewPatch; return m, m.fetchPatchCmd()`; in `Update` add

```go
	case patchResultMsg:
		m.patchLoading = false
		if msg.err != nil {
			m.patchBody = "Patch fetch failed: " + msg.err.Error()
		} else {
			m.patchBody = renderPatch(m.themeContext(), msg.t2, m.pathFilter, maxPatchChars)
		}
		return m, nil
```

and route `viewPatch` in the key dispatcher (`:964-976`) to `handlePatchKey` and in `View` (`:1912`) to `viewPatch()`. If `help.go` enumerates scopes for the help page, add `MainPatch`; `TestMainHelpRendersEveryScopedRegistryBindingExactly` will fail otherwise and tell you.

`docs/keymap.md`: add `Fork details | p | View patch of touched files` and a `Patch view` row for scroll/back keys.

- [ ] **Step 4: Goldens** `UPDATE_GOLDEN=1 go test ./internal/tui/ -run 'TestGoldenMainViewStates|TestHelpTransitionNoticeAnd80x24Goldens'`; `git diff --stat internal/tui/testdata` must touch only `*_help.golden` (and filter goldens from Task 8 if not yet committed). Inspect one help diff to confirm the new rows.

- [ ] **Step 5: Run** `go test ./internal/tui/...` → PASS. Commit `git commit -m "feat(tui): on-demand patch view for touched files (p)"`

---

### Task 11: End-to-end verification against the real store

- [ ] **Step 1: Build and full test**

```bash
go build ./cmd/... && go vet ./... && go test ./...
go build -o /tmp/spn ./cmd/spn
```

- [ ] **Step 2: Warm-cache run, zero API cost**

```bash
gh api rate_limit --jq .resources.core.remaining
/tmp/spn forks list pbakaus/impeccable --touching '**/registry/antipatterns.mjs' 2>touch.err \
  | jq -c '{id, ahead: .t2.ahead, vis: .visibility.status, partial: .touching.partial, files: [.touching.files[]?.path]}'
gh api rate_limit --jq .resources.core.remaining
tail -n 3 touch.err
```

Expected: nine matched forks present in the store today: `kaushalrog`, `Raudbjorn`, `libar-dev`, `faocampo`, `bgausden`, `Git-Dann`, `marianif/impeccable-native`, `lalit-codes-things`, `BespokeAgentics/impeccable-microdots`; every record `vis: "pinned"`; `touching.centrality_method` is `"directory"` and `kaushalrog` (edits `cli/engine/registry/`) sorts above forks that only touched vendored `.agents/skills/...` copies; `touching` summary on stderr with `never_pushed` ≈ 1476 and `unknown` ≈ 96 if the reserve was hit (or lower after backfill). Rate-limit delta equals the fork-listing cost plus at most 3 centrality calls on a cold `$XDG_CACHE_HOME/spoon/centrality/` cache (0 within 24 h); compares are all cache hits. If the delta is large, `SPOON_DEBUG=1` and check `[triage]` lines: the cached-T2 path must be serving.

- [ ] **Step 3: Literal path and partial handling**

```bash
/tmp/spn forks list pbakaus/impeccable --touching cli/engine/registry/antipatterns.mjs | jq -c '{id, files: [.touching.files[]?.path]}'
```
Expected: `kaushalrog/impeccable` only (`+7/-0`). Also confirm that the 10 forks at the 300-file cap appear as `touching.status: "unmatched", "partial": true` when they do not match (`jq 'select(.touching.partial)'`).

- [ ] **Step 4: Error paths**

```bash
/tmp/spn forks list pbakaus/impeccable --touching '[' ; echo "exit $?"
/tmp/spn forks list pbakaus/impeccable --touching a.go --tier 1 ; echo "exit $?"
/tmp/spn forks list pbakaus/impeccable --touching a.go --csv ; echo "exit $?"
```
Expected: exit 2 with the three messages from Task 6.

- [ ] **Step 5: TUI smoke**

```bash
go build -o /tmp/spoon ./cmd/spoon && /tmp/spoon pbakaus/impeccable
```
Press `/`, type `path:**/registry/antipatterns.mjs`, Enter: table narrows to the matched forks. `Enter` on `kaushalrog/impeccable`: detail shows the "Touches" block. `p`: patch view shows the `+7` hunk after one live call. `Esc` returns.

- [ ] **Step 6: PR body** ends with a "Not claimed" section per the user's global instructions: not tested on GitLab/Gitea providers (FilesTruncated only set for GitHub and Gitea's local cap); the never-pushed demotion is heuristic (one counterexample recorded); GitHub 300-file pagination not attempted; TUI patch view fetches live and does not persist; help/filter goldens regenerated and reviewed by eye, not by an independent renderer.

---

### Task 12: Follow-up spec — unit-level attribution inside a touched file

**Files:**
- Create: `docs/superpowers/specs/future/future-work-touching-unit-attribution.md`

**Interfaces:** none (design document only). Written after Tasks 1–11 so it can cite the shipped `touching` block.

- [ ] **Step 1: Write the spec** with exactly these sections and content:

```markdown
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
```

- [ ] **Step 2: Commit** `git commit -m "docs(spec): future work — unit-level attribution for --touching"`

---

## Out of scope (follow-ups, noted, not started)

- Paginating the GitHub compare file list beyond 300 (API behaviour unverified; flag first, fetch later).
- Applying the never-pushed demotion to the TUI enrichment sweep (`app.go:1618-1626` orders by `DispatchPriority` only).
- Persisting the patch fetched by the TUI patch view back into `compare_files`.
- A CSV column for touching (rejected with an explicit error instead).
- Per-file centrality in the TUI detail section (needs a TUI twin of `clusterEnv`).
- Anything touching the foreign `code_units` / `unit_alignments` / `file_centrality` / `module_edges` tables in the local store: no source here, all empty.
