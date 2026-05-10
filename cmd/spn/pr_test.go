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
