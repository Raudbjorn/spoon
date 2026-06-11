package github

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
	if !threads[1].IsResolved {
		t.Errorf("thread[1] should be resolved")
	}
	if threads[0].DiffSide != "RIGHT" || threads[1].DiffSide != "RIGHT" {
		t.Errorf("diffSide mismatch: %q, %q", threads[0].DiffSide, threads[1].DiffSide)
	}
	if threads[0].ReviewerType != "User" || threads[0].ReviewerLogin != "alice" {
		t.Errorf("thread[0] reviewer mismatch: type=%q login=%q", threads[0].ReviewerType, threads[0].ReviewerLogin)
	}
	if threads[1].ReviewerType != "Bot" || threads[1].ReviewerLogin != "dependabot" {
		t.Errorf("thread[1] reviewer mismatch: type=%q login=%q", threads[1].ReviewerType, threads[1].ReviewerLogin)
	}
	if !threads[0].IsOutdated {
		t.Errorf("thread[0] should be outdated")
	}
	if threads[1].IsOutdated {
		t.Errorf("thread[1] should not be outdated")
	}
}

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
		{"mannequin counts as user", ReviewThread{Comments: []ThreadComment{{AuthorType: "Mannequin"}}}, true},
		{"team counts as user", ReviewThread{Comments: []ThreadComment{{AuthorType: "Team"}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.t.RequiresBody(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBulkResultMerge(t *testing.T) {
	r := BulkResult{}
	r.AddSuccess("a")
	r.AddSuccess("b")
	r.AddFailure("c", fmt.Errorf("boom"))
	if len(r.Succeeded) != 2 || len(r.Failed) != 1 {
		t.Fatalf("got %+v", &r)
	}
	if r.Failed[0].ID != "c" || r.Failed[0].Err == nil {
		t.Errorf("failure capture broken: %+v", r.Failed[0])
	}
}

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
	if !threads[0].IsOutdated {
		t.Errorf("thread[0] should be outdated")
	}
	if status.OutdatedThreads != 1 {
		t.Errorf("OutdatedThreads=%d want 1", status.OutdatedThreads)
	}
}

// TestParseListThreadsResponse_VerboseFields verifies that the parser populates
// the per-comment verbose fields (createdAt, updatedAt, authorUrl) on the
// ThreadComment struct. These fields are always fetched server-side; gating is
// at the CLI / output layer via threadsops.StripVerboseFields.
func TestParseListThreadsResponse_VerboseFields(t *testing.T) {
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
	c0 := threads[0].Comments[0]
	if c0.CreatedAt != "2026-05-10T09:01:23Z" {
		t.Errorf("comment[0].CreatedAt=%q want 2026-05-10T09:01:23Z", c0.CreatedAt)
	}
	if c0.UpdatedAt != "2026-05-10T09:05:00Z" {
		t.Errorf("comment[0].UpdatedAt=%q want 2026-05-10T09:05:00Z", c0.UpdatedAt)
	}
	if c0.AuthorURL != "https://github.com/alice" {
		t.Errorf("comment[0].AuthorURL=%q want https://github.com/alice", c0.AuthorURL)
	}
	c1 := threads[1].Comments[0]
	if c1.CreatedAt != "2026-05-10T08:30:00Z" {
		t.Errorf("comment[1].CreatedAt=%q", c1.CreatedAt)
	}
	if c1.UpdatedAt != "2026-05-10T08:30:00Z" {
		t.Errorf("comment[1].UpdatedAt=%q", c1.UpdatedAt)
	}
	if c1.AuthorURL != "https://github.com/apps/dependabot" {
		t.Errorf("comment[1].AuthorURL=%q", c1.AuthorURL)
	}
}

// TestReviewThreadJSONRoundTrip verifies that IsOutdated round-trips through
// JSON serialization (load-bearing for spn/spoon JSON outputs that embed
// github.ReviewThread).
func TestReviewThreadJSONRoundTrip(t *testing.T) {
	in := ReviewThread{ID: "PRRT_X", IsResolved: false, IsOutdated: true, Path: "a.go", Line: 7}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"isOutdated":true`) {
		t.Errorf("marshaled JSON missing isOutdated:true — %s", b)
	}
	var out ReviewThread
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.IsOutdated {
		t.Errorf("round-trip lost IsOutdated")
	}
}
