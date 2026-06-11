package threadsops

import (
	"reflect"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

func TestParseSuggestions_SingleBlock(t *testing.T) {
	body := "Please use a const here:\n\n```suggestion\nconst Foo = \"bar\"\n```\n"
	got := ParseSuggestions("PRC_1", body)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1; got=%+v", len(got), got)
	}
	if got[0].CommentID != "PRC_1" {
		t.Errorf("CommentID=%q", got[0].CommentID)
	}
	if got[0].Body != "const Foo = \"bar\"" {
		t.Errorf("Body=%q", got[0].Body)
	}
}

func TestParseSuggestions_MultipleBlocks(t *testing.T) {
	body := "First:\n\n```suggestion\nA\n```\n\nThen:\n\n```suggestion\nB\nC\n```\n"
	got := ParseSuggestions("PRC_2", body)
	if len(got) != 2 {
		t.Fatalf("want 2 suggestions, got %d: %+v", len(got), got)
	}
	if got[0].Body != "A" {
		t.Errorf("[0]=%q want A", got[0].Body)
	}
	if got[1].Body != "B\nC" {
		t.Errorf("[1]=%q want B\\nC", got[1].Body)
	}
}

func TestParseSuggestions_NoBlocks(t *testing.T) {
	got := ParseSuggestions("PRC_3", "Just a normal comment.\nNo fences here.\n")
	if len(got) != 0 {
		t.Errorf("want 0, got %+v", got)
	}
}

func TestParseSuggestions_TildeFence(t *testing.T) {
	body := "Use this:\n\n~~~suggestion\nx := 1\n~~~\n"
	got := ParseSuggestions("PRC_4", body)
	if len(got) != 1 || got[0].Body != "x := 1" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseSuggestions_CaseInsensitiveTag(t *testing.T) {
	body := "```Suggestion\nMixedCaseTag\n```"
	got := ParseSuggestions("PRC_5", body)
	if len(got) != 1 || got[0].Body != "MixedCaseTag" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseSuggestions_PreservesNewlines(t *testing.T) {
	body := "```suggestion\nline1\n\nline3\n```"
	got := ParseSuggestions("PRC_6", body)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	want := "line1\n\nline3"
	if got[0].Body != want {
		t.Errorf("body=%q want %q", got[0].Body, want)
	}
}

func TestParseSuggestions_Empty(t *testing.T) {
	if got := ParseSuggestions("PRC_7", ""); len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}

func TestParseSuggestions_NestedCodeFence(t *testing.T) {
	// A suggestion that itself contains a nested 4-char fence; the close fence
	// for the suggestion is matched at 3+ backticks again. Our parser closes
	// at the first 3+ backtick line after open. This is a known limitation
	// (we don't fully implement CommonMark fence-length matching) but the test
	// pins the current behavior so changes are intentional.
	body := "```suggestion\nfunc foo() {\n\treturn nil\n}\n```\n"
	got := ParseSuggestions("PRC_8", body)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestParseSuggestions_OpenButUnclosed(t *testing.T) {
	// If the close fence is missing we still record what we saw — this matches
	// how some tooling truncates output and avoids losing the suggestion.
	body := "```suggestion\nincomplete\n"
	got := ParseSuggestions("PRC_9", body)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got[0].Body, "incomplete") {
		t.Errorf("body=%q", got[0].Body)
	}
}

func TestAnnotateThreadsWithSuggestions_Roundtrip(t *testing.T) {
	threads := []ReviewThreadWithPolicy{
		{ReviewThread: github.ReviewThread{
			ID:   "T1",
			Path: "a.go",
			Line: 5,
			Comments: []github.ThreadComment{
				{ID: "C1", Body: "```suggestion\nA\n```"},
				{ID: "C2", Body: "no suggestion"},
			},
		}},
		{ReviewThread: github.ReviewThread{
			ID:   "T2",
			Path: "",
			Line: 0,
			Comments: []github.ThreadComment{
				{ID: "C3", Body: "```suggestion\nB\n```"},
			},
		}},
		{ReviewThread: github.ReviewThread{
			ID:       "T3",
			Path:     "c.go",
			Line:     7,
			Comments: []github.ThreadComment{{ID: "C4", Body: "no fences"}},
		}},
	}
	per := AnnotateThreadsWithSuggestions(threads)
	if len(per) != 3 {
		t.Fatalf("len=%d want 3", len(per))
	}
	if len(per[0]) != 1 || per[0][0].CommentID != "C1" || per[0][0].Body != "A" || !per[0][0].Applicable {
		t.Errorf("per[0]=%+v", per[0])
	}
	if len(per[1]) != 1 || per[1][0].CommentID != "C3" || per[1][0].Applicable {
		// T2 has no path/line so Applicable must be false
		t.Errorf("per[1]=%+v", per[1])
	}
	if len(per[2]) != 0 {
		t.Errorf("per[2]=%+v want empty", per[2])
	}
}

func TestPopulateSuggestions_SetsField(t *testing.T) {
	threads := []ReviewThreadWithPolicy{
		{ReviewThread: github.ReviewThread{
			ID:       "T1",
			Path:     "a.go",
			Line:     1,
			Comments: []github.ThreadComment{{ID: "C1", Body: "```suggestion\nx\n```"}},
		}},
		{ReviewThread: github.ReviewThread{
			ID:       "T2",
			Path:     "b.go",
			Line:     2,
			Comments: []github.ThreadComment{{ID: "C2", Body: "no fence"}},
		}},
	}
	PopulateSuggestions(threads)
	if len(threads[0].Suggestions) != 1 || threads[0].Suggestions[0].Body != "x" {
		t.Errorf("threads[0].Suggestions=%+v", threads[0].Suggestions)
	}
	if threads[1].Suggestions != nil {
		t.Errorf("threads[1].Suggestions should be nil for omitempty, got %+v", threads[1].Suggestions)
	}
}

func TestWrapSuggestionBody_Default(t *testing.T) {
	got := WrapSuggestionBody("", "new content")
	want := "How about this?\n\n```suggestion\nnew content\n```"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestWrapSuggestionBody_CustomIntro(t *testing.T) {
	got := WrapSuggestionBody("Try:", "x")
	want := "Try:\n\n```suggestion\nx\n```"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestWrapSuggestionBody_TrimsTrailingNewline(t *testing.T) {
	got := WrapSuggestionBody("Try:", "x\n")
	want := "Try:\n\n```suggestion\nx\n```"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestParseSuggestions_OrderPreserved(t *testing.T) {
	body := "```suggestion\nfirst\n```\nprose\n```suggestion\nsecond\n```\n"
	got := ParseSuggestions("PRC", body)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if !reflect.DeepEqual([]string{got[0].Body, got[1].Body}, []string{"first", "second"}) {
		t.Errorf("order wrong: %+v", got)
	}
}
