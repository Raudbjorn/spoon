package threadsops

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

// stubFetcher implements ContentFetcher for tests.
type stubFetcher struct {
	content string
	err     error
}

func (s *stubFetcher) FetchFileContent(_ context.Context, _, _, _, _ string) (string, error) {
	return s.content, s.err
}

// tenLineFile builds a 10-line file "L1\nL2\n...\nL10\n".
func tenLineFile() string {
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		b.WriteString("L")
		// stringify i without strconv: cheap inline
		if i < 10 {
			b.WriteRune(rune('0' + i))
		} else {
			b.WriteString("10")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestFetchCodeContext_SingleLine(t *testing.T) {
	f := &stubFetcher{content: tenLineFile()}
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "a.go", Line: 5},
	}
	cc, err := FetchCodeContext(context.Background(), f, "ref", "o", "r", thread, 2)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cc == nil {
		t.Fatal("nil CodeContext")
	}
	if cc.StartLine != 3 || cc.EndLine != 7 {
		t.Errorf("range=[%d,%d] want [3,7]", cc.StartLine, cc.EndLine)
	}
	if len(cc.Lines) != 5 {
		t.Fatalf("lines=%d want 5: %v", len(cc.Lines), cc.Lines)
	}
	want := []string{"L3", "L4", "L5", "L6", "L7"}
	for i := range want {
		if cc.Lines[i] != want[i] {
			t.Errorf("[%d]=%q want %q", i, cc.Lines[i], want[i])
		}
	}
}

func TestFetchCodeContext_LineRange(t *testing.T) {
	f := &stubFetcher{content: tenLineFile()}
	start := 5
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "a.go", StartLine: &start, Line: 7},
	}
	cc, err := FetchCodeContext(context.Background(), f, "ref", "o", "r", thread, 2)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cc == nil {
		t.Fatal("nil CodeContext")
	}
	// startLine=5 - context=2 → 3; line=7 + context=2 → 9.
	if cc.StartLine != 3 || cc.EndLine != 9 {
		t.Errorf("range=[%d,%d] want [3,9]", cc.StartLine, cc.EndLine)
	}
	if len(cc.Lines) != 7 {
		t.Fatalf("lines=%d want 7: %v", len(cc.Lines), cc.Lines)
	}
}

func TestFetchCodeContext_ClampNearStart(t *testing.T) {
	f := &stubFetcher{content: tenLineFile()}
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "a.go", Line: 2},
	}
	cc, err := FetchCodeContext(context.Background(), f, "ref", "o", "r", thread, 5)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cc == nil {
		t.Fatal("nil CodeContext")
	}
	// Want clamp to 1, end=2+5=7.
	if cc.StartLine != 1 || cc.EndLine != 7 {
		t.Errorf("range=[%d,%d] want [1,7]", cc.StartLine, cc.EndLine)
	}
	if len(cc.Lines) != 7 {
		t.Fatalf("lines=%d want 7: %v", len(cc.Lines), cc.Lines)
	}
	if cc.Lines[0] != "L1" {
		t.Errorf("first line %q want L1", cc.Lines[0])
	}
}

func TestFetchCodeContext_NoAnchor_EmptyPath(t *testing.T) {
	f := &stubFetcher{content: tenLineFile()}
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "", Line: 5},
	}
	cc, err := FetchCodeContext(context.Background(), f, "ref", "o", "r", thread, 2)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cc != nil {
		t.Errorf("expected nil for empty path, got %+v", cc)
	}
}

func TestFetchCodeContext_NoAnchor_ZeroLine(t *testing.T) {
	f := &stubFetcher{content: tenLineFile()}
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "a.go", Line: 0},
	}
	cc, err := FetchCodeContext(context.Background(), f, "ref", "o", "r", thread, 2)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cc != nil {
		t.Errorf("expected nil for zero line, got %+v", cc)
	}
}

func TestFetchCodeContext_OutdatedFlag(t *testing.T) {
	f := &stubFetcher{content: tenLineFile()}
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "a.go", Line: 5, IsOutdated: true},
	}
	cc, err := FetchCodeContext(context.Background(), f, "ref", "o", "r", thread, 1)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cc == nil {
		t.Fatal("nil CodeContext")
	}
	if !cc.Outdated {
		t.Errorf("expected Outdated=true")
	}
}

func TestFetchCodeContext_EmptyContent(t *testing.T) {
	f := &stubFetcher{content: ""}
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "a.go", Line: 5},
	}
	cc, err := FetchCodeContext(context.Background(), f, "ref", "o", "r", thread, 2)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cc != nil {
		t.Errorf("expected nil on empty content, got %+v", cc)
	}
}

func TestFetchCodeContext_FetchError(t *testing.T) {
	fetch := &stubFetcher{err: fmt.Errorf("fetch failed")}
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "a.go", Line: 5},
	}
	cc, err := FetchCodeContext(context.Background(), fetch, "ref", "o", "r", thread, 2)
	if err == nil {
		t.Fatal("expected non-nil error when fetch fails")
	}
	if cc != nil {
		t.Errorf("expected nil CodeContext on error, got %+v", cc)
	}
}

func TestFetchCodeContext_ZeroContextLines(t *testing.T) {
	f := &stubFetcher{content: tenLineFile()}
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "a.go", Line: 5},
	}
	cc, err := FetchCodeContext(context.Background(), f, "ref", "o", "r", thread, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cc != nil {
		t.Errorf("expected nil for contextLines=0, got %+v", cc)
	}
}

func TestFetchCodeContext_NilFetcher(t *testing.T) {
	thread := ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{Path: "a.go", Line: 5},
	}
	cc, err := FetchCodeContext(context.Background(), nil, "ref", "o", "r", thread, 2)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cc != nil {
		t.Errorf("expected nil for nil fetcher, got %+v", cc)
	}
}
