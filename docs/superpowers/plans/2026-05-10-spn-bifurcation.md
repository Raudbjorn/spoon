# `spn` Agent CLI Bifurcation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a second binary, `spn`, that mirrors `spoon`'s functionality but is JSON-only and agent-shaped, with structured errors and NDJSON streaming. `spoon`'s CLI behavior does not change.

**Architecture:** Two `cmd/` packages (`spoon`, `spn`) sharing three new `internal/` packages: `threadsops` (PR thread operations and PR-ref parsing, hoisted from `cmd/spoon/threads.go`), `forksops` (streaming fetch/score/enrich pipeline), and `agentio` (JSON writers and structured error envelope). Refactoring is behavior-preserving for `spoon`.

**Tech Stack:** Go 1.26+, existing libs (`charmbracelet/bubbletea`, `cli/go-gh/v2`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-10-spn-bifurcation-design.md`

---

## File Structure

**New packages:**

- `internal/agentio/`
  - `writer.go` — `WriteJSON`, `WriteNDJSON`, `WriteNull`
  - `error.go` — `Code` consts, `Error` struct, `NewError`, exit-code mapping, `Emit`
  - `remediations.go` — remediation pattern constants with placeholders
  - `error_test.go`, `writer_test.go`

- `internal/threadsops/`
  - `prref.go` — `ParsePRRef`, `DetectRepoContext`, `ReadBody`
  - `types.go` — `OpError`, `ReviewThreadWithPolicy`, `BulkResult`, `BulkFailure`, `BulkSkip`
  - `api.go` — `API` interface (so `Resolve` etc. can be unit-tested with a fake)
  - `ops.go` — `List`, `Next`, `Reply`
  - `resolve.go` — `Resolve` (complex; lives in its own file)
  - `bulk.go` — `ResolveAll`, `UnresolveAll`
  - `prref_test.go`, `ops_test.go`, `resolve_test.go`, `bulk_test.go`

- `internal/forksops/`
  - `stream.go` — `Options`, `Result`, `Stream`
  - `stream_test.go`

- `cmd/spn/`
  - `main.go` — argv parsing, subcommand dispatch, help/version
  - `threads.go` — thread verb handlers
  - `pr.go` — pr status handler
  - `forks.go` — forks list handler (NDJSON)
  - `main_test.go`, `threads_test.go`, `forks_test.go` — black-box exec tests

**Modified files:**

- `internal/github/threads.go` — `ReplyToThread` returns full `ThreadComment` instead of just an ID.
- `internal/github/client.go` (or new `current_user.go`) — adds `Client.CurrentUserLogin(ctx)`.
- `cmd/spoon/threads.go` — refactored to delegate to `threadsops`; no observable behavior change.
- `README.md` — install instructions for `spn` and link to spec.

---

## Task 1: agentio — JSON writers

**Files:**
- Create: `internal/agentio/writer.go`
- Test: `internal/agentio/writer_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/agentio/writer_test.go
package agentio

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteJSON_object(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, map[string]int{"n": 1}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	got := b.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("expected trailing newline, got %q", got)
	}
	if !strings.Contains(got, `"n": 1`) && !strings.Contains(got, `"n":1`) {
		t.Errorf("missing key, got %q", got)
	}
}

func TestWriteNDJSON_multipleObjects(t *testing.T) {
	var b bytes.Buffer
	_ = WriteNDJSON(&b, map[string]string{"id": "a"})
	_ = WriteNDJSON(&b, map[string]string{"id": "b"})
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), b.String())
	}
}

func TestWriteNull(t *testing.T) {
	var b bytes.Buffer
	if err := WriteNull(&b); err != nil {
		t.Fatalf("WriteNull: %v", err)
	}
	if b.String() != "null\n" {
		t.Errorf("want %q, got %q", "null\n", b.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agentio/...`
Expected: build error — `WriteJSON`/`WriteNDJSON`/`WriteNull` undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/agentio/writer.go
package agentio

import (
	"encoding/json"
	"io"
)

// WriteJSON marshals v as JSON with 2-space indent and a trailing newline.
// Use this for single-value success output on stdout.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteNDJSON marshals v as a single-line JSON object followed by '\n'.
// Use this for streaming output where each line is a complete value.
func WriteNDJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	return enc.Encode(v)
}

// WriteNull writes the literal "null\n". Use for spn threads next when no
// thread remains.
func WriteNull(w io.Writer) error {
	_, err := io.WriteString(w, "null\n")
	return err
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/agentio/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentio/writer.go internal/agentio/writer_test.go
git commit -m "Add agentio JSON writers for spn output"
```

---

## Task 2: agentio — error envelope and exit codes

**Files:**
- Create: `internal/agentio/error.go`
- Test: `internal/agentio/error_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/agentio/error_test.go
package agentio

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestError_Emit_basic(t *testing.T) {
	var b bytes.Buffer
	exit := NewError(CodeBadInput, "missing PR ref", "Run spn threads list --help").Emit(&b)
	if exit != 2 {
		t.Errorf("bad_input should exit 2, got %d", exit)
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(b.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON on stderr: %v\n%s", err, b.String())
	}
	e := env["error"]
	if e["code"] != "bad_input" {
		t.Errorf("code=%v", e["code"])
	}
	if e["retryable"] != false {
		t.Errorf("retryable should default to false for bad_input")
	}
	if !strings.Contains(e["remediation"].(string), "spn threads list --help") {
		t.Errorf("remediation lost: %v", e["remediation"])
	}
}

func TestError_Emit_rateLimited_retryable(t *testing.T) {
	var b bytes.Buffer
	exit := NewError(CodeRateLimited, "limit hit", "wait").WithRetryAfter(60).Emit(&b)
	if exit != 1 {
		t.Errorf("rate_limited exits 1, got %d", exit)
	}
	var env map[string]map[string]any
	_ = json.Unmarshal(b.Bytes(), &env)
	e := env["error"]
	if e["retryable"] != true {
		t.Errorf("rate_limited should be retryable")
	}
	if e["retry_after_seconds"].(float64) != 60 {
		t.Errorf("retry_after_seconds=%v", e["retry_after_seconds"])
	}
}

func TestError_WithDetails(t *testing.T) {
	var b bytes.Buffer
	NewError(CodeNotFound, "thread missing", "verify ID").
		WithDetails(map[string]any{"thread_id": "PRRT_xyz"}).
		Emit(&b)
	var env map[string]map[string]any
	_ = json.Unmarshal(b.Bytes(), &env)
	d := env["error"]["details"].(map[string]any)
	if d["thread_id"] != "PRRT_xyz" {
		t.Errorf("details.thread_id=%v", d["thread_id"])
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/agentio/...`
Expected: build errors — undefined symbols.

- [ ] **Step 3: Write the implementation**

```go
// internal/agentio/error.go
package agentio

import (
	"encoding/json"
	"io"
)

// Code is the stable error category surfaced in the JSON envelope.
type Code string

const (
	CodeBadInput     Code = "bad_input"
	CodeAuthRequired Code = "auth_required"
	CodeAuthScope    Code = "auth_scope_missing"
	CodePolicy       Code = "policy_violation"
	CodeNotFound     Code = "not_found"
	CodeUpstream     Code = "upstream_error"
	CodeRateLimited  Code = "rate_limited"
	CodeInternal     Code = "internal"
)

// Error is the spn-side error envelope. Build with NewError and call Emit to
// write JSON to stderr and obtain the process exit code.
type Error struct {
	Code              Code
	Message           string
	Remediation       string
	Retryable         bool
	RetryAfterSeconds *int
	Details           map[string]any
}

// NewError constructs an Error. Retryable defaults from the code.
func NewError(code Code, message, remediation string) *Error {
	return &Error{
		Code:        code,
		Message:     message,
		Remediation: remediation,
		Retryable:   defaultRetryable(code),
	}
}

// WithDetails attaches a details map. Returns the receiver for chaining.
func (e *Error) WithDetails(d map[string]any) *Error {
	e.Details = d
	return e
}

// WithRetryAfter attaches retry_after_seconds.
func (e *Error) WithRetryAfter(seconds int) *Error {
	e.RetryAfterSeconds = &seconds
	return e
}

// Emit writes the envelope as JSON to w and returns the process exit code.
func (e *Error) Emit(w io.Writer) int {
	body := map[string]any{
		"code":        string(e.Code),
		"message":     e.Message,
		"remediation": e.Remediation,
		"retryable":   e.Retryable,
	}
	if e.RetryAfterSeconds != nil {
		body["retry_after_seconds"] = *e.RetryAfterSeconds
	}
	if e.Details != nil {
		body["details"] = e.Details
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{"error": body})
	return ExitCode(e.Code)
}

// ExitCode returns the process exit code for a given code.
func ExitCode(c Code) int {
	switch c {
	case CodeBadInput, CodeAuthRequired, CodeAuthScope, CodePolicy:
		return 2
	default:
		return 1
	}
}

func defaultRetryable(c Code) bool {
	switch c {
	case CodeRateLimited, CodeUpstream:
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/agentio/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentio/error.go internal/agentio/error_test.go
git commit -m "Add agentio error envelope with stable code vocabulary"
```

---

## Task 3: agentio — remediation patterns

**Files:**
- Create: `internal/agentio/remediations.go`

- [ ] **Step 1: Write the failing test** (extend `error_test.go`)

```go
// Append to internal/agentio/error_test.go
func TestRemediation_authRequired(t *testing.T) {
	r := RemediationAuthRequired()
	if !strings.Contains(r, "gh auth login") {
		t.Errorf("RemediationAuthRequired should mention gh auth login: %q", r)
	}
}

func TestRemediation_policyBodyRequired(t *testing.T) {
	r := RemediationPolicyBodyRequired("owner/repo#42", "PRRT_xyz")
	if !strings.Contains(r, "owner/repo#42") || !strings.Contains(r, "PRRT_xyz") {
		t.Errorf("placeholders not substituted: %q", r)
	}
}

func TestRemediation_authScope(t *testing.T) {
	r := RemediationAuthScope("repo")
	if !strings.Contains(r, "gh auth refresh -s repo") {
		t.Errorf("scope placeholder not substituted: %q", r)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Expected: undefined `RemediationAuthRequired` etc.

- [ ] **Step 3: Implementation**

```go
// internal/agentio/remediations.go
package agentio

import "fmt"

// RemediationBadInput names the subcommand the user should consult.
// noun and verb may be empty for top-level usage errors.
func RemediationBadInput(noun, verb string) string {
	cmd := "spn"
	if noun != "" {
		cmd += " " + noun
		if verb != "" {
			cmd += " " + verb
		}
	}
	return fmt.Sprintf("Run `%s --help` to see accepted forms. PR refs accept `owner/repo#42`, a full URL, or `#42` from inside a git checkout.", cmd)
}

func RemediationAuthRequired() string {
	return "Authenticate with `gh auth login` (GitHub) or set `GITLAB_TOKEN` (GitLab), then retry."
}

func RemediationAuthScope(scope string) string {
	return fmt.Sprintf("Refresh your token with the required scope: `gh auth refresh -s %s`, then retry.", scope)
}

func RemediationPolicyBodyRequired(prRef, threadID string) string {
	return fmt.Sprintf("Resolve with an explanation: `spn threads resolve %s %s --body \"<what you fixed, or why no change was needed>\"`.", prRef, threadID)
}

func RemediationPolicyBulkHumanThreads(prRef string) string {
	return fmt.Sprintf("Some threads need individual responses. List them with `spn threads list %s`, then resolve each with `spn threads resolve %s <id> --body \"...\"`. Bulk-resolve will not touch human-raised threads.", prRef, prRef)
}

func RemediationNotFound() string {
	return "Verify the PR / repo / thread exists and that your token has access. PR refs and IDs are case-sensitive."
}

func RemediationUpstream() string {
	return "Provider API failed. Retry in a few seconds. If persistent, check the provider's status page."
}

func RemediationRateLimited(resetAt string, retryAfterSec int) string {
	return fmt.Sprintf("Rate limit exceeded. Wait until %s (%ds), then retry. Authenticate (`gh auth login`) for a higher limit.", resetAt, retryAfterSec)
}

func RemediationResolvePartialFailure(prRef, threadID string) string {
	return fmt.Sprintf("Retry: `spn threads resolve %s %s` (omit --body; the comment is already posted).", prRef, threadID)
}

func RemediationInternal() string {
	return "Unexpected error. Re-run with the same arguments; if it persists, report at the project's issue tracker with the full stderr output."
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/agentio/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentio/remediations.go internal/agentio/error_test.go
git commit -m "Add agentio remediation patterns with named placeholders"
```

---

## Task 4: Hoist PR-ref parsing into threadsops

**Files:**
- Create: `internal/threadsops/prref.go`
- Create: `internal/threadsops/prref_test.go`
- Modify: `cmd/spoon/threads.go` (delegate to threadsops)

- [ ] **Step 1: Write the failing test**

```go
// internal/threadsops/prref_test.go
package threadsops

import "testing"

func TestParsePRRef_ownerRepoHash(t *testing.T) {
	owner, repo, n, err := ParsePRRef("owner/repo#42", "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if owner != "owner" || repo != "repo" || n != 42 {
		t.Errorf("got (%q,%q,%d)", owner, repo, n)
	}
}

func TestParsePRRef_url(t *testing.T) {
	owner, repo, n, err := ParsePRRef("https://github.com/owner/repo/pull/123", "", "")
	if err != nil || owner != "owner" || repo != "repo" || n != 123 {
		t.Errorf("got (%q,%q,%d) err=%v", owner, repo, n, err)
	}
}

func TestParsePRRef_hashOnly_withFallback(t *testing.T) {
	owner, repo, n, err := ParsePRRef("#5", "fb", "fr")
	if err != nil || owner != "fb" || repo != "fr" || n != 5 {
		t.Errorf("got (%q,%q,%d) err=%v", owner, repo, n, err)
	}
}

func TestParsePRRef_hashOnly_noFallback(t *testing.T) {
	_, _, _, err := ParsePRRef("#5", "", "")
	if err == nil {
		t.Error("expected error when no fallback")
	}
}

func TestParsePRRef_gitlabURLRejected(t *testing.T) {
	_, _, _, err := ParsePRRef("https://gitlab.com/g/r/merge_requests/1", "", "")
	if err == nil {
		t.Error("expected error for non-github URL")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/threadsops/...`
Expected: undefined `ParsePRRef`.

- [ ] **Step 3: Move parsing code** — copy the body of `ParsePRRef` and `detectRepoContext` and `readBody` from `cmd/spoon/threads.go` into the new package.

```go
// internal/threadsops/prref.go
package threadsops

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
)

// DetectRepoContext returns owner and repo by parsing the local origin remote.
// Returns ("", "") on any error.
func DetectRepoContext() (owner, repo string) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", ""
	}
	provider, _, o, r, perr := forge.ParseRepoURL(strings.TrimSpace(string(out)), "github.com", 0)
	if perr != nil || provider != forge.ProviderGitHub {
		return "", ""
	}
	return o, r
}

// ParsePRRef parses a PR reference. Accepts owner/repo#N, full github.com URL,
// or #N (resolved via fallback).
func ParsePRRef(s, fallbackOwner, fallbackRepo string) (owner, repo string, number int, err error) {
	if s == "" {
		return "", "", 0, fmt.Errorf("empty PR ref")
	}
	if strings.HasPrefix(s, "#") {
		n, e := strconv.Atoi(s[1:])
		if e != nil {
			return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
		}
		if fallbackOwner == "" || fallbackRepo == "" {
			return "", "", 0, fmt.Errorf("%q has no repo context (run inside a git checkout or pass owner/repo#N)", s)
		}
		return fallbackOwner, fallbackRepo, n, nil
	}
	if strings.Contains(s, "://") {
		u, e := url.Parse(s)
		if e != nil {
			return "", "", 0, fmt.Errorf("parse %q: %w", s, e)
		}
		if strings.ToLower(u.Hostname()) != "github.com" {
			return "", "", 0, fmt.Errorf("only github.com PR URLs are supported, got %q", u.Hostname())
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 4 || parts[2] != "pull" {
			return "", "", 0, fmt.Errorf("URL %q is not a github.com PR URL", s)
		}
		n, e := strconv.Atoi(parts[3])
		if e != nil {
			return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
		}
		return parts[0], parts[1], n, nil
	}
	hash := strings.LastIndex(s, "#")
	if hash < 0 {
		return "", "", 0, fmt.Errorf("PR ref %q missing '#N' suffix", s)
	}
	n, e := strconv.Atoi(s[hash+1:])
	if e != nil {
		return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
	}
	repoPart := s[:hash]
	slash := strings.IndexByte(repoPart, '/')
	if slash < 0 {
		return "", "", 0, fmt.Errorf("PR ref %q missing 'owner/repo' prefix", s)
	}
	return repoPart[:slash], repoPart[slash+1:], n, nil
}

// ReadBody returns the body content from path; path "-" reads stdin.
func ReadBody(path string) (string, error) {
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

- [ ] **Step 4: Update `cmd/spoon/threads.go`** to delegate

Replace the local `parsePRRef`, `detectRepoContext`, and `readBody` functions with thin shims calling `threadsops.ParsePRRef` / `threadsops.DetectRepoContext` / `threadsops.ReadBody`. Update imports.

```go
// At top of cmd/spoon/threads.go, add:
import threadsops "github.com/svnbjrn/spoon/internal/threadsops"

// Replace existing detectRepoContext, parsePRRef, readBody with:
func detectRepoContext() (owner, repo string)              { return threadsops.DetectRepoContext() }
func parsePRRef(s, fo, fr string) (string, string, int, error) { return threadsops.ParsePRRef(s, fo, fr) }
func readBody(path string) (string, error)                 { return threadsops.ReadBody(path) }
```

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: PASS — both the new threadsops tests and existing `cmd/spoon` tests.

- [ ] **Step 6: Commit**

```bash
git add internal/threadsops/prref.go internal/threadsops/prref_test.go cmd/spoon/threads.go
git commit -m "Hoist PR-ref parsing into internal/threadsops"
```

---

## Task 5: github.Client.CurrentUserLogin

**Files:**
- Modify: `internal/github/client.go` (add field + method)
- Create or modify: `internal/github/current_user.go`
- Modify: `internal/github/client_test.go` (or new test file)

- [ ] **Step 1: Write the failing test**

```go
// internal/github/current_user_test.go
package github

import (
	"context"
	"testing"
)

func TestCurrentUserLogin_unauthenticated(t *testing.T) {
	c := &Client{authenticated: false}
	if _, err := c.CurrentUserLogin(context.Background()); err == nil {
		t.Error("expected error when unauthenticated")
	}
}

func TestCurrentUserLogin_cached(t *testing.T) {
	c := &Client{authenticated: true, currentUserLogin: "octocat"}
	got, err := c.CurrentUserLogin(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "octocat" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/github/...`
Expected: undefined `CurrentUserLogin` / `currentUserLogin`.

- [ ] **Step 3: Implementation**

Add a `currentUserLogin string` field to `Client` struct in `client.go` (alongside `authenticated`):

```go
// In internal/github/client.go, inside the Client struct (around line 19):
type Client struct {
    rest             *ghAPI.RESTClient
    gql              *ghAPI.GraphQLClient
    authenticated    bool
    currentUserLogin string  // populated lazily by CurrentUserLogin

    mu        sync.Mutex
    rateLimit RateLimit
}
```

Then add the method in a new file:

```go
// internal/github/current_user.go
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// CurrentUserLogin returns the authenticated user's GitHub login (e.g. "octocat").
// The first call hits the API; subsequent calls return the cached value.
// Returns an error when unauthenticated or when the API call fails.
func (c *Client) CurrentUserLogin(ctx context.Context) (string, error) {
	if !c.authenticated {
		return "", fmt.Errorf("not authenticated")
	}
	c.mu.Lock()
	cached := c.currentUserLogin
	c.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	resp, err := c.GetRaw(ctx, "user")
	if err != nil {
		return "", fmt.Errorf("fetch current user: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read user body: %w", err)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", fmt.Errorf("parse user: %w", err)
	}
	if u.Login == "" {
		return "", fmt.Errorf("empty login in user response")
	}
	c.mu.Lock()
	c.currentUserLogin = u.Login
	c.mu.Unlock()
	return u.Login, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/github/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/github/client.go internal/github/current_user.go internal/github/current_user_test.go
git commit -m "Add Client.CurrentUserLogin for authenticated user lookup"
```

---

## Task 6: ReplyToThread returns full ThreadComment

**Files:**
- Modify: `internal/github/threads.go`
- Modify: `cmd/spoon/threads.go` (call sites)
- Modify: `internal/github/threads_test.go` (existing tests)

Today `ReplyToThread` returns `(string, error)` where the string is the comment ID. `spn threads reply` needs to return the full comment. Extend the mutation and the return type.

- [ ] **Step 1: Write the failing test**

```go
// Append to internal/github/threads_test.go
func TestReplyToThread_returnsThreadComment(t *testing.T) {
	t.Skip("integration — covered by cmd/spn end-to-end tests; here we only verify the type signature compiles")
}
```

The substantive test is exercised through `cmd/spn` E2E. The point of this step is forcing the compilation to break.

- [ ] **Step 2: Run, verify fail**

Run: `go build ./...`
Expected: fine right now (signature unchanged); the build break appears after step 3.

- [ ] **Step 3: Update `ReplyToThread`** in `internal/github/threads.go`

```go
// ReplyToThread appends a reply comment to a review thread.
func (c *Client) ReplyToThread(ctx context.Context, threadID, body string) (ThreadComment, error) {
	if c.gql == nil {
		return ThreadComment{}, fmt.Errorf("GraphQL client not available (auth required)")
	}
	const mutation = `
mutation($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: { pullRequestReviewThreadId: $threadId, body: $body }) {
    comment {
      id
      body
      createdAt
      author { __typename login }
    }
  }
}`
	var resp struct {
		AddPullRequestReviewThreadReply struct {
			Comment struct {
				ID        string `json:"id"`
				Body      string `json:"body"`
				CreatedAt string `json:"createdAt"`
				Author    struct {
					Typename string `json:"__typename"`
					Login    string `json:"login"`
				} `json:"author"`
			} `json:"comment"`
		} `json:"addPullRequestReviewThreadReply"`
	}
	vars := map[string]interface{}{"threadId": threadID, "body": body}
	if err := c.gql.DoWithContext(ctx, mutation, vars, &resp); err != nil {
		return ThreadComment{}, fmt.Errorf("reply to thread %s: %w", threadID, err)
	}
	c2 := resp.AddPullRequestReviewThreadReply.Comment
	return ThreadComment{
		ID:         c2.ID,
		Body:       c2.Body,
		CreatedAt:  c2.CreatedAt,
		Author:     c2.Author.Login,
		AuthorType: c2.Author.Typename,
	}, nil
}
```

- [ ] **Step 4: Fix call sites in `cmd/spoon/threads.go`**

Replace `if _, err := client.ReplyToThread(ctx, ...)` with `if _, err := client.ReplyToThread(ctx, ...)` (the underscore still works — but verify the type usage is consistent). Search for every call site and confirm the discarded return is now a `ThreadComment` value, not a string. No semantic change needed for spoon.

```bash
grep -n "ReplyToThread" cmd/spoon/
```

For each match, leave the call as `_, err := client.ReplyToThread(...)` — the change in return type is transparent because the value is discarded.

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: PASS — including existing `cmd/spoon` tests.

- [ ] **Step 6: Commit**

```bash
git add internal/github/threads.go internal/github/threads_test.go
git commit -m "ReplyToThread returns full ThreadComment for spn output"
```

---

## Task 7: threadsops types

**Files:**
- Create: `internal/threadsops/types.go`
- Create: `internal/threadsops/api.go`

- [ ] **Step 1: Define types**

```go
// internal/threadsops/types.go
package threadsops

import (
	"github.com/svnbjrn/spoon/internal/github"
)

// ReviewThreadWithPolicy embeds github.ReviewThread plus a derived requiresBody
// field for agent-facing JSON output.
type ReviewThreadWithPolicy struct {
	github.ReviewThread
	RequiresBody bool `json:"requiresBody"`
}

// AnnotateWithPolicy wraps a slice of ReviewThread into the policy-annotated form.
func AnnotateWithPolicy(in []github.ReviewThread) []ReviewThreadWithPolicy {
	out := make([]ReviewThreadWithPolicy, len(in))
	for i, t := range in {
		out[i] = ReviewThreadWithPolicy{ReviewThread: t, RequiresBody: t.RequiresBody()}
	}
	return out
}

// AnnotateOneWithPolicy returns a single annotated thread or nil.
func AnnotateOneWithPolicy(t *github.ReviewThread) *ReviewThreadWithPolicy {
	if t == nil {
		return nil
	}
	return &ReviewThreadWithPolicy{ReviewThread: *t, RequiresBody: t.RequiresBody()}
}

// BulkResult is the spn-facing outcome of resolve-all / unresolve-all.
type BulkResult struct {
	Succeeded []string         `json:"succeeded"`
	Failed    []BulkFailure    `json:"failed"`
	Skipped   []BulkSkip       `json:"skipped"`
}

// BulkFailure captures one failure inside a bulk operation.
type BulkFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// BulkSkip captures one thread skipped by policy.
type BulkSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// OpError carries a stable code, retryability flag, and details. Callers
// translate this into the appropriate agentio.Error (spn) or text + exit code
// (spoon).
type OpError struct {
	Code      string
	Message   string
	Retryable bool
	Details   map[string]any
}

// Op error codes — mirror agentio.Code values exactly so translation is trivial.
const (
	OpCodeBadInput     = "bad_input"
	OpCodeAuthRequired = "auth_required"
	OpCodeAuthScope    = "auth_scope_missing"
	OpCodePolicy       = "policy_violation"
	OpCodeNotFound     = "not_found"
	OpCodeUpstream     = "upstream_error"
	OpCodeRateLimited  = "rate_limited"
	OpCodeInternal     = "internal"
)
```

- [ ] **Step 2: Define API interface**

```go
// internal/threadsops/api.go
package threadsops

import (
	"context"

	"github.com/svnbjrn/spoon/internal/github"
)

// API is the subset of *github.Client that threadsops needs. *github.Client
// satisfies this directly; tests use a fake implementation.
type API interface {
	FetchPR(ctx context.Context, owner, repo string, number int, states string) (github.PullRequestStatus, []github.ReviewThread, error)
	ReplyToThread(ctx context.Context, threadID, body string) (github.ThreadComment, error)
	ResolveThread(ctx context.Context, threadID string) error
	UnresolveThread(ctx context.Context, threadID string) error
	ResolveAllThreads(ctx context.Context, owner, repo string, number, workers int) (*github.BulkResult, error)
	UnresolveAllThreads(ctx context.Context, owner, repo string, number, workers int) (*github.BulkResult, error)
	CurrentUserLogin(ctx context.Context) (string, error)
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add internal/threadsops/types.go internal/threadsops/api.go
git commit -m "Add threadsops types and API interface for testable ops"
```

---

## Task 8: threadsops.List and threadsops.Next

**Files:**
- Create: `internal/threadsops/ops.go`
- Create: `internal/threadsops/ops_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/threadsops/ops_test.go
package threadsops

import (
	"context"
	"errors"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

type fakeAPI struct {
	status  github.PullRequestStatus
	threads []github.ReviewThread
	err     error
}

func (f *fakeAPI) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	return f.status, f.threads, f.err
}
func (f *fakeAPI) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) { return github.ThreadComment{}, nil }
func (f *fakeAPI) ResolveThread(_ context.Context, _ string) error                            { return nil }
func (f *fakeAPI) UnresolveThread(_ context.Context, _ string) error                          { return nil }
func (f *fakeAPI) ResolveAllThreads(_ context.Context, _, _ string, _, _ int) (*github.BulkResult, error) { return nil, nil }
func (f *fakeAPI) UnresolveAllThreads(_ context.Context, _, _ string, _, _ int) (*github.BulkResult, error) { return nil, nil }
func (f *fakeAPI) CurrentUserLogin(_ context.Context) (string, error)                         { return "", errors.New("not stubbed") }

func TestList_annotatesPolicy(t *testing.T) {
	f := &fakeAPI{threads: []github.ReviewThread{
		{ID: "PRRT_1", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_2", Comments: []github.ThreadComment{{AuthorType: "User"}}},
	}}
	_, threads, opErr := List(context.Background(), f, "o", "r", 1, false)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if len(threads) != 2 {
		t.Fatalf("want 2, got %d", len(threads))
	}
	if threads[0].RequiresBody {
		t.Errorf("bot thread should not require body")
	}
	if !threads[1].RequiresBody {
		t.Errorf("user thread should require body")
	}
}

func TestNext_returnsOldestUnresolved_tiebreakerByID(t *testing.T) {
	f := &fakeAPI{threads: []github.ReviewThread{
		{ID: "PRRT_b", IsResolved: false, Comments: []github.ThreadComment{{CreatedAt: "2026-05-10T10:00:00Z", AuthorType: "User"}}},
		{ID: "PRRT_a", IsResolved: false, Comments: []github.ThreadComment{{CreatedAt: "2026-05-10T10:00:00Z", AuthorType: "User"}}},
		{ID: "PRRT_c", IsResolved: true, Comments: []github.ThreadComment{{CreatedAt: "2026-05-10T09:00:00Z", AuthorType: "User"}}},
	}}
	_, got, opErr := Next(context.Background(), f, "o", "r", 1)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got == nil {
		t.Fatal("expected a thread, got nil")
	}
	if got.ID != "PRRT_a" {
		t.Errorf("tiebreaker should pick PRRT_a, got %q", got.ID)
	}
}

func TestNext_noUnresolved_returnsNil(t *testing.T) {
	f := &fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_1", IsResolved: true}}}
	_, got, opErr := Next(context.Background(), f, "o", "r", 1)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Expected: undefined `List`, `Next`.

- [ ] **Step 3: Implementation**

```go
// internal/threadsops/ops.go
package threadsops

import (
	"context"
	"sort"

	"github.com/svnbjrn/spoon/internal/github"
)

// List returns the PR status plus all threads (annotated with requiresBody).
// includeResolved=false filters to unresolved only.
func List(ctx context.Context, api API, owner, repo string, number int, includeResolved bool) (github.PullRequestStatus, []ReviewThreadWithPolicy, *OpError) {
	states := github.ThreadStateUnresolved
	if includeResolved {
		states = github.ThreadStateAll
	}
	status, raw, err := api.FetchPR(ctx, owner, repo, number, states)
	if err != nil {
		return github.PullRequestStatus{}, nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	return status, AnnotateWithPolicy(raw), nil
}

// Next returns the oldest unresolved thread (sorted by firstCommentCreatedAt
// ASC, then threadID ASC for determinism), or nil.
func Next(ctx context.Context, api API, owner, repo string, number int) (github.PullRequestStatus, *ReviewThreadWithPolicy, *OpError) {
	status, raw, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateUnresolved)
	if err != nil {
		return github.PullRequestStatus{}, nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	unresolved := make([]github.ReviewThread, 0, len(raw))
	for _, t := range raw {
		if !t.IsResolved {
			unresolved = append(unresolved, t)
		}
	}
	if len(unresolved) == 0 {
		return status, nil, nil
	}
	sort.Slice(unresolved, func(i, j int) bool {
		ai, aj := firstCommentTime(unresolved[i]), firstCommentTime(unresolved[j])
		if ai != aj {
			return ai < aj
		}
		return unresolved[i].ID < unresolved[j].ID
	})
	return status, AnnotateOneWithPolicy(&unresolved[0]), nil
}

func firstCommentTime(t github.ReviewThread) string {
	if len(t.Comments) == 0 {
		return ""
	}
	return t.Comments[0].CreatedAt
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/threadsops/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/threadsops/ops.go internal/threadsops/ops_test.go
git commit -m "Add threadsops.List and threadsops.Next with tiebreaker"
```

---

## Task 9: threadsops.Reply

**Files:**
- Modify: `internal/threadsops/ops.go`
- Modify: `internal/threadsops/ops_test.go`

- [ ] **Step 1: Add the failing test**

```go
// Append to internal/threadsops/ops_test.go
func TestReply_returnsComment(t *testing.T) {
	f := &fakeAPI{}
	// override ReplyToThread via subclassing
	api := &replyFake{fakeAPI: *f, want: github.ThreadComment{ID: "PRC_x", Body: "ok"}}
	got, opErr := Reply(context.Background(), api, "PRRT_1", "ok")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got.ID != "PRC_x" {
		t.Errorf("got %+v", got)
	}
}

type replyFake struct {
	fakeAPI
	want github.ThreadComment
}

func (r *replyFake) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	return r.want, nil
}
```

- [ ] **Step 2: Run, verify fail**

Expected: undefined `Reply`.

- [ ] **Step 3: Add `Reply` to `ops.go`**

```go
// Reply posts a comment on a thread. Returns the new comment.
func Reply(ctx context.Context, api API, threadID, body string) (github.ThreadComment, *OpError) {
	if body == "" {
		return github.ThreadComment{}, &OpError{Code: OpCodeBadInput, Message: "body is required for reply"}
	}
	c, err := api.ReplyToThread(ctx, threadID, body)
	if err != nil {
		return github.ThreadComment{}, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	return c, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/threadsops/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/threadsops/ops.go internal/threadsops/ops_test.go
git commit -m "Add threadsops.Reply"
```

---

## Task 10: threadsops.Resolve — main flow + idempotency + body-required gate

**Files:**
- Create: `internal/threadsops/resolve.go`
- Create: `internal/threadsops/resolve_test.go`

This task covers single-thread resolve under non-failure conditions. Partial-failure handling (comment posted, resolve failed) is Task 11.

- [ ] **Step 1: Write failing tests**

```go
// internal/threadsops/resolve_test.go
package threadsops

import (
	"context"
	"errors"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

// resolveFake extends fakeAPI with call tracking and per-thread ResolveThread control.
type resolveFake struct {
	fakeAPI
	currentUser    string
	replyCalls     int
	replyComment   github.ThreadComment
	resolveCalls   int
	resolveErr     error
}

func (r *resolveFake) CurrentUserLogin(_ context.Context) (string, error) {
	if r.currentUser == "" {
		return "", errors.New("no user")
	}
	return r.currentUser, nil
}
func (r *resolveFake) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	r.replyCalls++
	return r.replyComment, nil
}
func (r *resolveFake) ResolveThread(_ context.Context, _ string) error {
	r.resolveCalls++
	return r.resolveErr
}

func TestResolve_threadNotFound(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a"}}}}
	_, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_missing", "")
	if opErr == nil || opErr.Code != OpCodeNotFound {
		t.Errorf("expected not_found, got %+v", opErr)
	}
}

func TestResolve_alreadyResolved_idempotent(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", IsResolved: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}}}
	got, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got == nil || got.ID != "PRRT_a" {
		t.Errorf("expected current state, got %+v", got)
	}
	if f.resolveCalls != 0 {
		t.Errorf("should not call ResolveThread when already resolved")
	}
}

func TestResolve_botThread_noBodyNeeded(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}}}
	got, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got == nil || got.RequiresBody {
		t.Errorf("expected bot thread, no body required")
	}
	if f.resolveCalls != 1 {
		t.Errorf("expected one ResolveThread call, got %d", f.resolveCalls)
	}
}

func TestResolve_humanThread_missingBody_policyViolation(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}}}
	_, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr == nil || opErr.Code != OpCodePolicy {
		t.Errorf("expected policy_violation, got %+v", opErr)
	}
	if f.resolveCalls != 0 {
		t.Errorf("should not resolve when policy violated")
	}
}

func TestResolve_humanThread_withBody_postsAndResolves(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}}}
	_, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "fixed it")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if f.replyCalls != 1 || f.resolveCalls != 1 {
		t.Errorf("expected one reply + one resolve, got %d/%d", f.replyCalls, f.resolveCalls)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Expected: undefined `Resolve`.

- [ ] **Step 3: Implementation** (partial — Task 11 adds partial-failure + body-satisfied dedup)

```go
// internal/threadsops/resolve.go
package threadsops

import (
	"context"

	"github.com/svnbjrn/spoon/internal/github"
)

// Resolve resolves a single thread. Behavior:
//   - Returns OpCodeNotFound if the thread isn't on the PR.
//   - If already resolved, returns the current state and does nothing (idempotent).
//   - If the thread requires a body and none is provided, returns OpCodePolicy
//     unless the body-satisfied case applies (see Task 11).
//   - If body is non-empty, posts the comment, then resolves. On partial
//     failure, returns a structured error (see Task 11).
func Resolve(ctx context.Context, api API, owner, repo string, number int, threadID, body string) (*ReviewThreadWithPolicy, *OpError) {
	_, all, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateAll)
	if err != nil {
		return nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	var target *github.ReviewThread
	for i := range all {
		if all[i].ID == threadID {
			target = &all[i]
			break
		}
	}
	if target == nil {
		return nil, &OpError{Code: OpCodeNotFound, Message: "thread " + threadID + " not found on PR", Details: map[string]any{"thread_id": threadID}}
	}
	annotated := AnnotateOneWithPolicy(target)
	if target.IsResolved {
		return annotated, nil
	}
	if annotated.RequiresBody && body == "" {
		return nil, &OpError{
			Code:    OpCodePolicy,
			Message: "thread has a human commenter; --body is required",
			Details: map[string]any{"thread_id": threadID},
		}
	}
	if body != "" {
		if _, rerr := api.ReplyToThread(ctx, threadID, body); rerr != nil {
			return nil, &OpError{Code: OpCodeUpstream, Message: "reply failed: " + rerr.Error(), Retryable: true}
		}
	}
	if rerr := api.ResolveThread(ctx, threadID); rerr != nil {
		return nil, &OpError{Code: OpCodeUpstream, Message: "resolve failed: " + rerr.Error(), Retryable: true}
	}
	annotated.IsResolved = true
	return annotated, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/threadsops/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/threadsops/resolve.go internal/threadsops/resolve_test.go
git commit -m "Add threadsops.Resolve with idempotency and body-required policy"
```

---

## Task 11: threadsops.Resolve — partial-failure handling and body-satisfied dedup

**Files:**
- Modify: `internal/threadsops/resolve.go`
- Modify: `internal/threadsops/resolve_test.go`

- [ ] **Step 1: Add failing tests**

```go
// Append to internal/threadsops/resolve_test.go
func TestResolve_partialFailure_commentPosted(t *testing.T) {
	f := &resolveFake{
		fakeAPI:      fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}},
		replyComment: github.ThreadComment{ID: "PRC_new"},
		resolveErr:   errors.New("graphql error"),
	}
	_, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "fixed it")
	if opErr == nil || opErr.Code != OpCodeUpstream {
		t.Fatalf("expected upstream_error, got %+v", opErr)
	}
	if opErr.Details["comment_posted"] != true {
		t.Errorf("expected details.comment_posted=true, got %+v", opErr.Details)
	}
	if opErr.Details["comment_id"] != "PRC_new" {
		t.Errorf("expected details.comment_id, got %+v", opErr.Details)
	}
}

func TestResolve_bodySatisfied_byCurrentUser(t *testing.T) {
	// requiresBody=true, no --body, but most recent comment is by the agent.
	f := &resolveFake{
		fakeAPI: fakeAPI{threads: []github.ReviewThread{{
			ID:       "PRRT_a",
			Comments: []github.ThreadComment{
				{AuthorType: "User", Author: "alice", CreatedAt: "2026-05-10T09:00:00Z"},
				{AuthorType: "User", Author: "agent-bot", CreatedAt: "2026-05-10T10:00:00Z"},
			},
		}}},
		currentUser: "agent-bot",
	}
	got, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("expected success, got %+v", opErr)
	}
	if got == nil {
		t.Fatal("expected thread, got nil")
	}
	if f.replyCalls != 0 {
		t.Errorf("expected no reply call (body satisfied), got %d", f.replyCalls)
	}
	if f.resolveCalls != 1 {
		t.Errorf("expected one resolve call, got %d", f.resolveCalls)
	}
}

func TestResolve_bodySatisfied_byRecencyFallback(t *testing.T) {
	// requiresBody=true, no --body, currentUser lookup fails, but most recent
	// comment is within the last 60s.
	recent := time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339)
	f := &resolveFake{
		fakeAPI: fakeAPI{threads: []github.ReviewThread{{
			ID:       "PRRT_a",
			Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice", CreatedAt: recent}},
		}}},
		currentUser: "", // lookup fails
	}
	got, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("expected success via recency fallback, got %+v", opErr)
	}
	if got == nil {
		t.Fatal("expected thread, got nil")
	}
}
```

Add `import "time"` to the file if not present.

- [ ] **Step 2: Run, verify fail**

Expected: tests 1-3 fail — no partial-failure handling, no body-satisfied logic.

- [ ] **Step 3: Update `Resolve`**

Replace the implementation in `internal/threadsops/resolve.go`:

```go
package threadsops

import (
	"context"
	"time"

	"github.com/svnbjrn/spoon/internal/github"
)

const bodySatisfiedRecencyWindow = 60 * time.Second

func Resolve(ctx context.Context, api API, owner, repo string, number int, threadID, body string) (*ReviewThreadWithPolicy, *OpError) {
	_, all, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateAll)
	if err != nil {
		return nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	var target *github.ReviewThread
	for i := range all {
		if all[i].ID == threadID {
			target = &all[i]
			break
		}
	}
	if target == nil {
		return nil, &OpError{Code: OpCodeNotFound, Message: "thread " + threadID + " not found on PR", Details: map[string]any{"thread_id": threadID}}
	}
	annotated := AnnotateOneWithPolicy(target)
	if target.IsResolved {
		return annotated, nil
	}
	if annotated.RequiresBody && body == "" {
		if !bodySatisfied(ctx, api, target) {
			return nil, &OpError{
				Code:    OpCodePolicy,
				Message: "thread has a human commenter; --body is required",
				Details: map[string]any{"thread_id": threadID},
			}
		}
	}
	commentID := ""
	if body != "" {
		c, rerr := api.ReplyToThread(ctx, threadID, body)
		if rerr != nil {
			return nil, &OpError{Code: OpCodeUpstream, Message: "reply failed: " + rerr.Error(), Retryable: true}
		}
		commentID = c.ID
	}
	if rerr := api.ResolveThread(ctx, threadID); rerr != nil {
		details := map[string]any{"thread_id": threadID}
		if commentID != "" {
			details["comment_posted"] = true
			details["comment_id"] = commentID
		}
		return nil, &OpError{
			Code:      OpCodeUpstream,
			Message:   "comment posted but resolve failed: " + rerr.Error(),
			Retryable: true,
			Details:   details,
		}
	}
	annotated.IsResolved = true
	return annotated, nil
}

// bodySatisfied returns true when the body-required gate should be skipped
// because the agent has already explained itself on this thread.
//
// Primary signal: most recent comment is authored by the current authenticated
// user. Fallback (when CurrentUserLogin fails or returns empty): most recent
// comment is within bodySatisfiedRecencyWindow.
func bodySatisfied(ctx context.Context, api API, t *github.ReviewThread) bool {
	if len(t.Comments) == 0 {
		return false
	}
	last := t.Comments[len(t.Comments)-1]

	if login, err := api.CurrentUserLogin(ctx); err == nil && login != "" {
		return last.Author == login
	}

	createdAt, err := time.Parse(time.RFC3339, last.CreatedAt)
	if err != nil {
		return false
	}
	return time.Since(createdAt) <= bodySatisfiedRecencyWindow
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/threadsops/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/threadsops/resolve.go internal/threadsops/resolve_test.go
git commit -m "Add Resolve partial-failure and body-satisfied dedup"
```

---

## Task 12: threadsops.ResolveAll with skipHumanThreads

**Files:**
- Create: `internal/threadsops/bulk.go`
- Create: `internal/threadsops/bulk_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/threadsops/bulk_test.go
package threadsops

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

// bulkFake collects per-thread resolve calls.
type bulkFake struct {
	fakeAPI
	resolved map[string]bool
	failOn   map[string]error
}

func newBulkFake(threads []github.ReviewThread) *bulkFake {
	return &bulkFake{
		fakeAPI:  fakeAPI{threads: threads},
		resolved: map[string]bool{},
		failOn:   map[string]error{},
	}
}

func (b *bulkFake) ResolveThread(_ context.Context, id string) error {
	if err, ok := b.failOn[id]; ok {
		return err
	}
	b.resolved[id] = true
	return nil
}
func (b *bulkFake) UnresolveThread(_ context.Context, id string) error {
	b.resolved[id] = false
	return nil
}

func TestResolveAll_skipsHumanThreads(t *testing.T) {
	threads := []github.ReviewThread{
		{ID: "PRRT_bot", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_user", Comments: []github.ThreadComment{{AuthorType: "User"}}},
	}
	b := newBulkFake(threads)
	res, opErr := ResolveAll(context.Background(), b, "o", "r", 1, true)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !equalUnordered(res.Succeeded, []string{"PRRT_bot"}) {
		t.Errorf("succeeded: %+v", res.Succeeded)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ID != "PRRT_user" || res.Skipped[0].Reason != "requires_body" {
		t.Errorf("skipped: %+v", res.Skipped)
	}
	if b.resolved["PRRT_user"] {
		t.Errorf("must not resolve human thread")
	}
}

func TestResolveAll_legacyMode_resolvesHumanThreads(t *testing.T) {
	threads := []github.ReviewThread{{ID: "PRRT_user", Comments: []github.ThreadComment{{AuthorType: "User"}}}}
	b := newBulkFake(threads)
	res, opErr := ResolveAll(context.Background(), b, "o", "r", 1, false)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("legacy mode should not skip, got %+v", res.Skipped)
	}
	if !b.resolved["PRRT_user"] {
		t.Errorf("legacy mode should resolve human thread")
	}
}

func TestResolveAll_perThreadFailure(t *testing.T) {
	threads := []github.ReviewThread{
		{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_b", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}
	b := newBulkFake(threads)
	b.failOn["PRRT_b"] = errors.New("boom")
	res, _ := ResolveAll(context.Background(), b, "o", "r", 1, true)
	if !equalUnordered(res.Succeeded, []string{"PRRT_a"}) {
		t.Errorf("succeeded: %+v", res.Succeeded)
	}
	if len(res.Failed) != 1 || res.Failed[0].ID != "PRRT_b" {
		t.Errorf("failed: %+v", res.Failed)
	}
}

func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run, verify fail**

Expected: undefined `ResolveAll`.

- [ ] **Step 3: Implementation**

```go
// internal/threadsops/bulk.go
package threadsops

import (
	"context"
	"sync"

	"github.com/svnbjrn/spoon/internal/github"
)

const defaultBulkWorkers = 4

// ResolveAll resolves every unresolved thread on the PR. When skipHumanThreads
// is true (the spn policy), threads where RequiresBody=true are added to
// res.Skipped with reason "requires_body" instead of being resolved. The
// legacy spoon behavior is preserved by passing skipHumanThreads=false.
func ResolveAll(ctx context.Context, api API, owner, repo string, number int, skipHumanThreads bool) (*BulkResult, *OpError) {
	_, raw, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateUnresolved)
	if err != nil {
		return nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	res := &BulkResult{Succeeded: []string{}, Failed: []BulkFailure{}, Skipped: []BulkSkip{}}
	var toResolve []string
	for _, t := range raw {
		if skipHumanThreads && t.RequiresBody() {
			res.Skipped = append(res.Skipped, BulkSkip{ID: t.ID, Reason: "requires_body"})
			continue
		}
		toResolve = append(toResolve, t.ID)
	}
	runBulk(ctx, api, toResolve, true, res)
	return res, nil
}

// UnresolveAll unresolves every resolved thread on the PR. Skipped is always empty.
func UnresolveAll(ctx context.Context, api API, owner, repo string, number int) (*BulkResult, *OpError) {
	_, raw, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateResolved)
	if err != nil {
		return nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	res := &BulkResult{Succeeded: []string{}, Failed: []BulkFailure{}, Skipped: []BulkSkip{}}
	ids := make([]string, len(raw))
	for i, t := range raw {
		ids[i] = t.ID
	}
	runBulk(ctx, api, ids, false, res)
	return res, nil
}

func runBulk(ctx context.Context, api API, ids []string, resolve bool, res *BulkResult) {
	var mu sync.Mutex
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < defaultBulkWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				var err error
				if resolve {
					err = api.ResolveThread(ctx, id)
				} else {
					err = api.UnresolveThread(ctx, id)
				}
				mu.Lock()
				if err != nil {
					res.Failed = append(res.Failed, BulkFailure{ID: id, Error: err.Error()})
				} else {
					res.Succeeded = append(res.Succeeded, id)
				}
				mu.Unlock()
			}
		}()
	}
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/threadsops/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/threadsops/bulk.go internal/threadsops/bulk_test.go
git commit -m "Add threadsops.ResolveAll and UnresolveAll with skipHumanThreads"
```

---

## Task 13: threadsops.UnresolveAll test coverage

Covered in Task 12. Skip to Task 14.

---

## Task 14: Refactor cmd/spoon/threads.go to use threadsops

**Files:**
- Modify: `cmd/spoon/threads.go`
- Verify: `cmd/spoon/threads_test.go` still passes unmodified.

The goal: spoon's CLI behavior is unchanged. The case bodies of `runThreads`'s switch call into `threadsops.*` instead of calling the github client directly. Bulk modes pass `skipHumanThreads=false` to preserve the legacy permissive behavior.

- [ ] **Step 1: Read current `cmd/spoon/threads.go` thoroughly**

Run: `cat cmd/spoon/threads.go | head -200` and `cat cmd/spoon/threads.go | tail -200`. Identify the case blocks for modeJSON, modeNext, modeReply, modeResolve, modeResolveAll, modeUnresolveAll.

- [ ] **Step 2: Rewrite each case to delegate**

For example, `modeJSON` becomes:

```go
case modeJSON:
    includeResolved := flags.includeResolved
    status, threads, opErr := threadsops.List(ctx, client, owner, repo, number, includeResolved)
    if opErr != nil {
        fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
        return 1
    }
    emitStatus(os.Stderr, status, number, flags.noStatus)
    // emitJSON expects []gh.ReviewThread; pass the embedded ones for backward compat.
    raw := make([]gh.ReviewThread, len(threads))
    for i, t := range threads {
        raw[i] = t.ReviewThread
    }
    if err := emitJSON(os.Stdout, raw); err != nil {
        fmt.Fprintln(os.Stderr, "❌ Error:", err)
        return 1
    }
    return 0
```

`modeNext`:

```go
case modeNext:
    status, t, opErr := threadsops.Next(ctx, client, owner, repo, number)
    if opErr != nil {
        fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
        return 1
    }
    emitStatus(os.Stderr, status, number, flags.noStatus)
    if t == nil {
        if _, err := io.WriteString(os.Stdout, "null\n"); err != nil {
            return 1
        }
        return 0
    }
    enc := json.NewEncoder(os.Stdout)
    enc.SetIndent("", "  ")
    return errInt(enc.Encode(t.ReviewThread))
```

`modeReply`:

```go
case modeReply:
    // Spoon still wants the status header — fetch separately.
    status, _, ferr := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
    if ferr != nil {
        fmt.Fprintln(os.Stderr, "❌ Error:", ferr)
        return 1
    }
    emitStatus(os.Stdout, status, number, flags.noStatus)
    if _, opErr := threadsops.Reply(ctx, client, flags.targetID, flags.body); opErr != nil {
        fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
        return 1
    }
    return 0
```

`modeResolve` — this is the case that changes the most. The legacy behavior (body required when human commenter exists) is preserved by `threadsops.Resolve` itself:

```go
case modeResolve:
    status, _, ferr := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
    if ferr != nil {
        fmt.Fprintln(os.Stderr, "❌ Error:", ferr)
        return 1
    }
    emitStatus(os.Stdout, status, number, flags.noStatus)
    _, opErr := threadsops.Resolve(ctx, client, owner, repo, number, flags.targetID, flags.body)
    if opErr != nil {
        switch opErr.Code {
        case threadsops.OpCodeNotFound:
            fmt.Fprintf(os.Stderr, "❌ Error: thread %s not found on PR\n", flags.targetID)
            return 1
        case threadsops.OpCodePolicy:
            fmt.Fprintln(os.Stderr, "❌ Error: thread has a non-bot reviewer; --body (or --body-file) is required")
            return 2
        default:
            fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
            return 1
        }
    }
    return 0
```

`modeResolveAll` and `modeUnresolveAll`:

```go
case modeResolveAll:
    status, _, ferr := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
    if ferr != nil {
        fmt.Fprintln(os.Stderr, "❌ Error:", ferr)
        return 1
    }
    emitStatus(os.Stdout, status, number, flags.noStatus)
    res, opErr := threadsops.ResolveAll(ctx, client, owner, repo, number, false) // legacy mode
    if opErr != nil {
        fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
        return 1
    }
    fmt.Printf("✅ resolved %d threads\n", len(res.Succeeded))
    if len(res.Failed) > 0 {
        for _, f := range res.Failed {
            fmt.Fprintf(os.Stderr, "❌ failed %s: %v\n", f.ID, f.Error)
        }
        return 1
    }
    return 0
```

And the unresolve-all case is symmetric.

Helper:

```go
func errInt(err error) int {
    if err != nil {
        fmt.Fprintln(os.Stderr, "❌ Error:", err)
        return 1
    }
    return 0
}
```

- [ ] **Step 3: Update imports** in `cmd/spoon/threads.go`

Add `"io"` (for the null write), `"encoding/json"` if missing, and `threadsops "github.com/svnbjrn/spoon/internal/threadsops"`.

- [ ] **Step 4: Run all tests**

Run: `go test ./...`
Expected: PASS — both threadsops tests and existing `cmd/spoon` tests unmodified.

- [ ] **Step 5: Manual sanity check** (no live network)

Run: `go build ./cmd/spoon && ./spoon threads --help`
Expected: help text identical to before.

- [ ] **Step 6: Commit**

```bash
git add cmd/spoon/threads.go
git commit -m "Refactor cmd/spoon/threads.go to delegate to threadsops"
```

---

## Task 15: internal/forksops — streaming fetch/score/enrich

**Files:**
- Create: `internal/forksops/stream.go`
- Create: `internal/forksops/stream_test.go`

The streaming pipeline is a refactor of the existing logic in `internal/dump/dump.go::Run` and the TUI's enrichment path. For v1, `internal/dump` keeps its own (batched) implementation; `forksops.Stream` is a parallel implementation used only by `spn`. The implementation plan does NOT touch `internal/dump`. This keeps spoon's `--json/--csv` output identical.

- [ ] **Step 1: Write failing test with a fake forge**

```go
// internal/forksops/stream_test.go
package forksops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

type fakeForge struct {
	parent     forge.ParentData
	parentErr  error
	forks      []forge.T1Data
	forkErrors map[string]error
	t2         map[string]forge.T2Data
	t3         map[string]forge.T3Data
}

func (f *fakeForge) Auth(_ context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{Tier: forge.AuthCLI, Concurrency: 2}, nil
}
func (f *fakeForge) Parent(_ context.Context, _, _ string) (forge.ParentData, error) {
	return f.parent, f.parentErr
}
func (f *fakeForge) ListForks(_ context.Context, _, _ string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, len(f.forks))
	for _, fk := range f.forks {
		ch <- forge.ForkMsg{Fork: fk}
	}
	close(ch)
	return ch, nil
}
func (f *fakeForge) Branches(_ context.Context, fk forge.T1Data, _ int) ([]forge.BranchRef, error) {
	return []forge.BranchRef{{Name: fk.DefaultBranch}}, nil
}
func (f *fakeForge) Compare(_ context.Context, fk forge.T1Data, _ string) (forge.T2Data, error) {
	if err, ok := f.forkErrors[fk.ID]; ok {
		return forge.T2Data{}, err
	}
	return f.t2[fk.ID], nil
}
func (f *fakeForge) Contributors(_ context.Context, fk forge.T1Data) (forge.T3Data, error) {
	return f.t3[fk.ID], nil
}
func (f *fakeForge) Headroom() float64 { return 1.0 }

func TestStream_emitsAllForks(t *testing.T) {
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
			{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now()},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 1})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	count := 0
	for r := range ch {
		if r.Err != nil {
			t.Errorf("per-fork err: %+v", r.Err)
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 results, got %d", count)
	}
}

func TestStream_perForkError_continuesStream(t *testing.T) {
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
			{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now()},
		},
		forkErrors: map[string]error{"o/a": errors.New("compare failed")},
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	var errs, ok int
	for r := range ch {
		if r.Err != nil {
			errs++
		} else {
			ok++
		}
	}
	if errs != 1 || ok != 1 {
		t.Errorf("expected 1 err + 1 ok, got %d/%d", errs, ok)
	}
}

func TestStream_parentErr_isFatal(t *testing.T) {
	ff := &fakeForge{parentErr: errors.New("nope")}
	_, err := Stream(context.Background(), ff, "o", "r", Options{})
	if err == nil {
		t.Error("expected fatal error when Parent fails")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Expected: undefined `Stream`, `Options`, `Result`.

- [ ] **Step 3: Implementation**

```go
// internal/forksops/stream.go
package forksops

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// Options controls the streaming pipeline.
type Options struct {
	Refresh      bool
	Tier         int // 1 = surface only, 2 = + compare, 3 = + contributors. 0 = full.
	TopN         int
	BotAllowlist map[string]bool
	HeatWeights  map[string]float64
}

// Result is a single fork's outcome. Exactly one of Fork or Err is meaningful;
// Fork is always populated when Err == nil, and may still be partially
// populated when Err != nil (e.g. T1 succeeded, T2 failed).
type Result struct {
	Fork forge.T1Data
	T2   *forge.T2Data
	T3   *forge.T3Data
	Heat heat.HeatResult
	Err  *Error
}

// Error is the per-fork error reported on the stream. Distinct from a fatal
// error which is returned synchronously from Stream() itself.
type Error struct {
	Code    string
	Message string
	Details map[string]any
}

// Stream returns a channel that yields one Result per fork. The channel is
// closed when enumeration completes or ctx is cancelled.
//
// Fatal errors (auth, Parent fetch failure, ctx cancel before any output)
// are returned synchronously. Per-fork errors are surfaced via Result.Err.
func Stream(ctx context.Context, provider forge.Forge, owner, repo string, opts Options) (<-chan Result, error) {
	parent, err := provider.Parent(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("fetch parent: %w", err)
	}
	t1ch, err := provider.ListForks(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("list forks: %w", err)
	}

	out := make(chan Result)
	go func() {
		defer close(out)
		var t1Forks []forge.T1Data
		for msg := range t1ch {
			if msg.Err != nil {
				continue
			}
			t1Forks = append(t1Forks, msg.Fork)
		}

		// Score T1, sort, optionally truncate to TopN.
		stats := makeStats(t1Forks, parent)
		scorer := heat.NewScorer(stats)

		type scored struct {
			fork forge.T1Data
			res  heat.HeatResult
		}
		all := make([]scored, len(t1Forks))
		now := time.Now()
		for i, f := range t1Forks {
			input := buildScoreInput(f, parent, now)
			all[i] = scored{fork: f, res: scorer.ScoreRaw(input)}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].res.Score > all[j].res.Score })
		topN := opts.TopN
		if topN <= 0 || topN > len(all) {
			topN = len(all)
		}

		// Emit each fork. Top-N receive T2/T3 enrichment; the rest are T1 only.
		tier := opts.Tier
		if tier == 0 {
			tier = 3
		}
		concurrency := 4
		if a, _ := provider.Auth(ctx); a.Concurrency > 0 {
			concurrency = a.Concurrency
		}
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		for i, s := range all {
			i := i
			s := s
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				r := Result{Fork: s.fork, Heat: s.res}
				if tier >= 2 && i < topN {
					t2, terr := provider.Compare(ctx, s.fork, s.fork.DefaultBranch)
					if terr != nil {
						r.Err = &Error{Code: "upstream_error", Message: terr.Error(), Details: map[string]any{"fork": s.fork.ID, "stage": "compare"}}
					} else {
						r.T2 = &t2
					}
				}
				if tier >= 3 && i < topN && r.Err == nil {
					t3, terr := provider.Contributors(ctx, s.fork)
					if terr != nil {
						r.Err = &Error{Code: "upstream_error", Message: terr.Error(), Details: map[string]any{"fork": s.fork.ID, "stage": "contributors"}}
					} else {
						r.T3 = &t3
					}
				}
				select {
				case out <- r:
				case <-ctx.Done():
				}
			}()
		}
		wg.Wait()
	}()
	return out, nil
}

func makeStats(forks []forge.T1Data, parent forge.ParentData) []heat.ForkStats {
	stats := make([]heat.ForkStats, len(forks))
	for i, f := range forks {
		stats[i] = heat.ForkStats{ID: int64(i), Stars: f.Stars, SubForks: f.SubForkCount}
	}
	_ = parent
	return stats
}

func buildScoreInput(f forge.T1Data, parent forge.ParentData, now time.Time) heat.ScoreInput {
	return heat.ScoreInput{
		T1: heat.Tier1ParamsV2{
			Stars:             f.Stars,
			SubForks:          f.SubForkCount,
			ReleaseCount:      f.ReleaseCount,
			DaysSincePush:     now.Sub(f.PushedAt).Hours() / 24,
			DaysSinceUpstream: now.Sub(parent.PushedAt).Hours() / 24,
			Archived:          f.IsArchived,
			Now:               now,
		},
	}
}
```

NOTE: the exact `heat.ForkStats` field layout — verify by grepping `internal/heat/percentile.go` for the struct definition. Adjust `makeStats` to match. The implementing engineer should also check `internal/dump/dump.go` for the reference shape of how `Scorer` is wired up against real fork data and align field handling.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/forksops/...`
Expected: PASS for the three tests above.

- [ ] **Step 5: Commit**

```bash
git add internal/forksops/stream.go internal/forksops/stream_test.go
git commit -m "Add internal/forksops.Stream for NDJSON fork enrichment"
```

---

## Task 16: cmd/spn main, dispatch, help, version

**Files:**
- Create: `cmd/spn/main.go`

- [ ] **Step 1: Implementation**

```go
// cmd/spn/main.go
package main

import (
	"fmt"
	"os"
)

const version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "--help":
		printHelp()
		os.Exit(0)
	case "-v", "--version":
		fmt.Printf("spn %s\n", version)
		os.Exit(0)
	case "threads":
		os.Exit(runThreads(os.Args[2:]))
	case "pr":
		os.Exit(runPR(os.Args[2:]))
	case "forks":
		os.Exit(runForks(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "spn: unknown subcommand %q\n", os.Args[1])
		printHelp()
		os.Exit(2)
	}
}

func printHelp() {
	fmt.Print(`spn — agent-shaped CLI for spoon

Usage:
  spn <noun> <verb> [args]

Nouns and verbs:
  threads list <pr-ref> [--all]
  threads next <pr-ref>
  threads reply <pr-ref> <thread-id> --body T | --body-file PATH
  threads resolve <pr-ref> <thread-id> [--body T | --body-file PATH]
  threads resolve-all <pr-ref>
  threads unresolve-all <pr-ref>
  pr status <pr-ref>
  forks list <repo> [--tier 1|2|3] [--top N] [--heat-weights PATH] [--bot-allowlist L] [--refresh] [--forge github|gitlab] [--forge-host H]

PR refs accept:
  owner/repo#42
  https://github.com/owner/repo/pull/42
  #42                  (uses local repo context)

Output:
  Success: bare JSON on stdout (single value for reads; NDJSON for forks list).
  Failure: structured JSON envelope on stderr with code, message, remediation,
           retryable, optional retry_after_seconds, and details.

Exit codes: 0 success; 2 user error / policy; 1 everything else.

Authentication: gh auth login (GitHub).
`)
}
```

Add `runThreads`, `runPR`, `runForks` as stubs that emit an "unimplemented" error and exit 1:

```go
// In cmd/spn/threads.go (stub):
package main

import (
	"fmt"
	"os"
)

func runThreads(args []string) int {
	fmt.Fprintln(os.Stderr, "spn threads: unimplemented")
	return 1
}
```

Same pattern for `runPR` (cmd/spn/pr.go) and `runForks` (cmd/spn/forks.go).

- [ ] **Step 2: Verify build**

Run: `go build ./cmd/spn`
Expected: produces a `spn` binary in the project root.

- [ ] **Step 3: Smoke test help**

Run: `./spn --help`
Expected: help text printed; exit code 0.

Run: `./spn`
Expected: help printed; exit code 2.

Run: `./spn nonsense`
Expected: "unknown subcommand" on stderr; exit 2.

- [ ] **Step 4: Commit**

```bash
git add cmd/spn/main.go cmd/spn/threads.go cmd/spn/pr.go cmd/spn/forks.go
git commit -m "Scaffold cmd/spn binary with subcommand dispatch"
```

---

## Task 17: spn threads list / next

**Files:**
- Modify: `cmd/spn/threads.go`
- Create: `cmd/spn/threads_test.go`

- [ ] **Step 1: Write end-to-end test using a fake **client** wrapped by main**

Building a full E2E test against the real binary requires a fake GitHub client and exec-style assertions. For this task, write a unit-level test in the `cmd/spn` package that exercises the dispatcher with a fake API. Use the same `fakeAPI` pattern from `internal/threadsops` (copy/adapt or expose via threadsops/threadsoptest helpers).

Add an exported test helper:

```go
// internal/threadsops/testing.go (new file inside the package, no _test.go suffix so it can be imported from cmd/spn tests)
package threadsops

// NewFakeAPI returns a customizable API for testing. Internal use only.
func NewFakeAPI() *FakeAPI { return &FakeAPI{} }

type FakeAPI struct {
	// Public fields — set them before passing to ops.
	Status  github.PullRequestStatus
	Threads []github.ReviewThread
	... (mirror the per-method fields shown in internal/threadsops/resolve_test.go)
}
```

Actually — to avoid a public testing surface, keep the test inside the `cmd/spn` package and have it inject the API through a small function variable:

```go
// In cmd/spn/threads.go, declare:
var apiFactory = func() (threadsops.API, *agentio.Error) {
    client, status, err := gh.CheckAuth()
    if err != nil {
        return nil, agentio.NewError(agentio.CodeAuthRequired, "github auth: "+err.Error(), agentio.RemediationAuthRequired())
    }
    if !client.IsAuthenticated() {
        return nil, agentio.NewError(agentio.CodeAuthRequired, "not authenticated", agentio.RemediationAuthRequired())
    }
    if !status.HasScope("repo") {
        return nil, agentio.NewError(agentio.CodeAuthScope, "missing 'repo' scope", agentio.RemediationAuthScope("repo"))
    }
    return client, nil
}
```

Tests can replace `apiFactory` with one returning a fake.

```go
// cmd/spn/threads_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

type stubAPI struct {
	threads []github.ReviewThread
}

func (s *stubAPI) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	return github.PullRequestStatus{Title: "test"}, s.threads, nil
}
// other methods returning zero values...

func TestSpnThreadsList_emitsJSONArray(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{threads: []github.ReviewThread{{ID: "PRRT_1", Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON array: %v\n%s", err, stdout.String())
	}
	if len(got) != 1 || got[0]["id"] != "PRRT_1" {
		t.Errorf("unexpected stdout: %+v", got)
	}
	if got[0]["requiresBody"] != false {
		t.Errorf("requiresBody should be present and false for bot thread")
	}
}
```

This requires the dispatcher to accept injectable stdout/stderr — refactor accordingly.

- [ ] **Step 2: Implement `runThreads` and `runThreadsWith`**

```go
// cmd/spn/threads.go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

var apiFactory = func() (threadsops.API, *agentio.Error) {
	client, status, err := gh.CheckAuth()
	if err != nil {
		return nil, agentio.NewError(agentio.CodeAuthRequired, "github auth: "+err.Error(), agentio.RemediationAuthRequired())
	}
	if !client.IsAuthenticated() {
		return nil, agentio.NewError(agentio.CodeAuthRequired, "not authenticated", agentio.RemediationAuthRequired())
	}
	if !status.HasScope("repo") {
		return nil, agentio.NewError(agentio.CodeAuthScope, "missing 'repo' scope", agentio.RemediationAuthScope("repo"))
	}
	return client, nil
}

func runThreads(args []string) int { return runThreadsWith(args, os.Stdout, os.Stderr) }

func runThreadsWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (list|next|reply|resolve|resolve-all|unresolve-all)", agentio.RemediationBadInput("threads", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		return doThreadsList(rest, stdout, stderr)
	case "next":
		return doThreadsNext(rest, stdout, stderr)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("threads", "")).Emit(stderr)
	}
}

func doThreadsList(args []string, stdout, stderr io.Writer) int {
	var prRef string
	includeResolved := false
	for _, a := range args {
		switch {
		case a == "--all":
			includeResolved = true
		case strings.HasPrefix(a, "--"):
			return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+a, agentio.RemediationBadInput("threads", "list")).Emit(stderr)
		default:
			if prRef != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+a, agentio.RemediationBadInput("threads", "list")).Emit(stderr)
			}
			prRef = a
		}
	}
	if prRef == "" {
		return agentio.NewError(agentio.CodeBadInput, "missing PR reference", agentio.RemediationBadInput("threads", "list")).Emit(stderr)
	}
	owner, repo, number, ok := resolvePRRef(prRef, stderr)
	if !ok {
		return 2
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	_, threads, opErr := threadsops.List(context.Background(), api, owner, repo, number, includeResolved)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if err := agentio.WriteJSON(stdout, threads); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doThreadsNext(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads next <pr-ref>", agentio.RemediationBadInput("threads", "next")).Emit(stderr)
	}
	owner, repo, number, ok := resolvePRRef(args[0], stderr)
	if !ok {
		return 2
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	_, t, opErr := threadsops.Next(context.Background(), api, owner, repo, number)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if t == nil {
		_ = agentio.WriteNull(stdout)
		return 0
	}
	if err := agentio.WriteJSON(stdout, t); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func resolvePRRef(prRef string, stderr io.Writer) (owner, repo string, number int, ok bool) {
	fbO, fbR := threadsops.DetectRepoContext()
	o, r, n, err := threadsops.ParsePRRef(prRef, fbO, fbR)
	if err != nil {
		agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "")).Emit(stderr)
		return "", "", 0, false
	}
	return o, r, n, true
}

// translateOpErr turns a threadsops.OpError into the corresponding agentio.Error and emits it.
func translateOpErr(op *threadsops.OpError, stderr io.Writer) int {
	code := agentio.Code(op.Code)
	rem := ""
	switch code {
	case agentio.CodeNotFound:
		rem = agentio.RemediationNotFound()
	case agentio.CodeUpstream:
		rem = agentio.RemediationUpstream()
	case agentio.CodePolicy:
		// caller (resolve / resolve-all) overrides with a more specific remediation
		rem = "Provide additional context with --body."
	default:
		rem = "Re-run with --help for usage details."
	}
	e := agentio.NewError(code, op.Message, rem)
	if op.Details != nil {
		e = e.WithDetails(op.Details)
	}
	return e.Emit(stderr)
}

// Print usage to stderr for fmt.Errorf placeholders that need a fallback writer
func usageStderr(msg string) { fmt.Fprintln(os.Stderr, msg) }
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/spn/...`
Expected: the `TestSpnThreadsList_emitsJSONArray` test passes.

- [ ] **Step 4: Manual smoke test (offline)**

Run: `./spn threads list` (no args)
Expected: error envelope JSON on stderr, exit 2.

- [ ] **Step 5: Commit**

```bash
git add cmd/spn/threads.go cmd/spn/threads_test.go
git commit -m "Add spn threads list and next"
```

---

## Task 18: spn threads reply

**Files:**
- Modify: `cmd/spn/threads.go`
- Modify: `cmd/spn/threads_test.go`

- [ ] **Step 1: Add the failing test**

```go
// Append to cmd/spn/threads_test.go
type replyStub struct {
	stubAPI
	posted github.ThreadComment
}

func (r *replyStub) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	return r.posted, nil
}

func TestSpnThreadsReply_emitsComment(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &replyStub{posted: github.ThreadComment{ID: "PRC_new", Body: "ack"}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"reply", "owner/repo#1", "PRRT_1", "--body", "ack"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if got["id"] != "PRC_new" {
		t.Errorf("got %+v", got)
	}
}
```

- [ ] **Step 2: Implementation** — add `doThreadsReply` and dispatch

In `runThreadsWith`, add `case "reply": return doThreadsReply(rest, stdout, stderr)`.

```go
func doThreadsReply(args []string, stdout, stderr io.Writer) int {
	var prRef, threadID, body, bodyFile string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--body":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body requires a value", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
			i++
			body = args[i]
		case "--body-file":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body-file requires a path", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
			i++
			bodyFile = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
			if prRef == "" {
				prRef = args[i]
			} else if threadID == "" {
				threadID = args[i]
			} else {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
		}
	}
	if prRef == "" || threadID == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads reply <pr-ref> <thread-id> --body T", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
	}
	if bodyFile != "" && body == "" {
		b, err := threadsops.ReadBody(bodyFile)
		if err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
		}
		body = b
	}
	if body == "" {
		return agentio.NewError(agentio.CodeBadInput, "--body or --body-file is required", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
	}
	_, _, _, ok := resolvePRRef(prRef, stderr)
	if !ok {
		return 2
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	comment, opErr := threadsops.Reply(context.Background(), api, threadID, body)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if err := agentio.WriteJSON(stdout, comment); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/spn/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/spn/threads.go cmd/spn/threads_test.go
git commit -m "Add spn threads reply"
```

---

## Task 19: spn threads resolve

**Files:**
- Modify: `cmd/spn/threads.go`
- Modify: `cmd/spn/threads_test.go`

- [ ] **Step 1: Add tests**

```go
// Append to cmd/spn/threads_test.go
type resolveStub struct {
	stubAPI
	currentUser  string
	posted       github.ThreadComment
	resolveErr   error
}

func (r *resolveStub) CurrentUserLogin(_ context.Context) (string, error) {
	if r.currentUser == "" {
		return "", errors.New("no user")
	}
	return r.currentUser, nil
}
func (r *resolveStub) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	return r.posted, nil
}
func (r *resolveStub) ResolveThread(_ context.Context, _ string) error {
	return r.resolveErr
}

func TestSpnThreadsResolve_policyViolation(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &resolveStub{stubAPI: stubAPI{threads: []github.ReviewThread{
			{ID: "PRRT_1", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}},
		}}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve", "owner/repo#1", "PRRT_1"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on error, got %q", stdout.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "policy_violation" {
		t.Errorf("code=%v", env["error"]["code"])
	}
	rem, _ := env["error"]["remediation"].(string)
	if !strings.Contains(rem, "owner/repo#1") || !strings.Contains(rem, "PRRT_1") {
		t.Errorf("remediation missing placeholders: %q", rem)
	}
}

func TestSpnThreadsResolve_partialFailure(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &resolveStub{
			stubAPI: stubAPI{threads: []github.ReviewThread{{ID: "PRRT_1", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}},
			posted:  github.ThreadComment{ID: "PRC_new"},
			resolveErr: errors.New("graphql 500"),
		}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve", "owner/repo#1", "PRRT_1", "--body", "ack"}, &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var env map[string]map[string]any
	_ = json.Unmarshal(stderr.Bytes(), &env)
	if env["error"]["retryable"] != true {
		t.Errorf("expected retryable")
	}
	d := env["error"]["details"].(map[string]any)
	if d["comment_posted"] != true {
		t.Errorf("expected comment_posted=true")
	}
}
```

Imports needed in this test file: `errors`, `strings`.

- [ ] **Step 2: Implementation** — add `doThreadsResolve` and dispatch

Add `case "resolve": return doThreadsResolve(rest, stdout, stderr)` and:

```go
func doThreadsResolve(args []string, stdout, stderr io.Writer) int {
	var prRef, threadID, body, bodyFile string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--body":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body requires a value", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			i++
			body = args[i]
		case "--body-file":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body-file requires a path", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			i++
			bodyFile = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			if prRef == "" {
				prRef = args[i]
			} else if threadID == "" {
				threadID = args[i]
			} else {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
		}
	}
	if prRef == "" || threadID == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads resolve <pr-ref> <thread-id> [--body T]", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
	}
	if bodyFile != "" && body == "" {
		b, err := threadsops.ReadBody(bodyFile)
		if err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
		}
		body = b
	}
	owner, repo, number, ok := resolvePRRef(prRef, stderr)
	if !ok {
		return 2
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	t, opErr := threadsops.Resolve(context.Background(), api, owner, repo, number, threadID, body)
	if opErr != nil {
		return translateResolveErr(opErr, prRef, threadID, stderr)
	}
	if err := agentio.WriteJSON(stdout, t); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

// translateResolveErr produces a resolve-specific remediation.
func translateResolveErr(op *threadsops.OpError, prRef, threadID string, stderr io.Writer) int {
	code := agentio.Code(op.Code)
	var rem string
	switch code {
	case agentio.CodePolicy:
		rem = agentio.RemediationPolicyBodyRequired(prRef, threadID)
	case agentio.CodeNotFound:
		rem = agentio.RemediationNotFound()
	case agentio.CodeUpstream:
		if op.Details != nil && op.Details["comment_posted"] == true {
			rem = agentio.RemediationResolvePartialFailure(prRef, threadID)
		} else {
			rem = agentio.RemediationUpstream()
		}
	default:
		rem = agentio.RemediationInternal()
	}
	e := agentio.NewError(code, op.Message, rem)
	if op.Details != nil {
		e = e.WithDetails(op.Details)
	}
	return e.Emit(stderr)
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/spn/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/spn/threads.go cmd/spn/threads_test.go
git commit -m "Add spn threads resolve with policy and partial-failure remediation"
```

---

## Task 20: spn threads resolve-all and unresolve-all

**Files:**
- Modify: `cmd/spn/threads.go`
- Modify: `cmd/spn/threads_test.go`

- [ ] **Step 1: Add the failing test**

```go
// Append to cmd/spn/threads_test.go
type bulkStub struct {
	stubAPI
	resolveCalls map[string]bool
}

func (b *bulkStub) ResolveThread(_ context.Context, id string) error {
	if b.resolveCalls == nil {
		b.resolveCalls = map[string]bool{}
	}
	b.resolveCalls[id] = true
	return nil
}
func (b *bulkStub) UnresolveThread(_ context.Context, id string) error { return nil }

func TestSpnThreadsResolveAll_skipsHumanThreads(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &bulkStub{stubAPI: stubAPI{threads: []github.ReviewThread{
			{ID: "PRRT_bot", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
			{ID: "PRRT_user", Comments: []github.ThreadComment{{AuthorType: "User"}}},
		}}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve-all", "owner/repo#1"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	_ = json.Unmarshal(stdout.Bytes(), &got)
	succeeded, _ := got["succeeded"].([]any)
	skipped, _ := got["skipped"].([]any)
	if len(succeeded) != 1 || succeeded[0] != "PRRT_bot" {
		t.Errorf("succeeded=%+v", succeeded)
	}
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skipped, got %+v", skipped)
	}
	sk := skipped[0].(map[string]any)
	if sk["id"] != "PRRT_user" || sk["reason"] != "requires_body" {
		t.Errorf("skipped item: %+v", sk)
	}
}
```

- [ ] **Step 2: Implementation**

Add to dispatcher: `case "resolve-all": return doThreadsResolveAll(rest, stdout, stderr)` and `case "unresolve-all": return doThreadsUnresolveAll(rest, stdout, stderr)`.

```go
func doThreadsResolveAll(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads resolve-all <pr-ref>", agentio.RemediationBadInput("threads", "resolve-all")).Emit(stderr)
	}
	owner, repo, number, ok := resolvePRRef(args[0], stderr)
	if !ok {
		return 2
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	res, opErr := threadsops.ResolveAll(context.Background(), api, owner, repo, number, true)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if err := agentio.WriteJSON(stdout, res); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doThreadsUnresolveAll(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads unresolve-all <pr-ref>", agentio.RemediationBadInput("threads", "unresolve-all")).Emit(stderr)
	}
	owner, repo, number, ok := resolvePRRef(args[0], stderr)
	if !ok {
		return 2
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	res, opErr := threadsops.UnresolveAll(context.Background(), api, owner, repo, number)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if err := agentio.WriteJSON(stdout, res); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/spn/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/spn/threads.go cmd/spn/threads_test.go
git commit -m "Add spn threads resolve-all and unresolve-all"
```

---

## Task 21: spn pr status

**Files:**
- Modify: `cmd/spn/pr.go`
- Create: `cmd/spn/pr_test.go`

- [ ] **Step 1: Add the failing test**

```go
// cmd/spn/pr_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

func TestSpnPRStatus_emitsJSON(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{}, nil
	}
	// Override the fetch to return a populated status.
	prevFetch := fetchPRStatus
	defer func() { fetchPRStatus = prevFetch }()
	fetchPRStatus = func(ctx context.Context, api threadsops.API, owner, repo string, number int) (github.PullRequestStatus, *agentio.Error) {
		return github.PullRequestStatus{Title: "Test PR", Mergeable: "MERGEABLE"}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runPRWith([]string{"status", "owner/repo#1"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if got["title"] != "Test PR" {
		t.Errorf("title=%v", got["title"])
	}
}
```

- [ ] **Step 2: Implementation**

```go
// cmd/spn/pr.go
package main

import (
	"context"
	"io"
	"os"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

// fetchPRStatus is overridable for tests; production fetches via threadsops.List.
var fetchPRStatus = func(ctx context.Context, api threadsops.API, owner, repo string, number int) (github.PullRequestStatus, *agentio.Error) {
	status, _, opErr := threadsops.List(ctx, api, owner, repo, number, false)
	if opErr != nil {
		code := agentio.Code(opErr.Code)
		rem := agentio.RemediationUpstream()
		return github.PullRequestStatus{}, agentio.NewError(code, opErr.Message, rem)
	}
	return status, nil
}

func runPR(args []string) int { return runPRWith(args, os.Stdout, os.Stderr) }

func runPRWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (status)", agentio.RemediationBadInput("pr", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "status":
		return doPRStatus(rest, stdout, stderr)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("pr", "")).Emit(stderr)
	}
}

func doPRStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn pr status <pr-ref>", agentio.RemediationBadInput("pr", "status")).Emit(stderr)
	}
	owner, repo, number, ok := resolvePRRef(args[0], stderr)
	if !ok {
		return 2
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	status, e := fetchPRStatus(context.Background(), api, owner, repo, number)
	if e != nil {
		return e.Emit(stderr)
	}
	if err := agentio.WriteJSON(stdout, status); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/spn/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/spn/pr.go cmd/spn/pr_test.go
git commit -m "Add spn pr status"
```

---

## Task 22: spn forks list (NDJSON)

**Files:**
- Modify: `cmd/spn/forks.go`
- Create: `cmd/spn/forks_test.go`

- [ ] **Step 1: Implementation**

The forks subcommand needs to construct a `forge.Forge` provider — for tests, that's also overridable via a factory.

```go
// cmd/spn/forks.go
package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/gitlab"
)

// providerFactory creates the forge provider for the given owner/repo. Overridable in tests.
var providerFactory = func(ctx context.Context, repo, forgeFlag, forgeHost string) (forge.Forge, string, *agentio.Error) {
	var forced forge.Provider
	switch forgeFlag {
	case "gitlab":
		forced = forge.ProviderGitLab
	case "github":
		forced = forge.ProviderGitHub
	}
	parsed, err := forge.Parse(forge.Config{RepoURL: repo, ForceProvider: forced, ForgeHost: forgeHost})
	if err != nil {
		return nil, "", agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("forks", "list"))
	}
	switch parsed.Provider {
	case forge.ProviderGitHub:
		client, status, cerr := gh.CheckAuth()
		if cerr != nil {
			return nil, "", agentio.NewError(agentio.CodeAuthRequired, cerr.Error(), agentio.RemediationAuthRequired())
		}
		return gh.NewGHProvider(client, status), parsed.Owner + "/" + parsed.Repo, nil
	case forge.ProviderGitLab:
		auth, gerr := gitlab.DetectAuth(ctx, parsed.Host)
		if gerr != nil {
			return nil, "", agentio.NewError(agentio.CodeAuthRequired, gerr.Error(), agentio.RemediationAuthRequired())
		}
		tok := gitlab.TokenFromAuth(ctx, parsed.Host)
		return gitlab.NewProvider(gitlab.NewClient(parsed.Host, tok), auth), parsed.Owner + "/" + parsed.Repo, nil
	default:
		return nil, "", agentio.NewError(agentio.CodeBadInput, "unsupported provider", agentio.RemediationBadInput("forks", "list"))
	}
}

func runForks(args []string) int { return runForksWith(args, os.Stdout, os.Stderr) }

func runForksWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (list)", agentio.RemediationBadInput("forks", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		return doForksList(rest, stdout, stderr)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("forks", "")).Emit(stderr)
	}
}

func doForksList(args []string, stdout, stderr io.Writer) int {
	var repo, forgeFlag, forgeHost, botList string
	opts := forksops.Options{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tier":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--tier requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 3 {
				return agentio.NewError(agentio.CodeBadInput, "--tier must be 1, 2, or 3", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.Tier = n
		case "--top":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--top requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--top must be a positive integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.TopN = n
		case "--bot-allowlist":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--bot-allowlist requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			botList = args[i]
		case "--refresh", "--no-cache":
			opts.Refresh = true
		case "--forge":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--forge requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			forgeFlag = strings.ToLower(args[i])
		case "--forge-host":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--forge-host requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			forgeHost = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			if repo != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			repo = args[i]
		}
	}
	if repo == "" {
		return agentio.NewError(agentio.CodeBadInput, "missing repository argument", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	if botList != "" {
		opts.BotAllowlist = map[string]bool{}
		for _, b := range strings.Split(botList, ",") {
			b = strings.TrimSpace(b)
			if b != "" {
				opts.BotAllowlist[strings.ToLower(b)] = true
			}
		}
	}

	ctx := context.Background()
	provider, repoArg, e := providerFactory(ctx, repo, forgeFlag, forgeHost)
	if e != nil {
		return e.Emit(stderr)
	}
	owner, name := splitRepoArg(repoArg)
	if owner == "" || name == "" {
		return agentio.NewError(agentio.CodeBadInput, "invalid repo: "+repo, agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	ch, err := forksops.Stream(ctx, provider, owner, name, opts)
	if err != nil {
		return agentio.NewError(agentio.CodeUpstream, err.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}
	for r := range ch {
		if r.Err != nil {
			// Compact one-line stderr error per failing fork.
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
}

func splitRepoArg(s string) (owner, repo string) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func forkToJSON(r forksops.Result) map[string]any {
	out := map[string]any{
		"id":          r.Fork.ID,
		"owner":       r.Fork.Owner,
		"name":        r.Fork.Name,
		"url":         r.Fork.URL,
		"stars":       r.Fork.Stars,
		"pushed_at":   r.Fork.PushedAt,
		"is_archived": r.Fork.IsArchived,
		"sub_forks":   r.Fork.SubForkCount,
		"releases":    r.Fork.ReleaseCount,
		"heat":        r.Heat.Score,
		"tier":        r.Heat.Tier,
	}
	if r.T2 != nil {
		out["t2"] = map[string]any{
			"ahead":  r.T2.AheadCount,
			"behind": r.T2.BehindCount,
			"mna":    r.T2.MNA,
		}
	}
	if r.T3 != nil {
		out["t3"] = map[string]any{
			"contributors":      len(r.T3.Contributors),
			"commit_span_days":  r.T3.CommitSpanDays,
		}
	}
	return out
}
```

- [ ] **Step 2: Add the failing test using a fake forge**

```go
// cmd/spn/forks_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/forge"
)

type fakeForge struct {
	parent forge.ParentData
	forks  []forge.T1Data
}

func (f *fakeForge) Auth(_ context.Context) (forge.AuthInfo, error)              { return forge.AuthInfo{Tier: forge.AuthCLI, Concurrency: 2}, nil }
func (f *fakeForge) Parent(_ context.Context, _, _ string) (forge.ParentData, error) { return f.parent, nil }
func (f *fakeForge) ListForks(_ context.Context, _, _ string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, len(f.forks))
	for _, fk := range f.forks { ch <- forge.ForkMsg{Fork: fk} }
	close(ch)
	return ch, nil
}
func (f *fakeForge) Branches(_ context.Context, fk forge.T1Data, _ int) ([]forge.BranchRef, error) { return []forge.BranchRef{{Name: fk.DefaultBranch}}, nil }
func (f *fakeForge) Compare(_ context.Context, _ forge.T1Data, _ string) (forge.T2Data, error) { return forge.T2Data{}, nil }
func (f *fakeForge) Contributors(_ context.Context, _ forge.T1Data) (forge.T3Data, error) { return forge.T3Data{}, nil }
func (f *fakeForge) Headroom() float64 { return 1.0 }

func TestSpnForksList_emitsNDJSON(t *testing.T) {
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, repo, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
				{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now()},
			},
		}, "o/r", nil
	}
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d:\n%s", len(lines), stdout.String())
	}
	for _, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("invalid JSON line %q: %v", line, err)
		}
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/spn/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/spn/forks.go cmd/spn/forks_test.go
git commit -m "Add spn forks list with NDJSON streaming"
```

---

## Task 23: README update

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add a new section after the existing "PR review threads" section**

```markdown
## Agent CLI (`spn`)

`spn` is a sibling binary aimed at LLM/agent consumption. JSON-only, no
TUI, no color, structured error envelope with remediation hints, NDJSON
streaming for long-running queries.

```sh
go install github.com/svnbjrn/spoon/cmd/spn@latest
```

Verbs:

```sh
spn threads list <pr-ref> [--all]
spn threads next <pr-ref>
spn threads reply <pr-ref> <id> --body T
spn threads resolve <pr-ref> <id> [--body T]
spn threads resolve-all <pr-ref>     # skips human-raised threads (returned in `skipped`)
spn threads unresolve-all <pr-ref>
spn pr status <pr-ref>
spn forks list <repo> [--tier N] [--top N] [...]   # NDJSON
```

Success: bare JSON to stdout. Failure: structured envelope to stderr:

```json
{"error": {"code": "policy_violation", "message": "...", "remediation": "spn threads resolve owner/repo#42 PRRT_... --body \"...\"", "retryable": false, "details": {...}}}
```

See [`docs/superpowers/specs/2026-05-10-spn-bifurcation-design.md`](docs/superpowers/specs/2026-05-10-spn-bifurcation-design.md) for the full design.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "Document spn agent CLI in README"
```

---

## Self-review

Cross-check against spec sections:

- **Goal (parity + JSON-only):** Tasks 17–22 cover all verbs from spec command surface. ✓
- **Non-goals (no removal of spoon flags):** Task 14 is a pure refactor, no flag removal. Task 6 preserves call-site compatibility. ✓
- **Body-required policy (single + bulk):** Tasks 10, 11, 12, 20 cover. ✓
- **Resolve idempotency:** Task 10 test `TestResolve_alreadyResolved_idempotent`. ✓
- **Partial-failure semantics:** Task 11 tests + Task 19 stdout/stderr structure. ✓
- **Output contract (stdout/stderr split):** Tasks 17, 19, 22 enforce. ✓
- **Error vocabulary + remediation:** Tasks 2, 3, 17–22 wire codes and patterns. ✓
- **TTY behavior (never adapt):** No isatty calls in any `cmd/spn` file. ✓
- **Streaming (NDJSON):** Task 15 + Task 22. ✓
- **Schema versioning (deferred):** No `$schema_version` field anywhere. ✓
- **Code-sharing structure:** Tasks 4, 7–14 (threadsops) + Task 15 (forksops) + Tasks 1–3 (agentio). ✓
- **Username prerequisite + fallback:** Task 5 (Username), Task 11 (recency fallback). ✓
- **Per-fork error on stderr compact line:** Task 22, `doForksList`. ✓

No outstanding placeholders. Type names match across tasks (`ReviewThreadWithPolicy`, `OpError`, `BulkResult`, `Code`, `Error`, `Stream`/`Result` are consistent).

---

Plan complete and saved to `docs/superpowers/plans/2026-05-10-spn-bifurcation.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
