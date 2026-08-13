package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

// Row filtering for the fork table.
//
// The filter never reslices m.forks. m.cursor stays an absolute index into the
// complete list, and only rendering and cursor movement go through the
// visible-index projection below. That is not a stylistic choice: the cluster
// pipeline hands the live slice to a background goroutine that writes back
// through &forks[i].Heat (cluster_bridge.go) and requires callers not to
// reslice it, and assignDuplicateGroups recomputes SiblingCount over the whole
// slice -- filtering the slice itself would make a three-member duplicate
// group report a count of one, and that wrong number reaches the export.
//
// The invariant every caller may rely on: m.cursor is either -1 (nothing
// visible) or the index of a fork that passes the active filter.

// matchesFilter reports whether a fork passes the query: a case-insensitive
// substring test against the fork's "owner/name" identity.
//
// Deliberately substring rather than fuzzy. Fuzzy matching implies a relevance
// ordering, which would fight the user's chosen sort column -- either the rows
// reorder (breaking the sort and the contiguity that gatherDuplicateGroups
// depends on) or the ranking is computed and thrown away. Substring preserves
// list order exactly, so composing with sort and cluster grouping is free.
//
// Deliberately ID-only. Matching description, language or topics would make
// rows appear for reasons that are invisible on screen, which reads as a
// broken filter. Field-scoped queries ("lang:go", "topic:cli") are the natural
// extension point if that is ever wanted; they are not implemented here.
func matchesFilter(sf ScoredFork, q string) bool {
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(sf.Fork.ID), strings.ToLower(q))
}

// visibleIdx returns the absolute indices of the forks passing the active
// filter, in list order. An empty filter yields every index.
//
// Recomputed on demand rather than cached: it is O(n) over a few thousand
// forks per keystroke, which is irrelevant, and a cache would introduce a
// staleness bug for every mutation of m.forks -- sort, resort, enrichment
// write-back, duplicate gathering -- to remember to invalidate.
func (m Model) visibleIdx() []int {
	idx := make([]int, 0, len(m.forks))
	for i := range m.forks {
		if matchesFilter(m.forks[i], m.filter) {
			idx = append(idx, i)
		}
	}
	return idx
}

// applyFilter installs a new query and re-anchors the cursor: on the same fork
// when it survives the filter, otherwise on the nearest visible row.
func (m *Model) applyFilter(q string) {
	var selectedID string
	if m.cursor >= 0 && m.cursor < len(m.forks) {
		selectedID = m.forks[m.cursor].Fork.ID
	}
	m.filter = q
	m.restoreCursorByID(selectedID)
	m.cursor = clampCursorVisible(m.cursor, m.visibleIdx())
}

// handleFilterKey drives the filter prompt. Mirrors handleExportPathKey: the
// query is applied on Enter rather than as you type, which matches the other
// two prompts in this TUI and keeps the cursor re-anchoring a single discrete
// event instead of one per keystroke.
//
// Esc here cancels the edit and leaves the active filter untouched; Esc on the
// table itself is what clears it. Enter on an empty query also clears.
func (m *Model) handleFilterKey(key, typed string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		m.applyFilter(strings.TrimSpace(m.filterInput))
		m.view = viewTable
	case "esc":
		m.filterInput = ""
		m.filterCursor = 0
		m.view = viewTable
	default:
		m.filterInput, m.filterCursor, _ = lineEdit(m.filterInput, m.filterCursor, key, typed)
	}
	return m, nil
}

// viewFilterPrompt renders the filter prompt.
func (m Model) viewFilterPrompt() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  Filter %d forks by owner/name\n\n", len(m.forks)))
	b.WriteString("  Match: " + ui.Input(m.themeContext(), ui.InputState{
		Value: m.filterInput, Cursor: m.filterCursor, Focused: true, Enabled: true,
	}, ui.ContentWidth(m.width)-11) + "\n\n")
	b.WriteString("  " + m.styles().help.Render(m.themeContext().Glyph(theme.ArrowLeft)+"/"+m.themeContext().Glyph(theme.ArrowRight)+" move  Home/End  Enter apply  Esc cancel  Ctrl+U clear  (empty clears the filter)") + "\n")
	return b.String()
}

// visibleCount is the number of forks passing the active filter.
func (m Model) visibleCount() int {
	if m.filter == "" {
		return len(m.forks)
	}
	n := 0
	for i := range m.forks {
		if matchesFilter(m.forks[i], m.filter) {
			n++
		}
	}
	return n
}
