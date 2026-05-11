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
