// cmd/spn/threads_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

// stubAPI is a minimal threadsops.API implementation for cmd/spn tests.
type stubAPI struct {
	threads []github.ReviewThread
	headSHA string
}

func (s *stubAPI) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	sha := s.headSHA
	if sha == "" {
		sha = "deadbeefcafe"
	}
	return github.PullRequestStatus{Title: "test", HeadSHA: sha}, s.threads, nil
}
func (s *stubAPI) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	return github.ThreadComment{}, nil
}
func (s *stubAPI) ResolveThread(_ context.Context, _ string) error   { return nil }
func (s *stubAPI) UnresolveThread(_ context.Context, _ string) error { return nil }
func (s *stubAPI) CurrentUserLogin(_ context.Context) (string, error) {
	return "", errors.New("no user")
}

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

func TestSpnThreadsList_emitsIsOutdated(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{threads: []github.ReviewThread{
			{ID: "PRRT_outdated", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
			{ID: "PRRT_active", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	// String-level assertion: every thread must include "isOutdated".
	if !strings.Contains(stdout.String(), `"isOutdated"`) {
		t.Errorf("expected isOutdated key in JSON; got:\n%s", stdout.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON array: %v\n%s", err, stdout.String())
	}
	if len(got) != 2 {
		t.Fatalf("want 2 threads, got %d", len(got))
	}
	// Output is sorted canonically (first-comment time ASC, then ID ASC).
	// With identical timestamps, PRRT_active sorts before PRRT_outdated.
	byID := map[string]map[string]any{}
	for _, g := range got {
		byID[g["id"].(string)] = g
	}
	if byID["PRRT_outdated"]["isOutdated"] != true {
		t.Errorf("PRRT_outdated isOutdated mismatch: %+v", byID["PRRT_outdated"])
	}
	if byID["PRRT_active"]["isOutdated"] != false {
		t.Errorf("PRRT_active isOutdated mismatch: %+v", byID["PRRT_active"])
	}
}

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

type resolveStub struct {
	stubAPI
	currentUser string
	posted      github.ThreadComment
	resolveErr  error
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
			stubAPI:    stubAPI{threads: []github.ReviewThread{{ID: "PRRT_1", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}},
			posted:     github.ThreadComment{ID: "PRC_new"},
			resolveErr: errors.New("graphql 500"),
		}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve", "owner/repo#1", "PRRT_1", "--body", "ack"}, &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal stderr: %v", err)
	}
	if env["error"]["retryable"] != true {
		t.Errorf("expected retryable")
	}
	d, ok := env["error"]["details"].(map[string]any)
	if !ok {
		t.Fatalf("details not a map: %T", env["error"]["details"])
	}
	if d["comment_posted"] != true {
		t.Errorf("expected comment_posted=true")
	}
}

// fourThreadFixture is the canonical 2x2 fixture (resolved × outdated) used by
// the --filter end-to-end tests. Timestamps are distinct so the canonical sort
// order is deterministic and easy to assert.
func fourThreadFixture() []github.ReviewThread {
	return []github.ReviewThread{
		{ID: "T_uu", IsResolved: false, IsOutdated: false,
			Comments: []github.ThreadComment{{CreatedAt: "2026-05-01T00:00:00Z", AuthorType: "User", Author: "a"}}},
		{ID: "T_uo", IsResolved: false, IsOutdated: true,
			Comments: []github.ThreadComment{{CreatedAt: "2026-05-02T00:00:00Z", AuthorType: "User", Author: "a"}}},
		{ID: "T_ra", IsResolved: true, IsOutdated: false,
			Comments: []github.ThreadComment{{CreatedAt: "2026-05-03T00:00:00Z", AuthorType: "Bot"}}},
		{ID: "T_ro", IsResolved: true, IsOutdated: true,
			Comments: []github.ThreadComment{{CreatedAt: "2026-05-04T00:00:00Z", AuthorType: "Bot"}}},
	}
}

// runListWithFilter exercises the full `spn threads list` handler against the
// 2x2 fixture, returning the parsed thread IDs from stdout.
func runListWithFilter(t *testing.T, filterArgs ...string) (ids []string, exit int, stderrText string) {
	t.Helper()
	prev := apiFactory
	t.Cleanup(func() { apiFactory = prev })
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{threads: fourThreadFixture()}, nil
	}
	var stdout, stderr bytes.Buffer
	args := append([]string{"list", "owner/repo#1"}, filterArgs...)
	exit = runThreadsWith(args, &stdout, &stderr)
	stderrText = stderr.String()
	if exit != 0 {
		return nil, exit, stderrText
	}
	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON array (exit=%d): %v\n%s", exit, err, stdout.String())
	}
	for _, g := range got {
		ids = append(ids, g["id"].(string))
	}
	return ids, exit, stderrText
}

func TestSpnThreadsList_FilterModes_E2E(t *testing.T) {
	cases := []struct {
		mode    string
		wantIDs []string // in canonical sort order (time ASC, ID ASC)
	}{
		// All four threads have distinct ascending timestamps: T_uu < T_uo < T_ra < T_ro.
		{"all", []string{"T_uu", "T_uo", "T_ra", "T_ro"}},
		{"unresolved", []string{"T_uu", "T_uo"}},
		{"resolved-active", []string{"T_ra"}},
		{"unresolved-outdated", []string{"T_uo"}},
		{"current-unresolved", []string{"T_uu"}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			gotIDs, exit, stderrText := runListWithFilter(t, "--filter", tc.mode)
			if exit != 0 {
				t.Fatalf("exit=%d stderr=%s", exit, stderrText)
			}
			if len(gotIDs) != len(tc.wantIDs) {
				t.Fatalf("len=%d want %d: got=%v want=%v", len(gotIDs), len(tc.wantIDs), gotIDs, tc.wantIDs)
			}
			for i := range tc.wantIDs {
				if gotIDs[i] != tc.wantIDs[i] {
					t.Errorf("pos %d: got %q want %q (full: %v)", i, gotIDs[i], tc.wantIDs[i], gotIDs)
				}
			}
		})
	}
}

func TestFilter_BadInput_EmitsBadInput(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{threads: fourThreadFixture()}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1", "--filter", "bogus"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d (want 2 for bad_input); stderr=%s", exit, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on bad_input, got %q", stdout.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v; full=%+v", env["error"]["code"], env["error"])
	}
	rem, _ := env["error"]["remediation"].(string)
	for _, m := range []string{"all", "unresolved", "resolved-active", "unresolved-outdated", "current-unresolved"} {
		if !strings.Contains(rem, m) {
			t.Errorf("remediation missing mode %q: %s", m, rem)
		}
	}
}

func TestFilter_AllPreservesSortOrder(t *testing.T) {
	gotIDs, exit, stderrText := runListWithFilter(t, "--filter", "all")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrText)
	}
	// Canonical sort: by first-comment CreatedAt ASC, then ID ASC. The fixture
	// timestamps were chosen so the result reads top-to-bottom by ID prefix.
	want := []string{"T_uu", "T_uo", "T_ra", "T_ro"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Errorf("got %v want %v", gotIDs, want)
	}
}

func TestFilter_DefaultIsUnresolved(t *testing.T) {
	// No --filter at all → must match --filter unresolved.
	gotIDs, exit, stderrText := runListWithFilter(t /* no args */)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrText)
	}
	want := []string{"T_uu", "T_uo"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Errorf("default got %v want %v", gotIDs, want)
	}
}

func TestFilter_AllFlagIsShorthandForFilterAll(t *testing.T) {
	gotIDs, exit, stderrText := runListWithFilter(t, "--all")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrText)
	}
	want := []string{"T_uu", "T_uo", "T_ra", "T_ro"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Errorf("--all got %v want %v", gotIDs, want)
	}
}

func TestFilter_EqualsForm(t *testing.T) {
	// --filter=resolved-active should work identically to --filter resolved-active.
	gotIDs, exit, stderrText := runListWithFilter(t, "--filter=resolved-active")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderrText)
	}
	want := []string{"T_ra"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Errorf("got %v want %v", gotIDs, want)
	}
}

// Append to cmd/spn/threads_test.go
type bulkStub struct {
	stubAPI
	mu           sync.Mutex
	resolveCalls map[string]bool
}

func (b *bulkStub) ResolveThread(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.resolveCalls == nil {
		b.resolveCalls = map[string]bool{}
	}
	b.resolveCalls[id] = true
	return nil
}
func (b *bulkStub) UnresolveThread(_ context.Context, _ string) error { return nil }

type rateLimitedListStub struct {
	stubAPI
}

func (r *rateLimitedListStub) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	return github.PullRequestStatus{}, nil, &github.RateLimitError{ResetAt: time.Now().Add(60 * time.Second)}
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
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "rate_limited" {
		t.Errorf("code=%v", env["error"]["code"])
	}
	if env["error"]["retryable"] != true {
		t.Error("expected retryable=true")
	}
	if env["error"]["retry_after_seconds"] == nil {
		t.Error("expected retry_after_seconds populated")
	}
}

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
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v", err)
	}
	succeeded, _ := got["succeeded"].([]any)
	skipped, _ := got["skipped"].([]any)
	if len(succeeded) != 1 || succeeded[0] != "PRRT_bot" {
		t.Errorf("succeeded=%+v", succeeded)
	}
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skipped, got %+v", skipped)
	}
	sk, ok := skipped[0].(map[string]any)
	if !ok {
		t.Fatalf("skipped[0] not a map: %T", skipped[0])
	}
	if sk["id"] != "PRRT_user" || sk["reason"] != "requires_body" {
		t.Errorf("skipped item: %+v", sk)
	}
}

// --- G3: --outdated bulk-resolve tests -------------------------------------

// outdatedFixture builds a four-thread fixture: two outdated bot threads and
// two current (non-outdated) bot threads. Used by the --outdated E2E tests.
func outdatedFixture() []github.ReviewThread {
	return []github.ReviewThread{
		{ID: "PRRT_o1", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_o2", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_c1", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_c2", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}
}

func TestSpnThreadsResolveAll_OutdatedFlag_E2E(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &bulkStub{stubAPI: stubAPI{threads: outdatedFixture()}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve-all", "owner/repo#1", "--outdated"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v\n%s", err, stdout.String())
	}
	succeeded, _ := got["succeeded"].([]any)
	skipped, _ := got["skipped"].([]any)
	if len(succeeded) != 2 {
		t.Errorf("succeeded=%+v want 2", succeeded)
	}
	gotSucceededIDs := map[string]bool{}
	for _, s := range succeeded {
		gotSucceededIDs[s.(string)] = true
	}
	for _, want := range []string{"PRRT_o1", "PRRT_o2"} {
		if !gotSucceededIDs[want] {
			t.Errorf("expected %s in succeeded; got %+v", want, succeeded)
		}
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped=%+v want 2", skipped)
	}
	for _, raw := range skipped {
		sk, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("skipped item not a map: %T", raw)
		}
		if sk["reason"] != "not_outdated" {
			t.Errorf("skipped %v reason=%q want not_outdated", sk["id"], sk["reason"])
		}
	}
}

func TestSpnThreadsResolveAll_OutdatedAndAllHuman(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &bulkStub{stubAPI: stubAPI{threads: []github.ReviewThread{
			{ID: "PRRT_u1", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}},
			{ID: "PRRT_u2", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "User", Author: "bob"}}},
		}}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve-all", "owner/repo#1", "--outdated"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v\n%s", err, stdout.String())
	}
	succeeded, _ := got["succeeded"].([]any)
	skipped, _ := got["skipped"].([]any)
	if len(succeeded) != 0 {
		t.Errorf("succeeded=%+v want 0", succeeded)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped=%+v want 2", skipped)
	}
	for _, raw := range skipped {
		sk, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("skipped item not a map: %T", raw)
		}
		if sk["reason"] != "requires_body" {
			t.Errorf("skipped %v reason=%q want requires_body", sk["id"], sk["reason"])
		}
	}
}

func TestSpnThreadsResolveAll_NoOutdatedFlag_PreservesExisting(t *testing.T) {
	// Same fixture as the --outdated E2E test, no --outdated. The default spn
	// resolve-all path applies SkipHumanThreads only — every bot thread (both
	// outdated and current) should resolve, with no skips.
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &bulkStub{stubAPI: stubAPI{threads: outdatedFixture()}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve-all", "owner/repo#1"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v\n%s", err, stdout.String())
	}
	succeeded, _ := got["succeeded"].([]any)
	skipped, _ := got["skipped"].([]any)
	if len(succeeded) != 4 {
		t.Errorf("succeeded=%+v want 4 (all bot threads)", succeeded)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped=%+v want 0 (no human threads in fixture)", skipped)
	}
}

// --- G8: suggestion-block tests --------------------------------------------

// captureReplyStub records the body of the most recent reply.
type captureReplyStub struct {
	stubAPI
	lastBody string
}

func (c *captureReplyStub) ReplyToThread(_ context.Context, _, body string) (github.ThreadComment, error) {
	c.lastBody = body
	return github.ThreadComment{ID: "PRC_new", Body: body}, nil
}

func TestSpnThreadsList_EmitsSuggestionsField(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{threads: []github.ReviewThread{{
			ID: "PRRT_x", Path: "a.go", Line: 3,
			Comments: []github.ThreadComment{
				{ID: "PRC_1", Body: "Please use:\n\n```suggestion\nconst Foo = 1\n```"},
			},
		}}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"suggestions"`) {
		t.Errorf("expected suggestions key, got: %s", stdout.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	sugs, ok := got[0]["suggestions"].([]any)
	if !ok || len(sugs) != 1 {
		t.Fatalf("suggestions=%+v", got[0]["suggestions"])
	}
	s := sugs[0].(map[string]any)
	if s["body"] != "const Foo = 1" {
		t.Errorf("body=%q", s["body"])
	}
	if s["commentId"] != "PRC_1" {
		t.Errorf("commentId=%q", s["commentId"])
	}
	if s["applicable"] != true {
		t.Errorf("applicable=%v", s["applicable"])
	}
}

func TestSpnThreadsApplySuggestion_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	srcPath := tmp + "/x.go"
	if err := os.WriteFile(srcPath, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{threads: []github.ReviewThread{{
			ID: "PRRT_x", Path: "x.go", Line: 2,
			Comments: []github.ThreadComment{{ID: "PRC_1", Body: "```suggestion\nNEW\n```"}},
		}}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"apply-suggestion", "owner/repo#1", "PRRT_x", "--repo-root", tmp}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var res map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout.String())
	}
	if res["applied"] != true {
		t.Errorf("applied=%v full=%+v", res["applied"], res)
	}
	b, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "alpha\nNEW\ngamma\n" {
		t.Errorf("file=%q", string(b))
	}
}

func TestSpnThreadsApplySuggestion_NoSuggestion(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{threads: []github.ReviewThread{{
			ID: "PRRT_x", Path: "x.go", Line: 1,
			Comments: []github.ThreadComment{{ID: "PRC_1", Body: "no suggestion here"}},
		}}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"apply-suggestion", "owner/repo#1", "PRRT_x"}, &stdout, &stderr)
	if exit == 0 {
		t.Fatalf("expected non-zero exit; stdout=%s", stdout.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "not_found" {
		t.Errorf("code=%v", env["error"]["code"])
	}
}

func TestSpnThreadsApplySuggestion_OutdatedRejected(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(tmp+"/x.go", []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{threads: []github.ReviewThread{{
			ID: "PRRT_x", Path: "x.go", Line: 1, IsOutdated: true,
			Comments: []github.ThreadComment{{ID: "PRC_1", Body: "```suggestion\nZ\n```"}},
		}}}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"apply-suggestion", "owner/repo#1", "PRRT_x", "--repo-root", tmp}, &stdout, &stderr)
	if exit == 0 {
		t.Fatalf("expected non-zero exit; stdout=%s", stdout.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "policy_violation" {
		t.Errorf("code=%v want policy_violation", env["error"]["code"])
	}
	// With --force the same call should succeed.
	stdout.Reset()
	stderr.Reset()
	exit = runThreadsWith([]string{"apply-suggestion", "owner/repo#1", "PRRT_x", "--repo-root", tmp, "--force"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit with --force=%d stderr=%s", exit, stderr.String())
	}
}

func TestSpnThreadsReply_SuggestFlag(t *testing.T) {
	cap := &captureReplyStub{}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return cap, nil }
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{
		"reply", "owner/repo#1", "PRRT_1",
		"--suggest", "new content",
		"--intro", "how about",
	}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	want := "how about\n\n```suggestion\nnew content\n```"
	if cap.lastBody != want {
		t.Errorf("body=%q\nwant %q", cap.lastBody, want)
	}
}

func TestSpnThreadsReply_SuggestAndBodyMutuallyExclusive(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return &captureReplyStub{}, nil }
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{
		"reply", "owner/repo#1", "PRRT_1",
		"--suggest", "X", "--body", "Y",
	}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d (want 2); stderr=%s", exit, stderr.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v", env["error"]["code"])
	}
}

// --- G4: --dry-run E2E tests -----------------------------------------------

// dryRunStub counts mutation calls for dry-run assertions and lets each test
// configure the threads slice. CurrentUserLogin and ReplyToThread are stubbed
// to known no-mutation behavior; ResolveThread / UnresolveThread bump counters.
type dryRunStub struct {
	stubAPI
	mu             sync.Mutex
	resolveCalls   int
	unresolveCalls int
}

func (d *dryRunStub) ResolveThread(_ context.Context, _ string) error {
	d.mu.Lock()
	d.resolveCalls++
	d.mu.Unlock()
	return nil
}
func (d *dryRunStub) UnresolveThread(_ context.Context, _ string) error {
	d.mu.Lock()
	d.unresolveCalls++
	d.mu.Unlock()
	return nil
}
func (d *dryRunStub) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	d.mu.Lock()
	// reply IS a mutation we want to verify is suppressed under --dry-run; lump
	// it into resolveCalls so the assertion catches either.
	d.resolveCalls++
	d.mu.Unlock()
	return github.ThreadComment{}, nil
}

func TestSpnThreadsResolve_DryRun_E2E(t *testing.T) {
	d := &dryRunStub{stubAPI: stubAPI{threads: []github.ReviewThread{{
		ID:       "PRRT_1",
		Comments: []github.ThreadComment{{AuthorType: "Bot"}},
	}}}}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return d, nil }
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve", "owner/repo#1", "PRRT_1", "--dry-run"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if got["dryRun"] != true {
		t.Errorf("expected dryRun=true in stdout, got %+v", got)
	}
	if d.resolveCalls != 0 {
		t.Errorf("expected 0 mutation calls on dry-run, got %d", d.resolveCalls)
	}
}

func TestSpnThreadsResolveAll_DryRun_E2E(t *testing.T) {
	d := &dryRunStub{stubAPI: stubAPI{threads: []github.ReviewThread{
		{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_b", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}}}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return d, nil }
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve-all", "owner/repo#1", "--dry-run"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if got["dryRun"] != true {
		t.Errorf("expected dryRun=true on BulkResult, got %+v", got)
	}
	succeeded, _ := got["succeeded"].([]any)
	if len(succeeded) != 2 {
		t.Errorf("succeeded=%+v want 2", succeeded)
	}
	if d.resolveCalls != 0 {
		t.Errorf("expected 0 mutation calls on dry-run, got %d", d.resolveCalls)
	}
}

func TestSpnThreadsUnresolveAll_DryRun_E2E(t *testing.T) {
	d := &dryRunStub{stubAPI: stubAPI{threads: []github.ReviewThread{
		{ID: "PRRT_x", IsResolved: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_y", IsResolved: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}}}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return d, nil }
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"unresolve-all", "owner/repo#1", "--dry-run"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if got["dryRun"] != true {
		t.Errorf("expected dryRun=true on BulkResult, got %+v", got)
	}
	succeeded, _ := got["succeeded"].([]any)
	if len(succeeded) != 2 {
		t.Errorf("succeeded=%+v want 2", succeeded)
	}
	if d.unresolveCalls != 0 {
		t.Errorf("expected 0 mutation calls on dry-run, got %d", d.unresolveCalls)
	}
}

func TestSpnThreadsResolve_DryRun_StillRejectsPolicyViolation(t *testing.T) {
	// Human thread, --dry-run, no --body: the policy gate still trips. exit=2
	// (bad_input / policy_violation), no mutation calls.
	d := &dryRunStub{stubAPI: stubAPI{threads: []github.ReviewThread{{
		ID:       "PRRT_1",
		Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}},
	}}}}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return d, nil }
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"resolve", "owner/repo#1", "PRRT_1", "--dry-run"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d (want 2 for policy_violation); stderr=%s", exit, stderr.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "policy_violation" {
		t.Errorf("code=%v want policy_violation", env["error"]["code"])
	}
	if d.resolveCalls != 0 {
		t.Errorf("expected 0 mutation calls; got %d", d.resolveCalls)
	}
}

func TestSpnThreadsReply_SuggestFile(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/sug.txt"
	if err := os.WriteFile(path, []byte("from file"), 0o644); err != nil {
		t.Fatal(err)
	}
	cap := &captureReplyStub{}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return cap, nil }
	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{
		"reply", "owner/repo#1", "PRRT_1",
		"--suggest-file", path,
	}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	want := "How about this?\n\n```suggestion\nfrom file\n```"
	if cap.lastBody != want {
		t.Errorf("body=%q\nwant %q", cap.lastBody, want)
	}
}

// --- G5: --show-code E2E tests ---------------------------------------------

// codeStub combines stubAPI's FetchPR with a ContentFetcher implementation so
// the api value passed to the spn handler satisfies both threadsops.API and
// threadsops.ContentFetcher (the latter is sniffed via contentFetcherFor).
type codeStub struct {
	stubAPI
	fileContent map[string]string // path → content; empty content means "fetch returns ''" (graceful skip)
	fetchedRef  string
}

func (c *codeStub) FetchFileContent(_ context.Context, _, _, path, ref string) (string, error) {
	c.fetchedRef = ref
	if content, ok := c.fileContent[path]; ok {
		return content, nil
	}
	return "", nil
}

// tenLineFileSpn builds a 10-line file used by spn tests.
func tenLineFileSpn() string {
	return "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10\n"
}

func TestSpnThreadsList_ShowCode_E2E(t *testing.T) {
	st := &codeStub{
		stubAPI: stubAPI{
			threads: []github.ReviewThread{{
				ID:       "PRRT_1",
				Path:     "a.go",
				Line:     5,
				Comments: []github.ThreadComment{{AuthorType: "Bot"}},
			}},
			headSHA: "abc1234567890",
		},
		fileContent: map[string]string{"a.go": tenLineFileSpn()},
	}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return st, nil }

	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1", "--show-code", "2"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON array: %v\n%s", err, stdout.String())
	}
	if len(got) != 1 {
		t.Fatalf("len=%d want 1", len(got))
	}
	cc, ok := got[0]["codeContext"].(map[string]any)
	if !ok {
		t.Fatalf("codeContext missing or wrong type: %+v", got[0])
	}
	if cc["ref"] != "abc1234567890" {
		t.Errorf("ref=%v want abc1234567890", cc["ref"])
	}
	lines, _ := cc["lines"].([]any)
	if len(lines) != 5 {
		t.Fatalf("lines=%d want 5", len(lines))
	}
	want := []string{"L3", "L4", "L5", "L6", "L7"}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("lines[%d]=%v want %s", i, lines[i], want[i])
		}
	}
}

func TestSpnThreadsList_ShowCode_NoAnchor(t *testing.T) {
	st := &codeStub{
		stubAPI: stubAPI{
			threads: []github.ReviewThread{{
				ID: "PRRT_1", Path: "", Line: 0,
				Comments: []github.ThreadComment{{AuthorType: "Bot"}},
			}},
		},
		fileContent: map[string]string{"a.go": tenLineFileSpn()},
	}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return st, nil }

	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1", "--show-code", "2"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	// codeContext must be absent (omitempty).
	if strings.Contains(stdout.String(), "codeContext") {
		t.Errorf("expected no codeContext for unanchored thread, got: %s", stdout.String())
	}
}

func TestSpnThreadsList_ShowCode_FileDeleted(t *testing.T) {
	st := &codeStub{
		stubAPI: stubAPI{
			threads: []github.ReviewThread{{
				ID: "PRRT_1", Path: "gone.go", Line: 3,
				Comments: []github.ThreadComment{{AuthorType: "Bot"}},
			}},
		},
		// Empty fileContent map → fetcher returns "" for any path → graceful skip.
		fileContent: map[string]string{},
	}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return st, nil }

	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1", "--show-code", "2"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if strings.Contains(stdout.String(), "codeContext") {
		t.Errorf("expected no codeContext when file fetch returns empty: %s", stdout.String())
	}
}

func TestSpnThreadsList_ShowCode_Outdated(t *testing.T) {
	st := &codeStub{
		stubAPI: stubAPI{
			threads: []github.ReviewThread{{
				ID: "PRRT_1", Path: "a.go", Line: 5, IsOutdated: true,
				Comments: []github.ThreadComment{{AuthorType: "Bot"}},
			}},
		},
		fileContent: map[string]string{"a.go": tenLineFileSpn()},
	}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return st, nil }

	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1", "--show-code", "2"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	cc, ok := got[0]["codeContext"].(map[string]any)
	if !ok {
		t.Fatalf("codeContext missing: %+v", got[0])
	}
	if cc["outdated"] != true {
		t.Errorf("outdated=%v want true", cc["outdated"])
	}
}

func TestSpnThreadsList_ShowCode_BadValue(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return &stubAPI{}, nil }

	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1", "--show-code", "abc"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d (want 2 for bad_input); stderr=%s", exit, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on bad_input, got %q", stdout.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v", env["error"]["code"])
	}
}

func TestSpnThreadsList_ShowCode_Zero(t *testing.T) {
	st := &codeStub{
		stubAPI: stubAPI{
			threads: []github.ReviewThread{{
				ID: "PRRT_1", Path: "a.go", Line: 5,
				Comments: []github.ThreadComment{{AuthorType: "Bot"}},
			}},
		},
		fileContent: map[string]string{"a.go": tenLineFileSpn()},
	}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return st, nil }

	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1", "--show-code", "0"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	// --show-code 0 must behave as if the flag weren't set: no codeContext key.
	if strings.Contains(stdout.String(), "codeContext") {
		t.Errorf("expected no codeContext with --show-code 0: %s", stdout.String())
	}
}

func TestSpnThreadsNext_ShowCode_E2E(t *testing.T) {
	st := &codeStub{
		stubAPI: stubAPI{
			threads: []github.ReviewThread{{
				ID:       "PRRT_1",
				Path:     "a.go",
				Line:     5,
				Comments: []github.ThreadComment{{AuthorType: "Bot", CreatedAt: "2026-01-01T00:00:00Z"}},
			}},
			headSHA: "feedface",
		},
		fileContent: map[string]string{"a.go": tenLineFileSpn()},
	}
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return st, nil }

	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"next", "owner/repo#1", "--show-code", "3"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	cc, ok := got["codeContext"].(map[string]any)
	if !ok {
		t.Fatalf("codeContext missing: %+v", got)
	}
	if cc["ref"] != "feedface" {
		t.Errorf("ref=%v want feedface", cc["ref"])
	}
	lines, _ := cc["lines"].([]any)
	// Single-line=5, context=3 → [2,8] → 7 lines.
	if len(lines) != 7 {
		t.Fatalf("lines=%d want 7: %+v", len(lines), lines)
	}
}

func TestSpnThreadsList_ShowCode_Negative(t *testing.T) {
	prev := apiFactory
	defer func() { apiFactory = prev }()
	apiFactory = func() (threadsops.API, *agentio.Error) { return &stubAPI{}, nil }

	var stdout, stderr bytes.Buffer
	exit := runThreadsWith([]string{"list", "owner/repo#1", "--show-code", "-3"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d (want 2 for bad_input); stderr=%s", exit, stderr.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, stderr.String())
	}
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v want bad_input", env["error"]["code"])
	}
}
