# PR Mergeability Status Header Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the `spoon threads` mergeability/reviews/checks/threads status header per `docs/superpowers/specs/2026-05-10-pr-status-header-design.md`. Status prints above every mode's output (stderr for `--json`/`--next`, stdout for mutation modes, header panel in TUI).

**Architecture:** Replace the existing `ListThreads` GraphQL query with a `FetchPR` query that returns both PR status and threads in one round trip. Add a `RenderStatusBlock` helper in `cmd/spoon/status.go` that writes a 4-line block to any `io.Writer` with TTY-aware glyphs. Wire it into the dispatcher and the TUI Model.

**Tech Stack:** Go 1.26, `github.com/cli/go-gh/v2` GraphQL, `github.com/mattn/go-isatty` (already a transitive dep) for TTY detection, ANSI codes via `github.com/charmbracelet/lipgloss` (already direct dep).

---

## File Structure

| File | Purpose |
| --- | --- |
| `internal/github/threads.go` | Modify: add `PullRequestStatus` type, expand GraphQL query, rename `ListThreads` → `FetchPR` with new return tuple |
| `internal/github/threads_test.go` | Add fixture + test for status fields |
| `internal/github/testdata/threads_status_basic.json` | New fixture with non-trivial status |
| `cmd/spoon/status.go` | Create: `RenderStatusBlock` + glyph/ASCII rendering |
| `cmd/spoon/status_test.go` | Create: table tests for renderer |
| `cmd/spoon/threads.go` | Modify: call `FetchPR` instead of `ListThreads`; print status block; add `--no-status` flag |
| `cmd/spoon/threads_test.go` | Add `--no-status` flag-parser case |
| `internal/tui/threads/model.go` | Modify: store `PullRequestStatus`, fetch via `FetchPR`, expose to view |
| `internal/tui/threads/view.go` | Modify: render status header above thread list |
| `internal/tui/threads/model_test.go` | Existing tests pass through unchanged-signature `New()` |
| `README.md` | Modify: note the status header in the threads section |

---

## Task 1: PullRequestStatus type + expanded GraphQL query + parser

**Files:**
- Modify: `internal/github/threads.go`
- Modify: `internal/github/threads_test.go`
- Create: `internal/github/testdata/threads_status_basic.json`

- [ ] **Step 1.1: Add the test fixture**

Create `internal/github/testdata/threads_status_basic.json`:

```json
{
  "data": {
    "repository": {
      "pullRequest": {
        "title": "Add feature X",
        "isDraft": false,
        "merged": false,
        "mergeable": "CONFLICTING",
        "mergeStateStatus": "DIRTY",
        "reviewDecision": "REVIEW_REQUIRED",
        "commits": {
          "nodes": [
            {
              "commit": {
                "statusCheckRollup": { "state": "FAILURE" }
              }
            }
          ]
        },
        "reviewThreads": {
          "pageInfo": { "hasNextPage": false, "endCursor": null },
          "nodes": [
            {
              "id": "PRRT_1",
              "isResolved": false,
              "path": "x.go",
              "line": 1,
              "startLine": null,
              "diffSide": "RIGHT",
              "comments": {
                "nodes": [
                  {
                    "id": "PRRC_1",
                    "body": "fix this",
                    "createdAt": "2026-05-10T09:00:00Z",
                    "author": { "__typename": "User", "login": "alice" }
                  }
                ]
              }
            }
          ]
        }
      }
    }
  }
}
```

- [ ] **Step 1.2: Write the failing test**

Append to `internal/github/threads_test.go`:

```go
func TestParseFetchPRResponse(t *testing.T) {
	data, err := os.ReadFile("testdata/threads_status_basic.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var raw struct {
		Data listThreadsData `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	status, threads := parseFetchPRResponse(raw.Data)

	if status.Title != "Add feature X" {
		t.Errorf("title %q", status.Title)
	}
	if status.Mergeable != "CONFLICTING" || status.MergeStateStatus != "DIRTY" {
		t.Errorf("mergeable=%q state=%q", status.Mergeable, status.MergeStateStatus)
	}
	if status.ReviewDecision != "REVIEW_REQUIRED" {
		t.Errorf("review=%q", status.ReviewDecision)
	}
	if status.ChecksState != "FAILURE" {
		t.Errorf("checks=%q", status.ChecksState)
	}
	if len(threads) != 1 || threads[0].ID != "PRRT_1" {
		t.Errorf("threads: %+v", threads)
	}
}
```

- [ ] **Step 1.3: Run test, verify failure**

Run: `go test ./internal/github/ -run TestParseFetchPRResponse -v`
Expected: FAIL — `undefined: parseFetchPRResponse`, `undefined: PullRequestStatus`.

- [ ] **Step 1.4: Add the `PullRequestStatus` type**

In `internal/github/threads.go`, near the top with the other public types (after `ThreadComment`), add:

```go
// PullRequestStatus carries the top-of-output mergeability summary.
// Empty-string fields mean "not applicable" (e.g. no checks configured).
type PullRequestStatus struct {
	Title             string `json:"title"`
	IsDraft           bool   `json:"isDraft"`
	Merged            bool   `json:"merged"`
	Mergeable         string `json:"mergeable"`        // MERGEABLE | CONFLICTING | UNKNOWN
	MergeStateStatus  string `json:"mergeStateStatus"` // CLEAN | BEHIND | DIRTY | BLOCKED | DRAFT | HAS_HOOKS | UNSTABLE | UNKNOWN
	ReviewDecision    string `json:"reviewDecision"`   // APPROVED | REVIEW_REQUIRED | CHANGES_REQUESTED | ""
	ChecksState       string `json:"checksState"`      // SUCCESS | FAILURE | PENDING | ERROR | EXPECTED | ""
	UnresolvedThreads int    `json:"unresolvedThreads"`
}
```

- [ ] **Step 1.5: Extend `listThreadsData` with status fields**

In `internal/github/threads.go`, modify `listThreadsData` to include the new fields. Replace the struct definition with:

```go
type listThreadsData struct {
	Repository struct {
		PullRequest struct {
			Title            string `json:"title"`
			IsDraft          bool   `json:"isDraft"`
			Merged           bool   `json:"merged"`
			Mergeable        string `json:"mergeable"`
			MergeStateStatus string `json:"mergeStateStatus"`
			ReviewDecision   string `json:"reviewDecision"`
			Commits          struct {
				Nodes []struct {
					Commit struct {
						StatusCheckRollup *struct {
							State string `json:"state"`
						} `json:"statusCheckRollup"`
					} `json:"commit"`
				} `json:"nodes"`
			} `json:"commits"`
			ReviewThreads struct {
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []rawThread `json:"nodes"`
			} `json:"reviewThreads"`
		} `json:"pullRequest"`
	} `json:"repository"`
}
```

- [ ] **Step 1.6: Add the parser**

Below `parseListThreadsResponse`, add:

```go
// parseFetchPRResponse extracts both the PR status and the threads from a
// FetchPR response. UnresolvedThreads is computed from all threads (not
// affected by client-side state filtering).
func parseFetchPRResponse(data listThreadsData) (PullRequestStatus, []ReviewThread) {
	pr := data.Repository.PullRequest
	status := PullRequestStatus{
		Title:            pr.Title,
		IsDraft:          pr.IsDraft,
		Merged:           pr.Merged,
		Mergeable:        pr.Mergeable,
		MergeStateStatus: pr.MergeStateStatus,
		ReviewDecision:   pr.ReviewDecision,
	}
	if len(pr.Commits.Nodes) > 0 && pr.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		status.ChecksState = pr.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	threads := parseListThreadsResponse(data)
	for _, t := range threads {
		if !t.IsResolved {
			status.UnresolvedThreads++
		}
	}
	return status, threads
}
```

- [ ] **Step 1.7: Run test, verify pass**

Run: `go test ./internal/github/ -run TestParseFetchPRResponse -v`
Expected: PASS.

- [ ] **Step 1.8: Run all package tests** (existing `TestParseListThreadsResponse` should still pass — it asserts only the thread fields, which are unchanged)

Run: `go test ./internal/github/`
Expected: green.

- [ ] **Step 1.9: Commit**

```bash
git add internal/github/threads.go internal/github/threads_test.go internal/github/testdata/threads_status_basic.json
git commit -m "Add PullRequestStatus type and parser"
```

---

## Task 2: Rename `ListThreads` to `FetchPR`, update 6 call sites

**Files:**
- Modify: `internal/github/threads.go` (method definition + 2 internal callers in bulk ops)
- Modify: `cmd/spoon/threads.go` (3 call sites in dispatcher)
- Modify: `internal/tui/threads/model.go` (1 call site in loadCmd)

- [ ] **Step 2.1: Update the method definition**

In `internal/github/threads.go`, replace the existing `ListThreads` method with `FetchPR`:

```go
// FetchPR fetches the PR status and review threads in one GraphQL round trip.
// resolvedStates should be one of ThreadStateAll, ThreadStateUnresolved, or
// ThreadStateResolved (filtering is client-side; the server does not expose a
// resolvedStates filter on reviewThreads). UnresolvedThreads in the returned
// status is computed from ALL threads, not just the filtered subset.
func (c *Client) FetchPR(ctx context.Context, owner, repo string, number int, resolvedStates string) (PullRequestStatus, []ReviewThread, error) {
	if c.gql == nil {
		return PullRequestStatus{}, nil, fmt.Errorf("GraphQL client not available (auth required)")
	}
	const query = `
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      title
      isDraft
      merged
      mergeable
      mergeStateStatus
      reviewDecision
      commits(last: 1) {
        nodes {
          commit {
            statusCheckRollup { state }
          }
        }
      }
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          path
          line
          startLine
          diffSide
          comments(first: 100) {
            nodes {
              id
              body
              createdAt
              author { __typename login }
            }
          }
        }
      }
    }
  }
}`
	var status PullRequestStatus
	var all []ReviewThread
	var cursor *string
	firstPage := true
	for {
		vars := map[string]interface{}{
			"owner":  owner,
			"name":   repo,
			"number": number,
			"after":  cursor,
		}
		var resp listThreadsData
		if err := c.gql.DoWithContext(ctx, query, vars, &resp); err != nil {
			return PullRequestStatus{}, nil, fmt.Errorf("fetch PR: %w", err)
		}
		pageStatus, pageThreads := parseFetchPRResponse(resp)
		if firstPage {
			status = pageStatus
			firstPage = false
		}
		all = append(all, pageThreads...)
		page := resp.Repository.PullRequest.ReviewThreads.PageInfo
		if !page.HasNextPage || page.EndCursor == nil {
			break
		}
		cursor = page.EndCursor
	}
	// Apply client-side state filter.
	if resolvedStates == ThreadStateResolved {
		filtered := all[:0]
		for _, t := range all {
			if t.IsResolved {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	} else if resolvedStates == ThreadStateUnresolved {
		filtered := all[:0]
		for _, t := range all {
			if !t.IsResolved {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	}
	// Recompute UnresolvedThreads from the unfiltered slice was done in parser;
	// don't overwrite based on the filtered slice.
	return status, all, nil
}
```

Remove the old `ListThreads` definition entirely.

- [ ] **Step 2.2: Update internal call sites in `internal/github/threads.go`**

Find `ResolveAllThreads` and `UnresolveAllThreads`. Each currently calls `c.ListThreads(...)`. Change them to discard the status:

```go
func (c *Client) ResolveAllThreads(ctx context.Context, owner, repo string, number, workers int) (*BulkResult, error) {
	_, threads, err := c.FetchPR(ctx, owner, repo, number, ThreadStateUnresolved)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(threads))
	for i, t := range threads {
		ids[i] = t.ID
	}
	return c.bulkFlip(ctx, ids, true, workers), nil
}

func (c *Client) UnresolveAllThreads(ctx context.Context, owner, repo string, number, workers int) (*BulkResult, error) {
	_, threads, err := c.FetchPR(ctx, owner, repo, number, ThreadStateResolved)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(threads))
	for i, t := range threads {
		ids[i] = t.ID
	}
	return c.bulkFlip(ctx, ids, false, workers), nil
}
```

- [ ] **Step 2.3: Update dispatcher call sites in `cmd/spoon/threads.go`**

Find the three `client.ListThreads(...)` call sites in `runThreads`. Update each:

**`modeJSON` branch** — change:

```go
		threads, err := client.ListThreads(ctx, owner, repo, number, states)
```

to:

```go
		_, threads, err := client.FetchPR(ctx, owner, repo, number, states)
```

(The status will be wired in via a later task. For now this preserves behavior.)

**`modeNext` branch** — same pattern:

```go
		_, threads, err := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateUnresolved)
```

**`modeResolve` branch** — same pattern:

```go
		_, all, err := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
```

- [ ] **Step 2.4: Update the TUI loader**

In `internal/tui/threads/model.go`, find `loadCmd`. The existing implementation:

```go
func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		state := gh.ThreadStateUnresolved
		if m.includeResolved {
			state = gh.ThreadStateAll
		}
		ts, err := m.client.ListThreads(context.Background(), m.owner, m.repo, m.number, state)
		return loadedMsg{threads: ts, err: err}
	}
}
```

Change to:

```go
func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		state := gh.ThreadStateUnresolved
		if m.includeResolved {
			state = gh.ThreadStateAll
		}
		status, ts, err := m.client.FetchPR(context.Background(), m.owner, m.repo, m.number, state)
		return loadedMsg{status: status, threads: ts, err: err}
	}
}
```

And update the `loadedMsg` type to carry the status (a few lines below `loadCmd`):

```go
type loadedMsg struct {
	status  gh.PullRequestStatus
	threads []gh.ReviewThread
	err     error
}
```

Add a `status gh.PullRequestStatus` field to the `Model` struct (alongside `threads`). Update the `loadedMsg` case in `Update` to also store the status:

```go
	case loadedMsg:
		m.loaded = true
		m.status = msg.status
		m.threads = msg.threads
		m.err = msg.err
		if m.cursor >= len(m.threads) {
			m.cursor = max(0, len(m.threads)-1)
		}
		return m, nil
```

- [ ] **Step 2.5: Verify build and existing tests**

Run: `go build ./... && go test ./...`
Expected: all green. The existing tests didn't assert anything about status, so they continue passing with the new signature.

- [ ] **Step 2.6: Commit**

```bash
git add internal/github/threads.go cmd/spoon/threads.go internal/tui/threads/model.go
git commit -m "Rename ListThreads to FetchPR, returning PR status alongside threads"
```

---

## Task 3: `RenderStatusBlock` helper

**Files:**
- Create: `cmd/spoon/status.go`
- Create: `cmd/spoon/status_test.go`

- [ ] **Step 3.1: Write the failing test**

Create `cmd/spoon/status_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"

	gh "github.com/svnbjrn/spoon/internal/github"
)

func TestRenderStatusBlock(t *testing.T) {
	allGreen := gh.PullRequestStatus{
		Title:             "Add feature",
		MergeStateStatus:  "CLEAN",
		Mergeable:         "MERGEABLE",
		ReviewDecision:    "APPROVED",
		ChecksState:       "SUCCESS",
		UnresolvedThreads: 0,
	}
	allRed := gh.PullRequestStatus{
		Title:             "Add feature",
		MergeStateStatus:  "DIRTY",
		Mergeable:         "CONFLICTING",
		ReviewDecision:    "REVIEW_REQUIRED",
		ChecksState:       "FAILURE",
		UnresolvedThreads: 3,
	}
	missing := gh.PullRequestStatus{
		Title:             "WIP",
		MergeStateStatus:  "UNKNOWN",
		Mergeable:         "UNKNOWN",
		ReviewDecision:    "",
		ChecksState:       "",
		UnresolvedThreads: 0,
	}

	cases := []struct {
		name    string
		status  gh.PullRequestStatus
		number  int
		ascii   bool
		expects []string // substrings the output must contain
	}{
		{
			"all green ASCII",
			allGreen, 1, true,
			[]string{"PR #1", "Add feature", "[OK]", "CLEAN", "APPROVED", "SUCCESS", "0 unresolved"},
		},
		{
			"all red ASCII",
			allRed, 42, true,
			[]string{"PR #42", "[X]", "DIRTY", "REVIEW_REQUIRED", "FAILURE", "3 unresolved"},
		},
		{
			"missing review and checks shown as dash",
			missing, 7, true,
			[]string{"—", "UNKNOWN"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := RenderStatusBlock(&buf, tc.status, tc.number, !tc.ascii); err != nil {
				t.Fatalf("render: %v", err)
			}
			out := buf.String()
			for _, sub := range tc.expects {
				if !strings.Contains(out, sub) {
					t.Errorf("output missing %q\n---\n%s", sub, out)
				}
			}
			// Block ends with a blank line.
			if !strings.HasSuffix(out, "\n\n") {
				t.Errorf("block must end with blank line, got %q", out[len(out)-3:])
			}
		})
	}
}
```

- [ ] **Step 3.2: Run test, verify failure**

Run: `go test ./cmd/spoon/ -run TestRenderStatusBlock -v`
Expected: FAIL — `undefined: RenderStatusBlock`.

- [ ] **Step 3.3: Implement `RenderStatusBlock`**

Create `cmd/spoon/status.go`:

```go
package main

import (
	"fmt"
	"io"

	gh "github.com/svnbjrn/spoon/internal/github"
)

// statusSignal categorizes a status line for color/glyph selection.
type statusSignal int

const (
	signalGreen statusSignal = iota
	signalYellow
	signalRed
	signalNeutral // for "—" / not applicable
)

// RenderStatusBlock writes a four-line PR status header to w followed by
// a blank line. useGlyphs selects ✓/⏳/✗ vs [OK]/[..]/[X] / ASCII output.
func RenderStatusBlock(w io.Writer, s gh.PullRequestStatus, number int, useGlyphs bool) error {
	mergeSig := mergeSignal(s.MergeStateStatus)
	mergeText := mergeText(s.MergeStateStatus)
	reviewSig, reviewText := reviewSignal(s.ReviewDecision)
	checksSig, checksText := checksSignal(s.ChecksState)
	threadsSig := signalGreen
	if s.UnresolvedThreads > 0 {
		threadsSig = signalRed
	}

	if _, err := fmt.Fprintf(w, "PR #%d — %s\n", number, s.Title); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Mergeable: %s %s\n", marker(mergeSig, useGlyphs), mergeText); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Reviews:   %s %s\n", marker(reviewSig, useGlyphs), reviewText); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Checks:    %s %s\n", marker(checksSig, useGlyphs), checksText); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Threads:   %s %d unresolved\n", marker(threadsSig, useGlyphs), s.UnresolvedThreads); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func marker(sig statusSignal, useGlyphs bool) string {
	if useGlyphs {
		switch sig {
		case signalGreen:
			return "✓"
		case signalYellow:
			return "⏳"
		case signalRed:
			return "✗"
		default:
			return "—"
		}
	}
	switch sig {
	case signalGreen:
		return "[OK]"
	case signalYellow:
		return "[..]"
	case signalRed:
		return "[X]"
	default:
		return "—"
	}
}

func mergeSignal(state string) statusSignal {
	switch state {
	case "CLEAN":
		return signalGreen
	case "DIRTY", "BLOCKED", "DRAFT":
		return signalRed
	default: // BEHIND, UNSTABLE, HAS_HOOKS, UNKNOWN
		return signalYellow
	}
}

func mergeText(state string) string {
	if state == "" {
		return "—"
	}
	return state
}

func reviewSignal(decision string) (statusSignal, string) {
	if decision == "" {
		return signalNeutral, "—"
	}
	if decision == "APPROVED" {
		return signalGreen, decision
	}
	return signalRed, decision
}

func checksSignal(state string) (statusSignal, string) {
	if state == "" {
		return signalNeutral, "—"
	}
	switch state {
	case "SUCCESS", "EXPECTED":
		return signalGreen, state
	case "PENDING":
		return signalYellow, state
	default: // FAILURE, ERROR
		return signalRed, state
	}
}
```

- [ ] **Step 3.4: Run test, verify pass**

Run: `go test ./cmd/spoon/ -run TestRenderStatusBlock -v`
Expected: PASS — all 3 cases.

- [ ] **Step 3.5: Commit**

```bash
git add cmd/spoon/status.go cmd/spoon/status_test.go
git commit -m "Add RenderStatusBlock helper with ASCII/glyph and color signals"
```

---

## Task 4: `--no-status` flag

**Files:**
- Modify: `cmd/spoon/threads.go` (flag parser, help text)
- Modify: `cmd/spoon/threads_test.go` (parser test case)

- [ ] **Step 4.1: Write failing test**

Append to `cmd/spoon/threads_test.go` — append one more case to the existing `TestParseThreadsFlags` cases slice:

```go
		{
			name: "no-status sets the flag",
			args: []string{"owner/repo#42", "--no-status"},
			check: func(t *testing.T, f threadsFlags) {
				if !f.noStatus {
					t.Errorf("noStatus should be true")
				}
			},
		},
```

- [ ] **Step 4.2: Run test, verify failure**

Run: `go test ./cmd/spoon/ -run TestParseThreadsFlags -v`
Expected: FAIL — `f.noStatus undefined`.

- [ ] **Step 4.3: Add field + flag handling**

In `cmd/spoon/threads.go`, find the `threadsFlags` struct definition and add the field:

```go
type threadsFlags struct {
	prRef           string
	mode            threadsMode
	targetID        string
	body            string
	bodyFile        string
	includeResolved bool
	noStatus        bool
}
```

In `parseThreadsFlags`, add a case to the `switch a` block:

```go
		case "--no-status":
			f.noStatus = true
```

Place it next to `--include-resolved` for proximity.

In `printThreadsHelp`, add the flag to the help text:

```
  --no-status           Suppress the PR status header
```

(Insert in the flag list near `--include-resolved`.)

- [ ] **Step 4.4: Run test, verify pass**

Run: `go test ./cmd/spoon/ -run TestParseThreadsFlags -v`
Expected: PASS — 12 cases now.

- [ ] **Step 4.5: Commit**

```bash
git add cmd/spoon/threads.go cmd/spoon/threads_test.go
git commit -m "Add --no-status flag to suppress PR status header"
```

---

## Task 5: Wire status block into the non-TUI dispatcher

**Files:**
- Modify: `cmd/spoon/threads.go`

This task moves the status output into each non-TUI mode branch. The status is printed before mode output. Routing per spec:

- `--json` / `--next` → stderr
- `--reply` / `--resolve` / `--resolve-all` / `--unresolve-all` → stdout

- [ ] **Step 5.1: Add a helper to decide writer + glyph use**

Add at the bottom of `cmd/spoon/threads.go`:

```go
import "github.com/mattn/go-isatty"

// emitStatus writes the PR status header to w (unless suppressed). glyphs
// are used when w is a TTY.
func emitStatus(w *os.File, status gh.PullRequestStatus, number int, noStatus bool) {
	if noStatus {
		return
	}
	useGlyphs := isatty.IsTerminal(w.Fd())
	_ = RenderStatusBlock(w, status, number, useGlyphs)
}
```

Add `github.com/mattn/go-isatty` to imports. (It's already a transitive dep; `go mod tidy` will promote it after the import.)

- [ ] **Step 5.2: Wire into `modeJSON`**

In `runThreads`, find the `modeJSON` branch. Change:

```go
		states := gh.ThreadStateUnresolved
		if flags.includeResolved {
			states = gh.ThreadStateAll
		}
		_, threads, err := client.FetchPR(ctx, owner, repo, number, states)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		if err := emitJSON(os.Stdout, threads); err != nil {
```

to:

```go
		states := gh.ThreadStateUnresolved
		if flags.includeResolved {
			states = gh.ThreadStateAll
		}
		status, threads, err := client.FetchPR(ctx, owner, repo, number, states)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		emitStatus(os.Stderr, status, number, flags.noStatus)
		if err := emitJSON(os.Stdout, threads); err != nil {
```

- [ ] **Step 5.3: Wire into `modeNext`**

```go
		status, threads, err := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateUnresolved)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		emitStatus(os.Stderr, status, number, flags.noStatus)
		if err := emitNext(os.Stdout, threads); err != nil {
```

- [ ] **Step 5.4: Wire into `modeReply`**

```go
	case modeReply:
		status, _, err := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		emitStatus(os.Stdout, status, number, flags.noStatus)
		if _, err := client.ReplyToThread(ctx, flags.targetID, flags.body); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		return 0
```

- [ ] **Step 5.5: Wire into `modeResolve`**

In the `modeResolve` branch, the existing first call already fetches the threads via `FetchPR(... ThreadStateAll)`. Use the status it returns:

```go
	case modeResolve:
		status, all, err := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		emitStatus(os.Stdout, status, number, flags.noStatus)
		// ... rest of the modeResolve logic (find target, RequiresBody check, reply, resolve) unchanged
```

- [ ] **Step 5.6: Wire into `modeResolveAll` and `modeUnresolveAll`**

`modeResolveAll` currently calls `client.ResolveAllThreads(...)` which internally calls `FetchPR`. To avoid a duplicate query, fetch status here first:

```go
	case modeResolveAll:
		status, _, err := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		emitStatus(os.Stdout, status, number, flags.noStatus)
		res, err := client.ResolveAllThreads(ctx, owner, repo, number, 4)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		fmt.Printf("resolved %d threads\n", len(res.Succeeded))
		// ... rest unchanged
```

Same pattern for `modeUnresolveAll`.

Note: this *does* introduce a second `FetchPR` call (status here + the one inside the bulk method). The cost is one extra query per bulk invocation. Acceptable. (A future optimization: change the bulk method signatures to accept a pre-fetched thread list.)

- [ ] **Step 5.7: Verify build and tests**

Run: `go mod tidy && go build ./... && go vet ./... && go test ./...`
Expected: green. `go mod tidy` promotes `go-isatty` from indirect to direct in go.mod.

- [ ] **Step 5.8: Smoke test**

```
go build -o /tmp/spoon-test ./cmd/spoon
/tmp/spoon-test threads Raudbjorn/spoon#1 --json 2>/tmp/spoon-stderr; cat /tmp/spoon-stderr
```

Expected: stderr file contains the 5-line status block (or "[X]/[OK]"-style markers if ANSI is disabled in test env). Stdout has clean JSON.

- [ ] **Step 5.9: Commit**

```bash
git add cmd/spoon/threads.go go.mod go.sum
git commit -m "Print PR status header in non-TUI threads modes"
```

---

## Task 6: TUI status header

**Files:**
- Modify: `internal/tui/threads/view.go`
- Modify: `internal/tui/threads/model_test.go`

- [ ] **Step 6.1: Render status header above the thread list**

In `internal/tui/threads/view.go`, modify `renderModel`. The current function starts with:

```go
func renderModel(m Model) string {
	if m.showHelp {
		return renderHelp()
	}
	if m.err != nil {
		return fmt.Sprintf("error: %v\n\npress q to quit", m.err)
	}
	if !m.loaded {
		return "loading threads…"
	}
	...
```

After the `if !m.loaded { ... }` early return, before the existing list-rendering block, insert the status header:

```go
	var b strings.Builder
	b.WriteString(renderTUIStatus(m.status, m.number))
	b.WriteString("\n")
```

Then continue with the existing list rendering. Replace the existing line that initializes `b`:

```go
	var b strings.Builder
	fmt.Fprintf(&b, "PR #%d — %d unresolved\n\n", m.number, len(m.threads))
```

with the new init above (the `renderTUIStatus` already includes PR/title). Now remove the old `fmt.Fprintf(&b, "PR #%d — ...")` line.

Add the new helper at the bottom of `view.go`:

```go
// renderTUIStatus formats the PR status header for the TUI panel.
// Always uses glyph markers (TUI is interactive).
func renderTUIStatus(s gh.PullRequestStatus, number int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PR #%d — %s\n", number, s.Title)
	fmt.Fprintf(&b, "  Mergeable: %s\n", statusOrDash(s.MergeStateStatus))
	fmt.Fprintf(&b, "  Reviews:   %s\n", statusOrDash(s.ReviewDecision))
	fmt.Fprintf(&b, "  Checks:    %s\n", statusOrDash(s.ChecksState))
	fmt.Fprintf(&b, "  Threads:   %d unresolved\n", s.UnresolvedThreads)
	return b.String()
}

func statusOrDash(v string) string {
	if v == "" {
		return "—"
	}
	return v
}
```

Add `gh "github.com/svnbjrn/spoon/internal/github"` to the imports at the top of `view.go` (it's not yet imported there).

- [ ] **Step 6.2: Add a test that the status appears in the rendered view**

Append to `internal/tui/threads/model_test.go`:

```go
func TestViewIncludesStatusHeader(t *testing.T) {
	m := New(nil, "owner", "repo", 42, false)
	m.status = gh.PullRequestStatus{
		Title:            "Test",
		MergeStateStatus: "CLEAN",
		ReviewDecision:   "APPROVED",
		ChecksState:      "SUCCESS",
	}
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{Author: "alice", AuthorType: "User"}}}}
	m.loaded = true

	out := m.View()
	for _, sub := range []string{"PR #42", "Test", "CLEAN", "APPROVED", "SUCCESS"} {
		if !strings.Contains(out, sub) {
			t.Errorf("view missing %q\n---\n%s", sub, out)
		}
	}
}
```

Add `"strings"` to the imports at the top of `model_test.go` if not already imported.

- [ ] **Step 6.3: Run tests**

Run: `go test ./internal/tui/threads/ -v`
Expected: all tests green, including the new `TestViewIncludesStatusHeader`.

- [ ] **Step 6.4: Commit**

```bash
git add internal/tui/threads/view.go internal/tui/threads/model_test.go
git commit -m "Render PR status header at top of threads TUI"
```

---

## Task 7: README update

**Files:**
- Modify: `README.md`

- [ ] **Step 7.1: Add a paragraph below the PR-threads section**

Find the existing `### PR review threads` section in `README.md`. After the agent-loop pattern code block and before the next `##` heading, insert:

```markdown

Every invocation prints a status header above its normal output: PR
title, mergeability (CLEAN / DIRTY / BLOCKED / …), review decision
(APPROVED / REVIEW_REQUIRED / …), check rollup (SUCCESS / FAILURE /
…), and unresolved thread count. The header lands on stderr for
`--json` / `--next` so stdout stays pure JSON. Suppress with
`--no-status`.
```

- [ ] **Step 7.2: Verify the file renders**

Run: `head -110 README.md`
Expected: the new paragraph is present, no broken markdown.

- [ ] **Step 7.3: Commit**

```bash
git add README.md
git commit -m "Document PR status header in README"
```

---

## Task 8: Final verification

**Files:** none

- [ ] **Step 8.1: Full build / vet / test sweep**

```
go build ./...
go vet ./...
go test ./...
```
All must be green.

- [ ] **Step 8.2: Smoke matrix**

Build: `go build -o /tmp/spoon-test ./cmd/spoon`

Run each and confirm the expected behavior:

```
/tmp/spoon-test threads Raudbjorn/spoon#1 --json 2>/tmp/err >/tmp/out
# Expect: /tmp/err has 5-line status block; /tmp/out is valid JSON.
cat /tmp/out | jq . >/dev/null && echo "JSON valid"

/tmp/spoon-test threads Raudbjorn/spoon#1 --next 2>/tmp/err >/tmp/out
# Expect: /tmp/err has status block; /tmp/out is single thread JSON or "null".

/tmp/spoon-test threads Raudbjorn/spoon#1 --json --no-status 2>/tmp/err >/tmp/out
# Expect: /tmp/err is empty; /tmp/out is valid JSON.
[ ! -s /tmp/err ] && echo "no-status suppressed correctly"

/tmp/spoon-test threads Raudbjorn/spoon#1 --help
# Expect: --no-status flag listed in help.
```

- [ ] **Step 8.3: Push and report**

```bash
git push origin feat/spoon-threads
```

The push triggers the existing PR (#1) to update with the new commits.
