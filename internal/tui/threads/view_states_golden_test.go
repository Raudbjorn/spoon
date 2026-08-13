package threads

import (
	"errors"
	"fmt"
	"testing"

	"github.com/muesli/termenv"

	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestGoldenThreadViewStates(t *testing.T) {
	for _, profile := range threadRenderProfiles(t) {
		t.Run(profile.name, func(t *testing.T) {
			rendertest.Force(t, profile.termenv)
			for name, render := range threadStateFixtures(profile.context) {
				rendertest.Golden(t, fmt.Sprintf("threads_%s_%s", profile.name, name), render())
			}
		})
	}
}

type threadRenderProfile struct {
	name    string
	context theme.Context
	termenv termenv.Profile
}

func threadRenderProfiles(t *testing.T) []threadRenderProfile {
	t.Helper()
	profiles := []struct {
		name, color, glyph string
		termenv            termenv.Profile
	}{
		{"truecolor-unicode", "truecolor", "unicode", termenv.TrueColor},
		{"ansi16-unicode", "ansi16", "unicode", termenv.ANSI},
		{"mono-ascii", "mono", "ascii", termenv.Ascii},
	}
	out := make([]threadRenderProfile, 0, len(profiles))
	for _, profile := range profiles {
		ctx, err := theme.ResolveContext("dark", "", profile.color, "", profile.glyph)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, threadRenderProfile{name: profile.name, context: ctx, termenv: profile.termenv})
	}
	return out
}

func threadStateFixtures(ctx theme.Context) map[string]func() string {
	base := func() Model {
		m := New(nil, "owner", "repo", 42, false).WithTheme(ctx)
		m.width, m.height = 120, 30
		m.prStatus = gh.PullRequestStatus{Title: "Fixture PR", UnresolvedThreads: 1}
		m.loaded = true
		m.threads = []gh.ReviewThread{{
			ID: "thread-1", Path: "main.go", Line: 7,
			Comments: []gh.ThreadComment{{ID: "comment-1", Author: "alice", AuthorType: "User", Body: "Please update this."}},
		}}
		return m
	}
	return map[string]func() string{
		"loading": func() string { m := base(); m.loaded = false; return m.View() },
		"error":   func() string { m := base(); m.err = errors.New("fixture failure"); return m.View() },
		"empty":   func() string { m := base(); m.threads = nil; return m.View() },
		"list":    func() string { return base().View() },
		"composer": func() string {
			m := base()
			m.composing, m.composeFor, m.composeBuf = true, "reply", []rune("fixture reply")
			return m.View()
		},
		"confirm": func() string { m := base(); m.confirm = "resolve-all"; return m.View() },
		"help":    func() string { m := base(); m.showHelp = true; return m.View() },
		"code-context": func() string {
			m := base()
			m.codeContexts = map[string]*threadsops.CodeContext{"thread-1": {Path: "main.go", StartLine: 7, Lines: []string{"before", "after"}}}
			return m.View()
		},
	}
}
