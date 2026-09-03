package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/pathmatch"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

// maxPatchChars bounds how much rendered patch text the view holds, so a
// fork with a sprawling diff cannot balloon the model's memory or the
// terminal's scrollback.
const maxPatchChars = 200_000

// fetchPatchCmd issues a live provider.Compare call for the selected fork.
// Unlike the enrichment sweep's cached T2 (store.go:1195-1202 deliberately
// omits patch text to keep the snapshot small), the patch view needs the
// actual diff hunks, so it always goes to the network rather than the store.
func (m *Model) fetchPatchCmd() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.forks) || m.provider == nil {
		return nil
	}
	fork := m.forks[m.cursor].Fork
	provider := m.provider
	m.patchLoading = true
	return func() tea.Msg {
		// Live call on purpose: cached compares carry no patch text.
		t2, err := provider.Compare(context.Background(), fork, fork.DefaultBranch)
		return patchResultMsg{forkID: fork.ID, t2: t2, err: err}
	}
}

// renderPatch renders unified-diff hunks for the files selected by pm (nil =
// every file), stopping after limit characters with a visible marker.
func renderPatch(ctx theme.Context, t2 forge.T2Data, pm *pathmatch.Matcher, limit int) string {
	var b strings.Builder
	add := lipgloss.NewStyle().Foreground(ctx.Palette.Success)
	del := lipgloss.NewStyle().Foreground(ctx.Palette.Error)
	hunk := lipgloss.NewStyle().Foreground(ctx.Palette.Info)
	shown := 0
	for _, d := range t2.Diffs {
		if pm != nil && !diffMatches(d, *pm) {
			continue
		}
		shown++
		b.WriteString(lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("=== %s %s (+%d/-%d)", d.Status, d.Path, d.Additions, d.Deletions)) + "\n")
		if d.Patch == "" {
			b.WriteString("  (no patch text from provider " + ctx.Glyph(theme.EmDash) + " binary, too large, or omitted)\n\n")
			continue
		}
		for _, line := range strings.Split(d.Patch, "\n") {
			if b.Len() > limit {
				b.WriteString("\n... patch truncated at " + strconv.Itoa(limit) + " characters\n")
				return b.String()
			}
			switch {
			case strings.HasPrefix(line, "+"):
				b.WriteString(add.Render(line))
			case strings.HasPrefix(line, "-"):
				b.WriteString(del.Render(line))
			case strings.HasPrefix(line, "@@"):
				b.WriteString(hunk.Render(line))
			default:
				b.WriteString(line)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if shown == 0 {
		return "No files match the active path filter in this fork's own commits.\n"
	}
	return b.String()
}

func (m *Model) handlePatchKey(key string) (tea.Model, tea.Cmd) {
	switch keymap.Dispatch(keymap.MainPatch, key) {
	case keymap.Back:
		m.patchOffset = 0
		m.view = viewDetail
	case keymap.Up:
		m.patchOffset = max(0, m.patchOffset-1)
	case keymap.Down:
		m.patchOffset = min(maxScrollOffset(m.patchBody, m.detailViewHeight()), m.patchOffset+1)
	case keymap.PageUp:
		m.patchOffset = max(0, m.patchOffset-m.detailViewHeight())
	case keymap.PageDown:
		m.patchOffset = min(maxScrollOffset(m.patchBody, m.detailViewHeight()), m.patchOffset+m.detailViewHeight())
	case keymap.Home:
		m.patchOffset = 0
	case keymap.End:
		m.patchOffset = maxScrollOffset(m.patchBody, m.detailViewHeight())
	}
	return m, nil
}

func (m Model) viewPatch() string {
	if m.patchLoading {
		return "\n  Fetching patch (one live compare call)...\n"
	}
	body := scrollLines(m.patchBody, m.patchOffset, m.detailViewHeight())
	return body + "\n" + ui.KeyLegend(m.themeContext(), ui.ContentWidth(m.width)-2, keymap.MainPatch)
}
