# Surface & schema convergence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retire spoon's non-TUI output paths, delete `internal/dump`, remove v1 heat scoring once the TUI migrates to v2, wire `DetectLoneWolfV2` into `forksops.Stream`, add `--csv` to `spn forks list`, surface GitHub rate-limit errors as a first-class `rate_limited` envelope, delete the dead `github.UniqueAuthors` duplicate, and document the new capabilities in the `using-spn` skill.

**Architecture:** Single PR, branch `convergence-d`, 9 sequential commits. Each commit independently green under `go test ./...`. Spec at `docs/superpowers/specs/2026-05-11-surface-schema-convergence-design.md`.

**Tech Stack:** Go 1.26+, existing libs (`charmbracelet/bubbletea`, `cli/go-gh/v2`). No new dependencies.

---

## File Structure

**Modified files:**

- `internal/forksops/stream.go` — `rescore()` populates `Tier3ParamsV2.LoneWolf` via `heat.DetectLoneWolfV2`; per-fork and fatal rate-limit propagation.
- `internal/forksops/stream_test.go` — three new test cases (lone-wolf wiring, per-fork rate-limit, fatal rate-limit).
- `internal/tui/app.go` — v1 scoring replaced by `heat.NewScorer` + `Scorer.ScoreRaw`; `DetectLoneWolfV2` called inline.
- `internal/tui/detail.go` — Components loop replaces Signals; LoneWolfV2 block replaces LoneWolf.
- `internal/tui/export.go` — `ExportLoneWolf` derived from `LoneWolfV2`; JSON uses `components`, `tierScores`, `trust`, `penalties`.
- `internal/heat/types.go` — remove `HeatResult.Signals`, `HeatResult.LoneWolf` fields; remove `Signal`, `LoneWolfSignal` types.
- `internal/heat/score.go` — remove `ComputeTier1`, `ComputeTier2`, `weightedSum`, `clampScore` if dead.
- `internal/heat/lonewolf.go` — remove `DetectLoneWolf` legacy wrapper.
- `internal/github/client.go` — HTTP layer detects rate-limit responses, wraps as `*RateLimitError`.
- `internal/github/compare.go` — delete `UniqueAuthors` function and its test.
- `internal/threadsops/types.go` — add `OpCodeRateLimited` constant.
- `internal/threadsops/{ops,resolve,bulk}.go` — `errors.As` routing before the upstream_error fallback.
- `internal/threadsops/{ops,resolve,bulk}_test.go` — one rate-limit test per file.
- `cmd/spoon/main.go` — remove `--json`, `--csv`, `-o`, `--output` flags; shrink help text; remove the `if jsonMode || csvMode` branch.
- `cmd/spoon/main_test.go` — drop tests for the removed flags; add tests that those flags now fail with `bad_input`.
- `cmd/spn/forks.go` — add `--csv` flag, batched CSV emitter, rate-limit case in translation.
- `cmd/spn/forks_test.go` — tests for `--csv` happy path, header presence, NDJSON/CSV mutex, rate-limit propagation.
- `cmd/spn/threads.go`, `cmd/spn/pr.go`, `cmd/spn/repo.go`, `cmd/spn/embed.go` — add `agentio.CodeRateLimited` case in translation helpers.
- `cmd/spn/threads_test.go` and friends — one rate-limit black-box test per file.
- `docs/superpowers/skills/using-spn/SKILL.md` — add Rate Limits subsection and `--csv` row.

**New files:**

- `internal/github/rate_limit_error.go` — `*RateLimitError` type and detection helpers.
- `internal/github/rate_limit_error_test.go` — tests for header parsing and `errors.As`.

**Deleted files:**

- `internal/dump/dump.go`
- `internal/dump/cluster_pipeline.go`
- `internal/dump/cluster_pipeline_test.go`

**Mirrored outside git:**

- `/home/svnbjrn/.claude/skills/using-spn/SKILL.md` — kept in sync with the repo copy via a `cp` step at the end (not part of any commit).

---

## Task 1: Wire `DetectLoneWolfV2` into `forksops.Stream.rescore()`

**Files:**
- Modify: `internal/forksops/stream.go`
- Modify: `internal/forksops/stream_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/forksops/stream_test.go`:

```go
func TestStream_loneWolfV2Wired(t *testing.T) {
	pushedAt := time.Now()
	commit := forge.AheadCommit{
		SHA:         "abc123",
		Message:     "Implement feature X end-to-end",
		AuthorLogin: "solo-dev",
		AuthorEmail: "solo@example.com",
		Timestamp:   pushedAt,
		Files:       []forge.FileDiff{{Path: "internal/feature/x.go", Additions: 250, Deletions: 5}},
	}
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: pushedAt.Add(-30 * 24 * time.Hour)},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: pushedAt, DefaultBranch: "main"},
		},
		t2: map[string]forge.T2Data{
			"o/a": {
				AheadCount: 3,
				MNA:        245,
				Commits:    []forge.AheadCommit{commit, commit, commit},
				Diffs:      []forge.FileDiff{{Path: "internal/feature/x.go", Additions: 750, Deletions: 15}},
			},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 3, TopN: 1})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	r := <-ch
	if r.Err != nil {
		t.Fatalf("per-fork err: %+v", r.Err)
	}
	if r.Heat.LoneWolfV2 == nil {
		t.Fatalf("expected Heat.LoneWolfV2 to be populated; got nil")
	}
	if r.Heat.LoneWolfV2.EffectiveContribs != 1 {
		t.Errorf("expected EffectiveContribs=1 (single author), got %d", r.Heat.LoneWolfV2.EffectiveContribs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/forksops/... -run TestStream_loneWolfV2Wired`
Expected: FAIL — `r.Heat.LoneWolfV2 == nil` because `rescore()` currently leaves the field nil.

- [ ] **Step 3: Update `rescore()` in `internal/forksops/stream.go`**

Locate `rescore(scorer, f, parent, now, t2, t3)` (around line 431). Replace the `if t3 != nil` block with the version below, which builds a `heat.LoneWolfInput` from the available data and calls `heat.DetectLoneWolfV2`. The `heat.FileChange` shape is defined in `internal/heat/lonewolf.go` — verify before writing the adapter; the adapter must accept the project's actual `FileChange` field names (likely `Path string`, `Additions int`, `Deletions int`).

```go
if t3 != nil {
	input.T3 = &heat.Tier3ParamsV2{
		CommitSpanDays: float64(t3.CommitSpanDays),
	}
}
// Wire v2 lone wolf when we have commits to analyze.
if t2 != nil && len(t2.Commits) > 0 {
	lw := buildLoneWolfInput(f, parent, now, t2)
	if input.T3 == nil {
		input.T3 = &heat.Tier3ParamsV2{}
	}
	input.T3.LoneWolf = heat.DetectLoneWolfV2(lw)
}
```

Then append the new helper `buildLoneWolfInput` at the end of `stream.go`:

```go
// buildLoneWolfInput adapts forge T2Data into the input shape DetectLoneWolfV2 expects.
func buildLoneWolfInput(f forge.T1Data, parent forge.ParentData, now time.Time, t2 *forge.T2Data) heat.LoneWolfInput {
	commits := make([]heat.LWCommitInfo, 0, len(t2.Commits))
	authors := make([]string, 0, len(t2.Commits))
	for _, c := range t2.Commits {
		login := c.AuthorLogin
		if login == "" {
			login = c.AuthorEmail
		}
		commits = append(commits, heat.LWCommitInfo{
			AuthorLogin: login,
			Message:     c.Message,
			Date:        c.Timestamp,
		})
		authors = append(authors, login)
	}
	files := make([]heat.FileChange, 0, len(t2.Diffs))
	for _, d := range t2.Diffs {
		files = append(files, heat.FileChange{
			Path:      d.Path,
			Additions: d.Additions,
			Deletions: d.Deletions,
		})
	}
	return heat.LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  authors,
		AheadBy:       t2.AheadCount,
		DaysSincePush: now.Sub(f.PushedAt).Hours() / 24,
	}
}
```

Verify `heat.FileChange` field names by reading the existing struct in `internal/heat/lonewolf.go` before pasting — if the struct uses different names (e.g., `Adds`/`Dels`), adjust the adapter accordingly. Do not invent field names.

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/forksops/...`
Expected: all tests pass, including the new one.

Run: `go test -race ./internal/forksops/...`
Expected: no data races.

- [ ] **Step 5: Commit**

```bash
git add internal/forksops/stream.go internal/forksops/stream_test.go
git commit -m "Wire DetectLoneWolfV2 into forksops.Stream rescore"
```

---

## Task 2: Migrate `internal/tui/` to v2 heat

This task replaces v1 scoring in three TUI files. The migration is mechanical — same data, different shapes. After this task, the TUI no longer references `HeatResult.Signals` or `HeatResult.LoneWolf`, so Task 3 can delete those fields.

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/detail.go`
- Modify: `internal/tui/export.go`
- Test: existing `internal/tui/*_test.go` files (no new tests; existing assertions update)

- [ ] **Step 1: Read the current v1 call sites**

Run these greps and read each context (3–5 lines around each match) to understand the current shape before editing:

```bash
grep -n "heat\.\(ComputeTier1\|ComputeTier2\|DetectLoneWolf\|WeightedAdditions\|WeightedDeletions\)\b" internal/tui/app.go
grep -n "\.Heat\.\(Signals\|LoneWolf\)\b" internal/tui/{app,detail,export}.go
grep -n "ExportLoneWolf" internal/tui/export.go
```

This identifies the exact lines you'll be replacing. Make notes.

- [ ] **Step 2: Replace v1 scoring in `internal/tui/app.go`**

Find where the model scores forks (look for `heat.ComputeTier1` and `heat.ComputeTier2` calls). Replace the per-fork scoring block with the v2 pattern. The minimal shape:

```go
// Build ForkStats for the percentile table (mirrors forksops.Stream).
stats := make([]heat.ForkStats, len(forks))
for i, fk := range forks {
	stats[i] = heat.ForkStats{ForkID: int64(i), Stars: fk.Stars, SubForks: fk.SubForkCount}
}
scorer := heat.NewScorer(stats)
now := time.Now()
for i, fk := range forks {
	input := heat.ScoreInput{
		T1: heat.Tier1ParamsV2{
			Stars:             fk.Stars,
			SubForks:          fk.SubForkCount,
			ReleaseCount:      fk.ReleaseCount,
			DaysSincePush:     now.Sub(fk.PushedAt).Hours() / 24,
			DaysSinceUpstream: now.Sub(parent.PushedAt).Hours() / 24,
			Archived:          fk.IsArchived,
			Now:               now,
		},
	}
	m.forks[i].Heat = scorer.ScoreRaw(input)
}
```

For per-fork T2/T3 enrichment paths in the TUI (the asynchronous tier2ResultMsg handler), build `LoneWolfInput` and call `heat.DetectLoneWolfV2` directly — mirror the `buildLoneWolfInput` helper from Task 1 (a copy is fine; do not extract a shared utility in this PR). Assign the result to `input.T3.LoneWolf` before calling `scorer.ScoreRaw`.

Remove every call to `heat.ComputeTier1`, `heat.ComputeTier2`, `heat.DetectLoneWolf`. If `heat.WeightedAdditions` / `heat.WeightedDeletions` were called only inside the v1 scoring path, remove those calls; if they're used to populate a `T2Data` totals display (separate from scoring), leave them — they're shared utilities that stay.

- [ ] **Step 3: Replace the Signals/LoneWolf rendering in `internal/tui/detail.go`**

Find the block that iterates `sf.Heat.Signals` (around `len(sf.Heat.Signals) > 0`) and the block referencing `sf.Heat.LoneWolf`. Replace with v2:

```go
// Components loop (was: Signals loop):
if len(sf.Heat.Components) > 0 {
	for _, c := range sf.Heat.Components {
		// Existing rendering function gets new args. Adjust the format string
		// from "Name Weight×Value=Score" to "Name Points/Max":
		lines = append(lines, fmt.Sprintf("  %s  %.1f/%.1f  (raw=%.2f)", c.Name, c.Points, c.Max, c.Raw))
	}
}
// LoneWolfV2 block (was: LoneWolf block):
if sf.Heat.LoneWolfV2 != nil && sf.Heat.LoneWolfV2.Detected {
	lw := sf.Heat.LoneWolfV2
	lines = append(lines, fmt.Sprintf("Lone wolf: %s (strength %.2f)", lw.Archetype.String(), lw.Strength))
	lines = append(lines, fmt.Sprintf("  commits=%d, mna=%d, span=%.1fd, spread=%.2f", lw.MeaningfulCommits, lw.MNA, lw.CommitSpanDays, lw.FileSpread))
}
```

The exact rendering matches the existing visual style — preserve indentation, color (via `lipgloss`), and ordering. Goal is functional equivalence with the v2 data shape.

- [ ] **Step 4: Replace the ExportLoneWolf shape in `internal/tui/export.go`**

Find the `ExportLoneWolf` struct (or equivalent name) and the code that populates it from `sf.Heat.LoneWolf`. Replace both. New struct fields mirror `LoneWolfResult`:

```go
type ExportLoneWolf struct {
	Detected          bool    `json:"detected"`
	Strength          float64 `json:"strength"`
	Archetype         string  `json:"archetype"`
	Label             string  `json:"label"`
	EffectiveContribs int     `json:"effectiveContribs"`
	MeaningfulCommits int     `json:"meaningfulCommits"`
	MNA               int     `json:"mna"`
	CommitSpanDays    float64 `json:"commitSpanDays"`
	FileSpread        float64 `json:"fileSpread"`
	RevertCount       int     `json:"revertCount"`
	IsSquash          bool    `json:"isSquash"`
	MsgQualityScore   float64 `json:"msgQualityScore"`
}

// Population:
if sf.Heat.LoneWolfV2 != nil && sf.Heat.LoneWolfV2.Detected {
	lw := sf.Heat.LoneWolfV2
	ef.LoneWolf = &ExportLoneWolf{
		Detected:          true,
		Strength:          lw.Strength,
		Archetype:         lw.Archetype.String(),
		Label:             lw.Label,
		EffectiveContribs: lw.EffectiveContribs,
		MeaningfulCommits: lw.MeaningfulCommits,
		MNA:               lw.MNA,
		CommitSpanDays:    lw.CommitSpanDays,
		FileSpread:        lw.FileSpread,
		RevertCount:       lw.RevertCount,
		IsSquash:          lw.IsSquash,
		MsgQualityScore:   lw.MsgQualityScore,
	}
}
```

Also replace the `signals: [...]` block in the per-fork export JSON with `components: [...]`. Add `tierScores`, `trust`, and `penalties` fields. The exact JSON keys should match what `spn forks list` emits in `cmd/spn/forks.go::forkToJSON` (read it for reference) so the two outputs converge.

- [ ] **Step 5: Update existing TUI tests**

Run `go test ./internal/tui/...` and observe failures. For each failure, update the assertion to reference v2 field names:

- `Signals` → `Components`
- `LoneWolf` → `LoneWolfV2`
- Old fields like `Signal.Value`/`Signal.Weight` → `Component.Points`/`Component.Max`
- v1 lone-wolf label string → `Archetype.String()` or `Strength`

If a test was asserting on v1-specific output structure (e.g., a Signals length of 7 because v1 had exactly 7 named signals), rewrite the assertion to test the equivalent v2 behavior (Components length ≥ 1 for an enriched fork, etc.).

- [ ] **Step 6: Run all tests, verify clean**

```bash
go test ./...
go vet ./...
go build ./cmd/spoon ./cmd/spn
```

All clean. If the build fails because v1 symbols are still referenced somewhere unexpected, locate and migrate that reference before proceeding.

- [ ] **Step 7: Manual smoke (optional but recommended)**

Build and run against a small repo:

```bash
go build -o /tmp/spoon ./cmd/spoon
/tmp/spoon Raudbjorn/spoon  # adjust to any small public repo
# Press Enter on a fork to see detail pane.
# Press E to export. Verify exported JSON contains "components" and "loneWolf" with v2 fields.
# Press q to quit.
rm /tmp/spoon
```

If detail pane or export look right, proceed. If not, fix before committing.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/app.go internal/tui/detail.go internal/tui/export.go internal/tui/*_test.go
git commit -m "Migrate TUI to v2 heat scoring shape"
```

---

## Task 3: Delete v1 from `internal/heat`

After Task 2, the only references to v1 should be inside `internal/heat` itself. This task verifies that and removes the v1 surface.

**Files:**
- Modify: `internal/heat/types.go`
- Modify: `internal/heat/score.go`
- Modify: `internal/heat/lonewolf.go`
- Modify: `internal/heat/score_test.go` (delete v1-specific tests)
- Modify: `internal/heat/lonewolf_test.go` (delete legacy DetectLoneWolf tests)

- [ ] **Step 1: Verify zero non-heat callers**

Run:

```bash
for sym in ComputeTier1 ComputeTier2 DetectLoneWolf Signal LoneWolfSignal; do
  echo "=== $sym ==="
  grep -rln "heat\.${sym}\b\|^func ${sym}\b\|^type ${sym}\b" --include="*.go" . | grep -v internal/heat/ | grep -v _test.go || echo "(no non-heat, non-test callers)"
done
echo
echo "=== HeatResult.Signals ===" && grep -rn "\.Heat\.Signals\b" --include="*.go" . | grep -v internal/heat/ || echo "(no callers)"
echo "=== HeatResult.LoneWolf ===" && grep -rn "\.Heat\.LoneWolf\b" --include="*.go" . | grep -v internal/heat/ | grep -v LoneWolfV2 || echo "(no callers)"
```

Expected: every section reports "no callers" (or only `internal/heat/` itself). If any non-heat caller remains, STOP. Migrate that caller before continuing — do not delete the symbol while it has callers.

- [ ] **Step 2: Remove v1 fields from `HeatResult`**

In `internal/heat/types.go`, delete these two lines from the `HeatResult` struct:

```go
Signals    []Signal // component breakdown (legacy v1)
LoneWolf   *LoneWolfSignal
```

Update the `IsTinySet bool` comment block above the v2 fields if it mentions v1.

- [ ] **Step 3: Delete the `Signal` and `LoneWolfSignal` type declarations**

In `internal/heat/types.go`, delete:

- `type Signal struct { … }` and its doc comment
- `type LoneWolfSignal struct { … }` and its doc comment

- [ ] **Step 4: Delete v1 scoring functions**

In `internal/heat/score.go`, delete:

- `type Tier1Params struct { … }`
- `type Tier2Params struct { … }`
- `func ComputeTier1(p Tier1Params) HeatResult { … }`
- `func ComputeTier2(p Tier2Params) HeatResult { … }`
- `func weightedSum(signals []Signal) float64 { … }` (only if unused elsewhere — grep first; v2 doesn't use it)
- `func clampScore(s float64) float64 { … }` (only if unused — grep first)

Do not remove v2 functions (`ComputeTier1V2`, `ComputeTier2V2`, `ComputeTier3V2`, `RawScore`, `ApplyTrust`, `ApplyPenalties`, `TinySetScore`, `tierConfidence`) or their parameter types.

- [ ] **Step 5: Delete the legacy `DetectLoneWolf` wrapper**

In `internal/heat/lonewolf.go`, find the legacy `DetectLoneWolf` function (returns `*LoneWolfSignal`) and delete it along with any helpers used only by it. Keep `DetectLoneWolfV2`, `LoneWolfInput`, `LWCommitInfo`, `LoneWolfResult`, and all the supporting filters (`filterBots`, `hasCoAuthors`, `filterMergeCommits`, etc.).

- [ ] **Step 6: Delete v1 tests**

In `internal/heat/score_test.go`, delete every test that exercises `ComputeTier1`, `ComputeTier2`, `Signal`, or `Tier1Params`/`Tier2Params`.

In `internal/heat/lonewolf_test.go`, delete every test that exercises the legacy `DetectLoneWolf`. Keep tests for `DetectLoneWolfV2`.

- [ ] **Step 7: Run tests, verify clean**

```bash
go test ./...
go vet ./...
go build ./...
```

All clean. If a downstream test fails because it referenced a v1 symbol that the grep in Step 1 didn't catch, return to Task 2 and migrate that test.

- [ ] **Step 8: Final grep — confirm v1 is gone**

```bash
grep -rn "ComputeTier1\b\|ComputeTier2\b\|type Signal struct\|type LoneWolfSignal struct\|\.Heat\.Signals\b" --include="*.go" .
```

Expected: empty output. If anything remains, address before committing.

- [ ] **Step 9: Commit**

```bash
git add internal/heat/
git commit -m "Remove v1 heat scoring surface

ComputeTier1, ComputeTier2, the legacy DetectLoneWolf wrapper, the
Signal and LoneWolfSignal types, and the HeatResult.Signals /
HeatResult.LoneWolf fields had no remaining callers after the TUI
migration. Removed along with their tests."
```

---

## Task 4: Delete `internal/dump/`

**Files:**
- Delete: `internal/dump/dump.go`
- Delete: `internal/dump/cluster_pipeline.go`
- Delete: `internal/dump/cluster_pipeline_test.go`

- [ ] **Step 1: Verify zero callers outside dump**

```bash
grep -rln "internal/dump\"" --include="*.go" . | grep -v internal/dump/
```

Expected: empty output. If any non-dump file imports `internal/dump`, STOP — that file needs to migrate to `internal/forksops` first. (As of the spec, only `cmd/spoon/main.go` imports dump, and Task 5 removes that import; if you're doing Tasks in order, do Task 5 before Task 4. The order in this plan reflects logical dependency, not strict execution order — see note below.)

**Note on order:** Tasks 4 and 5 have a circular dependency: dump can't be deleted while `cmd/spoon` imports it, and `cmd/spoon`'s removal of `--json/--csv` removes the only place dump is imported. Resolve by doing **Task 5 first** (remove `--json/--csv`) then **Task 4** (delete dump). Renumber locally if you prefer; the commit-message order in `git log` doesn't have to match the task numbers.

Once Task 5 is complete and `grep` returns empty, proceed:

- [ ] **Step 2: Delete the directory**

```bash
rm -r internal/dump/
```

- [ ] **Step 3: Run tests, verify clean**

```bash
go test ./...
go vet ./...
go build ./cmd/spoon ./cmd/spn
```

All clean.

- [ ] **Step 4: Commit**

```bash
git add -A internal/dump/
git commit -m "Delete internal/dump

The package's pipeline orchestration duplicated forksops.Stream, and its
v1-based output schema was being retired alongside the spoon --json/--csv
flag removal. forksops.Stream is now the single canonical fetch/score/
enrich path for non-TUI callers."
```

---

## Task 5: Remove `spoon --json` / `--csv` / `-o` / `--output`

**Files:**
- Modify: `cmd/spoon/main.go`
- Modify: `cmd/spoon/main_test.go`

This task must run BEFORE Task 4 (see Task 4 Step 1 note).

- [ ] **Step 1: Write the failing test**

Append to `cmd/spoon/main_test.go` (or wherever the flag-parsing tests live). The test asserts that `--json` is now treated as an unknown flag:

```go
func TestSpoonRejectsRemovedJSONFlag(t *testing.T) {
	// We don't actually execute main; we want to know that flag parsing
	// no longer recognizes --json. The simplest assertion is to scan the
	// help output (or the flag-parsing switch) and confirm --json is absent.
	// Build the binary and capture --help; assert "--json" does not appear.
	cmd := exec.Command("go", "run", ".")
	cmd.Args = append(cmd.Args, "--help")
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "--json") {
		t.Errorf("--help still advertises --json; expected to be removed")
	}
	if strings.Contains(string(out), "--csv") {
		t.Errorf("--help still advertises --csv; expected to be removed")
	}
}
```

Add `"os/exec"` and `"strings"` imports if not already present. If a different test pattern is more idiomatic for this codebase (look at existing tests in `cmd/spoon/main_test.go`), use that.

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./cmd/spoon/... -run TestSpoonRejectsRemovedJSONFlag`
Expected: FAIL — `--json` is still in help text.

- [ ] **Step 3: Remove the flag handling**

In `cmd/spoon/main.go`:

1. Remove the variable declarations near the top of `main()`: `jsonMode := false`, `csvMode := false`, `outputPath := ""`.
2. Remove the `case "--json":`, `case "--csv":`, and `case "-o", "--output":` branches from the flag-parsing switch.
3. Remove the entire `if jsonMode || csvMode { … }` block (around line 258) including the dump call and the output-path logic.
4. Remove the `internal/dump` import.
5. Remove any helper functions only used by the removed block (e.g., `splitRepo` if dump was its only caller — grep first).
6. Update `printHelp()` (or wherever help text lives) to drop the `--json`, `--csv`, `-o`, `--output` lines.

- [ ] **Step 4: Update existing tests**

In `cmd/spoon/main_test.go`, delete any test that exercised `--json`, `--csv`, or `--output`. Keep the new `TestSpoonRejectsRemovedJSONFlag` test.

- [ ] **Step 5: Run tests, verify clean**

```bash
go test ./cmd/spoon/...
go vet ./...
go build ./cmd/spoon
```

All clean. The new test passes.

- [ ] **Step 6: Commit**

```bash
git add cmd/spoon/main.go cmd/spoon/main_test.go
git commit -m "Remove spoon --json/--csv/-o flags

The non-TUI output paths are now provided by spn forks list (with
--csv for batched output). Spoon is TUI-only for fork discovery; its
threads and embed subcommands remain. Migration is a hard cut — no
deprecation warning."
```

---

## Task 6: Add `spn forks list --csv` batched mode

**Files:**
- Modify: `cmd/spn/forks.go`
- Modify: `cmd/spn/forks_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/spn/forks_test.go`:

```go
func TestSpnForksList_csv_emitsHeaderAndRows(t *testing.T) {
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", URL: "https://github.com/o/a", Stars: 5, PushedAt: time.Now()},
				{ID: "o/b", Owner: "o", Name: "b", URL: "https://github.com/o/b", Stars: 3, PushedAt: time.Now()},
			},
		}, "o/r", nil
	}
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--csv"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 1 header + 2 data rows, got %d:\n%s", len(lines), stdout.String())
	}
	wantHeader := "id,owner,name,url,stars,pushed_at,is_archived,sub_forks,releases,heat,tier,t2_ahead,t2_behind,t2_mna,t3_contributors,t3_commit_span_days,cluster_name,cluster_score"
	if lines[0] != wantHeader {
		t.Errorf("header mismatch:\n got: %s\nwant: %s", lines[0], wantHeader)
	}
	if !strings.HasPrefix(lines[1], "o/a,o,a,https://github.com/o/a,5,") {
		t.Errorf("first row prefix: %q", lines[1])
	}
}

func TestSpnForksList_csv_noNDJSONLeak(t *testing.T) {
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks:  []forge.T1Data{{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()}},
		}, "o/r", nil
	}
	var stdout, stderr bytes.Buffer
	runForksWith([]string{"list", "o/r", "--tier", "1", "--csv"}, &stdout, &stderr)
	// CSV output should contain commas; no NDJSON object should leak.
	if strings.Contains(stdout.String(), `{"id":`) || strings.Contains(stdout.String(), `"id":`) {
		t.Errorf("CSV output contains NDJSON-shaped JSON object: %s", stdout.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/spn/... -run TestSpnForksList_csv
```

Expected: FAIL — `--csv` is currently rejected as an unknown flag.

- [ ] **Step 3: Add `--csv` flag parsing**

In `cmd/spn/forks.go`, find the arg-parsing switch in `doForksList`. Add a new boolean `csv` variable at the top of the function:

```go
var repo, forgeFlag, forgeHost, botList string
csv := false
opts := forksops.Options{}
```

Add the flag case to the switch:

```go
case "--csv":
	csv = true
```

- [ ] **Step 4: Implement batched CSV emission**

At the bottom of `doForksList`, replace the existing `for r := range ch { … }` loop with branching on the `csv` flag:

```go
if csv {
	return emitForksCSV(stdout, stderr, ch)
}
// Existing NDJSON streaming path (unchanged):
for r := range ch {
	if r.Err != nil {
		_ = agentio.WriteNDJSON(stderr, map[string]any{
			"error": map[string]any{
				"code":    r.Err.Code,
				"message": r.Err.Message,
				"details": r.Err.Details,
			},
		})
		continue
	}
	if err := agentio.WriteNDJSON(stdout, forkToJSON(r)); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
}
return 0
```

Then add `emitForksCSV` at the bottom of the file:

```go
func emitForksCSV(stdout, stderr io.Writer, ch <-chan forksops.Result) int {
	w := csv.NewWriter(stdout)
	header := []string{
		"id", "owner", "name", "url", "stars", "pushed_at", "is_archived",
		"sub_forks", "releases", "heat", "tier",
		"t2_ahead", "t2_behind", "t2_mna",
		"t3_contributors", "t3_commit_span_days",
		"cluster_name", "cluster_score",
	}
	if err := w.Write(header); err != nil {
		return agentio.NewError(agentio.CodeInternal, "write csv header: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	for r := range ch {
		if r.Err != nil {
			_ = agentio.WriteNDJSON(stderr, map[string]any{
				"error": map[string]any{
					"code":    r.Err.Code,
					"message": r.Err.Message,
					"details": r.Err.Details,
				},
			})
			continue
		}
		row := forkToCSVRow(r)
		if err := w.Write(row); err != nil {
			return agentio.NewError(agentio.CodeInternal, "write csv row: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return agentio.NewError(agentio.CodeInternal, "flush csv: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func forkToCSVRow(r forksops.Result) []string {
	t2Ahead, t2Behind, t2MNA := "", "", ""
	if r.T2 != nil {
		t2Ahead = strconv.Itoa(r.T2.AheadCount)
		t2Behind = strconv.Itoa(r.T2.BehindCount)
		t2MNA = strconv.Itoa(r.T2.MNA)
	}
	t3Contribs, t3Span := "", ""
	if r.T3 != nil {
		t3Contribs = strconv.Itoa(len(r.T3.Contributors))
		t3Span = strconv.Itoa(r.T3.CommitSpanDays)
	}
	clusterName, clusterScore := "", ""
	if r.Heat.ClusterID != "" {
		clusterName = r.Heat.ClusterLabel
		clusterScore = strconv.FormatFloat(r.Heat.NoveltyScore, 'f', 3, 64)
	}
	return []string{
		r.Fork.ID,
		r.Fork.Owner,
		r.Fork.Name,
		r.Fork.URL,
		strconv.Itoa(r.Fork.Stars),
		r.Fork.PushedAt.UTC().Format(time.RFC3339),
		strconv.FormatBool(r.Fork.IsArchived),
		strconv.Itoa(r.Fork.SubForkCount),
		strconv.Itoa(r.Fork.ReleaseCount),
		strconv.FormatFloat(r.Heat.Score, 'f', 2, 64),
		strconv.Itoa(r.Heat.Tier),
		t2Ahead,
		t2Behind,
		t2MNA,
		t3Contribs,
		t3Span,
		clusterName,
		clusterScore,
	}
}
```

Add the necessary imports to the file: `"encoding/csv"`, `"strconv"`.

- [ ] **Step 5: Update `printHelp()` in `cmd/spn/main.go`**

Find the line listing `forks list` flags and add `[--csv]`:

```
  forks list <repo> [--tier 1|2|3] [--top N] [--bot-allowlist L] [--refresh] [--csv] [--forge github|gitlab] [--forge-host H]
```

- [ ] **Step 6: Run tests, verify pass**

```bash
go test ./cmd/spn/...
go vet ./...
go build ./cmd/spn
```

Both CSV tests pass; existing NDJSON test (`TestSpnForksList_emitsNDJSON`) still passes.

- [ ] **Step 7: Commit**

```bash
git add cmd/spn/forks.go cmd/spn/forks_test.go cmd/spn/main.go
git commit -m "Add spn forks list --csv batched mode

CSV emission collects every Result from forksops.Stream, then writes a
single fixed-column document on stdout with a header row. Per-fork
enrichment errors continue to land on stderr as compact JSON,
matching the NDJSON path. --csv is mutually exclusive with the
default NDJSON streaming."
```

---

## Task 7: Wire `OpCodeRateLimited` (the typed rate-limit error path)

This task spans `internal/github`, `internal/threadsops`, `internal/forksops`, and the cmd/spn translation helpers. Substantial but mechanical once the typed error exists.

**Files:**
- Create: `internal/github/rate_limit_error.go`
- Create: `internal/github/rate_limit_error_test.go`
- Modify: `internal/github/client.go` (HTTP layer)
- Modify: `internal/threadsops/types.go` (constant)
- Modify: `internal/threadsops/ops.go`, `internal/threadsops/resolve.go`, `internal/threadsops/bulk.go`
- Modify: `internal/threadsops/{ops,resolve,bulk}_test.go`
- Modify: `internal/forksops/stream.go` (rate-limit propagation)
- Modify: `internal/forksops/stream_test.go`
- Modify: `cmd/spn/threads.go` (translateOpErr, translateResolveErr)
- Modify: `cmd/spn/pr.go`, `cmd/spn/repo.go`, `cmd/spn/embed.go`, `cmd/spn/forks.go` (translation helpers if they exist independently)
- Modify: cmd/spn `*_test.go` files

- [ ] **Step 1: Create the typed error**

Create `internal/github/rate_limit_error.go`:

```go
package github

import (
	"fmt"
	"time"
)

// RateLimitError is returned by *Client when the GitHub API responds with a
// rate-limit signal: HTTP 403 with X-RateLimit-Remaining: 0, or HTTP 429
// with Retry-After. ResetAt is the absolute time the window resets;
// Remaining is the documented per-window remaining count at the time of
// the failure (typically 0).
type RateLimitError struct {
	ResetAt   time.Time
	Remaining int
	cause     error
}

// Error implements the error interface.
func (e *RateLimitError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("github rate limit exceeded (resets %s): %v", e.ResetAt.UTC().Format(time.RFC3339), e.cause)
	}
	return fmt.Sprintf("github rate limit exceeded (resets %s)", e.ResetAt.UTC().Format(time.RFC3339))
}

// Unwrap supports errors.Is/errors.As walking the cause chain.
func (e *RateLimitError) Unwrap() error { return e.cause }

// RetryAfterSeconds returns the number of seconds until ResetAt, clamped
// at zero when ResetAt is in the past (rare; rate-limit window just
// rolled over).
func (e *RateLimitError) RetryAfterSeconds() int {
	s := int(time.Until(e.ResetAt).Seconds())
	if s < 0 {
		return 0
	}
	return s
}
```

- [ ] **Step 2: Write the failing tests**

Create `internal/github/rate_limit_error_test.go`:

```go
package github

import (
	"errors"
	"testing"
	"time"
)

func TestRateLimitError_Error(t *testing.T) {
	rl := &RateLimitError{ResetAt: time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)}
	if !contains(rl.Error(), "rate limit exceeded") {
		t.Errorf("missing prefix: %q", rl.Error())
	}
}

func TestRateLimitError_Unwrap(t *testing.T) {
	cause := errors.New("upstream")
	rl := &RateLimitError{cause: cause}
	if errors.Unwrap(rl) != cause {
		t.Errorf("Unwrap returned wrong cause")
	}
}

func TestRateLimitError_RetryAfterSeconds_clampsNegative(t *testing.T) {
	rl := &RateLimitError{ResetAt: time.Now().Add(-1 * time.Hour)}
	if rl.RetryAfterSeconds() != 0 {
		t.Errorf("expected clamped 0, got %d", rl.RetryAfterSeconds())
	}
}

func TestRateLimitError_RetryAfterSeconds_positive(t *testing.T) {
	rl := &RateLimitError{ResetAt: time.Now().Add(120 * time.Second)}
	got := rl.RetryAfterSeconds()
	if got < 110 || got > 130 {
		t.Errorf("expected ~120s, got %d", got)
	}
}

func TestRateLimitError_errorsAs(t *testing.T) {
	rl := &RateLimitError{ResetAt: time.Now().Add(time.Minute)}
	wrapped := errors.New("wrapped: " + rl.Error())
	_ = wrapped
	// errors.As should find the typed error when it's directly returned
	// or when wrapped via fmt.Errorf with %w. Direct case:
	var target *RateLimitError
	if !errors.As(error(rl), &target) {
		t.Errorf("errors.As failed on direct *RateLimitError")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

(The `contains` helper avoids pulling in `strings` here; if the file already imports `strings`, use that instead.)

- [ ] **Step 3: Verify tests pass**

```bash
go test ./internal/github/... -run TestRateLimitError
```

Expected: all four pass.

- [ ] **Step 4: Wire detection into the HTTP layer**

In `internal/github/client.go`, find every place that returns errors from API calls. The simplest pattern is to wrap the existing response-handling code in a helper that inspects the headers when status is 403 or 429:

```go
// detectRateLimit returns a *RateLimitError if resp signals a rate-limit
// condition; nil otherwise. Call this on every non-2xx response before
// constructing a generic error.
func detectRateLimit(resp *http.Response) *RateLimitError {
	if resp == nil {
		return nil
	}
	// 429 with Retry-After is the most explicit signal.
	if resp.StatusCode == 429 {
		ra := resp.Header.Get("Retry-After")
		reset := time.Now().Add(60 * time.Second) // fallback if header is missing or unparseable
		if secs, err := strconv.Atoi(ra); err == nil {
			reset = time.Now().Add(time.Duration(secs) * time.Second)
		} else if t, err := http.ParseTime(ra); err == nil {
			reset = t
		}
		return &RateLimitError{ResetAt: reset}
	}
	// 403 with X-RateLimit-Remaining: 0 is GitHub's primary rate-limit signal.
	if resp.StatusCode == 403 && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		rl := &RateLimitError{}
		if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
			if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
				rl.ResetAt = time.Unix(epoch, 0)
			}
		}
		return rl
	}
	return nil
}
```

Then in each error-returning code path in `client.go`, call `detectRateLimit(resp)` and return the typed error if non-nil. Specifically, look for the `Get`, `GetPaginated`, and `GetRaw` methods (and any other request helpers) — each one needs the rate-limit check inserted before the existing generic error construction.

Imports: add `"net/http"` and `"strconv"` if not already present.

Also do the same check inside the GraphQL client wrapper if one exists (search for `gql.DoWithContext` or similar). GraphQL responses use the same headers.

- [ ] **Step 5: Add `OpCodeRateLimited` to threadsops**

In `internal/threadsops/types.go`, in the `OpCode*` constants block:

```go
const (
	OpCodeBadInput     = "bad_input"
	OpCodeAuthRequired = "auth_required"
	OpCodeAuthScope    = "auth_scope_missing"
	OpCodePolicy       = "policy_violation"
	OpCodeNotFound     = "not_found"
	OpCodeUpstream     = "upstream_error"
	OpCodeRateLimited  = "rate_limited"  // ← new
	OpCodeInternal     = "internal"
)
```

- [ ] **Step 6: Route the typed error in every threadsops op**

In `internal/threadsops/ops.go`, `resolve.go`, and `bulk.go`, after every `api.FetchPR`, `api.ReplyToThread`, `api.ResolveThread`, `api.UnresolveThread`, `api.ResolveAllThreads`, `api.UnresolveAllThreads` call that produces an error, wrap the error handling in a helper:

```go
// rateLimitedOpError converts a *github.RateLimitError into an OpError,
// or returns nil if the underlying error is not a rate-limit.
func rateLimitedOpError(err error) *OpError {
	var rl *github.RateLimitError
	if !errors.As(err, &rl) {
		return nil
	}
	return &OpError{
		Code:      OpCodeRateLimited,
		Message:   "rate limit exceeded",
		Retryable: true,
		Details: map[string]any{
			"reset_at":            rl.ResetAt.UTC().Format(time.RFC3339),
			"retry_after_seconds": rl.RetryAfterSeconds(),
			"remaining":           rl.Remaining,
		},
	}
}
```

Add this helper to `internal/threadsops/types.go` or a new `internal/threadsops/errors.go`. Add `"errors"`, `"time"`, and `"github.com/svnbjrn/spoon/internal/github"` to the imports.

Then in every op's error path, prefer the rate-limit error:

```go
// Before:
if err != nil {
	return nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
}
// After:
if err != nil {
	if op := rateLimitedOpError(err); op != nil {
		return nil, op
	}
	return nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
}
```

Apply this transformation to every `&OpError{Code: OpCodeUpstream, ...}` construction in `ops.go`, `resolve.go`, and `bulk.go`. For functions with multiple return values like `(*ReviewThreadWithPolicy, bool, *OpError)`, adjust the rate-limit path to return the zero/false values for the non-error fields.

- [ ] **Step 7: Add threadsops tests**

Append a rate-limit test to each of `internal/threadsops/ops_test.go`, `resolve_test.go`, and `bulk_test.go`. Example for `ops_test.go`:

```go
type rateLimitFake struct {
	fakeAPI
	limitErr error
}

func (r *rateLimitFake) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	return github.PullRequestStatus{}, nil, r.limitErr
}

func TestList_routesRateLimited(t *testing.T) {
	reset := time.Now().Add(90 * time.Second)
	f := &rateLimitFake{limitErr: &github.RateLimitError{ResetAt: reset, Remaining: 0}}
	_, _, opErr := List(context.Background(), f, "o", "r", 1, false)
	if opErr == nil || opErr.Code != OpCodeRateLimited {
		t.Fatalf("expected OpCodeRateLimited, got %+v", opErr)
	}
	if opErr.Retryable != true {
		t.Errorf("expected Retryable=true")
	}
	if _, ok := opErr.Details["reset_at"].(string); !ok {
		t.Errorf("missing details.reset_at")
	}
	if secs, ok := opErr.Details["retry_after_seconds"].(int); !ok || secs < 80 {
		t.Errorf("expected retry_after_seconds ~90, got %v", opErr.Details["retry_after_seconds"])
	}
}
```

Add equivalent tests in `resolve_test.go` (cover `Resolve` and `ResolveWithThreads`) and `bulk_test.go` (cover `ResolveAll`).

- [ ] **Step 8: Add rate-limit propagation to `forksops.Stream`**

In `internal/forksops/stream.go`:

1. After `provider.Parent(...)` returns an error, check for `*github.RateLimitError` and wrap the synchronous return with a structured message:

```go
parent, err := provider.Parent(ctx, owner, repo)
if err != nil {
	var rl *github.RateLimitError
	if errors.As(err, &rl) {
		return nil, fmt.Errorf("rate_limited: reset_at=%s retry_after_seconds=%d: %w",
			rl.ResetAt.UTC().Format(time.RFC3339), rl.RetryAfterSeconds(), err)
	}
	return nil, fmt.Errorf("fetch parent: %w", err)
}
```

Similar for `provider.ListForks(...)`.

2. In the per-fork worker, after `provider.Compare(...)` or `provider.Contributors(...)` returns an error, build a `&Error{Code: "rate_limited", Details: { reset_at, retry_after_seconds }}` when the underlying error is a `*RateLimitError`:

```go
t2, terr := provider.Compare(ctx, s.fork, s.fork.DefaultBranch)
if terr != nil {
	var rl *github.RateLimitError
	if errors.As(terr, &rl) {
		r.Err = &Error{
			Code:    "rate_limited",
			Message: "rate limit exceeded",
			Details: map[string]any{
				"fork":                s.fork.ID,
				"stage":               "compare",
				"reset_at":            rl.ResetAt.UTC().Format(time.RFC3339),
				"retry_after_seconds": rl.RetryAfterSeconds(),
			},
		}
	} else {
		r.Err = &Error{Code: "upstream_error", Message: terr.Error(), Details: map[string]any{"fork": s.fork.ID, "stage": "compare"}}
	}
}
```

Same pattern for `provider.Contributors(...)`.

Imports: add `"errors"` and `"github.com/svnbjrn/spoon/internal/github"` to `internal/forksops/stream.go`.

- [ ] **Step 9: Add forksops rate-limit tests**

Append to `internal/forksops/stream_test.go`:

```go
func TestStream_perForkRateLimit(t *testing.T) {
	pushedAt := time.Now()
	reset := time.Now().Add(60 * time.Second)
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: pushedAt},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: pushedAt, DefaultBranch: "main"},
		},
		forkErrors: map[string]error{
			"o/a": &github.RateLimitError{ResetAt: reset, Remaining: 0},
		},
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, TopN: 1})
	r := <-ch
	if r.Err == nil || r.Err.Code != "rate_limited" {
		t.Fatalf("expected per-fork rate_limited, got %+v", r.Err)
	}
	if _, ok := r.Err.Details["reset_at"].(string); !ok {
		t.Errorf("missing details.reset_at")
	}
}

func TestStream_fatalRateLimit_parent(t *testing.T) {
	ff := &fakeForge{parentErr: &github.RateLimitError{ResetAt: time.Now().Add(time.Minute)}}
	_, err := Stream(context.Background(), ff, "o", "r", Options{})
	if err == nil {
		t.Fatal("expected fatal error")
	}
	if !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("expected error to mention rate_limited; got %v", err)
	}
}
```

Add `"github.com/svnbjrn/spoon/internal/github"` and `"strings"` to the test file imports.

- [ ] **Step 10: Update cmd/spn translation helpers**

In `cmd/spn/threads.go`, find `translateOpErr` and `translateResolveErr`. Add a case for `agentio.CodeRateLimited`:

```go
case agentio.CodeRateLimited:
	resetAt, _ := op.Details["reset_at"].(string)
	secs, _ := op.Details["retry_after_seconds"].(int)
	rem = agentio.RemediationRateLimited(resetAt, secs)
```

After the agentio.NewError + WithDetails chain, add `e = e.WithRetryAfter(secs)` if `secs > 0`. Verify the exact API of `agentio.Error.WithRetryAfter` by reading `internal/agentio/error.go`.

Mirror the same case into any other translation helpers in `cmd/spn/pr.go`, `cmd/spn/repo.go`, `cmd/spn/embed.go`. If those files share `translateOpErr` from `threads.go`, only one edit is needed.

For `cmd/spn/forks.go`'s per-fork error handler (where a `forksops.Error` with `Code: "rate_limited"` arrives), no change is needed if the existing handler already prints the details map to stderr.

- [ ] **Step 11: Add cmd/spn end-to-end tests**

Add a test to `cmd/spn/threads_test.go`:

```go
type rateLimitedListStub struct {
	stubAPI
}

func (r *rateLimitedListStub) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	return github.PullRequestStatus{}, nil, &github.RateLimitError{ResetAt: time.Now().Add(60 * time.Second), Remaining: 0}
}

func TestSpnThreadsList_rateLimited(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &rateLimitedListStub{}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1"}, &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit=%d", exit)
	}
	var env map[string]map[string]any
	_ = json.Unmarshal(stderr.Bytes(), &env)
	if env["error"]["code"] != "rate_limited" {
		t.Errorf("code=%v", env["error"]["code"])
	}
	if env["error"]["retryable"] != true {
		t.Errorf("expected retryable=true")
	}
	if env["error"]["retry_after_seconds"] == nil {
		t.Errorf("expected retry_after_seconds populated")
	}
}
```

Add similar tests to `cmd/spn/pr_test.go` (against `spn pr status`), `cmd/spn/forks_test.go` (against `spn forks list` with a stubbed provider whose `Parent` returns the typed error), and so on. One representative test per spn verb is sufficient; the threadsops/forksops tests already cover the deeper paths.

- [ ] **Step 12: Run everything**

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/spoon ./cmd/spn
```

All clean.

- [ ] **Step 13: Commit**

```bash
git add internal/github/rate_limit_error.go internal/github/rate_limit_error_test.go internal/github/client.go internal/threadsops/ internal/forksops/stream.go internal/forksops/stream_test.go cmd/spn/
git commit -m "Wire OpCodeRateLimited as a first-class agent error

A new *github.RateLimitError typed error captures the GitHub
X-RateLimit-Remaining: 0 and Retry-After signals. The client HTTP
layer detects them on every non-2xx response. threadsops gains
OpCodeRateLimited, and every op routes the typed error via errors.As
before falling through to upstream_error. forksops.Stream propagates
the same signal as a per-fork Error.Code = \"rate_limited\" or as a
fatal synchronous error for Parent/ListForks failures. cmd/spn
translation populates agentio.Error.RetryAfterSeconds and the
remediation string for the rate-limited case."
```

---

## Task 8: Delete `github.UniqueAuthors`

**Files:**
- Modify: `internal/github/compare.go`
- Modify: `internal/github/compare_test.go` if a test exists; otherwise nothing

- [ ] **Step 1: Verify no live callers**

```bash
grep -rn "github\.UniqueAuthors\b" --include="*.go" . | grep -v _test.go
```

Expected: empty output. If any caller appears, migrate it to `forge.UniqueAuthors` before continuing.

- [ ] **Step 2: Delete the function**

In `internal/github/compare.go`, delete the `UniqueAuthors` function (and its doc comment).

- [ ] **Step 3: Delete any tests for it**

If `internal/github/compare_test.go` has a `TestUniqueAuthors` (or similarly named test), delete it.

```bash
grep -ln "TestUniqueAuthors\|UniqueAuthors\b" internal/github/*_test.go
```

For each match, delete the test.

- [ ] **Step 4: Verify clean**

```bash
go test ./...
go vet ./...
go build ./...
```

All clean.

- [ ] **Step 5: Commit**

```bash
git add internal/github/compare.go internal/github/compare_test.go
git commit -m "Delete dead github.UniqueAuthors

forge.UniqueAuthors (email-fallback) is the canonical identity-set
primitive; the github copy with a name-fallback had no production
callers and would silently inflate author counts for unsigned-commit
forks if revived."
```

---

## Task 9: Update the `using-spn` skill

**Files:**
- Modify: `docs/superpowers/skills/using-spn/SKILL.md`
- Out-of-git: `/home/svnbjrn/.claude/skills/using-spn/SKILL.md` (copied at the end of this task)

- [ ] **Step 1: Read the current skill structure**

```bash
grep -n '^## ' docs/superpowers/skills/using-spn/SKILL.md
```

Note the current section ordering so the new content slots in naturally. The "Rate Limits" subsection goes inside the **Output Contract** section, after "Exit codes". The "CSV mode" subsection goes inside **Output Contract** as well. The new Quick Reference row goes at the end of the **Quick Reference** table.

- [ ] **Step 2: Add the Rate Limits subsection**

In `docs/superpowers/skills/using-spn/SKILL.md`, find the Exit codes block (search for `**Exit codes:**`). After that block, insert:

````markdown
### Rate Limits

When GitHub rate-limits a request, the error envelope uses `code: "rate_limited"`, populates `retry_after_seconds`, and carries `details.reset_at` (RFC3339 UTC) plus `details.remaining`:

```json
{"error": {
  "code": "rate_limited",
  "message": "rate limit exceeded",
  "remediation": "Rate limit exceeded. Wait until <details.reset_at> ...",
  "retryable": true,
  "retry_after_seconds": 1234,
  "details": {"reset_at": "2026-05-11T14:30:00Z", "remaining": 0}
}}
```

Retry pattern:

```bash
out=$(spn pr status "$PR" 2>/tmp/err.json) || {
  code=$(jq -r .error.code /tmp/err.json)
  if [ "$code" = "rate_limited" ]; then
    wait=$(jq -r .error.retry_after_seconds /tmp/err.json)
    sleep "$wait"
    out=$(spn pr status "$PR")
  fi
}
```
````

- [ ] **Step 3: Add the CSV mode subsection**

After the new Rate Limits subsection (or wherever fits the document flow), insert:

````markdown
### CSV mode

`spn forks list <repo> --csv` collects all enriched forks and emits a single CSV blob on stdout with a fixed header (`id,owner,name,url,stars,pushed_at,is_archived,sub_forks,releases,heat,tier,t2_ahead,t2_behind,t2_mna,t3_contributors,t3_commit_span_days,cluster_name,cluster_score`). Per-fork enrichment errors still go to stderr as compact JSON. Use this when downstream tooling expects tabular data; use the default NDJSON when streaming or jq pipelines fit better.
````

- [ ] **Step 4: Add the Common Mistakes row**

Find the **Common Mistakes** table. Append a new row:

```markdown
| Treating `rate_limited` as `upstream_error` | Check `code` explicitly — `rate_limited` has a known `retry_after_seconds`. Sleeping that long is reliable; blind retry on `upstream_error` may keep hitting the limit. |
```

- [ ] **Step 5: Add the Quick Reference row**

Find the **Quick Reference** table near the bottom. Append:

```markdown
| `spn forks list <repo> --csv` | Batched CSV with fixed header; switches off NDJSON streaming. Use for spreadsheet/tabular consumers. |
```

- [ ] **Step 6: Verify the skill renders**

Read the file end-to-end (`cat docs/superpowers/skills/using-spn/SKILL.md`) and confirm:

- Frontmatter is intact (`---` block at the top)
- Section headers (`##`) are in a sensible order
- Code fences are balanced (no orphan `\`\`\``)
- Word count is under 1300 — check with `wc -w docs/superpowers/skills/using-spn/SKILL.md`

If a code fence is broken or a section header is misnumbered, fix inline.

- [ ] **Step 7: Sync the personal copy**

```bash
cp docs/superpowers/skills/using-spn/SKILL.md /home/svnbjrn/.claude/skills/using-spn/SKILL.md
```

This step is not part of the git commit (the personal copy is outside the repo). Mention it in the commit message so future readers know.

- [ ] **Step 8: Commit**

```bash
git add docs/superpowers/skills/using-spn/SKILL.md
git commit -m "Update using-spn skill with rate_limited envelope and --csv mode

Adds:
- Rate Limits subsection under Output Contract describing the
  rate_limited code, retry_after_seconds, and a retry pattern.
- CSV mode subsection describing spn forks list --csv.
- Common Mistakes row warning against treating rate_limited as
  upstream_error.
- Quick Reference row for --csv.

Personal copy at ~/.claude/skills/using-spn/SKILL.md is synced
manually via cp (outside the git commit since it's not under
version control)."
```

---

## Final verification

After all nine commits land:

- [ ] **Full test sweep**

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/spoon ./cmd/spn
```

All clean.

- [ ] **Grep — confirm v1 surface is gone**

```bash
grep -rn "ComputeTier1\b\|ComputeTier2\b\|type Signal struct\|type LoneWolfSignal struct\|\.Heat\.Signals\b\|github\.UniqueAuthors\b\|internal/dump\"" --include="*.go" .
```

Expected: empty output.

- [ ] **Manual smoke**

```bash
go build -o /tmp/spoon ./cmd/spoon
/tmp/spoon                                 # TUI launches; --json/--csv are not in help
/tmp/spoon --json                          # exits 2 with "unknown flag"
rm /tmp/spoon

go build -o /tmp/spn ./cmd/spn
/tmp/spn forks list Raudbjorn/spoon --tier 1 | head -3     # NDJSON lines
/tmp/spn forks list Raudbjorn/spoon --tier 1 --csv | head -3   # CSV with header
rm /tmp/spn
```

- [ ] **Push and open PR**

```bash
git push -u origin convergence-d
gh pr create --title "Phase D: surface and schema convergence" --body "$(cat <<'EOF'
## Summary

Retires the silent v1/v2 fork-scoring schema split and removes spoon's
non-TUI output paths. Spoon becomes TUI-only for fork discovery; spn
forks list grows a --csv batched mode for tabular consumers.

- Delete \`internal/dump/\` entirely.
- Delete v1 heat scoring (ComputeTier1, ComputeTier2, DetectLoneWolf
  legacy wrapper, Signal, LoneWolfSignal, HeatResult.Signals/.LoneWolf
  fields).
- Migrate the TUI off v1 (\`tui/{app,detail,export}.go\`).
- Wire DetectLoneWolfV2 into forksops.Stream — previously left as a
  TODO; fork results now populate LoneWolfV2.
- Add a typed *github.RateLimitError, OpCodeRateLimited at the
  threadsops layer, and matching translation in cmd/spn so GitHub
  rate-limit responses become a first-class \`rate_limited\` envelope
  with \`retry_after_seconds\`.
- Add \`spn forks list --csv\` batched mode.
- Delete the dead \`github.UniqueAuthors\` duplicate;
  \`forge.UniqueAuthors\` (email-fallback) stays canonical.
- Update both copies of the using-spn skill with the new
  rate_limited envelope docs and --csv mode docs.

Spec: \`docs/superpowers/specs/2026-05-11-surface-schema-convergence-design.md\`

## Test plan

- [x] \`go test ./...\` pass
- [x] \`go test -race ./...\` pass
- [x] \`go vet ./...\` clean
- [x] \`go build ./cmd/spoon ./cmd/spn\` clean
- [x] Grep confirms zero remaining v1 references
- [x] Manual smoke: spoon TUI works, spn forks list --csv emits header + rows
- [ ] Manual live: spn pr status against an unauthenticated repo to trigger rate-limited (optional)

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review notes

- **Spec coverage:** every section of the spec maps to a task. The Goal, Non-goals, and Audiences sections inform Tasks 1–9 collectively. The Architecture table is realized across Tasks 1–8. The Surface convergence and CSV schema sections are Tasks 5 and 6. Schema convergence is Tasks 3 and 2. TUI v2 migration is Task 2. DetectLoneWolfV2 wiring is Task 1. Output contract additions (RateLimitError, OpCodeRateLimited, forksops/spn translation) are Task 7. The internal/dump deletion is Task 4. The github.UniqueAuthors deletion is Task 8. The skill update is Task 9. No spec requirement is unaddressed.

- **Placeholder scan:** no "TBD", "TODO", "implement later", or "fill in details" tokens in the plan body. The only "TODO" reference is in Task 1 Step 3 describing the existing TODO comment that this PR removes — that's a documentation reference, not a plan placeholder.

- **Type consistency:** the symbol names used across tasks match — `LoneWolfInput`, `LWCommitInfo`, `LoneWolfResult`, `FileChange`, `Tier3ParamsV2`, `OpCodeRateLimited`, `Error.Code`, `forksops.Result`, `forksops.Error`, `*github.RateLimitError` are all used consistently. The cross-task references (Task 1's `buildLoneWolfInput` is mentioned in Task 2 as a pattern to copy) are explicit.
