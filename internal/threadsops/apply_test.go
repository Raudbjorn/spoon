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
	sug := Suggestion{Body: "X\nY"}
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
	sug := Suggestion{Body: "NEW"}
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
	sug := Suggestion{Body: "x"}
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
	sug := Suggestion{Body: "X"}
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
	sug := Suggestion{Body: "FORCED"}
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
	sug := Suggestion{Body: "x"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Errorf("expected bad_input, got %+v", opErr)
	}
}

func TestApplySuggestion_LineOutOfRange(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "L1\n")
	thr := threadAt("a.go", 99, nil)
	sug := Suggestion{Body: "x"}
	_, opErr := ApplySuggestion(context.Background(), thr, sug, ApplyOptions{RepoRoot: dir})
	if opErr == nil || opErr.Code != OpCodeBadInput {
		t.Errorf("expected bad_input, got %+v", opErr)
	}
}

func TestApplySuggestion_NestedPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/a.go", "L1\nL2\nL3\n")
	thr := threadAt("sub/a.go", 2, nil)
	sug := Suggestion{Body: "X"}
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
	sug := Suggestion{Body: "X"}
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
