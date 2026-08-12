package threads

import (
	"strings"
	"testing"

	"github.com/muesli/termenv"

	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestViewViewportFloorRendersOnlyFallback(t *testing.T) {
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name          string
		width, height int
		want          string
	}{
		{"width below floor", 79, 24, "Terminal too small - requires 80x24, current 79x24"},
		{"height below floor", 80, 23, "Terminal too small - requires 80x24, current 80x23"},
		{"both below floor", 40, 12, "Terminal too small - requires 80x24, current 40x12"},
		{"unmeasured", 0, 0, "Terminal too small - requires 80x24, current 0x0"},
		{"negative", -1, -2, "Terminal too small - requires 80x24, current -1x-2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Model{
				width:   tt.width,
				height:  tt.height,
				loaded:  true,
				threads: []gh.ReviewThread{{ID: "must-not-render"}},
				status:  "must-not-render",
			}.WithTheme(ctx).View()
			if got != tt.want {
				t.Fatalf("View() = %q, want only %q", got, tt.want)
			}
			if strings.Contains(got, "\x1b[") {
				t.Fatalf("no-color fallback contains ANSI escape: %q", got)
			}
		})
	}
}

func TestViewViewportFloorAllowsFullViewAtAndAboveBoundary(t *testing.T) {
	for _, tt := range []struct {
		width, height int
	}{
		{80, 24}, {120, 30}, {160, 50},
	} {
		t.Run("full view", func(t *testing.T) {
			got := Model{width: tt.width, height: tt.height, loaded: true}.View()
			if strings.Contains(got, "Terminal too small") {
				t.Fatalf("%dx%d unexpectedly rendered fallback: %q", tt.width, tt.height, got)
			}
			if !strings.Contains(got, "no unresolved threads") {
				t.Fatalf("%dx%d did not render empty full view: %q", tt.width, tt.height, got)
			}
		})
	}
}

func TestGoldenViewportThreadSizes(t *testing.T) {
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name          string
		width, height int
	}{
		{"viewport-threads-80x24-no-color-ascii", 80, 24},
		{"viewport-threads-120x30-no-color-ascii", 120, 30},
		{"viewport-threads-160x50-no-color-ascii", 160, 50},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rendertest.Force(t, termenv.Ascii)
			got := Model{
				width:    tt.width,
				height:   tt.height,
				loaded:   true,
				number:   42,
				prStatus: gh.PullRequestStatus{Title: "库é"},
				threads: []gh.ReviewThread{{
					ID:       "thread-1",
					Path:     "库é.go",
					Line:     7,
					Comments: []gh.ThreadComment{{Author: "reviewer", AuthorType: "User", Body: "combining: é"}},
				}},
			}.WithTheme(ctx).View()
			if strings.Contains(got, "\x1b[") {
				t.Fatalf("no-color golden contains ANSI escape: %q", got)
			}
			rendertest.Golden(t, tt.name, got)
		})
	}
}
