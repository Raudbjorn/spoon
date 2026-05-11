# gh-toolkit surface extensions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close three gaps from PR #7: add `spn threads list-prs` (programmatic PR discovery), add a TUI `[c]` counter-propose key, and split the using-spn skill into PR-review and fork-discovery skills with full PR-7 coverage.

**Architecture:** One new spn verb (`list-prs`) wrapping the existing `client.ListOpenPRs`; one new TUI keybinding launching `$EDITOR` via `tea.ExecProcess`; two skill files (one narrowed, one new) replacing the current single-skill doc. Each commit independently green.

**Tech Stack:** Go 1.26+, existing libs (`charmbracelet/bubbletea`, `cli/go-gh/v2`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-11-gh-toolkit-surface-extensions-design.md`
**Depends on:** PR #7 (`integrate-gh-toolkit`) merging to main first; this branch rebases onto main when #7 lands.

---

## File Structure

**Code changes:**

- `cmd/spn/threads.go` — add `listPRsFn` test seam, add `doThreadsListPRs` handler, add `case "list-prs"` to `runThreadsWith` dispatch.
- `cmd/spn/threads_test.go` — add `TestSpnThreadsListPRs_*` tests.
- `cmd/spn/main.go` — add `[--limit N]` and `[--state open]` to the `list-prs` line in `printHelp()`.
- `internal/tui/threads/keys.go` — add `Counter` keybinding (key `c`).
- `internal/tui/threads/model.go` — add `launchEditor` test seam, add counter-propose state, handle `c` keypress, post via `threadsops.Reply` after editor exits.
- `internal/tui/threads/view.go` — add `[c] counter-propose` to footer; update help text.
- `internal/tui/threads/model_test.go` — add counter-propose tests using a stubbed `launchEditor`.

**Skill changes (in-repo + personal copy):**

- `docs/superpowers/skills/using-spn/SKILL.md` — narrow frontmatter description; remove Embedding & Centrality section and CSV mode subsection; add five new sections covering PR-7 capabilities + the new list-prs and counter-propose; expand Quick Reference and Common Mistakes.
- `docs/superpowers/skills/using-spn-forks/SKILL.md` — **new file**; receives the extracted forks/embed/centrality content plus its own frontmatter and Common Mistakes/Quick Reference.
- `/home/svnbjrn/.claude/skills/using-spn/SKILL.md` — synced via `cp` (outside git).
- `/home/svnbjrn/.claude/skills/using-spn-forks/SKILL.md` — synced via `cp` (outside git, includes `mkdir -p`).

---

## Task 1: `spn threads list-prs <owner/repo>` verb

**Files:**
- Modify: `cmd/spn/threads.go`
- Modify: `cmd/spn/threads_test.go`
- Modify: `cmd/spn/main.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/spn/threads_test.go`:

```go
func TestSpnThreadsListPRs_emitsJSONArray(t *testing.T) {
	prev := listPRsFn
	defer func() { listPRsFn = prev }()
	listPRsFn = func(_ context.Context, _ *gh.Client, _, _ string, _ int) ([]gh.PullRequest, error) {
		return []gh.PullRequest{
			{Number: 42, Title: "Add X", Author: "alice", HeadBranch: "feat/x", State: "open", URL: "https://github.com/o/r/pull/42"},
			{Number: 43, Title: "Fix Y", Author: "bob", HeadBranch: "fix/y", State: "open", URL: "https://github.com/o/r/pull/43"},
		}, nil
	}
	prevAPI := apiFactory
	defer func() { apiFactory = prevAPI }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		// list-prs needs a client but no auth shaping beyond that
		client, _, _ := gh.CheckAuth()
		return client, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list-prs", "owner/repo"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON array: %v\n%s", err, stdout.String())
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 PRs, got %d", len(got))
	}
	if got[0]["number"].(float64) != 42 {
		t.Errorf("first number=%v", got[0]["number"])
	}
	if got[0]["author"] != "alice" {
		t.Errorf("first author=%v", got[0]["author"])
	}
}

func TestSpnThreadsListPRs_missingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list-prs"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d", exit)
	}
	var env map[string]map[string]any
	_ = json.Unmarshal(stderr.Bytes(), &env)
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v", env["error"]["code"])
	}
}

func TestSpnThreadsListPRs_limitTruncates(t *testing.T) {
	prev := listPRsFn
	defer func() { listPRsFn = prev }()
	var capturedLimit int
	listPRsFn = func(_ context.Context, _ *gh.Client, _, _ string, limit int) ([]gh.PullRequest, error) {
		capturedLimit = limit
		// Return only what the limit asks for (matches the real fn)
		out := make([]gh.PullRequest, 0, limit)
		for i := 0; i < limit; i++ {
			out = append(out, gh.PullRequest{Number: i, Title: "PR", State: "open"})
		}
		return out, nil
	}
	prevAPI := apiFactory
	defer func() { apiFactory = prevAPI }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		client, _, _ := gh.CheckAuth()
		return client, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list-prs", "o/r", "--limit", "3"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if capturedLimit != 3 {
		t.Errorf("limit forwarded as %d, want 3", capturedLimit)
	}
}
```

Note: the existing test file already imports `gh` (alias for `internal/github`) and `json`, `bytes`, `context`. If not, add them.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/spn/... -run TestSpnThreadsListPRs`
Expected: build error — `listPRsFn` undefined, `runThreadsWith` doesn't know `list-prs`.

- [ ] **Step 3: Add the `listPRsFn` test seam and `doThreadsListPRs` handler in `cmd/spn/threads.go`**

Near the other test seams (`apiFactory`), add:

```go
// listPRsFn fetches open PRs on a repo. Overridable for tests.
var listPRsFn = func(ctx context.Context, c *gh.Client, owner, repo string, limit int) ([]gh.PullRequest, error) {
	return c.ListOpenPRs(ctx, owner, repo, limit)
}
```

Add a new dispatch case to `runThreadsWith` (find the existing switch on `verb`):

```go
case "list-prs":
	return doThreadsListPRs(rest, stdout, stderr)
```

Append `doThreadsListPRs` to the file:

```go
func doThreadsListPRs(args []string, stdout, stderr io.Writer) int {
	var repo string
	limit := 0
	state := "open"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--limit requires a value", agentio.RemediationBadInput("threads", "list-prs")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--limit must be a positive integer", agentio.RemediationBadInput("threads", "list-prs")).Emit(stderr)
			}
			limit = n
		case "--state":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--state requires a value", agentio.RemediationBadInput("threads", "list-prs")).Emit(stderr)
			}
			i++
			state = args[i]
			if state != "open" {
				return agentio.NewError(agentio.CodeBadInput, "only --state=open is supported in this version", agentio.RemediationBadInput("threads", "list-prs")).Emit(stderr)
			}
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("threads", "list-prs")).Emit(stderr)
			}
			if repo != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("threads", "list-prs")).Emit(stderr)
			}
			repo = args[i]
		}
	}
	_ = state // reserved; only "open" accepted today
	if repo == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads list-prs <owner/repo>", agentio.RemediationBadInput("threads", "list-prs")).Emit(stderr)
	}
	owner, name := splitRepoArg(repo)
	if owner == "" || name == "" {
		return agentio.NewError(agentio.CodeBadInput, "invalid repo format: use owner/repo", agentio.RemediationBadInput("threads", "list-prs")).Emit(stderr)
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	client, ok := api.(*gh.Client)
	if !ok {
		return agentio.NewError(agentio.CodeInternal, "list-prs requires a *github.Client API", agentio.RemediationInternal()).Emit(stderr)
	}
	prs, err := listPRsFn(context.Background(), client, owner, name, limit)
	if err != nil {
		if op := rateLimitedAgentioError(err); op != nil {
			return op.Emit(stderr)
		}
		return agentio.NewError(agentio.CodeUpstream, err.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}
	if err := agentio.WriteJSON(stdout, prs); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
```

The `rateLimitedAgentioError` helper may or may not already exist in `cmd/spn/threads.go`. **Check first**: grep for `rateLimited` in `cmd/spn/threads.go`. If a helper exists with a different name (e.g., the rate-limit case is inline inside `translateOpErr`), inline the equivalent here:

```go
if err != nil {
	var rl *gh.RateLimitError
	if errors.As(err, &rl) {
		resetAt := rl.ResetAt.UTC().Format(time.RFC3339)
		secs := rl.RetryAfterSeconds()
		return agentio.NewError(agentio.CodeRateLimited, "rate limit exceeded",
			agentio.RemediationRateLimited(resetAt, secs)).
			WithRetryAfter(secs).
			WithDetails(map[string]any{"reset_at": resetAt, "retry_after_seconds": secs}).
			Emit(stderr)
	}
	return agentio.NewError(agentio.CodeUpstream, err.Error(), agentio.RemediationUpstream()).Emit(stderr)
}
```

Add imports if missing: `"errors"`, `"strconv"`, `"strings"`, `"time"`. The `splitRepoArg` helper is already in `cmd/spn/forks.go` (same package).

- [ ] **Step 4: Add `[--limit N]` and `[--state open]` to printHelp in `cmd/spn/main.go`**

Find the line in `printHelp()` that mentions threads verbs. After the `unresolve-all` line, add:

```
  threads list-prs <owner/repo> [--limit N] [--state open]
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./cmd/spn/... -run TestSpnThreadsListPRs -v`
Expected: all three tests PASS.

Run: `go test ./cmd/spn/...`
Expected: existing tests still pass.

Run: `go vet ./...` and `go build ./cmd/spn`.
Expected: clean.

- [ ] **Step 6: Smoke test offline**

```bash
go build -o /tmp/spn ./cmd/spn
/tmp/spn threads list-prs 2>&1 | head -10     # missing arg → bad_input on stderr
echo "exit=$?"                                  # should be 2
/tmp/spn threads list-prs --help               # currently no verb-level --help; check main help instead
/tmp/spn --help | grep list-prs                # verify help text mentions it
rm /tmp/spn
```

- [ ] **Step 7: Commit**

```bash
git add cmd/spn/threads.go cmd/spn/threads_test.go cmd/spn/main.go
git commit -m "Add spn threads list-prs verb

Emits a JSON array of open PRs on a repo, wrapping the existing
client.ListOpenPRs. Programmatic alternative to spoon's --interactive
TUI picker. Flags: --limit N (passthrough), --state open (placeholder
for future closed/all support — only open accepted today).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
"
```

---

## Task 2: TUI `[c]` counter-propose key

**Files:**
- Modify: `internal/tui/threads/keys.go`
- Modify: `internal/tui/threads/model.go`
- Modify: `internal/tui/threads/view.go`
- Modify: `internal/tui/threads/model_test.go`

- [ ] **Step 1: Read existing keybindings and footer rendering**

Read `internal/tui/threads/keys.go` to find the existing key definitions (look for `key.NewBinding`). Read `internal/tui/threads/view.go` to find the footer-rendering function (likely `footerView` or similar) and the help text.

Read `internal/tui/threads/model.go` to find:
- The Update function's keypress switch
- How other actions (like apply-suggestion via `a`) post their replies — what `tea.Cmd` they return, what message type signals success/failure

This gives you the pattern to mirror. Don't invent — follow what's already there.

- [ ] **Step 2: Write the failing test**

Append to `internal/tui/threads/model_test.go`:

```go
func TestCounterPropose_emptyEditorContent_cancels(t *testing.T) {
	m := newTestModelWithThreads(t)
	// Override the editor launcher to return empty body.
	m.launchEditor = func(_ context.Context, _, _ string) (string, error) {
		return "", nil
	}
	// Press 'c' on the currently-focused thread.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	// Counter-propose should not have triggered an API call.
	// The command returned may be nil or a status-set message; either way no
	// Reply call should have been recorded.
	if cmd == nil {
		// Some implementations return nil; fine.
	}
	if m.status == "" {
		t.Error("expected non-empty status (cancellation message)")
	}
	if !strings.Contains(strings.ToLower(m.status), "cancel") {
		t.Errorf("expected cancellation message, got %q", m.status)
	}
}

func TestCounterPropose_nonEmptyContent_posts(t *testing.T) {
	m := newTestModelWithThreads(t)
	posted := false
	// Stub the API's ReplyToThread so we can detect the call.
	if stub, ok := m.api.(*stubAPI); ok {
		stub.replyHook = func(threadID, body string) {
			posted = true
			if !strings.Contains(body, "```suggestion") {
				t.Errorf("body should be wrapped in suggestion fence, got %q", body)
			}
		}
	}
	m.launchEditor = func(_ context.Context, _, _ string) (string, error) {
		return "newCode()", nil
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if cmd != nil {
		_ = cmd() // execute the returned Cmd to drive the post
	}
	if !posted {
		t.Error("expected ReplyToThread to be called with the wrapped suggestion")
	}
}
```

Note: `newTestModelWithThreads`, `stubAPI`, and `replyHook` may or may not exist. **Read the existing test file** for the actual test fixtures and adapt. The above is the shape; the actual symbols come from what's already there.

If the existing test file uses a different fixture pattern (e.g., `tea.NewProgram` driven through tea.Cmd messages), match that pattern instead of inventing.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/tui/threads/... -run TestCounterPropose`
Expected: build error — `launchEditor` field doesn't exist on Model.

- [ ] **Step 4: Add the `Counter` keybinding in `internal/tui/threads/keys.go`**

Add to the existing keybindings struct (or wherever the bindings are declared):

```go
Counter = key.NewBinding(
	key.WithKeys("c"),
	key.WithHelp("c", "counter-propose"),
)
```

If keybindings are in a struct, add `Counter key.Binding` to the struct and initialize it in the constructor function.

- [ ] **Step 5: Add the `launchEditor` field, counter-propose state, and `c` handling in `internal/tui/threads/model.go`**

Add a `launchEditor` field to the Model struct, near the other test seams:

```go
// launchEditor is overridable for tests. Production launches $EDITOR via
// tea.ExecProcess and waits for the editor to exit.
launchEditor func(ctx context.Context, threadID, prRef string) (string, error)
```

In the constructor (e.g., `New(...)` or wherever Model is initialized), set:

```go
launchEditor: defaultEditorLauncher,
```

Add `defaultEditorLauncher` somewhere in the file (or in a sibling file `internal/tui/threads/editor.go` if you prefer):

```go
// defaultEditorLauncher writes a temp file with a 3-line comment header,
// launches $EDITOR (or $VISUAL or vi) via tea.ExecProcess semantics, and
// returns the stripped body. Lines beginning with '#' are stripped.
// Returns the empty string when content is empty (cancelled).
func defaultEditorLauncher(ctx context.Context, threadID, prRef string) (string, error) {
	tmpPattern := "spoon-counter-propose-*.md"
	f, err := os.CreateTemp("", tmpPattern)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := f.Name()
	header := fmt.Sprintf("# Counter-propose for thread %s on %s\n# Lines beginning with # are stripped.\n# Empty content cancels.\n", threadID, prRef)
	if _, err := f.WriteString(header); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write header: %w", err)
	}
	f.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.CommandContext(ctx, editor, tmpPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("editor: %w", err)
	}

	body, err := os.ReadFile(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("read temp file: %w", err)
	}
	// Strip # comment lines and trim trailing whitespace.
	lines := strings.Split(string(body), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		out = append(out, line)
	}
	stripped := strings.TrimRight(strings.Join(out, "\n"), " \t\n")
	// Preserve the temp file on error paths handled by the caller. On
	// success/cancel, delete it.
	os.Remove(tmpPath)
	return stripped, nil
}
```

Imports: `"context"`, `"fmt"`, `"os"`, `"os/exec"`, `"strings"`.

Add `c` handling in the Update function's keypress switch. Look at the existing case for `a` (apply-suggestion) and mirror it:

```go
case key.Matches(msg, keys.Counter):
	thread := m.currentThread()
	if thread == nil {
		return m, nil
	}
	prRef := fmt.Sprintf("%s/%s#%d", m.owner, m.repo, m.prNumber)
	// Launch the editor synchronously (it suspends the TUI via the underlying
	// terminal). For Bubble Tea integration, we return a tea.ExecProcess Cmd
	// in production. For tests, the launchEditor is stubbed.
	return m, func() tea.Msg {
		body, err := m.launchEditor(context.Background(), thread.ID, prRef)
		if err != nil {
			return counterProposeResultMsg{err: err, threadID: thread.ID}
		}
		if body == "" {
			return counterProposeResultMsg{cancelled: true, threadID: thread.ID}
		}
		// Wrap and post.
		wrapped := threadsops.WrapSuggestionBody(body, "")
		comment, opErr := threadsops.Reply(context.Background(), m.api, thread.ID, wrapped)
		if opErr != nil {
			return counterProposeResultMsg{err: errors.New(opErr.Message), threadID: thread.ID}
		}
		return counterProposeResultMsg{commentID: comment.ID, threadID: thread.ID}
	}
```

Add a new message type:

```go
type counterProposeResultMsg struct {
	threadID  string
	commentID string
	cancelled bool
	err       error
}
```

Add a case to the Update function's switch on message types:

```go
case counterProposeResultMsg:
	switch {
	case msg.cancelled:
		m.status = "counter-propose cancelled"
	case msg.err != nil:
		m.status = "counter-propose failed: " + msg.err.Error()
	default:
		m.status = "counter-propose posted (comment " + msg.commentID + ")"
	}
	// Refresh suggestions for the affected thread, if the model has such a path.
	return m, nil
```

If `m.currentThread()`, `m.status`, `m.api`, or `m.owner/repo/prNumber` have different names in the actual Model struct, read the model.go file first and adapt. Don't invent fields.

- [ ] **Step 6: Add `[c] counter-propose` to the footer in `internal/tui/threads/view.go`**

Find the footer-rendering function. Add `[c] counter-propose` next to `[a] apply-suggestion`. The footer should always show `[c]` (matches the recent convention from the prior fix that always shows `[a]`).

Update the help text (likely a constant or string-returning function) to add one line under the existing `[a]` help:

```
c - Counter-propose: opens $EDITOR for a replacement, wraps the
    content in a suggestion block, and posts it as a reply.
```

- [ ] **Step 7: Run tests, verify pass**

Run: `go test ./internal/tui/threads/... -run TestCounterPropose`
Expected: both new tests PASS.

Run: `go test ./internal/tui/...`
Expected: existing TUI tests still pass.

Run: `go test -race ./internal/tui/...`
Expected: no races.

Run: `go vet ./...` and `go build ./cmd/spoon`.
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/threads/
git commit -m "Add TUI [c] counter-propose key

Launches \$EDITOR (or \$VISUAL or vi) with a temp file pre-populated with
a 3-line comment header. After the editor exits, lines starting with '#'
are stripped and the body is wrapped via threadsops.WrapSuggestionBody
before being posted as a reply via threadsops.Reply. Empty content
cancels with a status message; errors surface on the status line.

Closes the TUI parity gap noted in the gh-toolkit surface extensions
spec: WrapSuggestionBody existed and was used by spn threads reply
--suggest, but the TUI had no interactive counter-propose path.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
"
```

---

## Task 3: Split the using-spn skill (extract forks content)

This task creates the new `using-spn-forks` skill by extracting the forks-related sections from the current `using-spn`. After this task, the existing `using-spn` skill has lost its forks/embed/centrality content but hasn't yet gained the PR-7 capabilities (that comes in Task 4).

**Files:**
- Create: `docs/superpowers/skills/using-spn-forks/SKILL.md`
- Modify: `docs/superpowers/skills/using-spn/SKILL.md`
- Out-of-git: `/home/svnbjrn/.claude/skills/using-spn-forks/SKILL.md`
- Out-of-git: `/home/svnbjrn/.claude/skills/using-spn/SKILL.md`

- [ ] **Step 1: Read the current using-spn structure**

```bash
grep -n '^## ' docs/superpowers/skills/using-spn/SKILL.md
wc -w docs/superpowers/skills/using-spn/SKILL.md
```

Note the line numbers of the **Embedding & Centrality** section and the **CSV mode** subsection (inside Output Contract).

- [ ] **Step 2: Create the new `using-spn-forks` skill file**

```bash
mkdir -p docs/superpowers/skills/using-spn-forks
```

Create `docs/superpowers/skills/using-spn-forks/SKILL.md` with the following content:

```markdown
---
name: using-spn-forks
description: Use when discovering or scoring forks of a repository, comparing fork novelty, clustering forks with the Ollama embedder, managing the embedding model (probe / pull / list), or fetching directory centrality data for a repo. Applies when the `spn` CLI is on PATH (`command -v spn`). Output is NDJSON streaming by default; pass `--csv` for batched tabular output.
---

# Using `spn` for Fork Discovery and Clustering

`spn` enumerates and scores forks of a GitHub or GitLab repository. Per-fork JSON includes a "heat" score (additive 0–100 across T1/T2/T3 signals), cluster label when clustering ran, and optional T2/T3 enrichment.

## When to Use

- Discovering interesting forks of an upstream repo
- Bulk-scoring forks for triage
- Clustering forks by novelty (requires a reachable Ollama embedder)
- Per-directory centrality for a repo (which directories drive activity)

Do NOT use for: PR review work (see the `using-spn` skill for that) or non-fork repository analysis.

**First check:** `command -v spn`. If absent, fall back to hand-rolled `gh api` calls; otherwise prefer spn for streamed, scored output.

## Core Loop

```bash
spn forks list owner/repo | jq -c '. | select(.heat > 60)'
```

Streaming NDJSON: one fork per line, ordered by heat-score descending after sorting completes.

## CSV Mode

```bash
spn forks list owner/repo --csv > forks.csv
```

Collects all enriched forks and emits a single CSV blob on stdout with a fixed header (18 columns covering identity, T1, T2, T3, and cluster fields). Per-fork enrichment errors still go to stderr as compact JSON. Use this when downstream tooling expects tabular data; use the default NDJSON when streaming or jq pipelines fit better.

## Output Contract (shared with the PR-review skill)

| Case | Where | Shape |
| --- | --- | --- |
| Success — NDJSON | stdout | one JSON object per line |
| Success — CSV | stdout | header + rows |
| Failure | stderr | `{"error": {...}}` envelope |

Stdout is exclusively success data. Per-fork enrichment errors go to stderr (NDJSON-shaped); fatal errors go to stderr (full envelope) and exit non-zero.

**Exit codes:** 0 success; 2 user/policy error (do not retry); 1 transient/upstream (check `error.retryable`).

## Rate Limits

GitHub rate-limit hits produce a `rate_limited` envelope with `retry_after_seconds`. See the `using-spn` skill's Rate Limits section for the full envelope shape and retry pattern — identical here.

Detection works on REST API paths. GitHub's GraphQL endpoint (used by the forks-list GraphQL fast path) returns rate-limit hits as the generic `upstream_error` code instead.

## Embedding pipeline (preflight)

`spn forks list` enriches forks with cluster labels when an Ollama embedder is reachable. Three sibling verbs let an agent set that up or query the underlying data:

```sh
spn embed status                   # JSON: running, endpoint, installed[], recommended[]
spn embed models                   # JSON: known-good models (name, dim, size, codeAware)
spn embed pull <model>             # NDJSON progress: one {model,phase,pct} per line until phase=="done"
spn repo centrality owner/repo     # JSON: per-directory centrality + top-K core dirs
```

Preflight pattern before clustering:

```bash
status=$(spn embed status)
running=$(jq -r .running <<<"$status")
if [ "$running" != "true" ]; then
  echo "Ollama not reachable; clustering will be skipped" >&2
fi
installed=$(jq -r '.installed | join(",")' <<<"$status")
if ! grep -q nomic <<<"$installed"; then
  spn embed pull nomic-embed-text | jq -c .   # streaming progress
fi
```

## Common Mistakes

| Mistake | What to do instead |
| --- | --- |
| Treating `forks list` as a one-shot batched command by default | NDJSON streaming is the default; pipe through `jq -c` to consume incrementally. Use `--csv` only when the consumer expects tabular data. |
| Running `forks list` without checking the embedder | If clustering is critical, run `spn embed status` first and `spn embed pull` if a recommended model isn't installed. |
| Confusing `repo centrality` and fork centrality | `spn repo centrality <repo>` returns the upstream's directory centrality, not per-fork. There's no per-fork centrality verb yet. |

## Quick Reference

| Verb | Use |
| --- | --- |
| `spn forks list <repo>` | NDJSON stream of enriched forks |
| `spn forks list <repo> --csv` | Batched CSV with fixed 18-column header |
| `spn embed status` | Probe Ollama; report running state, installed models, recommended models |
| `spn embed pull <model>` | Pull a model with streaming progress NDJSON |
| `spn embed models` | List known-good embedding models |
| `spn repo centrality <owner/repo>` | Per-directory centrality JSON for the upstream repo |
```

- [ ] **Step 3: Remove the migrated content from `using-spn/SKILL.md`**

In `docs/superpowers/skills/using-spn/SKILL.md`:

1. **Delete the entire "Embedding & Centrality" section** (the `## Embedding & Centrality` heading through the next `## ` heading exclusive).
2. **Delete the "CSV mode" subsection inside Output Contract** (the `### CSV mode` heading through the next subsection or top-level heading exclusive).
3. **Remove forks-related rows from the Quick Reference table**:
   - The `spn forks list <repo>` row
   - The `spn forks list <repo> --csv` row (if present)
   - The `spn embed status | pull | models` row(s)
   - The `spn repo centrality <repo>` row
   These rows move to the new skill; the existing using-spn no longer needs them.
4. **Remove forks-related rows from the Common Mistakes table** if any exist.

Update the frontmatter description to narrow the scope (this prep is just removal; the new description goes in Task 4). For now leave the frontmatter intact — Task 4 rewrites it as part of the broader using-spn update.

- [ ] **Step 4: Verify both skills render**

```bash
head -3 docs/superpowers/skills/using-spn/SKILL.md   # frontmatter intact
head -3 docs/superpowers/skills/using-spn-forks/SKILL.md
grep -c '^## ' docs/superpowers/skills/using-spn/SKILL.md         # smaller than before
grep -c '^## ' docs/superpowers/skills/using-spn-forks/SKILL.md   # 8 sections
wc -w docs/superpowers/skills/using-spn-forks/SKILL.md            # ~700 words
```

- [ ] **Step 5: Sync personal copies**

```bash
mkdir -p ~/.claude/skills/using-spn-forks
cp docs/superpowers/skills/using-spn-forks/SKILL.md ~/.claude/skills/using-spn-forks/SKILL.md
cp docs/superpowers/skills/using-spn/SKILL.md ~/.claude/skills/using-spn/SKILL.md
```

This step is outside the git commit boundary; mention it in the commit message.

- [ ] **Step 6: Commit**

```bash
git add docs/superpowers/skills/
git commit -m "Split using-spn skill: extract forks content to using-spn-forks

Migrates the Embedding & Centrality section, the CSV mode subsection,
and the forks/embed/centrality Quick Reference and Common Mistakes
rows out of using-spn and into a new using-spn-forks skill at
docs/superpowers/skills/using-spn-forks/SKILL.md.

The narrowed using-spn keeps its frontmatter description for now;
Task 4 rewrites it and adds the PR-7 capabilities. Personal copies
at ~/.claude/skills/{using-spn,using-spn-forks}/SKILL.md are synced
manually via cp (outside the git commit since they're not under
version control).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
"
```

---

## Task 4: Update the narrowed `using-spn` skill with PR-7 capabilities

**Files:**
- Modify: `docs/superpowers/skills/using-spn/SKILL.md`
- Out-of-git: `/home/svnbjrn/.claude/skills/using-spn/SKILL.md`

- [ ] **Step 1: Rewrite the frontmatter description**

In `docs/superpowers/skills/using-spn/SKILL.md`, replace the entire `description:` line in the YAML frontmatter with:

```yaml
description: Use when addressing PR review threads on a GitHub PR — replying to review comments, resolving threads after fixes, looping through reviewer feedback, applying or counter-proposing suggestion blocks, sweeping outdated threads, checking PR mergeability, or discovering which PRs are open on a repository. Applies when the `spn` CLI is on PATH (`command -v spn`). Prefer spn over hand-rolled `gh api graphql` for review-thread work.
```

(Single line — YAML frontmatter; even if your editor wraps the display, keep it as a single line in the file. Verify the file still has exactly three `---` delimiters: opening, closing, and no extras.)

- [ ] **Step 2: Add the "Filter modes" section**

Insert this new section AFTER the existing **Output Contract** section and BEFORE the existing **Body-Required Policy** section:

```markdown
## Filter modes

`spn threads list <pr> --filter <mode>` selects which threads to surface.

| Mode | Includes |
| --- | --- |
| `all` | every thread, resolved + unresolved, active + outdated |
| `unresolved` (default) | every unresolved thread |
| `current-unresolved` | unresolved AND not outdated — the most urgent set |
| `resolved-active` | resolved threads whose anchored code is still active |
| `unresolved-outdated` | unresolved threads whose anchored code has shifted (sweep candidates) |

Rule of thumb: `unresolved` (default) for active review work; `current-unresolved` to exclude stale threads; `unresolved-outdated` to find sweep candidates before bulk-resolving.
```

- [ ] **Step 3: Add the "Stale-thread sweep" section**

Insert AFTER **Body-Required Policy** and BEFORE **Partial-Failure Dedup**:

````markdown
## Stale-thread sweep

`spn threads resolve-all <pr> --outdated` resolves only threads where `isOutdated: true`. Combined with the body-required policy:

- Bot threads with outdated anchors → resolved into `succeeded`
- Bot threads with active anchors → `skipped` with `reason: "not_outdated"`
- Human-raised threads (any state) → `skipped` with `reason: "requires_body"`

Two-step pattern: preview, then act.

```bash
spn threads list "$PR" --filter unresolved-outdated  # what would be swept
spn threads resolve-all "$PR" --outdated              # do it
```

The thread JSON has an `isOutdated` boolean. An outdated thread anchors to code that has since shifted; the comment may be moot. Agents reviewing a thread should check `isOutdated` before deciding whether a fix is still relevant.

### `BulkSkip.Reason` values

| Reason | Meaning |
| --- | --- |
| `requires_body` | Bulk-resolve refused because the thread has a human commenter — resolve individually with `--body` |
| `not_outdated` | Bulk-resolve with `--outdated` refused because the anchor is still active |
````

- [ ] **Step 4: Add the "Code context and verbose mode" section**

Insert AFTER **Stale-thread sweep** and BEFORE **Partial-Failure Dedup**:

````markdown
## Code context and verbose mode

Two opt-in flags add detail to the thread JSON without changing the default shape.

`--show-code N` adds a `codeContext` block per thread, containing N lines on either side of the anchor:

```json
{
  "id": "PRRT_...",
  "path": "internal/foo.go",
  "line": 42,
  "codeContext": {
    "ref": "deadbeef",
    "startLine": 36,
    "endLine": 48,
    "lines": ["...", "...", "..."]
  }
}
```

Cost: one extra `FetchFileContent` call per unique `(path, ref)` pair — the caching wrapper collapses duplicates across threads anchored to the same file.

`--verbose` adds `createdAt`, `updatedAt`, and `authorUrl` to each thread. Useful when grounding a reply in commit history or building per-author dashboards.

Combined call for a grounded review loop:

```bash
spn threads next "$PR" --show-code 6 --verbose
```

Returns enough context to write a referenced reply without a separate `gh api` call.
````

- [ ] **Step 5: Add the "Suggestions" section**

Insert AFTER **Code context and verbose mode** and BEFORE **Partial-Failure Dedup**:

````markdown
## Suggestions

GitHub review threads can embed `suggestion` fenced blocks (```suggestion … ```) that propose replacement code. spn parses them into a per-thread `suggestions` array:

```json
{
  "suggestions": [
    {"body": "newCode()", "index": 0, "commentId": "PRC_..."}
  ]
}
```

### Applying a suggestion locally

```bash
spn threads apply-suggestion "$PR" "$id"
```

Writes the replacement to the local file at the thread's anchor. Flags:

| Flag | Effect |
| --- | --- |
| `--suggestion-index N` | Pick the N-th suggestion when the thread has multiple (default 0) |
| `--dry-run` | Print what would change as JSON; do not write the file |
| `--force` | Apply even when the target file has uncommitted changes (default refuses) |
| `--repo-root PATH` | Anchor for relative path resolution (default `pwd`) |

The verb refuses if the path traverses outside the repo root.

### Counter-proposing

`spn threads reply --suggest BODY` (or `--suggest-file PATH`) wraps the body in a suggestion fenced block before posting:

```bash
spn threads reply "$PR" "$id" \
  --intro "Narrower scope here:" \
  --suggest "return ctx.Err()"
```

`--intro TEXT` prefixes a leading line of prose before the suggestion block.

### TUI parity

When working interactively in the spoon TUI, press `[c]` to open `$EDITOR` for a counter-proposal. The wrapper applies `WrapSuggestionBody` and posts via the same path.

### Spoon flag parallel

`spoon --apply-suggestion <id>` (flag) calls into the same `threadsops.ApplySuggestion` that `spn threads apply-suggestion` (verb) uses. The flag/verb difference is the human/agent bifurcation; both produce identical results.
````

- [ ] **Step 6: Add the "Dry-run previews" section**

Insert AFTER **Suggestions** and BEFORE **Partial-Failure Dedup**:

````markdown
## Dry-run previews

`--dry-run` is available on `resolve`, `resolve-all`, `unresolve-all`, and `apply-suggestion`. Output shape matches the non-dry-run path with an added `dryRun: true` field:

```json
{"succeeded": ["PRRT_..."], "failed": [], "skipped": [], "dryRun": true}
```

Gating pattern:

```bash
preview=$(spn threads resolve-all "$PR" --outdated --dry-run)
count=$(jq -r '.succeeded | length' <<<"$preview")
if [ "$count" -gt 0 ]; then
  spn threads resolve-all "$PR" --outdated
fi
```
````

- [ ] **Step 7: Add new rows to the Quick Reference table**

Find the existing **Quick Reference** table near the bottom of the file. Append these five rows:

```markdown
| `spn threads list-prs <owner/repo>` | JSON array of open PRs on a repo. Programmatic alternative to spoon's --interactive picker. |
| `spn threads list <pr> --filter MODE` | Filter modes: all, unresolved, current-unresolved, resolved-active, unresolved-outdated. |
| `spn threads apply-suggestion <pr> <id>` | Apply a thread's suggestion block to the local file. Use --dry-run first. |
| `spn threads reply <pr> <id> --suggest T` | Reply that wraps T in a suggestion fenced block. Optional --intro for context. |
| `spn threads * --dry-run` / `--show-code N` / `--verbose` | Cross-cutting modifiers for mutations / context-aware reads / extra metadata. |
```

- [ ] **Step 8: Add new rows to the Common Mistakes table**

Find the existing **Common Mistakes** table. Append these three rows:

```markdown
| Resolving outdated threads individually | Use `spn threads resolve-all <pr> --outdated` to sweep them in one call. Preview with `--dry-run` first. |
| Ignoring `isOutdated` when deciding to reply | Outdated threads anchor to code that has since shifted; the comment may no longer be relevant. Check the field. |
| Calling `gh pr list` then `spn` | Use `spn threads list-prs <owner/repo>` for one round trip in the agent's preferred JSON shape. |
```

- [ ] **Step 9: Light-touch updates to existing sections**

In the existing **Core Loop** section, add a one-line note after the existing example:

```markdown
For richer per-iteration context, add `--show-code N` and `--verbose` to the `spn threads next` call (see Code context and verbose mode).
```

In the existing **Output Contract** section (just after the table), add a bullet list:

```markdown
Threads may carry these optional fields when the corresponding flag is set:
- `isOutdated` (always present after PR #7) — true when the anchored code has shifted
- `suggestions` — non-empty when the thread contains `suggestion` fenced blocks
- `codeContext` — populated by `--show-code N`
- `createdAt`, `updatedAt`, `authorUrl` — populated by `--verbose`
```

In the existing **PR Ref Forms** section, add a parenthetical:

```markdown
(`list-prs` takes `<owner/repo>` without the `#N` suffix, since the whole point is *discovering* PR numbers.)
```

- [ ] **Step 10: Verify the file renders**

```bash
head -3 docs/superpowers/skills/using-spn/SKILL.md   # frontmatter intact, 3 --- delimiters total
grep -c '^---$' docs/superpowers/skills/using-spn/SKILL.md   # should be 2
wc -w docs/superpowers/skills/using-spn/SKILL.md             # ~1800-2000 words
grep -c '^## ' docs/superpowers/skills/using-spn/SKILL.md    # 12-13 sections
```

If the word count exceeds 2200, the skill is getting unwieldy — flag it but proceed; tightening can be a follow-up.

- [ ] **Step 11: Sync the personal copy**

```bash
cp docs/superpowers/skills/using-spn/SKILL.md ~/.claude/skills/using-spn/SKILL.md
```

Outside the git commit boundary; mention in the commit message.

- [ ] **Step 12: Commit**

```bash
git add docs/superpowers/skills/using-spn/SKILL.md
git commit -m "Add PR-7 capabilities to using-spn skill

Documents the eight new capabilities shipped on PR #7 plus the new
spn threads list-prs verb (Task 1) and TUI [c] counter-propose key
(Task 2):

- Filter modes (5 values; default unchanged)
- Stale-thread sweep with --outdated; BulkSkip.Reason values
- Code context (--show-code N) and verbose mode (--verbose)
- Suggestions: apply locally, counter-propose, TUI key, spoon flag
  parallel
- Dry-run previews
- New Quick Reference and Common Mistakes rows
- Frontmatter description rewritten to trigger on the broader task
  set (now includes PR discovery and suggestion workflows)

Personal copy at ~/.claude/skills/using-spn/SKILL.md is synced
manually via cp.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
"
```

---

## Task 5: Final verification pass

**Files:** none modified; this task verifies the cumulative state.

- [ ] **Step 1: Run the full test suite**

```bash
go test ./...
```

Expected: all packages pass.

- [ ] **Step 2: Race detector**

```bash
go test -race ./...
```

Expected: clean.

- [ ] **Step 3: vet**

```bash
go vet ./...
```

Expected: clean.

- [ ] **Step 4: build both binaries**

```bash
go build ./cmd/spoon ./cmd/spn
```

Expected: clean.

- [ ] **Step 5: Smoke-test the new verb against a real public repo**

```bash
go build -o /tmp/spn ./cmd/spn
/tmp/spn threads list-prs golang/go --limit 5 | jq '. | length'
```

Expected: 5 (or fewer if the repo has fewer than 5 open PRs at the moment). The JSON should include `number`, `title`, `author`, `headBranch`, `state`, `url`, `createdAt`, `updatedAt`.

```bash
/tmp/spn threads list-prs nonexistent/nonexistent 2>&1 | head -5
```

Expected: structured `{"error": {...}}` envelope with `code: "not_found"` (or `code: "upstream_error"`, depending on how the github client surfaces the 404).

```bash
rm /tmp/spn
```

- [ ] **Step 6: Verify both skill files render on GitHub**

Manual check — open both `docs/superpowers/skills/using-spn/SKILL.md` and `docs/superpowers/skills/using-spn-forks/SKILL.md` in the GitHub web UI (after pushing the branch — see Task 5 Step 8) and confirm:

- Frontmatter renders as a YAML block (or is hidden by GitHub's markdown processor — either is fine; it's not rendered to humans)
- All `##` headings appear with correct hierarchy
- All ` ``` ` code fences are balanced (no stray backticks rendering as inline code)
- Tables render with proper columns
- No leftover conflict markers, no `<<<<<<<`, no `> ` quote prefixes around content that shouldn't be quoted

If anything is off, fix it inline and amend the corresponding commit.

- [ ] **Step 7: Verify the personal-copy sync**

```bash
diff docs/superpowers/skills/using-spn/SKILL.md ~/.claude/skills/using-spn/SKILL.md
diff docs/superpowers/skills/using-spn-forks/SKILL.md ~/.claude/skills/using-spn-forks/SKILL.md
```

Expected: both diffs empty (the personal copies match the repo copies).

- [ ] **Step 8: Push the branch**

```bash
git push -u origin gh-toolkit-cli-gaps
```

- [ ] **Step 9: Open the PR**

```bash
gh pr create --title "gh-toolkit surface extensions: list-prs verb, TUI counter-propose, skill split" --body "$(cat <<'EOF'
## Summary

Closes three gaps left by PR #7:

1. **`spn threads list-prs <owner/repo>`** — JSON array of open PRs on a repo, wrapping the existing `client.ListOpenPRs`. Programmatic alternative to spoon's `--interactive` picker.
2. **TUI `[c]` counter-propose key** — opens `\$EDITOR`, wraps the result via `WrapSuggestionBody`, posts via `threadsops.Reply`. Closes the TUI parity gap with `spn threads reply --suggest`.
3. **Skill split** — the existing `using-spn` skill is narrowed to PR review and gains all eight PR-7 capabilities (filter modes, --outdated, --dry-run, --show-code, --verbose, suggestions, isOutdated, list-prs). A new `using-spn-forks` skill receives the migrated forks/embed/centrality content. Each skill has a tighter frontmatter description for better triggering.

Spec: \`docs/superpowers/specs/2026-05-11-gh-toolkit-surface-extensions-design.md\`

## Depends on

- PR #7 (\`integrate-gh-toolkit\`) merging to main. This branch was based off #7's HEAD and rebases onto main once #7 lands.

## Test plan

- [x] \`go test ./...\` — pass
- [x] \`go test -race ./...\` — pass
- [x] \`go vet ./...\` — clean
- [x] \`go build ./cmd/spoon ./cmd/spn\` — clean
- [x] Smoke: \`spn threads list-prs golang/go --limit 5\` returns 5 PRs as JSON
- [x] Smoke: \`spn threads list-prs nonexistent/nonexistent\` returns a structured error envelope
- [x] Manual: both skill files render correctly on GitHub
- [x] Manual: personal-copy sync (\`~/.claude/skills/{using-spn,using-spn-forks}/SKILL.md\`) matches repo copies
- [ ] Manual: TUI counter-propose with a real PR (requires a live editor session)

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

**Spec coverage check:**
- Goal #1 (`spn threads list-prs`): Task 1 ✓
- Goal #2 (TUI `[c]` counter-propose): Task 2 ✓
- Goal #3 (skill split): Tasks 3 + 4 ✓
- Non-goal "no apply-suggestion reconciliation": preserved (no code change to either flag or verb)
- Non-goal "no rename of using-spn": preserved (path stays at `using-spn`)
- Architecture diagram (cmd/spn/threads.go + internal/tui/threads/{model,view,keys}.go + skill files): all touched in Tasks 1-4
- Frontmatter description rewrites: Task 4 Step 1 (using-spn) and Task 3 Step 2 (using-spn-forks)
- Personal-copy sync: each skill task has explicit `cp` step
- Open questions (`--state` placeholder, editor pre-fill, eventual `spn pr list` rename): listed in the spec's Open questions section; no task implements them this PR

**Placeholder scan:** no "TBD", "TODO", "implement later", "Similar to Task N", or unfinished code blocks. Every step has concrete code or commands.

**Type consistency:**
- `listPRsFn` (Task 1) — same name across test seam, handler, and test.
- `launchEditor` (Task 2) — same name across Model field, default implementation, and test stub.
- `counterProposeResultMsg` (Task 2) — single definition; used in the Cmd and the case handler.
- `PullRequest` (Task 1) — uses the actual struct's JSON tags (`number`, `title`, `author`, `headBranch`, `state`, `url`, `createdAt`, `updatedAt`).

**Known assumptions worth verifying during implementation:**
- The `rateLimitedAgentioError` helper may not exist in `cmd/spn/threads.go` by that name; Task 1 Step 3 provides both call patterns to handle either case.
- The TUI test fixtures (`newTestModelWithThreads`, `stubAPI`, `replyHook`) are placeholders matching the patterns in existing tests; the implementer should read `internal/tui/threads/model_test.go` first and adapt to whatever fixtures actually exist.
- `m.currentThread()`, `m.status`, `m.api`, `m.owner/repo/prNumber` field names assume the existing Model struct — read it first; adapt if names differ.

If any of these turn out wrong during implementation, the affected steps already direct the implementer to read the actual file before pasting.
