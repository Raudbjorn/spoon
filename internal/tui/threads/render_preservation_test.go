package threads

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestBindingLegendKeepsEveryActionWithinViewport(t *testing.T) {
	for _, width := range []int{80, 120} {
		legend := renderBindingLegend(theme.DefaultContext(), width)
		for _, hint := range threadBindingHints {
			if !strings.Contains(legend, hint.label) {
				t.Fatalf("%d-column legend omitted %q:\n%s", width, hint.label, legend)
			}
		}
		for _, line := range strings.Split(legend, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("%d-column legend line has width %d: %q", width, got, line)
			}
		}
	}
}

func TestThreadStatusTonesFollowOutcome(t *testing.T) {
	m := New(nil, "owner", "repo", 1, false)
	cases := []struct {
		name string
		msg  any
		want string
	}{
		{"mutation success", mutationDoneMsg{what: "resolve"}, "success"},
		{"mutation failure", mutationDoneMsg{what: "resolve", err: errors.New("boom")}, "error"},
		{"counter posted", counterProposeResultMsg{commentID: "1"}, "success"},
		{"counter cancelled", counterProposeResultMsg{cancelled: true}, "warning"},
		{"counter failure", counterProposeResultMsg{err: errors.New("boom")}, "error"},
		{"code context failure", codeContextLoadedMsg{err: errors.New("boom")}, "error"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := m.Update(tt.msg)
			got := out.(Model).statusTone
			if string(got) != tt.want {
				t.Fatalf("status tone = %q, want %q", got, tt.want)
			}
		})
	}

	m.loaded = true
	m.threads = []gh.ReviewThread{{ID: "thread", Comments: []gh.ThreadComment{{Body: "plain"}}}}
	out, _ := m.Update(keyRunes("a"))
	if got := out.(Model).statusTone; got != "warning" {
		t.Fatalf("no-suggestion status tone = %q, want warning", got)
	}
}

func TestMutationCompletionClearsLatchAndAllowsNextAction(t *testing.T) {
	for _, done := range []mutationDoneMsg{
		{what: "resolve"},
		{what: "resolve", err: errors.New("boom")},
	} {
		m := New(nil, "owner", "repo", 1, false)
		m.loaded, m.mutating = true, true
		m.threads = []gh.ReviewThread{{ID: "thread", Comments: []gh.ThreadComment{{AuthorType: "Bot"}}}}
		out, _ := m.Update(done)
		completed := out.(Model)
		if completed.mutating {
			t.Fatalf("mutation completion left latch set for %#v", done)
		}
		out, cmd := completed.Update(keyRunes("R"))
		next := out.(Model)
		if !next.mutating || cmd == nil {
			t.Fatalf("next resolvable mutation was blocked after %#v: mutating=%t cmd=%v", done, next.mutating, cmd != nil)
		}
	}
}

func TestThreadRenderingPreservesStateDetails(t *testing.T) {
	ctx := theme.DefaultContext()
	model := New(nil, "owner", "repo", 42, false).WithTheme(ctx)
	model.width, model.height, model.loaded, model.Verbose = 120, 30, true, true
	model.threads = []gh.ReviewThread{{
		ID: "resolved", Path: "main.go", Line: 7, IsResolved: true, IsOutdated: true,
		Comments: []gh.ThreadComment{{ID: "comment", Author: "alice", AuthorType: "User", CreatedAt: "2026-08-13T00:00:00Z", Body: "```suggestion\nnew code\n```"}},
	}}
	rendered := model.View()
	for _, want := range []string{"Resolved", "outdated", "Created: 2026-08-13T00:00:00Z", "Suggestion available"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("thread rendering lost %q:\n%s", want, rendered)
		}
	}
	for _, confirm := range []string{"resolve-all", "unresolve-all", "apply-suggestion"} {
		model.confirm = confirm
		if rendered := model.View(); !strings.Contains(rendered, "Confirm") || !strings.Contains(rendered, confirm) {
			t.Fatalf("confirmation %q missing:\n%s", confirm, rendered)
		}
	}
}

func keyRunes(value string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)} }
