// cmd/spn/threads_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

// stubAPI is a minimal threadsops.API implementation for cmd/spn tests.
type stubAPI struct {
	threads []github.ReviewThread
}

func (s *stubAPI) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	return github.PullRequestStatus{Title: "test"}, s.threads, nil
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
	resolveCalls map[string]bool
}

func (b *bulkStub) ResolveThread(_ context.Context, id string) error {
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
