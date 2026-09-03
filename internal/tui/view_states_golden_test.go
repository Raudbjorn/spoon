package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/muesli/termenv"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/topics"
	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestGoldenMainViewStates(t *testing.T) {
	for _, profile := range renderProfiles(t) {
		t.Run(profile.name, func(t *testing.T) {
			rendertest.Force(t, profile.termenv)
			ctx := profile.context
			for name, render := range mainStateFixtures(t, ctx) {
				rendertest.Golden(t, fmt.Sprintf("main_%s_%s", profile.name, name), trimGoldenRender(render()))
			}
		})
	}
}

type renderProfile struct {
	name    string
	context theme.Context
	termenv termenv.Profile
}

func renderProfiles(t *testing.T) []renderProfile {
	t.Helper()
	profiles := []struct {
		name, color, glyph string
		termenv            termenv.Profile
	}{
		{"truecolor-unicode", "truecolor", "unicode", termenv.TrueColor},
		{"ansi16-unicode", "ansi16", "unicode", termenv.ANSI},
		{"mono-ascii", "mono", "ascii", termenv.Ascii},
	}
	out := make([]renderProfile, 0, len(profiles))
	for _, profile := range profiles {
		ctx, err := theme.ResolveContext("dark", "", profile.color, "", profile.glyph)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, renderProfile{name: profile.name, context: ctx, termenv: profile.termenv})
	}
	return out
}

func mainStateFixtures(t *testing.T, ctx theme.Context) map[string]func() string {
	t.Helper()
	base := func() Model {
		m := NewModel(nil, forge.AuthInfo{}, "", false).WithTheme(ctx)
		m.width, m.height = 120, 30
		return m
	}
	return map[string]func() string{
		"input": func() string {
			m := base()
			m.input, m.inputCursor = "owner/repo", len([]rune("owner/repo"))
			return m.View()
		},
		"table": func() string {
			m := filterModel()
			m.theme, m.width, m.height = ctx, 120, 30
			return m.View()
		},
		"detail": func() string {
			m := detailTestModel(t, 100)
			m.theme, m.height = ctx, 30
			return m.View()
		},
		"patch": func() string {
			m := detailTestModel(t, 100)
			m.theme, m.height = ctx, 30
			m.view = viewPatch
			m.patchBody = renderPatch(ctx, *m.forks[m.cursor].T2, nil, maxPatchChars)
			return m.View()
		},
		"help": func() string {
			m := base()
			m.view = viewHelp
			return m.View()
		},
		"export-path": func() string {
			m := base()
			m.view, m.exportPath = viewExportPath, "forks.json"
			m.exportCursor = len([]rune(m.exportPath))
			m.exportForks = []ScoredFork{{}}
			return m.View()
		},
		"topic-picker": func() string {
			m := base()
			m.view, m.topicName = viewTopicPicker, "terminal"
			m.topicSelections = []topics.Selection{{TopicRepo: forge.TopicRepo{FullName: "owner/repo", Stars: 42, ForkCount: 3, Description: "A fixture repository"}, Score: 98.5}}
			return m.View()
		},
		"filter": func() string {
			m := base()
			m.view, m.filterInput, m.filterCursor = viewFilter, "owner", 5
			m.forks = filterModel().forks
			return m.View()
		},
		"rank": func() string {
			m := base()
			m.view, m.rankQuery, m.rankCursor = viewRank, "terminal", 8
			m.forks = filterModel().forks
			return m.View()
		},
	}
}

func trimGoldenRender(rendered string) string {
	lines := strings.Split(rendered, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}
