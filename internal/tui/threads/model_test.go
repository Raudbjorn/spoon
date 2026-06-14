package threads

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	gh "github.com/svnbjrn/spoon/internal/github"
	threadsops_pkg "github.com/svnbjrn/spoon/internal/threadsops"
)

func TestCursorClamps(t *testing.T) {
	m := New(nil, "owner", "repo", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	m.loaded = true

	// down once
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm := m2.(Model)
	if mm.cursor != 1 {
		t.Fatalf("cursor=%d want 1", mm.cursor)
	}

	// down past end stays at last
	mm.cursor = 2
	m3, _ := mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m3.(Model).cursor != 2 {
		t.Errorf("cursor=%d want 2 (clamped)", m3.(Model).cursor)
	}

	// up below 0 stays at 0
	mm.cursor = 0
	m4, _ := mm.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m4.(Model).cursor != 0 {
		t.Errorf("cursor=%d want 0 (clamped)", m4.(Model).cursor)
	}
}

func TestEnterReplyMode(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{AuthorType: "User"}}}}
	m.loaded = true
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !out.(Model).composing {
		t.Errorf("expected composing=true after 'r'")
	}
}

func TestResolveKeyOnBotThreadSkipsCompose(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{AuthorType: "Bot"}}}}
	m.loaded = true
	// 'R' on a bot thread should set mutating and not enter composing.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	mm := out.(Model)
	if mm.composing {
		t.Errorf("composing should be false on bot thread")
	}
	if !mm.mutating {
		t.Errorf("mutating should be true on bot thread")
	}
}

func TestResolveAllRequiresConfirm(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a"}}
	m.loaded = true

	// Ctrl+A should set confirm=resolve-all, NOT fire the cmd directly.
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	mm := out.(Model)
	if mm.confirm != "resolve-all" {
		t.Errorf("confirm=%q want resolve-all", mm.confirm)
	}
	if cmd != nil {
		t.Errorf("expected no cmd until confirm; got %v", cmd)
	}

	// Anything other than y cancels.
	out2, _ := mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if out2.(Model).confirm != "" {
		t.Errorf("confirm should clear after non-y key; got %q", out2.(Model).confirm)
	}
}

func TestApplySuggestionNoOpWhenNone(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	// Thread with no suggestion in its comments.
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{Body: "just a comment"}}}}
	m.loaded = true

	// 'a' with no suggestion should NOT trigger resolve-all or any confirm.
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	mm := out.(Model)
	if mm.confirm != "" {
		t.Errorf("confirm=%q want empty (no suggestion should be a no-op)", mm.confirm)
	}
	if mm.status == "" {
		t.Errorf("expected a status message when no suggestion present, got empty string")
	}
	if cmd != nil {
		t.Errorf("expected no cmd when no suggestion; got %v", cmd)
	}
}

func TestMutatingBlocksReply(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{AuthorType: "User"}}}}
	m.loaded = true
	m.mutating = true

	// 'r' should NOT enter composing mode while a mutation is in flight.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if out.(Model).composing {
		t.Errorf("composing should be false while mutating")
	}
}

func TestHelpKeyToggles(t *testing.T) {
	m := New(nil, "o", "r", 1, false)
	m.threads = []gh.ReviewThread{{ID: "a"}}
	m.loaded = true

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !out.(Model).showHelp {
		t.Errorf("expected showHelp=true after first '?'")
	}

	out2, _ := out.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if out2.(Model).showHelp {
		t.Errorf("expected showHelp=false after second '?'")
	}
}

func TestViewIncludesStatusHeader(t *testing.T) {
	m := New(nil, "owner", "repo", 42, false)
	m.prStatus = gh.PullRequestStatus{
		Title:            "Test",
		MergeStateStatus: "CLEAN",
		ReviewDecision:   "APPROVED",
		ChecksState:      "SUCCESS",
	}
	m.threads = []gh.ReviewThread{{ID: "a", Comments: []gh.ThreadComment{{Author: "alice", AuthorType: "User"}}}}
	m.loaded = true

	out := m.View()
	for _, sub := range []string{"PR #42", "Test", "CLEAN", "APPROVED", "SUCCESS"} {
		if !strings.Contains(out, sub) {
			t.Errorf("view missing %q\n---\n%s", sub, out)
		}
	}
}

func TestViewSurfacesOutdated(t *testing.T) {
	m := New(nil, "owner", "repo", 1, false)
	m.prStatus = gh.PullRequestStatus{
		Title:             "Test",
		UnresolvedThreads: 2,
		OutdatedThreads:   1,
	}
	m.threads = []gh.ReviewThread{
		{ID: "a", Path: "foo.go", Line: 10, IsOutdated: true, IsResolved: false,
			Comments: []gh.ThreadComment{{Author: "alice", AuthorType: "User", Body: "hi"}}},
		{ID: "b", Path: "bar.go", Line: 20, IsOutdated: false, IsResolved: false,
			Comments: []gh.ThreadComment{{Author: "bob", AuthorType: "User", Body: "yo"}}},
	}
	m.loaded = true

	out := m.View()
	// Header surfaces the aggregate outdated count.
	if !strings.Contains(out, "1 outdated") {
		t.Errorf("expected header to mention '1 outdated' — got:\n%s", out)
	}
	// First thread (cursor=0) is outdated; list entry and detail state label should reflect it.
	if !strings.Contains(out, "foo.go:10 (outdated)") {
		t.Errorf("expected list row to mark foo.go as outdated — got:\n%s", out)
	}
	if !strings.Contains(out, "Unresolved (outdated") {
		t.Errorf("expected state label 'Unresolved (outdated …)' for cursor thread — got:\n%s", out)
	}
}

func TestViewRendersCodeContextBlock(t *testing.T) {
	m := New(nil, "owner", "repo", 1, false)
	m.threads = []gh.ReviewThread{
		{ID: "PRRT_1", Path: "foo.go", Line: 5, IsResolved: false, IsOutdated: false,
			Comments: []gh.ThreadComment{{Author: "alice", AuthorType: "User", Body: "fix this"}}},
	}
	m.loaded = true
	m.ShowCodeLines = 2
	// Inject a pre-fetched code context so we don't need a real client.
	cc := &threadsops_pkg.CodeContext{
		Path:      "foo.go",
		Ref:       "abc1234567890",
		StartLine: 3,
		EndLine:   7,
		Lines:     []string{"L3", "L4", "L5", "L6", "L7"},
	}
	m.codeContexts = map[string]*threadsops_pkg.CodeContext{"PRRT_1": cc}

	out := m.View()
	if !strings.Contains(out, "code at foo.go:3-7") {
		t.Errorf("expected code-context header — got:\n%s", out)
	}
	if !strings.Contains(out, "abc1234") {
		t.Errorf("expected short ref in header — got:\n%s", out)
	}
	if !strings.Contains(out, "← L5") {
		t.Errorf("expected arrow marker on comment line (L5) — got:\n%s", out)
	}
	if !strings.Contains(out, "  L3") {
		t.Errorf("expected non-marker leading space for context lines — got:\n%s", out)
	}
}

func TestViewRendersOutdatedCodeContext(t *testing.T) {
	m := New(nil, "owner", "repo", 1, false)
	m.threads = []gh.ReviewThread{
		{ID: "PRRT_1", Path: "foo.go", Line: 5, IsOutdated: true,
			Comments: []gh.ThreadComment{{Author: "alice", AuthorType: "User", Body: "fix this"}}},
	}
	m.loaded = true
	m.ShowCodeLines = 1
	cc := &threadsops_pkg.CodeContext{
		Path: "foo.go", Ref: "abc", StartLine: 4, EndLine: 6,
		Lines: []string{"L4", "L5", "L6"}, Outdated: true,
	}
	m.codeContexts = map[string]*threadsops_pkg.CodeContext{"PRRT_1": cc}

	out := m.View()
	if !strings.Contains(out, "(outdated)") {
		t.Errorf("expected (outdated) tag in code-context header — got:\n%s", out)
	}
}

func TestViewSurfacesActiveThread(t *testing.T) {
	m := New(nil, "owner", "repo", 1, false)
	m.threads = []gh.ReviewThread{
		{ID: "a", Path: "foo.go", Line: 1, IsResolved: false, IsOutdated: false,
			Comments: []gh.ThreadComment{{Author: "alice", AuthorType: "User", Body: "x"}}},
	}
	m.loaded = true

	out := m.View()
	if !strings.Contains(out, "Unresolved (active)") {
		t.Errorf("expected 'Unresolved (active)' for active thread — got:\n%s", out)
	}
	if strings.Contains(out, "(outdated)") {
		t.Errorf("did not expect outdated marker — got:\n%s", out)
	}
}

// newTestModelWithThreads builds a Model with one loaded thread. It uses
// nil for the client because counter-propose goes through replyFunc (a test
// seam) instead of m.client.
func newTestModelWithThreads(t *testing.T) Model {
	t.Helper()
	m := New(nil, "owner", "repo", 42, false)
	m.threads = []gh.ReviewThread{{
		ID:   "THREAD_1",
		Path: "main.go",
		Line: 10,
		Comments: []gh.ThreadComment{{
			Author:     "reviewer",
			AuthorType: "User",
			Body:       "please fix this",
		}},
	}}
	m.loaded = true
	return m
}

func TestCounterPropose_emptyEditorContent_cancels(t *testing.T) {
	m := newTestModelWithThreads(t)
	m.launchEditor = func(_ context.Context, _, _ string) (string, error) {
		return "", nil
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a tea.Cmd from c keypress")
	}
	msg := cmd()
	updated, _ = m.Update(msg)
	m = updated.(Model)
	if !strings.Contains(strings.ToLower(m.status), "cancel") {
		t.Errorf("expected cancellation status, got %q", m.status)
	}
}

func TestCounterPropose_nonEmptyContent_postsWrappedReply(t *testing.T) {
	m := newTestModelWithThreads(t)
	posted := false
	var postedBody string
	m.replyFunc = func(_ context.Context, threadID, body string) (gh.ThreadComment, error) {
		posted = true
		postedBody = body
		return gh.ThreadComment{ID: "COMMENT_99"}, nil
	}
	m.launchEditor = func(_ context.Context, _, _ string) (string, error) {
		return "newCode()", nil
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a tea.Cmd from c keypress")
	}
	msg := cmd()
	updated, _ = m.Update(msg)
	m = updated.(Model)
	if !posted {
		t.Fatal("expected replyFunc to be called")
	}
	if !strings.Contains(postedBody, "```suggestion") {
		t.Errorf("body should be wrapped in suggestion fence, got %q", postedBody)
	}
	if !strings.Contains(m.status, "COMMENT_99") {
		t.Errorf("status should mention posted comment ID, got %q", m.status)
	}
}
