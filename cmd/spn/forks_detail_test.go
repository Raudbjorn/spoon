package main

import (
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
)

func TestDetailedForkJSONGatesAndAttributesFiles(t *testing.T) {
	r := forksops.Result{T2: &forge.T2Data{
		AheadCount: 1,
		Diffs:      []forge.FileDiff{{Path: "new.go", PreviousPath: "old.go", Status: "renamed", Patch: "@@", PatchSource: "compare_rest"}},
		Commits:    []forge.AheadCommit{{SHA: "abc", Message: "change", Timestamp: time.Unix(1, 0), Files: []forge.FileDiff{{Path: "new.go", PatchSource: "commit_rest"}}}},
	}, CommitFilesComplete: true}
	plain := forkToJSON(r)
	if _, ok := plain["t2"].(map[string]any)["files"]; ok {
		t.Fatal("default wire shape unexpectedly includes files")
	}
	if got := plain["t2"].(map[string]any)["files_truncated"]; got != false {
		t.Fatalf("files_truncated = %v, want false", got)
	}
	detailed := forkToJSONDetailed(r, detailOptions{files: true, commits: true, commitFiles: true})
	t2 := detailed["t2"].(map[string]any)
	files := t2["files"].([]map[string]any)
	if files[0]["previousPath"] != "old.go" || files[0]["patchSource"] != "compare_rest" {
		t.Fatalf("file attribution lost: %+v", files[0])
	}
	if t2["commit_files_complete"] != true {
		t.Fatalf("completion marker missing: %+v", t2)
	}
}

func TestForkToJSONTouchingBlock(t *testing.T) {
	r := forksops.Result{Touching: &forksops.TouchMatch{Status: forksops.TouchMatched, Files: []forksops.TouchedFile{{Path: "a.go", Status: "added", Additions: 3, Pattern: "*.go"}}}}
	out := forkToJSON(r)
	tb, ok := out["touching"].(map[string]any)
	if !ok || tb["status"] != "matched" {
		t.Fatalf("touching block missing: %v", out["touching"])
	}
	if _, has := forkToJSON(forksops.Result{})["touching"]; has {
		t.Fatal("touching must be omitted when the option was not set")
	}
}
