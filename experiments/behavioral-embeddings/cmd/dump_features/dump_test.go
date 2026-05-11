package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// fakeREST is an in-memory restClient. routes maps URL paths to JSON-encoded
// response bodies. A missing path returns an error.
type fakeREST struct {
	routes map[string]string
	calls  []string
}

func (f *fakeREST) Get(path string, response any) error {
	f.calls = append(f.calls, path)
	body, ok := f.routes[path]
	if !ok {
		return fmt.Errorf("fakeREST: no route for %q", path)
	}
	return json.Unmarshal([]byte(body), response)
}

// staticRoutes constructs a fakeREST seeded with a PR listing plus per-PR
// files/commits routes.
func staticRoutes(owner, repo string, prs []prInfo, files map[int][]prFile, commits map[int][]prCommit) *fakeREST {
	routes := map[string]string{}
	prJSON, _ := json.Marshal(prs)
	routes[fmt.Sprintf("repos/%s/%s/pulls?state=all&sort=updated&direction=desc&per_page=100&page=1", owner, repo)] = string(prJSON)
	routes[fmt.Sprintf("repos/%s/%s/pulls?state=all&sort=updated&direction=desc&per_page=100&page=2", owner, repo)] = "[]"
	for n, fs := range files {
		fb, _ := json.Marshal(fs)
		routes[fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100&page=1", owner, repo, n)] = string(fb)
		routes[fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100&page=2", owner, repo, n)] = "[]"
	}
	for n, cs := range commits {
		cb, _ := json.Marshal(cs)
		routes[fmt.Sprintf("repos/%s/%s/pulls/%d/commits?per_page=100&page=1", owner, repo, n)] = string(cb)
		routes[fmt.Sprintf("repos/%s/%s/pulls/%d/commits?per_page=100&page=2", owner, repo, n)] = "[]"
	}
	return &fakeREST{routes: routes}
}

func TestListPRs_TopNCap(t *testing.T) {
	prs := []prInfo{
		{Number: 1, Title: "first", State: "open"},
		{Number: 2, Title: "second", State: "open"},
		{Number: 3, Title: "third", State: "open"},
	}
	fr := staticRoutes("o", "r", prs, nil, nil)
	got, err := listPRs(fr, "o", "r", 2)
	if err != nil {
		t.Fatalf("listPRs: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 PRs, got %d", len(got))
	}
	if got[0].Number != 1 || got[1].Number != 2 {
		t.Errorf("unexpected order: %v", got)
	}
}

func TestPRToT2Data_BuildsExpectedShape(t *testing.T) {
	pr := prInfo{Number: 42, Title: "Add OAuth provider"}
	files := []prFile{
		{Filename: "auth/oauth.py", Additions: 80, Deletions: 0},
		{Filename: "tests/test_oauth.py", Additions: 40, Deletions: 0},
	}
	commits := []prCommit{
		{Commit: struct {
			Message string `json:"message"`
		}{Message: "implement OAuth provider"}},
		{Commit: struct {
			Message string `json:"message"`
		}{Message: "tests for OAuth"}},
	}
	t2 := prToT2Data(pr, files, commits)
	if len(t2.Diffs) != 2 {
		t.Fatalf("want 2 diffs, got %d", len(t2.Diffs))
	}
	if t2.Diffs[0].Path != "auth/oauth.py" || t2.Diffs[0].Additions != 80 {
		t.Errorf("Diffs[0] wrong: %+v", t2.Diffs[0])
	}
	// PR title prepended to commits.
	if len(t2.Commits) != 3 {
		t.Fatalf("want 3 commits (title + 2 originals), got %d", len(t2.Commits))
	}
	if t2.Commits[0].Message != "Add OAuth provider" {
		t.Errorf("title not prepended: %q", t2.Commits[0].Message)
	}
}

func TestPRToT2Data_EmptyTitleIsSkipped(t *testing.T) {
	pr := prInfo{Number: 1, Title: "   "}
	t2 := prToT2Data(pr, nil, []prCommit{
		{Commit: struct {
			Message string `json:"message"`
		}{Message: "fix"}},
	})
	if len(t2.Commits) != 1 || t2.Commits[0].Message != "fix" {
		t.Errorf("blank title should be omitted; got %v", t2.Commits)
	}
}

func TestDumpFeatures_BuildsRecordsAndDropsEmpty(t *testing.T) {
	// PR #1 has files (will produce non-empty DiffChunk).
	// PR #2 has no files (will produce empty DiffChunk → dropped).
	prs := []prInfo{
		{Number: 1, Title: "Add metrics", HTMLURL: "https://example/1"},
		{Number: 2, Title: "Empty PR", HTMLURL: "https://example/2"},
	}
	prs[0].Head.Repo = &struct {
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}{FullName: "alice/r", Owner: struct {
		Login string `json:"login"`
	}{Login: "alice"}, Name: "r"}
	files := map[int][]prFile{
		1: {{Filename: "metrics.go", Additions: 30, Deletions: 0}},
		2: {},
	}
	commits := map[int][]prCommit{
		1: {{Commit: struct {
			Message string `json:"message"`
		}{Message: "add /metrics endpoint"}}},
		2: {{Commit: struct {
			Message string `json:"message"`
		}{Message: "nothing"}}},
	}
	fr := staticRoutes("base", "repo", prs, files, commits)

	prev := defaultClient
	defaultClient = func() (restClient, error) { return fr, nil }
	t.Cleanup(func() { defaultClient = prev })

	got, err := dumpFeatures(context.Background(), "base", "repo", 0)
	if err != nil {
		t.Fatalf("dumpFeatures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 record (the empty PR should be dropped), got %d: %+v", len(got), got)
	}
	if got[0].ID != "pr-1" {
		t.Errorf("ID = %q, want pr-1", got[0].ID)
	}
	if got[0].Owner != "alice" || got[0].Name != "r" {
		t.Errorf("Owner/Name = %q/%q, want alice/r", got[0].Owner, got[0].Name)
	}
	if !strings.Contains(got[0].Features.Paths, "metrics.go") {
		t.Errorf("Paths missing metrics.go: %q", got[0].Features.Paths)
	}
}

func TestDumpFeatures_FallsBackToBaseRepoWhenHeadDeleted(t *testing.T) {
	// head.repo == nil happens when the source fork was deleted; we fall
	// back to the base repo identity so the record still carries some signal.
	prs := []prInfo{
		{Number: 5, Title: "Patch from deleted fork"},
	}
	files := map[int][]prFile{
		5: {{Filename: "x.go", Additions: 3}},
	}
	commits := map[int][]prCommit{
		5: {},
	}
	fr := staticRoutes("base", "repo", prs, files, commits)
	prev := defaultClient
	defaultClient = func() (restClient, error) { return fr, nil }
	t.Cleanup(func() { defaultClient = prev })

	got, err := dumpFeatures(context.Background(), "base", "repo", 0)
	if err != nil {
		t.Fatalf("dumpFeatures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 record, got %d", len(got))
	}
	if got[0].Owner != "base" || got[0].Name != "repo" {
		t.Errorf("Owner/Name = %q/%q, want base/repo", got[0].Owner, got[0].Name)
	}
}

func TestSplitRepo(t *testing.T) {
	cases := []struct {
		in, wantO, wantR string
		wantErr          bool
	}{
		{"foo/bar", "foo", "bar", false},
		{"foo", "", "", true},
		{"/bar", "", "", true},
		{"foo/", "", "", true},
		{"foo/bar/baz", "foo", "bar/baz", false}, // SplitN(_, 2) keeps the rest in part[1]
	}
	for _, c := range cases {
		o, r, err := splitRepo(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("splitRepo(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if o != c.wantO || r != c.wantR {
			t.Errorf("splitRepo(%q) = (%q, %q), want (%q, %q)", c.in, o, r, c.wantO, c.wantR)
		}
	}
}
