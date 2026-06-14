package threadsops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func threadAt(path string, line int, startLine *int) ReviewThreadWithPolicy {
	return ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{
			ID:        "T_x",
			Path:      path,
			Line:      line,
			StartLine: startLine,
		},
	}
}

func TestApplySuggestion_SingleLineReplace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\nL3\nL4\nL5\n")
	thr := threadAt("a.go", 3, nil)
	sug := Suggestion{CommentID: "C", Body: "REPLACED"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("Applied=false; res=%+v", res)
	}
	got := readFile(t, filepath.Join(dir, "a.go"))
	want := "L1\nL2\nREPLACED\nL4\nL5\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if len(res.OldLines) != 1 || res.OldLines[0] != "L3" {
		t.Errorf("OldLines=%+v", res.OldLines)
	}
	if len(res.NewLines) != 1 || res.NewLines[0] != "REPLACED" {
		t.Errorf("NewLines=%+v", res.NewLines)
	}
}

func TestApplySuggestion_MultiLineReplace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\nL3\nL4\nL5\n")
	start := 3
	thr := threadAt("a.go", 5, &start)
	sug := Suggestion{CommentID: "C", Body: "X\nY"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("not applied: %+v", res)
	}
	got := readFile(t, filepath.Join(dir, "a.go"))
	want := "L1\nL2\nX\nY\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestApplySuggestion_DryRun(t *testing.T) {
	dir := t.TempDir()
	original := "L1\nL2\nL3\n"
	writeFile(t, dir, "a.go", original)
	thr := threadAt("a.go", 2, nil)
	sug := Suggestion{CommentID: "C", Body: "NEW"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir, DryRun: true})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if res.Applied {
		t.Errorf("Applied should be false in dry-run")
	}
	if !res.DryRun {
		t.Errorf("DryRun should be true")
	}
	if got := readFile(t, filepath.Join(dir, "a.go")); got != original {
		t.Errorf("file changed in dry-run: %q", got)
	}
	if len(res.OldLines) != 1 || res.OldLines[0] != "L2" {
		t.Errorf("OldLines=%+v", res.OldLines)
	}
	if len(res.NewLines) != 1 || res.NewLines[0] != "NEW" {
		t.Errorf("NewLines=%+v", res.NewLines)
	}
}

func TestApplySuggestion_FileMissing(t *testing.T) {
	dir := t.TempDir()
	thr := threadAt("missing.go", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "x"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil {
		t.Fatal("expected error")
	}
	if opErr.Code != OpCodeNotFound {
		t.Errorf("code=%q want not_found", opErr.Code)
	}
}

func TestApplySuggestion_Outdated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\n")
	thr := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{
			ID: "T", Path: "a.go", Line: 1, IsOutdated: true,
		},
	}
	sug := Suggestion{CommentID: "C", Body: "X"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil {
		t.Fatal("expected outdated error")
	}
	if opErr.Code != OpCodePolicy {
		t.Errorf("code=%q want policy_violation", opErr.Code)
	}
	if got := readFile(t, filepath.Join(dir, "a.go")); got != "L1\nL2\n" {
		t.Errorf("file should be unchanged: %q", got)
	}
}

func TestApplySuggestion_OutdatedWithForce(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\n")
	thr := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{
			ID: "T", Path: "a.go", Line: 1, IsOutdated: true,
		},
	}
	sug := Suggestion{CommentID: "C", Body: "FORCED"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir, Force: true})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("not applied")
	}
	if got := readFile(t, filepath.Join(dir, "a.go")); got != "FORCED\nL2\n" {
		t.Errorf("got %q", got)
	}
}

func TestApplySuggestion_NoPath(t *testing.T) {
	dir := t.TempDir()
	thr := ReviewThreadWithPolicy{ReviewThread: github.ReviewThread{ID: "T", Path: "", Line: 0}}
	sug := Suggestion{CommentID: "C", Body: "x"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Errorf("expected bad_input, got %+v", opErr)
	}
}

func TestApplySuggestion_PreservesFilePermissions(t *testing.T) {
	// Files that ship with an executable bit (scripts, hooks) must keep that
	// bit after a suggestion is applied. A naive 0o644 write would silently
	// reset the mode and break the executable.
	dir := t.TempDir()
	p := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(p, []byte("L1\nL2\nL3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		// Chmod may be a no-op under restrictive umasks; the initial WriteFile
		// already requested 0o755 so this is belt-and-braces.
		t.Fatal(err)
	}
	thr := threadAt("run.sh", 2, nil)
	sug := Suggestion{CommentID: "C", Body: "NEW"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Fatalf("not applied: %+v", res)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("mode after write = %o, want 0755 (executable bit lost)", got)
	}
}

func TestApplySuggestion_EmptyCommentID(t *testing.T) {
	// A Suggestion with no CommentID isn't tied to a real review comment, so
	// applying it would silently lose attribution and skip the "did this come
	// from a reviewer?" sanity check. Reject up front as bad_input.
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\nL3\n")
	thr := threadAt("a.go", 2, nil)
	sug := Suggestion{CommentID: "", Body: "X"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Fatalf("expected bad_input for empty CommentID, got %+v", opErr)
	}
}

func TestApplySuggestion_LineOutOfRange(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\n")
	thr := threadAt("a.go", 99, nil)
	sug := Suggestion{CommentID: "C", Body: "x"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Errorf("expected bad_input, got %+v", opErr)
	}
}

func TestApplySuggestion_NestedPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/a.go", "L1\nL2\nL3\n")
	thr := threadAt("sub/a.go", 2, nil)
	sug := Suggestion{CommentID: "C", Body: "X"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("not applied")
	}
	got := readFile(t, filepath.Join(dir, "sub/a.go"))
	if !strings.Contains(got, "X") {
		t.Errorf("expected substitution; got %q", got)
	}
}

func TestApplySuggestion_NoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2")
	thr := threadAt("a.go", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "X"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("not applied")
	}
	got := readFile(t, filepath.Join(dir, "a.go"))
	if got != "X\nL2" {
		t.Errorf("got %q want %q (trailing newline must be preserved as-was)", got, "X\nL2")
	}
}

// --- Path traversal guards ---------------------------------------------------

func TestApplySuggestion_PathTraversal_ParentEscape(t *testing.T) {
	dir := t.TempDir()
	thr := threadAt("../escape.txt", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "x"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Fatalf("expected bad_input for parent escape, got %+v", opErr)
	}
	if !strings.Contains(opErr.Message, "escapes repo root") {
		t.Errorf("error message should mention escape: %q", opErr.Message)
	}
}

func TestApplySuggestion_PathTraversal_DeepEscape(t *testing.T) {
	dir := t.TempDir()
	thr := threadAt("../../../etc/passwd", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "x"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Fatalf("expected bad_input for deep escape, got %+v", opErr)
	}
}

func TestApplySuggestion_PathTraversal_Absolute(t *testing.T) {
	dir := t.TempDir()
	thr := threadAt("/etc/foo", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "x"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Fatalf("expected bad_input for absolute path, got %+v", opErr)
	}
}

func TestApplySuggestion_PathTraversal_SymlinkInRepo(t *testing.T) {
	// Create a tmpdir + a target file OUTSIDE it; symlink under tmpdir points
	// at the outside target. ApplySuggestion must refuse to write through
	// the symlink (avoids clobbering arbitrary files via a versioned link).
	outside := t.TempDir()
	target := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(target, []byte("DO NOT TOUCH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link.go")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation not supported: %v", err)
	}
	thr := threadAt("link.go", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "PWNED"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Fatalf("expected bad_input for symlink, got %+v", opErr)
	}
	if !strings.Contains(opErr.Message, "symlink") {
		t.Errorf("error message should mention symlink: %q", opErr.Message)
	}
	// And the outside file must be untouched.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "DO NOT TOUCH\n" {
		t.Fatalf("symlink target was clobbered: %q", got)
	}
}

func TestApplySuggestion_PathTraversal_SymlinkedDir(t *testing.T) {
	// An *intermediate* symlinked directory (e.g. `sub` -> /etc) passes the
	// textual `..` check and the final-element Lstat, yet still escapes the
	// repo. ApplySuggestion must resolve symlinks on the parent dir and refuse
	// to write when it lands outside the repo root.
	outside := t.TempDir()
	target := filepath.Join(outside, "passwd")
	if err := os.WriteFile(target, []byte("DO NOT TOUCH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// `sub` inside the repo points at the outside directory.
	if err := os.Symlink(outside, filepath.Join(dir, "sub")); err != nil {
		t.Skipf("symlink creation not supported: %v", err)
	}
	thr := threadAt("sub/passwd", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "PWNED"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Fatalf("expected bad_input for symlinked-dir escape, got %+v", opErr)
	}
	if !strings.Contains(opErr.Message, "symlinked directory") {
		t.Errorf("error message should mention symlinked directory: %q", opErr.Message)
	}
	// And the outside file must be untouched.
	if got := readFile(t, target); got != "DO NOT TOUCH\n" {
		t.Fatalf("symlink target was clobbered: %q", got)
	}
}

func TestApplySuggestion_BadRepoRoot(t *testing.T) {
	// A non-existent --repo-root is a user-input error, surfaced as bad_input
	// (not internal) for consistent error-code semantics.
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	thr := threadAt("a.go", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "X"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: missing})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Fatalf("expected bad_input for missing repo root, got %+v", opErr)
	}
	if !strings.Contains(opErr.Message, "repo root does not exist") {
		t.Errorf("error message should mention missing repo root: %q", opErr.Message)
	}
}

func TestApplySuggestion_NormalSubdir(t *testing.T) {
	// Sanity check: a legitimate nested subpath still works after the
	// traversal guards.
	dir := t.TempDir()
	writeFile(t, dir, "src/foo.go", "L1\nL2\nL3\n")
	thr := threadAt("src/foo.go", 2, nil)
	sug := Suggestion{CommentID: "C", Body: "X"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("not applied")
	}
	got := readFile(t, filepath.Join(dir, "src/foo.go"))
	if got != "L1\nX\nL3\n" {
		t.Errorf("got %q", got)
	}
}

// --- Dirty-file content check -----------------------------------------------

// applyTestFetcher is an in-memory ContentFetcher for the dirty-file tests.
// Separate from the stubFetcher in code_context_test.go because we also count
// invocations here.
type applyTestFetcher struct {
	content string
	err     error
	calls   int
}

func (s *applyTestFetcher) FetchFileContent(_ context.Context, _, _, _, _ string) (string, error) {
	s.calls++
	return s.content, s.err
}

func TestApplySuggestion_DirtyFile_Rejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "LOCAL\nL2\n")
	fetch := &applyTestFetcher{content: "REMOTE\nL2\n"}
	thr := threadAt("a.go", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "X"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{
		RepoRoot:  dir,
		Fetcher:   fetch,
		Owner:     "o",
		Repo:      "r",
		PRHeadRef: "abc",
	})
	if opErr == nil || opErr.Code != OpCodePolicy {
		t.Fatalf("expected policy_violation, got %+v", opErr)
	}
	if fetch.calls != 1 {
		t.Errorf("expected fetcher called once, got %d", fetch.calls)
	}
}

func TestApplySuggestion_DirtyFile_ForceBypasses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "LOCAL\nL2\n")
	fetch := &applyTestFetcher{content: "REMOTE\nL2\n"}
	thr := threadAt("a.go", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "X"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{
		RepoRoot:  dir,
		Fetcher:   fetch,
		Owner:     "o",
		Repo:      "r",
		PRHeadRef: "abc",
		Force:     true,
	})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("Force should apply despite mismatch; res=%+v", res)
	}
}

func TestApplySuggestion_FetcherFails_GracefulProceed(t *testing.T) {
	// A fetcher error is a soft skip: the dirty-file check is a safety net,
	// not a hard requirement, so the apply still proceeds.
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\n")
	fetch := &applyTestFetcher{err: errFetchBoom}
	thr := threadAt("a.go", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "X"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{
		RepoRoot:  dir,
		Fetcher:   fetch,
		Owner:     "o",
		Repo:      "r",
		PRHeadRef: "abc",
	})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("expected apply to proceed on fetch error; res=%+v", res)
	}
}

func TestApplySuggestion_NilFetcher_NoCheck(t *testing.T) {
	// Back-compat: a nil Fetcher (or empty PRHeadRef) skips the check entirely.
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\n")
	thr := threadAt("a.go", 1, nil)
	sug := Suggestion{CommentID: "C", Body: "X"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{
		RepoRoot: dir,
		// Fetcher: nil, PRHeadRef: ""
	})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("nil Fetcher should skip check and apply; res=%+v", res)
	}
}

// errFetchBoom is a distinguishing sentinel for fetcher-error tests.
var errFetchBoom = stubFetcherError("boom")

type stubFetcherError string

func (e stubFetcherError) Error() string { return string(e) }

// --- Empty-body deletion semantics ------------------------------------------

func TestApplySuggestion_EmptyBody_DeletesRange(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\nL3\nL4\nL5\n")
	thr := threadAt("a.go", 3, nil)
	sug := Suggestion{CommentID: "C", Body: ""}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("not applied")
	}
	got := readFile(t, filepath.Join(dir, "a.go"))
	want := "L1\nL2\nL4\nL5\n"
	if got != want {
		t.Errorf("got %q want %q (empty body should DELETE the range, no blank line)", got, want)
	}
}

func TestApplySuggestion_EmptyBody_MultiLineRange(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\nL3\nL4\nL5\n")
	start := 2
	thr := threadAt("a.go", 4, &start)
	sug := Suggestion{CommentID: "C", Body: ""}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("not applied")
	}
	got := readFile(t, filepath.Join(dir, "a.go"))
	want := "L1\nL5\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestApplySuggestion_SingleEmptyLine_NotDeletion(t *testing.T) {
	// A single explicit "\n" body still represents "replace with one blank
	// line" — distinct from the empty-body deletion case.
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\nL2\nL3\n")
	thr := threadAt("a.go", 2, nil)
	sug := Suggestion{CommentID: "C", Body: "\n"}
	res, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.Applied {
		t.Errorf("not applied")
	}
	got := readFile(t, filepath.Join(dir, "a.go"))
	want := "L1\n\nL3\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
