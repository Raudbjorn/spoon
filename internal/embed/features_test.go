package embed

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestBuildFeatures_Basic(t *testing.T) {
	t2 := forge.T2Data{
		Diffs: []forge.FileDiff{
			{Path: "z/late.go", Additions: 1, Deletions: 0},
			{Path: "a/early.go", Additions: 100, Deletions: 100},
			{Path: "b/mid.go", Additions: 5, Deletions: 3},
		},
		Commits: []forge.AheadCommit{
			{Message: "Add feature X"},
			{Message: "Merge branch 'main'"},
			{Message: "add feature x"},
			{Message: "Fix typo"},
		},
	}
	f := BuildFeatures(t2, "  My readme delta  ", 0)
	wantPaths := "a/early.go\nb/mid.go\nz/late.go"
	if f.Paths != wantPaths {
		t.Errorf("Paths = %q, want %q", f.Paths, wantPaths)
	}
	if !strings.Contains(f.Commits, "Add feature X") {
		t.Errorf("Commits missing first message: %q", f.Commits)
	}
	if strings.Contains(f.Commits, "Merge branch") {
		t.Errorf("Commits should drop merge messages: %q", f.Commits)
	}
	if strings.Count(strings.ToLower(f.Commits), "add feature") != 1 {
		t.Errorf("Commits should dedupe case-insensitively: %q", f.Commits)
	}
	if f.ReadmeDoc != "My readme delta" {
		t.Errorf("ReadmeDoc = %q", f.ReadmeDoc)
	}
	if f.DiffChunk == "" {
		t.Error("DiffChunk should not be empty")
	}
	// largest-first: a/early.go (200 churn) must come before z/late.go.
	earlyAt := strings.Index(f.DiffChunk, "a/early.go")
	lateAt := strings.Index(f.DiffChunk, "z/late.go")
	if earlyAt < 0 || lateAt < 0 || earlyAt > lateAt {
		t.Errorf("DiffChunk not ordered largest-first: %q", f.DiffChunk)
	}
}

func TestBuildFeatures_AlwaysIncludesLargestEvenWhenOverBudget(t *testing.T) {
	// With a tight budget below the size of a single diff line, the first
	// (largest) entry must still appear in DiffChunk — never silently dropped.
	t2 := forge.T2Data{
		Diffs: []forge.FileDiff{
			{Path: "some/long/path/to/file.go", Additions: 1000, Deletions: 1000},
		},
	}
	f := BuildFeatures(t2, "", 10) // 10 chars is way under the minimum diff-line length
	if f.DiffChunk == "" {
		t.Fatal("DiffChunk must contain at least the largest entry, got empty")
	}
	if !strings.Contains(f.DiffChunk, "some/long/path/to/file.go") {
		t.Errorf("DiffChunk missing the only entry: %q", f.DiffChunk)
	}
}

func TestBuildFeatures_EmptyInputs(t *testing.T) {
	f := BuildFeatures(forge.T2Data{}, "", 0)
	if f.Paths != "" || f.Commits != "" || f.ReadmeDoc != "" || f.DiffChunk != "" {
		t.Errorf("expected all empty, got %+v", f)
	}
}

func TestBuildFeatures_DiffTruncationByLargest(t *testing.T) {
	t2 := forge.T2Data{
		Diffs: []forge.FileDiff{
			{Path: "tiny.txt", Additions: 1, Deletions: 0},
			{Path: "huge.go", Additions: 10000, Deletions: 10000},
			{Path: "medium.md", Additions: 50, Deletions: 50},
		},
	}
	f := BuildFeatures(t2, "", 80) // tight budget so only the biggest fits
	if !strings.Contains(f.DiffChunk, "huge.go") {
		t.Errorf("DiffChunk should always include the largest entry: %q", f.DiffChunk)
	}
	if strings.Contains(f.DiffChunk, "tiny.txt") {
		t.Errorf("DiffChunk should not include tiny.txt under tight budget: %q", f.DiffChunk)
	}
}

func TestBuildFeatures_EmptyReadmeStillBuildsRest(t *testing.T) {
	t2 := forge.T2Data{
		Diffs: []forge.FileDiff{{Path: "x.go", Additions: 1, Deletions: 1}},
	}
	f := BuildFeatures(t2, "", 0)
	if f.ReadmeDoc != "" {
		t.Errorf("ReadmeDoc should be empty, got %q", f.ReadmeDoc)
	}
	if f.Paths == "" || f.DiffChunk == "" {
		t.Errorf("Paths/DiffChunk should still be built: %+v", f)
	}
}

func TestBuildFeatures_EmptyCommits(t *testing.T) {
	t2 := forge.T2Data{
		Diffs: []forge.FileDiff{{Path: "a", Additions: 1}},
	}
	f := BuildFeatures(t2, "readme", 0)
	if f.Commits != "" {
		t.Errorf("Commits should be empty, got %q", f.Commits)
	}
}

func TestNormalizeDiff_WhitespaceCollapse(t *testing.T) {
	in := "foo    bar\t\tbaz"
	got := NormalizeDiff(in)
	if got != "foo bar baz" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeDiff_PreservesNewlines(t *testing.T) {
	in := "line1\n\n  line2  "
	got := NormalizeDiff(in)
	if !strings.Contains(got, "\n\n") {
		t.Errorf("newlines not preserved: %q", got)
	}
}

func TestNormalizeDiff_HunkHeader(t *testing.T) {
	in := "@@ -10,5 +10,5 @@ context"
	got := NormalizeDiff(in)
	if !strings.HasPrefix(got, "@@@@") {
		t.Errorf("want @@@@ prefix, got %q", got)
	}
	if strings.Contains(got, "-10,5") {
		t.Errorf("hunk numbers not stripped: %q", got)
	}
}

func TestNormalizeDiff_IdentifierLowercasing(t *testing.T) {
	in := "MyVar OTHER_NAME 123 obj.Field func() 0xDEAD"
	got := NormalizeDiff(in)
	if !strings.Contains(got, "myvar") {
		t.Errorf("MyVar not lowercased: %q", got)
	}
	if !strings.Contains(got, "other_name") {
		t.Errorf("OTHER_NAME not lowercased: %q", got)
	}
	// 123 has no letters → unchanged
	if !strings.Contains(got, "123") {
		t.Errorf("123 should remain: %q", got)
	}
	// obj.Field contains '.' → not a pure identifier → untouched
	if !strings.Contains(got, "obj.Field") {
		t.Errorf("obj.Field should be untouched: %q", got)
	}
	// func() contains '(' → untouched
	if !strings.Contains(got, "func()") {
		t.Errorf("func() should be untouched: %q", got)
	}
	// 0xDEAD starts with digit → not a pure identifier per regex → untouched
	if !strings.Contains(got, "0xDEAD") {
		t.Errorf("0xDEAD should be untouched: %q", got)
	}
}

func TestNormalizeDiff_Empty(t *testing.T) {
	if got := NormalizeDiff(""); got != "" {
		t.Errorf("empty input should stay empty, got %q", got)
	}
}
