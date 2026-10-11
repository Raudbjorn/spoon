package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/unidiff"
)

// newDiffTestClient builds a *Client whose ordinary REST requests (Rest) and
// diff-Accept requests both land on srv, distinguished only by
// the Accept header each carries — mirroring how GitHub's compare endpoint
// serves JSON or a unified diff off the exact same URL depending on Accept.
func newDiffTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	tr := &rewriteTransport{target: u, base: http.DefaultTransport}
	rest, err := newRESTClient("x", tr)
	if err != nil {
		t.Fatalf("newRESTClient: %v", err)
	}
	return &Client{rest: rest, authenticated: true}
}

const sampleSingleFileDiff = "diff --git a/a.go b/a.go\n" +
	"index 1111111..2222222 100644\n" +
	"--- a/a.go\n" +
	"+++ b/a.go\n" +
	"@@ -1 +1 @@\n" +
	"-old\n" +
	"+new\n"

// FetchCompareDiff must request GitHub's unified-diff representation (not
// JSON) and parse the result.
func TestFetchCompareDiff_SetsAcceptHeaderAndParses(t *testing.T) {
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(sampleSingleFileDiff))
	}))
	defer srv.Close()

	c := newDiffTestClient(t, srv)
	files, complete, err := c.FetchCompareDiff(context.Background(), "up", "stream", "main", "fork", "feature")
	if err != nil {
		t.Fatalf("FetchCompareDiff: %v", err)
	}
	if !complete {
		t.Error("complete = false, want true")
	}
	if gotAccept != diffAcceptHeader {
		t.Errorf("Accept header = %q, want %q", gotAccept, diffAcceptHeader)
	}
	if len(files) != 1 || files[0].Path != "a.go" {
		t.Fatalf("files = %+v, want one entry for a.go", files)
	}
	if files[0].Additions != 1 || files[0].Deletions != 1 {
		t.Errorf("a.go Additions/Deletions = %d/%d, want 1/1", files[0].Additions, files[0].Deletions)
	}
}

// A response exceeding maxDiffBytes is fail-soft: no files, complete=false,
// and a descriptive error a caller can log/record as FilesTruncatedReason.
func TestFetchCompareDiff_OverCapIsFailSoft(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		chunk := bytes.Repeat([]byte("a"), 1<<20) // 1 MiB
		// maxDiffBytes is 32 MiB; write comfortably past it.
		for i := 0; i < 33; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := newDiffTestClient(t, srv)
	files, complete, err := c.FetchCompareDiff(context.Background(), "up", "stream", "main", "fork", "feature")
	if complete {
		t.Error("complete = true, want false for an over-cap diff")
	}
	if files != nil {
		t.Errorf("files = %v, want nil for an over-cap diff", files)
	}
	if err == nil {
		t.Fatal("err = nil, want a descriptive error for the over-cap case")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("err = %v, want it to mention the byte cap", err)
	}
}

// A 406 (GitHub's diff renderer refuses this compare) is fail-soft: no
// error, complete=false, no files — exactly like FetchCompare's 404 path.
func TestFetchCompareDiff_NotAcceptableIsFailSoft(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotAcceptable)
	}))
	defer srv.Close()

	c := newDiffTestClient(t, srv)
	files, complete, err := c.FetchCompareDiff(context.Background(), "up", "stream", "main", "fork", "feature")
	if err != nil {
		t.Errorf("err = %v, want nil (406 is fail-soft)", err)
	}
	if complete {
		t.Error("complete = true, want false")
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}

// A 404 (fork gone) is fail-soft the same way, matching FetchCompare.
func TestFetchCompareDiff_NotFoundIsFailSoft(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newDiffTestClient(t, srv)
	files, complete, err := c.FetchCompareDiff(context.Background(), "up", "stream", "main", "fork", "feature")
	if err != nil {
		t.Errorf("err = %v, want nil (404 is fail-soft)", err)
	}
	if complete {
		t.Error("complete = true, want false")
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}

// A 422 (compare not computable) is fail-soft the same way.
func TestFetchCompareDiff_UnprocessableIsFailSoft(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	c := newDiffTestClient(t, srv)
	files, complete, err := c.FetchCompareDiff(context.Background(), "up", "stream", "main", "fork", "feature")
	if err != nil {
		t.Errorf("err = %v, want nil (422 is fail-soft)", err)
	}
	if complete {
		t.Error("complete = true, want false")
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}

// End-to-end: a 300-entry JSON compare (hitting forge.CompareFilesCap) plus
// a 305-file diff response on the same server. finishT2 (reached here via
// CompareResolved) must recover the full file list, recompute totals/MNA
// from it, carry REST's patch over for files the JSON already covered, and
// clear FilesTruncated/set FilesComplete.
func TestFinishT2_DiffFallbackRecoversTruncatedFiles(t *testing.T) {
	const jsonFileCount = forge.CompareFilesCap // 300
	const diffFileCount = 305

	const restStubPatch = "@@ -1,1 +1,1 @@\n-stub old\n+stub new\n"

	jsonFiles := make([]map[string]any, jsonFileCount)
	for i := 0; i < jsonFileCount; i++ {
		jsonFiles[i] = map[string]any{
			"filename":  fmt.Sprintf("file%03d.go", i),
			"status":    "modified",
			"additions": 1,
			"deletions": 0,
			"patch":     restStubPatch,
		}
	}
	jsonResp := map[string]any{
		"ahead_by":      diffFileCount,
		"behind_by":     0,
		"total_commits": 1,
		"commits":       []map[string]any{{"sha": "sha_tip"}},
		"files":         jsonFiles,
	}

	// The diff carries the real hunks: 2 additions + 1 deletion for each of
	// the first 300 (paths matching the JSON stub, so the carry-over and
	// recompute logic is exercised against a REAL discrepancy between the
	// JSON's stub counts and the diff's actual counts), plus 5 new files the
	// JSON never saw at all (3 additions each, "added" status).
	var diffBuilder strings.Builder
	for i := 0; i < jsonFileCount; i++ {
		path := fmt.Sprintf("file%03d.go", i)
		fmt.Fprintf(&diffBuilder,
			"diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n"+
				"@@ -1,2 +1,3 @@\n context\n-old line\n+new line one\n+new line two\n",
			path, path, path, path)
	}
	for i := jsonFileCount; i < diffFileCount; i++ {
		path := fmt.Sprintf("extra%03d.go", i)
		fmt.Fprintf(&diffBuilder,
			"diff --git a/%s b/%s\nnew file mode 100644\nindex 0000000..3333333\n--- /dev/null\n+++ b/%s\n"+
				"@@ -0,0 +1,3 @@\n+line one\n+line two\n+line three\n",
			path, path, path)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == diffAcceptHeader {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(diffBuilder.String()))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jsonResp)
	}))
	defer srv.Close()

	c := newDiffTestClient(t, srv)
	p := NewGHProvider(c, AuthStatus{})
	p.SetCompareBaseline("up", "stream", "main")

	fork := forge.T1Data{ID: "forkowner/repo", Owner: "forkowner", Name: "repo", DefaultBranch: "main"}
	sel := forge.BranchSelection{Branch: "main"}

	t2, err := p.CompareResolved(context.Background(), fork, sel)
	if err != nil {
		t.Fatalf("CompareResolved: %v", err)
	}

	if !t2.Performed {
		t.Fatal("t2.Performed = false, want true")
	}
	if t2.FilesTruncated {
		t.Error("t2.FilesTruncated = true, want false after a successful diff fallback")
	}
	if !t2.FilesComplete {
		t.Error("t2.FilesComplete = false, want true after a successful diff fallback")
	}
	if t2.FilesTruncatedReason != "" {
		t.Errorf("t2.FilesTruncatedReason = %q, want empty", t2.FilesTruncatedReason)
	}
	if t2.IsFilesTruncated() {
		t.Error("t2.IsFilesTruncated() = true, want false")
	}
	if got := len(t2.Diffs); got != diffFileCount {
		t.Fatalf("len(t2.Diffs) = %d, want %d", got, diffFileCount)
	}

	wantTotalAdd := jsonFileCount*2 + (diffFileCount-jsonFileCount)*3
	wantTotalDel := jsonFileCount * 1
	if t2.TotalAdditions != wantTotalAdd {
		t.Errorf("TotalAdditions = %d, want %d", t2.TotalAdditions, wantTotalAdd)
	}
	if t2.TotalDeletions != wantTotalDel {
		t.Errorf("TotalDeletions = %d, want %d", t2.TotalDeletions, wantTotalDel)
	}
	// heat.FileWeight(".go") == 1.0 and every file's net is non-negative
	// here, so MNA reduces to plain net additions across all 305 files.
	wantMNA := wantTotalAdd - wantTotalDel
	if t2.MNA != wantMNA {
		t.Errorf("MNA = %d, want %d", t2.MNA, wantMNA)
	}

	byPath := make(map[string]forge.FileDiff, len(t2.Diffs))
	for _, d := range t2.Diffs {
		byPath[d.Path] = d
	}

	first, ok := byPath["file000.go"]
	if !ok {
		t.Fatal("file000.go missing from merged Diffs")
	}
	if first.Patch != restStubPatch {
		t.Errorf("file000.go Patch = %q, want the REST stub patch carried over", first.Patch)
	}
	if first.PatchSource != "compare_rest" {
		t.Errorf("file000.go PatchSource = %q, want compare_rest (carried over from the JSON compare)", first.PatchSource)
	}
	if first.Additions != 2 || first.Deletions != 1 {
		t.Errorf("file000.go Additions/Deletions = %d/%d, want 2/1 (from the diff, not the JSON stub)", first.Additions, first.Deletions)
	}

	extra, ok := byPath["extra300.go"]
	if !ok {
		t.Fatal("extra300.go missing from merged Diffs -- the diff fallback did not recover files beyond the JSON cap")
	}
	if extra.Status != "added" || extra.Additions != 3 || extra.Deletions != 0 {
		t.Errorf("extra300.go = %+v, want status=added additions=3 deletions=0", extra)
	}
	if extra.PatchSource != unidiff.PatchSourceComplete {
		t.Errorf("extra300.go PatchSource = %q, want %q (no REST patch existed to carry over)", extra.PatchSource, unidiff.PatchSourceComplete)
	}
}

// When the diff fallback itself fails (e.g. every request 404s), finishT2
// must leave t2.Performed and t2.Diffs alone and only record a reason --
// never turn a successful compare into an error.
func TestFinishT2_DiffFallbackFailureLeavesCompareIntact(t *testing.T) {
	const jsonFileCount = forge.CompareFilesCap

	jsonFiles := make([]map[string]any, jsonFileCount)
	for i := 0; i < jsonFileCount; i++ {
		jsonFiles[i] = map[string]any{
			"filename":  fmt.Sprintf("file%03d.go", i),
			"status":    "modified",
			"additions": 1,
			"deletions": 0,
		}
	}
	jsonResp := map[string]any{
		"ahead_by":      jsonFileCount,
		"behind_by":     0,
		"total_commits": 0,
		"files":         jsonFiles,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == diffAcceptHeader {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jsonResp)
	}))
	defer srv.Close()

	c := newDiffTestClient(t, srv)
	p := NewGHProvider(c, AuthStatus{})
	p.SetCompareBaseline("up", "stream", "main")

	fork := forge.T1Data{ID: "forkowner/repo", Owner: "forkowner", Name: "repo", DefaultBranch: "main"}
	sel := forge.BranchSelection{Branch: "main"}

	t2, err := p.CompareResolved(context.Background(), fork, sel)
	if err != nil {
		t.Fatalf("CompareResolved: %v", err)
	}
	if !t2.Performed {
		t.Error("t2.Performed = false, want true -- a failed diff fallback must not fail the compare")
	}
	if len(t2.Diffs) != jsonFileCount {
		t.Errorf("len(t2.Diffs) = %d, want %d (unchanged from the JSON compare)", len(t2.Diffs), jsonFileCount)
	}
	if !t2.FilesTruncated {
		t.Error("t2.FilesTruncated = false, want true (fallback did not resolve it)")
	}
	if t2.FilesComplete {
		t.Error("t2.FilesComplete = true, want false")
	}
	if t2.FilesTruncatedReason == "" {
		t.Error("t2.FilesTruncatedReason is empty, want an explanation")
	}
	if !t2.IsFilesTruncated() {
		t.Error("t2.IsFilesTruncated() = false, want true")
	}
}
