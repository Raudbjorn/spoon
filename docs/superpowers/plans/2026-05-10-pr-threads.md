# `spoon threads` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the `spoon threads` subcommand per `docs/superpowers/specs/2026-05-10-pr-threads-design.md` — list/reply/resolve GitHub PR review threads from CLI (JSON for agents, TUI for humans), with bulk `--resolve-all`/`--unresolve-all` and `--next` for agent loops.

**Architecture:** New package `internal/github/threads` for GraphQL queries and mutations (reusing the existing `Client.gql` field). New CLI dispatcher `cmd/spoon/threads.go` for flag parsing and JSON output. New bubbletea model `internal/tui/threads/` for the interactive view. PR ref parsing leverages the existing `forge.ParseRepoURL`.

**Tech Stack:** Go 1.26, `github.com/cli/go-gh/v2` (GraphQL + auth), `github.com/charmbracelet/bubbletea` (TUI), standard `testing` package.

---

## File Structure

| File | Purpose |
| --- | --- |
| `internal/github/threads.go` | GraphQL query/mutation wrappers: list, reply, resolve, unresolve, bulk |
| `internal/github/threads_test.go` | Parser + policy unit tests with fixture JSON |
| `internal/github/testdata/threads_*.json` | GraphQL response fixtures |
| `cmd/spoon/threads.go` | Subcommand entry point, flag parser, PR ref parser, JSON output |
| `cmd/spoon/threads_test.go` | Flag/PR-ref parsing tests |
| `cmd/spoon/main.go` | Modified: dispatch on `argv[1] == "threads"` |
| `internal/tui/threads/model.go` | bubbletea model (Init/Update/View) |
| `internal/tui/threads/view.go` | Rendering helpers |
| `internal/tui/threads/keys.go` | Keymap |
| `internal/tui/threads/model_test.go` | Update logic tests |
| `README.md` | Modified: add "PR review threads" section |

---

## Task 1: GraphQL types and list query for review threads

**Files:**
- Create: `internal/github/threads.go`
- Create: `internal/github/threads_test.go`
- Create: `internal/github/testdata/threads_list_basic.json`

- [ ] **Step 1.1: Create the fixture JSON**

Create `internal/github/testdata/threads_list_basic.json`:

```json
{
  "data": {
    "repository": {
      "pullRequest": {
        "title": "Add feature X",
        "reviewThreads": {
          "pageInfo": { "hasNextPage": false, "endCursor": null },
          "nodes": [
            {
              "id": "PRRT_1",
              "isResolved": false,
              "path": "internal/heat/score.go",
              "line": 42,
              "startLine": null,
              "diffSide": "RIGHT",
              "comments": {
                "nodes": [
                  {
                    "id": "PRRC_1",
                    "body": "this can panic",
                    "createdAt": "2026-05-10T09:01:23Z",
                    "author": { "__typename": "User", "login": "alice" }
                  }
                ]
              }
            },
            {
              "id": "PRRT_2",
              "isResolved": true,
              "path": "go.mod",
              "line": 1,
              "startLine": null,
              "diffSide": "RIGHT",
              "comments": {
                "nodes": [
                  {
                    "id": "PRRC_2",
                    "body": "bump dep",
                    "createdAt": "2026-05-10T08:30:00Z",
                    "author": { "__typename": "Bot", "login": "dependabot" }
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

- [ ] **Step 1.2: Write the failing test for response parsing**

Create `internal/github/threads_test.go`:

```go
package github

import (
	"encoding/json"
	"os"
	"testing"
)

func TestParseListThreadsResponse(t *testing.T) {
	data, err := os.ReadFile("testdata/threads_list_basic.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var raw struct {
		Data listThreadsData `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	threads := parseListThreadsResponse(raw.Data)
	if len(threads) != 2 {
		t.Fatalf("want 2 threads, got %d", len(threads))
	}
	if threads[0].ID != "PRRT_1" || threads[0].Path != "internal/heat/score.go" || threads[0].Line != 42 {
		t.Errorf("thread[0] mismatch: %+v", threads[0])
	}
	if threads[0].IsResolved {
		t.Errorf("thread[0] should be unresolved")
	}
	if threads[0].Comments[0].AuthorType != "User" || threads[0].Comments[0].Author != "alice" {
		t.Errorf("thread[0] author mismatch: %+v", threads[0].Comments[0])
	}
	if threads[1].Comments[0].AuthorType != "Bot" {
		t.Errorf("thread[1] author type mismatch: got %q", threads[1].Comments[0].AuthorType)
	}
}
```

- [ ] **Step 1.3: Run test to verify it fails**

Run: `go test ./internal/github/ -run TestParseListThreadsResponse -v`
Expected: FAIL — `undefined: listThreadsData` and `undefined: parseListThreadsResponse`.

- [ ] **Step 1.4: Implement types and parser**

Create `internal/github/threads.go`:

```go
package github

import (
	"context"
	"fmt"
)

// ReviewThread is the public representation of a PR review thread.
type ReviewThread struct {
	ID         string          `json:"id"`
	IsResolved bool            `json:"isResolved"`
	Path       string          `json:"path"`
	Line       int             `json:"line"`
	StartLine  *int            `json:"startLine"`
	DiffSide   string          `json:"diffSide"`
	Comments   []ThreadComment `json:"comments"`
}

// ThreadComment is one comment on a review thread.
type ThreadComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	AuthorType string `json:"authorType"`
	Body       string `json:"body"`
	CreatedAt  string `json:"createdAt"`
}

// listThreadsData mirrors the GraphQL response under data.
type listThreadsData struct {
	Repository struct {
		PullRequest struct {
			Title         string `json:"title"`
			ReviewThreads struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []rawThread `json:"nodes"`
			} `json:"reviewThreads"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

type rawThread struct {
	ID         string `json:"id"`
	IsResolved bool   `json:"isResolved"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	StartLine  *int   `json:"startLine"`
	DiffSide   string `json:"diffSide"`
	Comments   struct {
		Nodes []rawComment `json:"nodes"`
	} `json:"comments"`
}

type rawComment struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	Author    struct {
		Typename string `json:"__typename"`
		Login    string `json:"login"`
	} `json:"author"`
}

func parseListThreadsResponse(data listThreadsData) []ReviewThread {
	nodes := data.Repository.PullRequest.ReviewThreads.Nodes
	out := make([]ReviewThread, 0, len(nodes))
	for _, n := range nodes {
		t := ReviewThread{
			ID:         n.ID,
			IsResolved: n.IsResolved,
			Path:       n.Path,
			Line:       n.Line,
			StartLine:  n.StartLine,
			DiffSide:   n.DiffSide,
		}
		for _, c := range n.Comments.Nodes {
			t.Comments = append(t.Comments, ThreadComment{
				ID:         c.ID,
				Author:     c.Author.Login,
				AuthorType: c.Author.Typename,
				Body:       c.Body,
				CreatedAt:  c.CreatedAt,
			})
		}
		out = append(out, t)
	}
	return out
}

// ListThreads fetches review threads for a PR. resolvedStates is one of
// "" (all), "UNRESOLVED", "RESOLVED".
func (c *Client) ListThreads(ctx context.Context, owner, repo string, number int, resolvedStates string) ([]ReviewThread, error) {
	if c.gql == nil {
		return nil, fmt.Errorf("GraphQL client not available (auth required)")
	}
	const query = `
query($owner: String!, $name: String!, $number: Int!, $after: String, $states: [PullRequestReviewThreadState!]) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      title
      reviewThreads(first: 100, after: $after, resolvedStates: $states) {
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
	var all []ReviewThread
	var cursor *string
	for {
		vars := map[string]interface{}{
			"owner":  owner,
			"name":   repo,
			"number": number,
			"after":  cursor,
		}
		if resolvedStates != "" {
			vars["states"] = []string{resolvedStates}
		} else {
			vars["states"] = nil
		}
		var resp struct {
			Data listThreadsData `json:"data"`
		}
		if err := c.gql.DoWithContext(ctx, query, vars, &resp); err != nil {
			return nil, fmt.Errorf("list threads: %w", err)
		}
		all = append(all, parseListThreadsResponse(resp.Data)...)
		page := resp.Data.Repository.PullRequest.ReviewThreads.PageInfo
		if !page.HasNextPage {
			break
		}
		end := page.EndCursor
		cursor = &end
	}
	return all, nil
}
```

- [ ] **Step 1.5: Run test to verify it passes**

Run: `go test ./internal/github/ -run TestParseListThreadsResponse -v`
Expected: PASS.

- [ ] **Step 1.6: Run full package tests**

Run: `go build ./... && go test ./internal/github/`
Expected: all green.

- [ ] **Step 1.7: Commit**

```bash
git add internal/github/threads.go internal/github/threads_test.go internal/github/testdata/threads_list_basic.json
git commit -m "Add ListThreads GraphQL query for PR review threads"
```

---

## Task 2: `RequiresBody` policy (bot vs. human)

**Files:**
- Modify: `internal/github/threads.go`
- Modify: `internal/github/threads_test.go`

- [ ] **Step 2.1: Write failing test**

Append to `internal/github/threads_test.go`:

```go
func TestRequiresBody(t *testing.T) {
	cases := []struct {
		name string
		t    ReviewThread
		want bool
	}{
		{"bot only", ReviewThread{Comments: []ThreadComment{{AuthorType: "Bot"}}}, false},
		{"single user", ReviewThread{Comments: []ThreadComment{{AuthorType: "User"}}}, true},
		{"user then bot", ReviewThread{Comments: []ThreadComment{
			{AuthorType: "User"},
			{AuthorType: "Bot"},
		}}, true},
		{"two bots", ReviewThread{Comments: []ThreadComment{
			{AuthorType: "Bot"},
			{AuthorType: "Bot"},
		}}, false},
		{"unknown author type counts as user", ReviewThread{Comments: []ThreadComment{{AuthorType: ""}}}, true},
		{"empty thread defaults true", ReviewThread{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.t.RequiresBody(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2.2: Run test, verify failure**

Run: `go test ./internal/github/ -run TestRequiresBody -v`
Expected: FAIL — `RequiresBody undefined`.

- [ ] **Step 2.3: Implement**

Append to `internal/github/threads.go`:

```go
// RequiresBody reports whether resolving this thread requires a reply body.
// Returns true unless every comment is authored by a Bot.
func (t ReviewThread) RequiresBody() bool {
	if len(t.Comments) == 0 {
		return true
	}
	for _, c := range t.Comments {
		if c.AuthorType != "Bot" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2.4: Run test, verify pass**

Run: `go test ./internal/github/ -run TestRequiresBody -v`
Expected: PASS.

- [ ] **Step 2.5: Commit**

```bash
git add internal/github/threads.go internal/github/threads_test.go
git commit -m "Add RequiresBody policy for review threads"
```

---

## Task 3: Reply mutation

**Files:**
- Modify: `internal/github/threads.go`

(Mutation calls cannot be unit-tested without mocking the gql client; they're exercised in integration tests.)

- [ ] **Step 3.1: Implement Reply**

Append to `internal/github/threads.go`:

```go
// ReplyToThread appends a reply comment to a review thread. Returns the new
// comment id.
func (c *Client) ReplyToThread(ctx context.Context, threadID, body string) (string, error) {
	if c.gql == nil {
		return "", fmt.Errorf("GraphQL client not available (auth required)")
	}
	const mutation = `
mutation($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: { pullRequestReviewThreadId: $threadId, body: $body }) {
    comment { id }
  }
}`
	var resp struct {
		Data struct {
			AddPullRequestReviewThreadReply struct {
				Comment struct {
					ID string `json:"id"`
				} `json:"comment"`
			} `json:"addPullRequestReviewThreadReply"`
		} `json:"data"`
	}
	vars := map[string]interface{}{"threadId": threadID, "body": body}
	if err := c.gql.DoWithContext(ctx, mutation, vars, &resp); err != nil {
		return "", fmt.Errorf("reply to thread %s: %w", threadID, err)
	}
	return resp.Data.AddPullRequestReviewThreadReply.Comment.ID, nil
}
```

- [ ] **Step 3.2: Verify build**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 3.3: Commit**

```bash
git add internal/github/threads.go
git commit -m "Add ReplyToThread mutation"
```

---

## Task 4: Resolve / unresolve mutations (single thread)

**Files:**
- Modify: `internal/github/threads.go`

- [ ] **Step 4.1: Implement ResolveThread and UnresolveThread**

Append to `internal/github/threads.go`:

```go
// ResolveThread marks a review thread as resolved.
func (c *Client) ResolveThread(ctx context.Context, threadID string) error {
	return c.flipResolve(ctx, threadID, true)
}

// UnresolveThread marks a review thread as unresolved.
func (c *Client) UnresolveThread(ctx context.Context, threadID string) error {
	return c.flipResolve(ctx, threadID, false)
}

func (c *Client) flipResolve(ctx context.Context, threadID string, resolved bool) error {
	if c.gql == nil {
		return fmt.Errorf("GraphQL client not available (auth required)")
	}
	mutation := `
mutation($threadId: ID!) {
  resolveReviewThread(input: { threadId: $threadId }) { thread { id isResolved } }
}`
	verb := "resolve"
	if !resolved {
		mutation = `
mutation($threadId: ID!) {
  unresolveReviewThread(input: { threadId: $threadId }) { thread { id isResolved } }
}`
		verb = "unresolve"
	}
	var resp struct{}
	vars := map[string]interface{}{"threadId": threadID}
	if err := c.gql.DoWithContext(ctx, mutation, vars, &resp); err != nil {
		return fmt.Errorf("%s thread %s: %w", verb, threadID, err)
	}
	return nil
}
```

- [ ] **Step 4.2: Verify build**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 4.3: Commit**

```bash
git add internal/github/threads.go
git commit -m "Add ResolveThread and UnresolveThread mutations"
```

---

## Task 5: Bulk resolve / unresolve with worker pool

**Files:**
- Modify: `internal/github/threads.go`
- Modify: `internal/github/threads_test.go`

- [ ] **Step 5.1: Write failing test for the result aggregator**

Append to `internal/github/threads_test.go`:

```go
func TestBulkResultMerge(t *testing.T) {
	r := BulkResult{}
	r.AddSuccess("a")
	r.AddSuccess("b")
	r.AddFailure("c", fmt.Errorf("boom"))
	if len(r.Succeeded) != 2 || len(r.Failed) != 1 {
		t.Fatalf("got %+v", r)
	}
	if r.Failed[0].ID != "c" || r.Failed[0].Err == nil {
		t.Errorf("failure capture broken: %+v", r.Failed[0])
	}
}
```

Add `import "fmt"` at top of test file if not present.

- [ ] **Step 5.2: Run test, verify failure**

Run: `go test ./internal/github/ -run TestBulkResultMerge -v`
Expected: FAIL — `undefined: BulkResult`.

- [ ] **Step 5.3: Implement BulkResult and bulk helpers**

Append to `internal/github/threads.go`:

```go
import "sync"

// BulkResult aggregates the outcome of a bulk thread operation.
type BulkResult struct {
	mu        sync.Mutex
	Succeeded []string
	Failed    []BulkFailure
}

// BulkFailure captures a single mutation error.
type BulkFailure struct {
	ID  string
	Err error
}

func (r *BulkResult) AddSuccess(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Succeeded = append(r.Succeeded, id)
}

func (r *BulkResult) AddFailure(id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Failed = append(r.Failed, BulkFailure{ID: id, Err: err})
}

// ResolveAllThreads resolves every currently-unresolved thread on a PR.
func (c *Client) ResolveAllThreads(ctx context.Context, owner, repo string, number, workers int) (*BulkResult, error) {
	threads, err := c.ListThreads(ctx, owner, repo, number, "UNRESOLVED")
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(threads))
	for i, t := range threads {
		ids[i] = t.ID
	}
	return c.bulkFlip(ctx, ids, true, workers), nil
}

// UnresolveAllThreads unresolves every currently-resolved thread on a PR.
func (c *Client) UnresolveAllThreads(ctx context.Context, owner, repo string, number, workers int) (*BulkResult, error) {
	threads, err := c.ListThreads(ctx, owner, repo, number, "RESOLVED")
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(threads))
	for i, t := range threads {
		ids[i] = t.ID
	}
	return c.bulkFlip(ctx, ids, false, workers), nil
}

func (c *Client) bulkFlip(ctx context.Context, ids []string, resolved bool, workers int) *BulkResult {
	if workers < 1 {
		workers = 4
	}
	res := &BulkResult{}
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				if err := c.flipResolve(ctx, id, resolved); err != nil {
					res.AddFailure(id, err)
				} else {
					res.AddSuccess(id)
				}
			}
		}()
	}
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
	return res
}
```

Note: Go puts all imports in the existing import block at the top — add `"sync"` to the existing imports (do not introduce a second `import` block).

- [ ] **Step 5.4: Run test, verify pass**

Run: `go test ./internal/github/ -run TestBulkResultMerge -v`
Expected: PASS.

- [ ] **Step 5.5: Run full package**

Run: `go build ./... && go test ./internal/github/`
Expected: green.

- [ ] **Step 5.6: Commit**

```bash
git add internal/github/threads.go internal/github/threads_test.go
git commit -m "Add bulk resolve/unresolve with worker pool"
```

---

## Task 6: PR ref parser

**Files:**
- Create: `cmd/spoon/threads.go`
- Create: `cmd/spoon/threads_test.go`

- [ ] **Step 6.1: Write failing test**

Create `cmd/spoon/threads_test.go`:

```go
package main

import "testing"

func TestParsePRRef(t *testing.T) {
	cases := []struct {
		in            string
		fallbackOwner string
		fallbackRepo  string
		wantOwner     string
		wantRepo      string
		wantNumber    int
		wantErr       bool
	}{
		{"owner/repo#42", "", "", "owner", "repo", 42, false},
		{"https://github.com/owner/repo/pull/42", "", "", "owner", "repo", 42, false},
		{"#42", "fallO", "fallR", "fallO", "fallR", 42, false},
		{"#42", "", "", "", "", 0, true}, // no fallback
		{"owner/repo", "", "", "", "", 0, true},
		{"https://gitlab.com/g/r/-/merge_requests/1", "", "", "", "", 0, true}, // gitlab rejected
		{"owner/repo#abc", "", "", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			o, r, n, err := parsePRRef(tc.in, tc.fallbackOwner, tc.fallbackRepo)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err mismatch: got %v, wantErr=%v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if o != tc.wantOwner || r != tc.wantRepo || n != tc.wantNumber {
				t.Errorf("got (%q,%q,%d), want (%q,%q,%d)", o, r, n, tc.wantOwner, tc.wantRepo, tc.wantNumber)
			}
		})
	}
}
```

- [ ] **Step 6.2: Run test, verify failure**

Run: `go test ./cmd/spoon/ -run TestParsePRRef -v`
Expected: FAIL — `undefined: parsePRRef`.

- [ ] **Step 6.3: Implement parser**

Create `cmd/spoon/threads.go`:

```go
package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// parsePRRef parses a PR reference into (owner, repo, number).
// Accepted forms:
//   - "owner/repo#42"
//   - "https://github.com/owner/repo/pull/42"
//   - "#42" (uses fallbackOwner/fallbackRepo)
// Returns an error for GitLab URLs or malformed input.
func parsePRRef(s, fallbackOwner, fallbackRepo string) (owner, repo string, number int, err error) {
	if s == "" {
		return "", "", 0, fmt.Errorf("empty PR ref")
	}

	// "#42" form
	if strings.HasPrefix(s, "#") {
		n, perr := strconv.Atoi(s[1:])
		if perr != nil {
			return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
		}
		if fallbackOwner == "" || fallbackRepo == "" {
			return "", "", 0, fmt.Errorf("%q has no repo context (run inside a git checkout or pass owner/repo#N)", s)
		}
		return fallbackOwner, fallbackRepo, n, nil
	}

	// URL form
	if strings.Contains(s, "://") {
		u, perr := url.Parse(s)
		if perr != nil {
			return "", "", 0, fmt.Errorf("parse %q: %w", s, perr)
		}
		host := strings.ToLower(u.Hostname())
		if host != "github.com" {
			return "", "", 0, fmt.Errorf("only github.com PR URLs are supported, got %q", host)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		// Expected: owner/repo/pull/N
		if len(parts) < 4 || parts[2] != "pull" {
			return "", "", 0, fmt.Errorf("URL %q is not a github.com PR URL", s)
		}
		n, perr := strconv.Atoi(parts[3])
		if perr != nil {
			return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
		}
		return parts[0], parts[1], n, nil
	}

	// "owner/repo#N" form
	hash := strings.LastIndex(s, "#")
	if hash < 0 {
		return "", "", 0, fmt.Errorf("PR ref %q missing '#N' suffix", s)
	}
	n, perr := strconv.Atoi(s[hash+1:])
	if perr != nil {
		return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
	}
	repoPart := s[:hash]
	slash := strings.IndexByte(repoPart, '/')
	if slash < 0 {
		return "", "", 0, fmt.Errorf("PR ref %q missing 'owner/repo' prefix", s)
	}
	return repoPart[:slash], repoPart[slash+1:], n, nil
}
```

- [ ] **Step 6.4: Run test, verify pass**

Run: `go test ./cmd/spoon/ -run TestParsePRRef -v`
Expected: PASS.

- [ ] **Step 6.5: Commit**

```bash
git add cmd/spoon/threads.go cmd/spoon/threads_test.go
git commit -m "Add PR ref parser for spoon threads"
```

---

## Task 7: `threads` subcommand flag parser

**Files:**
- Modify: `cmd/spoon/threads.go`
- Modify: `cmd/spoon/threads_test.go`

- [ ] **Step 7.1: Write failing test for flag parsing**

Append to `cmd/spoon/threads_test.go`:

```go
func TestParseThreadsFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(t *testing.T, f threadsFlags)
	}{
		{
			name: "default mode (TUI)",
			args: []string{"owner/repo#42"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeTUI {
					t.Errorf("mode=%v want TUI", f.mode)
				}
			},
		},
		{
			name: "json",
			args: []string{"owner/repo#42", "--json"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeJSON {
					t.Errorf("mode=%v want JSON", f.mode)
				}
			},
		},
		{
			name: "next",
			args: []string{"owner/repo#42", "--next"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeNext {
					t.Errorf("mode=%v want Next", f.mode)
				}
			},
		},
		{
			name: "reply with body",
			args: []string{"owner/repo#42", "--reply", "PRRT_1", "--body", "hello"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeReply || f.targetID != "PRRT_1" || f.body != "hello" {
					t.Errorf("got %+v", f)
				}
			},
		},
		{
			name: "resolve",
			args: []string{"owner/repo#42", "--resolve", "PRRT_1", "--body", "fixed"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeResolve || f.targetID != "PRRT_1" || f.body != "fixed" {
					t.Errorf("got %+v", f)
				}
			},
		},
		{
			name: "resolve-all",
			args: []string{"owner/repo#42", "--resolve-all"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeResolveAll {
					t.Errorf("mode=%v", f.mode)
				}
			},
		},
		{
			name:    "json + next mutually exclusive",
			args:    []string{"owner/repo#42", "--json", "--next"},
			wantErr: true,
		},
		{
			name:    "reply needs target id",
			args:    []string{"owner/repo#42", "--reply"},
			wantErr: true,
		},
		{
			name:    "reply needs body",
			args:    []string{"owner/repo#42", "--reply", "PRRT_1"},
			wantErr: true,
		},
		{
			name:    "missing pr ref",
			args:    []string{"--json"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseThreadsFlags(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			tc.check(t, f)
		})
	}
}
```

- [ ] **Step 7.2: Run, verify failure**

Run: `go test ./cmd/spoon/ -run TestParseThreadsFlags -v`
Expected: FAIL — undefined identifiers.

- [ ] **Step 7.3: Implement flag parser**

Append to `cmd/spoon/threads.go`:

```go
import "os"

type threadsMode int

const (
	modeTUI threadsMode = iota
	modeJSON
	modeNext
	modeReply
	modeResolve
	modeResolveAll
	modeUnresolveAll
)

type threadsFlags struct {
	prRef           string
	mode            threadsMode
	targetID        string
	body            string
	bodyFile        string
	includeResolved bool
}

// parseThreadsFlags parses the args after "spoon threads".
// Returns flags or an error suitable for stderr output (exit code 2).
func parseThreadsFlags(args []string) (threadsFlags, error) {
	var f threadsFlags
	modeFlags := 0
	setMode := func(m threadsMode, name string) error {
		modeFlags++
		if modeFlags > 1 {
			return fmt.Errorf("--%s conflicts with another mode flag", name)
		}
		f.mode = m
		return nil
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--json":
			if err := setMode(modeJSON, "json"); err != nil {
				return f, err
			}
		case "--next":
			if err := setMode(modeNext, "next"); err != nil {
				return f, err
			}
		case "--include-resolved":
			f.includeResolved = true
		case "--reply":
			if err := setMode(modeReply, "reply"); err != nil {
				return f, err
			}
			if i+1 >= len(args) {
				return f, fmt.Errorf("--reply requires a thread id")
			}
			i++
			f.targetID = args[i]
		case "--resolve":
			if err := setMode(modeResolve, "resolve"); err != nil {
				return f, err
			}
			if i+1 >= len(args) {
				return f, fmt.Errorf("--resolve requires a thread id")
			}
			i++
			f.targetID = args[i]
		case "--resolve-all":
			if err := setMode(modeResolveAll, "resolve-all"); err != nil {
				return f, err
			}
		case "--unresolve-all":
			if err := setMode(modeUnresolveAll, "unresolve-all"); err != nil {
				return f, err
			}
		case "--body":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--body requires a value")
			}
			i++
			f.body = args[i]
		case "--body-file":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--body-file requires a path")
			}
			i++
			f.bodyFile = args[i]
		case "-h", "--help":
			return f, errThreadsHelp
		default:
			if strings.HasPrefix(a, "--") {
				return f, fmt.Errorf("unknown flag %q", a)
			}
			if f.prRef != "" {
				return f, fmt.Errorf("unexpected positional argument %q", a)
			}
			f.prRef = a
		}
	}

	if f.prRef == "" {
		return f, fmt.Errorf("missing PR reference (e.g. owner/repo#42)")
	}

	// Resolve --body-file before validating body requirement.
	if f.bodyFile != "" {
		body, err := readBody(f.bodyFile)
		if err != nil {
			return f, err
		}
		if f.body == "" {
			f.body = body
		}
	}

	if f.mode == modeReply && f.body == "" {
		return f, fmt.Errorf("--reply requires --body or --body-file")
	}
	return f, nil
}

// errThreadsHelp signals that the caller asked for --help and the parser
// should print help text. The dispatcher checks for this sentinel.
var errThreadsHelp = fmt.Errorf("threads help requested")

func readBody(path string) (string, error) {
	if path == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read body file: %w", err)
	}
	return string(b), nil
}
```

Add `"io"` to the existing import block at the top of `cmd/spoon/threads.go`.

- [ ] **Step 7.4: Run, verify pass**

Run: `go test ./cmd/spoon/ -run TestParseThreadsFlags -v`
Expected: PASS.

- [ ] **Step 7.5: Commit**

```bash
git add cmd/spoon/threads.go cmd/spoon/threads_test.go
git commit -m "Add threads subcommand flag parser"
```

---

## Task 8: `--json` and `--next` output

**Files:**
- Modify: `cmd/spoon/threads.go`

- [ ] **Step 8.1: Implement JSON encoder for threads**

Append to `cmd/spoon/threads.go`:

```go
import (
	"encoding/json"
	"sort"
)

// emitJSON prints all threads as a JSON array.
func emitJSON(w io.Writer, threads []gh.ReviewThread) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(threads)
}

// emitNext prints the single oldest unresolved thread as JSON, or "null".
func emitNext(w io.Writer, threads []gh.ReviewThread) error {
	unresolved := make([]gh.ReviewThread, 0, len(threads))
	for _, t := range threads {
		if !t.IsResolved {
			unresolved = append(unresolved, t)
		}
	}
	if len(unresolved) == 0 {
		_, err := io.WriteString(w, "null\n")
		return err
	}
	sort.Slice(unresolved, func(i, j int) bool {
		ai, aj := firstCommentTime(unresolved[i]), firstCommentTime(unresolved[j])
		return ai < aj
	})
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(unresolved[0])
}

func firstCommentTime(t gh.ReviewThread) string {
	if len(t.Comments) == 0 {
		return ""
	}
	return t.Comments[0].CreatedAt
}
```

Add the `gh "github.com/svnbjrn/spoon/internal/github"` import alias to the existing import block (matching the style used in `main.go`).

- [ ] **Step 8.2: Verify build**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 8.3: Commit**

```bash
git add cmd/spoon/threads.go
git commit -m "Add JSON and --next output for threads"
```

---

## Task 9: Subcommand dispatcher (non-TUI modes)

**Files:**
- Modify: `cmd/spoon/threads.go`

- [ ] **Step 9.1: Implement runThreads (non-TUI dispatch)**

Append to `cmd/spoon/threads.go`:

```go
import "context"

// runThreads is the entry point for the "threads" subcommand. It returns
// an exit code (0/1/2) and writes any error messages to stderr.
func runThreads(args []string) int {
	flags, err := parseThreadsFlags(args)
	if err != nil {
		if err == errThreadsHelp {
			printThreadsHelp()
			return 0
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		printThreadsHelp()
		return 2
	}

	owner, repo, number, err := parsePRRef(flags.prRef, "", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}

	client, _, err := gh.CheckAuth()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: GitHub auth:", err)
		return 1
	}
	if !client.IsAuthenticated() {
		fmt.Fprintln(os.Stderr, "Error: spoon threads requires authentication (run `gh auth login`).")
		return 1
	}

	ctx := context.Background()

	switch flags.mode {
	case modeJSON:
		states := "UNRESOLVED"
		if flags.includeResolved {
			states = ""
		}
		threads, err := client.ListThreads(ctx, owner, repo, number, states)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		if err := emitJSON(os.Stdout, threads); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		return 0

	case modeNext:
		threads, err := client.ListThreads(ctx, owner, repo, number, "UNRESOLVED")
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		if err := emitNext(os.Stdout, threads); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		return 0

	case modeReply:
		if _, err := client.ReplyToThread(ctx, flags.targetID, flags.body); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		return 0

	case modeResolve:
		// Apply bot/human policy: fetch the thread to inspect comments.
		all, err := client.ListThreads(ctx, owner, repo, number, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		var target *gh.ReviewThread
		for i := range all {
			if all[i].ID == flags.targetID {
				target = &all[i]
				break
			}
		}
		if target == nil {
			fmt.Fprintf(os.Stderr, "Error: thread %s not found on PR\n", flags.targetID)
			return 1
		}
		if target.IsResolved {
			fmt.Fprintln(os.Stderr, "Warning: thread already resolved; nothing to do")
			return 0
		}
		if target.RequiresBody() && flags.body == "" {
			fmt.Fprintln(os.Stderr, "Error: thread has a non-bot reviewer; --body (or --body-file) is required")
			return 2
		}
		if flags.body != "" {
			if _, err := client.ReplyToThread(ctx, flags.targetID, flags.body); err != nil {
				fmt.Fprintln(os.Stderr, "Error: reply failed:", err)
				return 1
			}
		}
		if err := client.ResolveThread(ctx, flags.targetID); err != nil {
			fmt.Fprintln(os.Stderr, "Error: resolve failed (reply already posted):", err)
			return 1
		}
		return 0

	case modeResolveAll:
		res, err := client.ResolveAllThreads(ctx, owner, repo, number, 4)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		fmt.Printf("resolved %d threads\n", len(res.Succeeded))
		if len(res.Failed) > 0 {
			for _, f := range res.Failed {
				fmt.Fprintf(os.Stderr, "failed %s: %v\n", f.ID, f.Err)
			}
			return 1
		}
		return 0

	case modeUnresolveAll:
		res, err := client.UnresolveAllThreads(ctx, owner, repo, number, 4)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		fmt.Printf("unresolved %d threads\n", len(res.Succeeded))
		if len(res.Failed) > 0 {
			for _, f := range res.Failed {
				fmt.Fprintf(os.Stderr, "failed %s: %v\n", f.ID, f.Err)
			}
			return 1
		}
		return 0

	case modeTUI:
		return runThreadsTUI(ctx, client, owner, repo, number)

	default:
		fmt.Fprintln(os.Stderr, "Error: unknown mode")
		return 2
	}
}

func printThreadsHelp() {
	fmt.Print(`spoon threads — operate on PR review threads

Usage:
  spoon threads <pr-ref> [flags]

PR reference forms:
  owner/repo#42
  https://github.com/owner/repo/pull/42
  #42                  (uses local repo context)

Flags:
  (no mode flag)        Open the TUI for unresolved threads (default)
  --json                Print all unresolved threads as JSON
  --include-resolved    Include resolved threads in --json output
  --next                Print the oldest unresolved thread as JSON, or null
  --reply <id> --body T   Append a reply to a thread
  --resolve <id> [--body T]
                        Resolve one thread; --body required for non-bot threads
  --resolve-all         Mark every unresolved thread as resolved
  --unresolve-all       Mark every resolved thread as unresolved
  --body T              Comment body
  --body-file PATH      Read body from file ('-' = stdin)
  -h, --help            Show this help

Examples:
  spoon threads owner/repo#42
  spoon threads owner/repo#42 --json
  spoon threads owner/repo#42 --resolve PRRT_1 --body "fixed in 1234abc"
  while [ "$(spoon threads owner/repo#42 --next)" != "null" ]; do ... ; done
`)
}

// runThreadsTUI is implemented in Task 12.
func runThreadsTUI(ctx context.Context, client *gh.Client, owner, repo string, number int) int {
	fmt.Fprintln(os.Stderr, "TUI mode not implemented yet")
	return 1
}
```

Add `"context"` to the existing import block.

- [ ] **Step 9.2: Verify build**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 9.3: Commit**

```bash
git add cmd/spoon/threads.go
git commit -m "Add threads subcommand dispatcher (non-TUI modes)"
```

---

## Task 10: Wire `threads` into `main.go`

**Files:**
- Modify: `cmd/spoon/main.go`

- [ ] **Step 10.1: Inspect existing dispatch**

Run: `grep -n "func main" cmd/spoon/main.go`
Read the surrounding 30 lines so the dispatch insertion point is clear.

- [ ] **Step 10.2: Add subcommand dispatch as the first thing in main()**

Edit `cmd/spoon/main.go`. Find the start of `func main() {` and insert this block immediately after the opening brace, before any flag parsing:

```go
	// Subcommand dispatch: "spoon threads <pr-ref> ..."
	if len(os.Args) >= 2 && os.Args[1] == "threads" {
		os.Exit(runThreads(os.Args[2:]))
	}
```

- [ ] **Step 10.3: Update --help to mention the subcommand**

Find `printHelp()` in `main.go` and add a "Subcommands:" section to the help text:

```
Subcommands:
  spoon threads <pr-ref>   Operate on PR review threads (see 'spoon threads --help')
```

- [ ] **Step 10.4: Verify build and tests**

Run: `go build ./... && go test ./...`
Expected: green.

- [ ] **Step 10.5: Smoke test the dispatcher**

Run: `./spoon threads --help`
Expected: prints the threads-specific help text and exits 0.
Run: `./spoon threads`
Expected: stderr "missing PR reference"; exit 2.

(Build the binary first if needed: `go build -o spoon ./cmd/spoon`.)

- [ ] **Step 10.6: Commit**

```bash
git add cmd/spoon/main.go
git commit -m "Wire threads subcommand into spoon main dispatch"
```

---

## Task 11: TUI model — list view skeleton

**Files:**
- Create: `internal/tui/threads/model.go`
- Create: `internal/tui/threads/keys.go`
- Create: `internal/tui/threads/view.go`
- Create: `internal/tui/threads/model_test.go`

- [ ] **Step 11.1: Write failing test for cursor movement**

Create `internal/tui/threads/model_test.go`:

```go
package threads

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	gh "github.com/svnbjrn/spoon/internal/github"
)

func TestCursorWraps(t *testing.T) {
	m := New(nil, "owner", "repo", 1)
	m.threads = []gh.ReviewThread{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	m.loaded = true

	// down once
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm := m2.(Model)
	if mm.cursor != 1 {
		t.Fatalf("cursor=%d want 1", mm.cursor)
	}

	// down past end stays at last
	mm.cursor = 2
	m3, _ := mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m3.(Model).cursor != 2 {
		t.Errorf("cursor=%d want 2 (clamped)", m3.(Model).cursor)
	}

	// up below 0 stays at 0
	mm.cursor = 0
	m4, _ := mm.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m4.(Model).cursor != 0 {
		t.Errorf("cursor=%d want 0 (clamped)", m4.(Model).cursor)
	}
}
```

- [ ] **Step 11.2: Run test, verify failure**

Run: `go test ./internal/tui/threads/ -run TestCursorWraps -v`
Expected: FAIL — package missing.

- [ ] **Step 11.3: Create the keymap**

Create `internal/tui/threads/keys.go`:

```go
package threads

import tea "github.com/charmbracelet/bubbletea"

// keyAction is the result of dispatching a tea.KeyMsg.
type keyAction int

const (
	actNone keyAction = iota
	actUp
	actDown
	actQuit
	actReply
	actResolve
	actResolveAll
	actUnresolveAll
	actOpen
	actHelp
)

func dispatchKey(k tea.KeyMsg) keyAction {
	switch k.String() {
	case "up", "k":
		return actUp
	case "down", "j":
		return actDown
	case "q", "ctrl+c":
		return actQuit
	case "r":
		return actReply
	case "R":
		return actResolve
	case "a":
		return actResolveAll
	case "A":
		return actUnresolveAll
	case "o":
		return actOpen
	case "?":
		return actHelp
	}
	return actNone
}
```

- [ ] **Step 11.4: Create the model**

Create `internal/tui/threads/model.go`:

```go
package threads

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// Model is the bubbletea model for the threads view.
type Model struct {
	client  *gh.Client
	owner   string
	repo    string
	number  int
	threads []gh.ReviewThread
	loaded  bool
	cursor  int
	err     error
	width   int
	height  int
}

// New constructs an empty Model.
func New(client *gh.Client, owner, repo string, number int) Model {
	return Model{
		client: client,
		owner:  owner,
		repo:   repo,
		number: number,
	}
}

// loadedMsg is delivered when ListThreads completes.
type loadedMsg struct {
	threads []gh.ReviewThread
	err     error
}

// loadCmd fetches unresolved threads.
func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		ts, err := m.client.ListThreads(context.Background(), m.owner, m.repo, m.number, "UNRESOLVED")
		return loadedMsg{threads: ts, err: err}
	}
}

func (m Model) Init() tea.Cmd {
	return m.loadCmd()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case loadedMsg:
		m.loaded = true
		m.threads = msg.threads
		m.err = msg.err
		if m.cursor >= len(m.threads) {
			m.cursor = max(0, len(m.threads)-1)
		}
		return m, nil
	case tea.KeyMsg:
		switch dispatchKey(msg) {
		case actUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case actDown:
			if m.cursor < len(m.threads)-1 {
				m.cursor++
			}
		case actQuit:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m Model) View() string {
	return renderModel(m)
}
```

- [ ] **Step 11.5: Create a placeholder view**

Create `internal/tui/threads/view.go`:

```go
package threads

import (
	"fmt"
	"strings"
)

func renderModel(m Model) string {
	if m.err != nil {
		return fmt.Sprintf("error: %v\n\npress q to quit", m.err)
	}
	if !m.loaded {
		return "loading threads…"
	}
	if len(m.threads) == 0 {
		return "no unresolved threads on this PR\n\npress q to quit"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "PR #%d — %d unresolved\n\n", m.number, len(m.threads))
	for i, t := range m.threads {
		marker := "  "
		if i == m.cursor {
			marker = "> "
		}
		reviewer := "unknown"
		if len(t.Comments) > 0 {
			reviewer = fmt.Sprintf("%s (%s)", t.Comments[0].Author, t.Comments[0].AuthorType)
		}
		fmt.Fprintf(&b, "%s%-16s  %s:%d\n", marker, reviewer, t.Path, t.Line)
	}
	if m.cursor < len(m.threads) {
		t := m.threads[m.cursor]
		b.WriteString("\n")
		if len(t.Comments) > 0 {
			b.WriteString(t.Comments[0].Body)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n[r] reply  [R] resolve  [a] resolve-all  [q] quit\n")
	return b.String()
}
```

- [ ] **Step 11.6: Run test, verify pass**

Run: `go test ./internal/tui/threads/ -v`
Expected: PASS.

- [ ] **Step 11.7: Commit**

```bash
git add internal/tui/threads/
git commit -m "Add bubbletea threads model with list view and cursor"
```

---

## Task 12: TUI reply composer + resolve key

**Files:**
- Modify: `internal/tui/threads/model.go`
- Modify: `internal/tui/threads/view.go`
- Modify: `internal/tui/threads/model_test.go`

- [ ] **Step 12.1: Write failing test for entering reply mode**

Append to `internal/tui/threads/model_test.go`:

```go
func TestEnterReplyMode(t *testing.T) {
	m := New(nil, "o", "r", 1)
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{AuthorType: "User"}}}}
	m.loaded = true
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !out.(Model).composing {
		t.Errorf("expected composing=true after 'r'")
	}
}

func TestResolveKeyOnBotThreadSkipsCompose(t *testing.T) {
	m := New(nil, "o", "r", 1)
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{AuthorType: "Bot"}}}}
	m.loaded = true
	// 'R' on a bot thread should set pendingResolve and not enter composing.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	mm := out.(Model)
	if mm.composing {
		t.Errorf("composing should be false on bot thread")
	}
	if !mm.pendingResolve {
		t.Errorf("pendingResolve should be true on bot thread")
	}
}
```

- [ ] **Step 12.2: Run test, verify failure**

Run: `go test ./internal/tui/threads/ -v`
Expected: FAIL — undefined fields.

- [ ] **Step 12.3: Add composer state and update logic**

Edit `internal/tui/threads/model.go`. Add fields to Model:

```go
	composing      bool
	composeBuf     []rune
	composeFor     string // "reply" or "resolve"
	pendingResolve bool   // bot-only resolve queued for the next tick
	status         string // last status line
```

Extend `Update` to handle composer keys and the new actions:

```go
	case tea.KeyMsg:
		if m.composing {
			switch msg.Type {
			case tea.KeyEsc:
				m.composing = false
				m.composeBuf = nil
			case tea.KeyCtrlS:
				body := string(m.composeBuf)
				m.composing = false
				m.composeBuf = nil
				if len(m.threads) == 0 {
					return m, nil
				}
				targetID := m.threads[m.cursor].ID
				switch m.composeFor {
				case "reply":
					return m, m.replyCmd(targetID, body)
				case "resolve":
					return m, m.replyThenResolveCmd(targetID, body)
				}
			case tea.KeyBackspace:
				if len(m.composeBuf) > 0 {
					m.composeBuf = m.composeBuf[:len(m.composeBuf)-1]
				}
			case tea.KeyRunes:
				m.composeBuf = append(m.composeBuf, msg.Runes...)
			case tea.KeyEnter:
				m.composeBuf = append(m.composeBuf, '\n')
			case tea.KeySpace:
				m.composeBuf = append(m.composeBuf, ' ')
			}
			return m, nil
		}
		switch dispatchKey(msg) {
		case actUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case actDown:
			if m.cursor < len(m.threads)-1 {
				m.cursor++
			}
		case actReply:
			if len(m.threads) > 0 {
				m.composing = true
				m.composeFor = "reply"
				m.composeBuf = nil
			}
		case actResolve:
			if len(m.threads) > 0 {
				cur := m.threads[m.cursor]
				if cur.RequiresBody() {
					m.composing = true
					m.composeFor = "resolve"
					m.composeBuf = nil
				} else {
					m.pendingResolve = true
					return m, m.resolveCmd(cur.ID)
				}
			}
		case actResolveAll:
			return m, m.resolveAllCmd()
		case actUnresolveAll:
			return m, m.unresolveAllCmd()
		case actQuit:
			return m, tea.Quit
		}
```

Add new commands and result messages near the bottom of `model.go`:

```go
type mutationDoneMsg struct {
	what string // "reply", "resolve", "bulk-resolve", "bulk-unresolve"
	err  error
}

func (m Model) replyCmd(threadID, body string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.ReplyToThread(context.Background(), threadID, body)
		return mutationDoneMsg{what: "reply", err: err}
	}
}

func (m Model) resolveCmd(threadID string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.ResolveThread(context.Background(), threadID)
		return mutationDoneMsg{what: "resolve", err: err}
	}
}

func (m Model) replyThenResolveCmd(threadID, body string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.ReplyToThread(context.Background(), threadID, body); err != nil {
			return mutationDoneMsg{what: "reply", err: err}
		}
		err := m.client.ResolveThread(context.Background(), threadID)
		return mutationDoneMsg{what: "resolve", err: err}
	}
}

func (m Model) resolveAllCmd() tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.ResolveAllThreads(context.Background(), m.owner, m.repo, m.number, 4)
		return mutationDoneMsg{what: "bulk-resolve", err: err}
	}
}

func (m Model) unresolveAllCmd() tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.UnresolveAllThreads(context.Background(), m.owner, m.repo, m.number, 4)
		return mutationDoneMsg{what: "bulk-unresolve", err: err}
	}
}
```

Handle `mutationDoneMsg` in `Update` (add a case alongside `loadedMsg`):

```go
	case mutationDoneMsg:
		m.pendingResolve = false
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
		} else {
			m.status = msg.what + " ok"
		}
		// Refresh the thread list.
		return m, m.loadCmd()
```

- [ ] **Step 12.4: Update view to render composer and status**

Edit `internal/tui/threads/view.go` `renderModel`. Right before the final `return b.String()`:

```go
	if m.status != "" {
		fmt.Fprintf(&b, "\n%s\n", m.status)
	}
	if m.composing {
		fmt.Fprintf(&b, "\n--- compose (%s) — Ctrl+S to send, Esc to cancel ---\n%s_\n", m.composeFor, string(m.composeBuf))
	}
```

- [ ] **Step 12.5: Run tests, verify pass**

Run: `go test ./internal/tui/threads/ -v`
Expected: PASS.

- [ ] **Step 12.6: Commit**

```bash
git add internal/tui/threads/
git commit -m "Add reply composer and resolve flow to threads TUI"
```

---

## Task 13: Wire TUI into the dispatcher

**Files:**
- Modify: `cmd/spoon/threads.go`

- [ ] **Step 13.1: Replace the TUI placeholder**

In `cmd/spoon/threads.go`, replace the `runThreadsTUI` placeholder body:

```go
func runThreadsTUI(ctx context.Context, client *gh.Client, owner, repo string, number int) int {
	m := threadstui.New(client, owner, repo, number)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	return 0
}
```

Add imports:

```go
	tea "github.com/charmbracelet/bubbletea"
	threadstui "github.com/svnbjrn/spoon/internal/tui/threads"
```

(Add to the existing import block; do not introduce a second one. The `threadstui` alias avoids collision with the existing top-level `tui` package.)

- [ ] **Step 13.2: Verify build**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 13.3: Manual smoke** (only if you have a sandbox PR)

```
go build -o spoon ./cmd/spoon
./spoon threads <your-test-pr> --json | head
./spoon threads <your-test-pr>          # opens TUI
```

If you don't have a sandbox PR, skip the smoke and rely on the next task's tests.

- [ ] **Step 13.4: Commit**

```bash
git add cmd/spoon/threads.go
git commit -m "Wire threads TUI into the dispatcher"
```

---

## Task 14: README update

**Files:**
- Modify: `README.md`

- [ ] **Step 14.1: Add a "PR review threads" section**

Insert this section into `README.md`, immediately after the "TUI keybindings" subsection of "Usage":

```markdown
### PR review threads

The `spoon threads` subcommand lists, replies to, and resolves GitHub PR
review threads. It's the primary way an AI agent reasons about review
feedback.

```sh
spoon threads owner/repo#42                   # interactive TUI
spoon threads owner/repo#42 --json            # all unresolved threads as JSON
spoon threads owner/repo#42 --next            # one thread (or null) for agent loops
spoon threads owner/repo#42 --reply PRRT_… --body "fixed"
spoon threads owner/repo#42 --resolve PRRT_… --body "addressed in 1234abc"
spoon threads owner/repo#42 --resolve-all     # bulk close
spoon threads owner/repo#42 --unresolve-all   # inverse
```

Single-thread `--resolve` enforces a body when any commenter is a human
reviewer. `--resolve-all`/`--unresolve-all` are deliberate bulk
operations and accept no body.

Agent loop pattern:

```sh
while [ "$(spoon threads owner/repo#42 --next)" != "null" ]; do
  thread=$(spoon threads owner/repo#42 --next)
  # ... address the comment in code ...
  id=$(echo "$thread" | jq -r .id)
  spoon threads owner/repo#42 --resolve "$id" --body "addressed in $(git rev-parse --short HEAD)"
done
```
```

- [ ] **Step 14.2: Verify the file renders**

Run: `head -120 README.md`
Expected: section present, no malformed code fences.

- [ ] **Step 14.3: Commit**

```bash
git add README.md
git commit -m "Document spoon threads subcommand in README"
```

---

## Task 15: Final verification

**Files:** none

- [ ] **Step 15.1: Build, vet, and test**

Run:
```
go build ./...
go vet ./...
go test ./...
```
Expected: all green.

- [ ] **Step 15.2: Help text smoke**

```
./spoon --help               # mentions Subcommands: spoon threads
./spoon threads --help       # full threads help
./spoon threads              # exits 2 with "missing PR reference"
./spoon threads owner/repo#1 --json --next   # exits 2 with mutual-exclusion error
```

- [ ] **Step 15.3: If you have a sandbox PR, exercise the live path**

```
./spoon threads owner/your-sandbox-repo#1 --json | jq '.[].id'
./spoon threads owner/your-sandbox-repo#1 --next
./spoon threads owner/your-sandbox-repo#1
```

Live calls aren't gated in the unit suite; only run against a PR you control.

- [ ] **Step 15.4: Push the branch**

```bash
git push origin main
```
