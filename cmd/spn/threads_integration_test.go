// cmd/spn/threads_integration_test.go
//
// Cross-feature integration tests for the gh-toolkit features (G1..G8).
// Each test drives the spn binary entry point (runThreadsWith) with a stubbed
// apiFactory so we exercise the real CLI flag parsing, dispatch, options
// plumbing, and JSON emission. The point of these tests is to catch bugs that
// only show up when two or more features interact (e.g. --filter+--show-code,
// --outdated+human-policy, --suggest then list, --dry-run idempotency).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

// mixedThreadFixture returns a fixture with four threads in the 2x2 matrix of
// (outdated × human-comment), used by several cross-feature tests below.
//
//	o_bot   : outdated, bot-only        (should resolve under --outdated)
//	o_human : outdated, human commenter (should skip: requires_body)
//	c_bot   : current,  bot-only        (should skip under --outdated: not_outdated)
//	c_human : current,  human commenter (should skip under --outdated: not_outdated)
//
// Distinct ascending timestamps keep canonical sort order deterministic.
func mixedThreadFixture() []github.ReviewThread {
	return []github.ReviewThread{
		{ID: "o_bot", IsResolved: false, IsOutdated: true,
			Path: "a.go", Line: 5,
			Comments: []github.ThreadComment{{
				ID: "PRRC_1", AuthorType: "Bot",
				CreatedAt: "2026-05-01T00:00:00Z",
				UpdatedAt: "2026-05-01T00:00:00Z",
				AuthorURL: "https://github.com/apps/bot1",
			}}},
		{ID: "o_human", IsResolved: false, IsOutdated: true,
			Path: "a.go", Line: 6,
			Comments: []github.ThreadComment{{
				ID: "PRRC_2", AuthorType: "User", Author: "alice",
				CreatedAt: "2026-05-02T00:00:00Z",
				UpdatedAt: "2026-05-02T00:00:00Z",
				AuthorURL: "https://github.com/alice",
			}}},
		{ID: "c_bot", IsResolved: false, IsOutdated: false,
			Path: "a.go", Line: 7,
			Comments: []github.ThreadComment{{
				ID: "PRRC_3", AuthorType: "Bot",
				CreatedAt: "2026-05-03T00:00:00Z",
				UpdatedAt: "2026-05-03T00:00:00Z",
				AuthorURL: "https://github.com/apps/bot2",
			}}},
		{ID: "c_human", IsResolved: false, IsOutdated: false,
			Path: "a.go", Line: 8,
			Comments: []github.ThreadComment{{
				ID: "PRRC_4", AuthorType: "User", Author: "bob",
				CreatedAt: "2026-05-04T00:00:00Z",
				UpdatedAt: "2026-05-04T00:00:00Z",
				AuthorURL: "https://github.com/bob",
			}}},
	}
}

// integrationStub combines the spn stub patterns: it satisfies threadsops.API,
// threadsops.ContentFetcher, and counts mutation calls so dry-run + outdated
// tests can assert no side effects.
type integrationStub struct {
	threads     []github.ReviewThread
	headSHA     string
	fileContent map[string]string

	mu            sync.Mutex
	resolveCalls  int32
	unresolveCalls int32
	replyCalls    int32
	lastReplyBody string
	currentUser   string
}

func (s *integrationStub) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	sha := s.headSHA
	if sha == "" {
		sha = "deadbeefcafe"
	}
	return github.PullRequestStatus{Title: "test", HeadSHA: sha}, s.threads, nil
}
func (s *integrationStub) ReplyToThread(_ context.Context, _, body string) (github.ThreadComment, error) {
	atomic.AddInt32(&s.replyCalls, 1)
	s.mu.Lock()
	s.lastReplyBody = body
	s.mu.Unlock()
	return github.ThreadComment{ID: "PRC_new", Body: body, AuthorType: "Bot"}, nil
}
func (s *integrationStub) ResolveThread(_ context.Context, _ string) error {
	atomic.AddInt32(&s.resolveCalls, 1)
	return nil
}
func (s *integrationStub) UnresolveThread(_ context.Context, _ string) error {
	atomic.AddInt32(&s.unresolveCalls, 1)
	return nil
}
func (s *integrationStub) CurrentUserLogin(_ context.Context) (string, error) {
	if s.currentUser == "" {
		return "", errors.New("no user")
	}
	return s.currentUser, nil
}
func (s *integrationStub) FetchFileContent(_ context.Context, _, _, path, ref string) (string, error) {
	if c, ok := s.fileContent[path]; ok {
		return c, nil
	}
	return "", nil
}

// totalMutations returns the sum of every mutation API call, used by dry-run
// tests to assert nothing escaped to the network.
func (s *integrationStub) totalMutations() int32 {
	return atomic.LoadInt32(&s.resolveCalls) +
		atomic.LoadInt32(&s.unresolveCalls) +
		atomic.LoadInt32(&s.replyCalls)
}

func installStub(t *testing.T, s *integrationStub) {
	t.Helper()
	prev := apiFactory
	t.Cleanup(func() { apiFactory = prev })
	apiFactory = func() (threadsops.API, *agentio.Error) { return s, nil }
}

func runSpnThreads(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	var so, se bytes.Buffer
	exit = runThreadsWith(args, &so, &se)
	return so.String(), se.String(), exit
}

// tenLineFileSpn already exists in threads_test.go; reuse it here.

// --- 1. Filter + ShowCode + Verbose composability ---------------------------

func TestCrossFeature_FilterShowCodeVerbose(t *testing.T) {
	// Fixture: four threads — a mix of outdated/current, all unresolved, all
	// with bot-only comments so the human policy doesn't get in the way. The
	// --filter unresolved-outdated cut should leave just the two outdated ones.
	threads := []github.ReviewThread{
		{ID: "T_co", IsResolved: false, IsOutdated: false,
			Path: "a.go", Line: 3,
			Comments: []github.ThreadComment{{
				ID: "PRRC_co", AuthorType: "Bot",
				CreatedAt: "2026-05-01T00:00:00Z",
				UpdatedAt: "2026-05-01T00:01:00Z",
				AuthorURL: "https://github.com/apps/bot",
			}}},
		{ID: "T_o1", IsResolved: false, IsOutdated: true,
			Path: "a.go", Line: 5,
			Comments: []github.ThreadComment{{
				ID: "PRRC_o1", AuthorType: "Bot",
				CreatedAt: "2026-05-02T00:00:00Z",
				UpdatedAt: "2026-05-02T00:02:00Z",
				AuthorURL: "https://github.com/apps/bot",
			}}},
		{ID: "T_o2", IsResolved: false, IsOutdated: true,
			Path: "a.go", Line: 7,
			Comments: []github.ThreadComment{{
				ID: "PRRC_o2", AuthorType: "Bot",
				CreatedAt: "2026-05-03T00:00:00Z",
				UpdatedAt: "2026-05-03T00:03:00Z",
				AuthorURL: "https://github.com/apps/bot",
			}}},
		{ID: "T_c2", IsResolved: false, IsOutdated: false,
			Path: "a.go", Line: 9,
			Comments: []github.ThreadComment{{
				ID: "PRRC_c2", AuthorType: "Bot",
				CreatedAt: "2026-05-04T00:00:00Z",
				UpdatedAt: "2026-05-04T00:04:00Z",
				AuthorURL: "https://github.com/apps/bot",
			}}},
	}
	s := &integrationStub{
		threads:     threads,
		headSHA:     "abc1234567890",
		fileContent: map[string]string{"a.go": tenLineFileSpn()},
	}
	installStub(t, s)

	stdout, stderrOut, exit := runSpnThreads(t,
		"list", "owner/repo#1",
		"--filter", "unresolved-outdated",
		"--show-code", "3",
		"--verbose",
	)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrOut)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse: %v\nstdout=%s", err, stdout)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 outdated threads, got %d: %+v", len(got), got)
	}
	for i, th := range got {
		id, _ := th["id"].(string)
		if id != "T_o1" && id != "T_o2" {
			t.Errorf("thread[%d].id=%q not in {T_o1,T_o2}", i, id)
		}
		// Each has codeContext.lines populated.
		cc, ok := th["codeContext"].(map[string]any)
		if !ok {
			t.Errorf("thread[%d] missing codeContext: %+v", i, th)
			continue
		}
		lines, _ := cc["lines"].([]any)
		if len(lines) == 0 {
			t.Errorf("thread[%d] codeContext.lines empty", i)
		}
		// Each comment has verbose fields (createdAt/updatedAt/authorUrl).
		comments, _ := th["comments"].([]any)
		if len(comments) == 0 {
			t.Errorf("thread[%d] has no comments", i)
			continue
		}
		c, _ := comments[0].(map[string]any)
		if c["createdAt"] == nil || c["createdAt"] == "" {
			t.Errorf("thread[%d] missing createdAt: %+v", i, c)
		}
		if c["updatedAt"] == nil || c["updatedAt"] == "" {
			t.Errorf("thread[%d] missing updatedAt: %+v", i, c)
		}
		if c["authorUrl"] == nil || c["authorUrl"] == "" {
			t.Errorf("thread[%d] missing authorUrl: %+v", i, c)
		}
	}
}

// --- 2. ResolveAll outdated + dry-run ---------------------------------------

func TestCrossFeature_ResolveAllOutdatedDryRun(t *testing.T) {
	s := &integrationStub{threads: mixedThreadFixture()}
	installStub(t, s)

	stdout, stderrOut, exit := runSpnThreads(t,
		"resolve-all", "owner/repo#1", "--outdated", "--dry-run",
	)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}
	if got["dryRun"] != true {
		t.Errorf("expected dryRun=true on BulkResult, got %+v", got)
	}
	// succeeded entries must be outdated-only (o_bot). o_human is skipped
	// (requires_body) by the human policy, and c_bot/c_human are skipped
	// (not_outdated) by the --outdated filter.
	succeeded, _ := got["succeeded"].([]any)
	if len(succeeded) != 1 {
		t.Fatalf("succeeded=%+v want exactly [o_bot]", succeeded)
	}
	if succeeded[0] != "o_bot" {
		t.Errorf("succeeded[0]=%v want o_bot", succeeded[0])
	}
	if s.totalMutations() != 0 {
		t.Errorf("expected 0 mutations on dry-run, got %d", s.totalMutations())
	}
}

// --- 3. ApplySuggestion + dry-run -------------------------------------------

func TestCrossFeature_ApplySuggestionDryRun(t *testing.T) {
	tmp := t.TempDir()
	srcPath := tmp + "/x.go"
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(srcPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &integrationStub{threads: []github.ReviewThread{{
		ID: "PRRT_x", Path: "x.go", Line: 2,
		Comments: []github.ThreadComment{{
			ID:   "PRC_1",
			Body: "Please use:\n\n```suggestion\nNEW\n```",
		}},
	}}}
	installStub(t, s)

	stdout, stderrOut, exit := runSpnThreads(t,
		"apply-suggestion", "owner/repo#1", "PRRT_x",
		"--dry-run", "--repo-root", tmp,
	)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrOut)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}
	if res["dryRun"] != true {
		t.Errorf("expected dryRun=true on ApplyResult, got %+v", res)
	}
	// The applied flag should be false on dry-run.
	if res["applied"] != false {
		t.Errorf("expected applied=false on dry-run, got %v", res["applied"])
	}
	// File must be unchanged on disk.
	b, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != original {
		t.Errorf("file changed under --dry-run\n got:  %q\nwant: %q", string(b), original)
	}
}

// --- 4. Reply with --suggest, then list, then re-parse suggestion -----------

func TestCrossFeature_ReplyWithSuggest_ThenList(t *testing.T) {
	// Start with one thread that has no suggestion. After --reply --suggest,
	// the stub records the wrapped body; we then re-list with a thread whose
	// comments include that wrapped body and assert that PopulateSuggestions
	// extracts the same content the agent proposed.
	s := &integrationStub{threads: []github.ReviewThread{{
		ID: "PRRT_x", Path: "x.go", Line: 2,
		Comments: []github.ThreadComment{{
			ID:         "PRC_orig",
			AuthorType: "Bot",
			Body:       "please change this",
			CreatedAt:  "2026-05-01T00:00:00Z",
		}},
	}}}
	installStub(t, s)

	// Step 1: reply with --suggest + --intro.
	_, stderrOut, exit := runSpnThreads(t,
		"reply", "owner/repo#1", "PRRT_x",
		"--suggest", "NEW",
		"--intro", "how about",
	)
	if exit != 0 {
		t.Fatalf("reply exit=%d stderr=%s", exit, stderrOut)
	}
	wantBody := "how about\n\n```suggestion\nNEW\n```"
	if s.lastReplyBody != wantBody {
		t.Errorf("reply body=%q\nwant %q", s.lastReplyBody, wantBody)
	}

	// Step 2: simulate the new comment showing up in a subsequent list. The
	// existing PRC_orig is preserved; PRC_new is the agent's reply with the
	// wrapped suggestion block.
	s.threads[0].Comments = append(s.threads[0].Comments, github.ThreadComment{
		ID:         "PRC_new",
		AuthorType: "Bot",
		Body:       s.lastReplyBody,
		CreatedAt:  "2026-05-01T00:01:00Z",
	})
	stdout, stderrOut, exit := runSpnThreads(t, "list", "owner/repo#1")
	if exit != 0 {
		t.Fatalf("list exit=%d stderr=%s", exit, stderrOut)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 thread, got %d", len(got))
	}
	sugs, ok := got[0]["suggestions"].([]any)
	if !ok || len(sugs) != 1 {
		t.Fatalf("expected exactly 1 parsed suggestion, got %+v", got[0]["suggestions"])
	}
	sug := sugs[0].(map[string]any)
	if sug["body"] != "NEW" {
		t.Errorf("parsed sug body=%q want %q", sug["body"], "NEW")
	}
	if sug["commentId"] != "PRC_new" {
		t.Errorf("parsed sug commentId=%q want PRC_new", sug["commentId"])
	}
}

// --- 5. Resolve --dry-run is idempotent on an already-resolved thread -------

func TestCrossFeature_ResolveDryRunIdempotentOnAlreadyResolved(t *testing.T) {
	s := &integrationStub{threads: []github.ReviewThread{{
		ID: "PRRT_done", IsResolved: true,
		Comments: []github.ThreadComment{{AuthorType: "Bot"}},
	}}}
	installStub(t, s)

	stdout, stderrOut, exit := runSpnThreads(t,
		"resolve", "owner/repo#1", "PRRT_done", "--dry-run",
	)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}
	// Already-resolved threads round-trip with isResolved=true. The DryRun
	// marker is intentionally NOT set on already-resolved (see resolveTarget),
	// because the outcome is the same whether or not dry-run was requested.
	if got["isResolved"] != true {
		t.Errorf("isResolved=%v want true", got["isResolved"])
	}
	if s.totalMutations() != 0 {
		t.Errorf("expected 0 mutations, got %d", s.totalMutations())
	}
}

// --- 6. Filter all without verbose: timestamps stripped ---------------------

func TestCrossFeature_FilterAllWithVerbose_OmitsTimestampsWhenNotVerbose(t *testing.T) {
	s := &integrationStub{threads: mixedThreadFixture()}
	installStub(t, s)

	stdout, stderrOut, exit := runSpnThreads(t,
		"list", "owner/repo#1", "--filter", "all",
	)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrOut)
	}
	// All four threads should appear (filter=all), but no comment has the
	// verbose keys because --verbose was not supplied.
	if strings.Contains(stdout, `"createdAt"`) {
		t.Errorf("expected no createdAt key without --verbose; got:\n%s", stdout)
	}
	if strings.Contains(stdout, `"updatedAt"`) {
		t.Errorf("expected no updatedAt key without --verbose; got:\n%s", stdout)
	}
	if strings.Contains(stdout, `"authorUrl"`) {
		t.Errorf("expected no authorUrl key without --verbose; got:\n%s", stdout)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}
	if len(got) != 4 {
		t.Errorf("want 4 threads under --filter all, got %d", len(got))
	}
}

// --- 7. --outdated + human-policy interaction -------------------------------

func TestCrossFeature_OutdatedAndHumanPolicy(t *testing.T) {
	s := &integrationStub{threads: mixedThreadFixture()}
	installStub(t, s)

	stdout, stderrOut, exit := runSpnThreads(t,
		"resolve-all", "owner/repo#1", "--outdated",
	)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}
	succeeded, _ := got["succeeded"].([]any)
	skipped, _ := got["skipped"].([]any)
	failed, _ := got["failed"].([]any)

	if len(succeeded) != 1 {
		t.Errorf("succeeded=%+v want [o_bot]", succeeded)
	} else if succeeded[0] != "o_bot" {
		t.Errorf("succeeded[0]=%v want o_bot", succeeded[0])
	}
	if len(failed) != 0 {
		t.Errorf("failed=%+v want 0", failed)
	}
	if len(skipped) != 3 {
		t.Fatalf("skipped=%+v want 3", skipped)
	}
	// Index skips by ID for stable assertions.
	skipReasons := map[string]string{}
	for _, raw := range skipped {
		sk, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("skip not a map: %T", raw)
		}
		skipReasons[sk["id"].(string)] = sk["reason"].(string)
	}
	// Precedence: the --outdated filter runs first, so c_bot/c_human get
	// "not_outdated". o_human is outdated but human-only → "requires_body".
	if got := skipReasons["o_human"]; got != "requires_body" {
		t.Errorf("o_human reason=%q want requires_body", got)
	}
	if got := skipReasons["c_bot"]; got != "not_outdated" {
		t.Errorf("c_bot reason=%q want not_outdated", got)
	}
	if got := skipReasons["c_human"]; got != "not_outdated" {
		t.Errorf("c_human reason=%q want not_outdated", got)
	}
}

// --- 8. spn threads next --show-code N --verbose ----------------------------

func TestCrossFeature_NextShowCodeVerbose(t *testing.T) {
	s := &integrationStub{
		threads: []github.ReviewThread{{
			ID: "PRRT_first", Path: "a.go", Line: 5,
			Comments: []github.ThreadComment{{
				ID:         "PRRC_x",
				AuthorType: "Bot",
				CreatedAt:  "2026-05-01T00:00:00Z",
				UpdatedAt:  "2026-05-01T00:01:00Z",
				AuthorURL:  "https://github.com/apps/bot",
			}},
		}},
		headSHA:     "feedface",
		fileContent: map[string]string{"a.go": tenLineFileSpn()},
	}
	installStub(t, s)

	stdout, stderrOut, exit := runSpnThreads(t,
		"next", "owner/repo#1",
		"--show-code", "2", "--verbose",
	)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}
	cc, ok := got["codeContext"].(map[string]any)
	if !ok {
		t.Fatalf("codeContext missing on next: %+v", got)
	}
	if cc["ref"] != "feedface" {
		t.Errorf("ref=%v want feedface", cc["ref"])
	}
	lines, _ := cc["lines"].([]any)
	if len(lines) == 0 {
		t.Errorf("expected non-empty codeContext.lines")
	}
	comments, _ := got["comments"].([]any)
	if len(comments) == 0 {
		t.Fatalf("expected comments on next, got %+v", got)
	}
	c, _ := comments[0].(map[string]any)
	if c["createdAt"] == nil || c["createdAt"] == "" {
		t.Errorf("missing createdAt: %+v", c)
	}
	if c["updatedAt"] == nil || c["updatedAt"] == "" {
		t.Errorf("missing updatedAt: %+v", c)
	}
	if c["authorUrl"] == nil || c["authorUrl"] == "" {
		t.Errorf("missing authorUrl: %+v", c)
	}
}
