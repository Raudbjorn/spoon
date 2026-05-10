// cmd/spn/threads_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

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
func (s *stubAPI) ResolveThread(_ context.Context, _ string) error                          { return nil }
func (s *stubAPI) UnresolveThread(_ context.Context, _ string) error                        { return nil }
func (s *stubAPI) ResolveAllThreads(_ context.Context, _, _ string, _, _ int) (*github.BulkResult, error) { return nil, nil }
func (s *stubAPI) UnresolveAllThreads(_ context.Context, _, _ string, _, _ int) (*github.BulkResult, error) { return nil, nil }
func (s *stubAPI) CurrentUserLogin(_ context.Context) (string, error)                       { return "", errors.New("no user") }

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
