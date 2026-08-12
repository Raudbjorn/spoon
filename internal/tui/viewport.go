package tui

import "strings"

// Cursor and scroll-window arithmetic, shared by the table, the detail view
// and the help overlay. Before this file the render window and the movement
// keys each did their own arithmetic inline, and the cursor clamp was written
// out by hand at three separate call sites -- which is exactly how a paging
// key and the renderer end up disagreeing about how tall a page is.

// clampCursorVisible snaps a cursor onto a visible row: unchanged when it is
// already visible, otherwise the nearest visible row at or after it, otherwise
// the last visible row. Returns -1 when nothing is visible at all.
//
// vis holds absolute indices into m.forks, ascending. The -1 result is not a
// new convention: every cursor consumer (detail.go, clipboard.go, the enter
// and Space handlers, open/compare) already guards m.cursor < 0, so an empty
// result set costs those call sites nothing.
func clampCursorVisible(cursor int, vis []int) int {
	if len(vis) == 0 {
		return -1
	}
	for _, abs := range vis {
		if abs >= cursor {
			return abs
		}
	}
	return vis[len(vis)-1]
}

// tableChrome is the number of lines viewTable spends on anything that is not
// a fork row: the status bar, the column header, the feedback line (always
// emitted, blank when there is nothing to say) and the keybinding footer.
const tableChrome = 4

// pageSize is how many fork rows one screen holds, and therefore how far
// PgUp/PgDn moves. Single source of truth for the render window and the paging
// keys -- they cannot drift apart if they call the same function.
//
// The badge legend is optional, so it is counted only when present; the old
// inline "m.height - 4" assumed it away and overdrew by a row whenever any
// fork carried a badge.
//
// Known and deliberately unhandled: under groupByCluster the renderer also
// emits a header line per cluster boundary in view, which this does not
// budget for, so the frame overruns by the number of headers on screen.
// Reserving rows for them is a fixpoint problem (the headers depend on the
// window, the window depends on the headers), and shrinking the window would
// hide the cursor when it sits on the last row. Paging stays self-consistent
// because the renderer and the keys use this same number either way.
func (m Model) pageSize() int {
	chrome := tableChrome
	if m.badgeLegend() != "" {
		chrome++
	}
	n := m.height - chrome
	if n < 1 {
		// No WindowSizeMsg yet (every model built directly in a test). Pick a
		// usable page rather than a negative one.
		n = 10
	}
	return n
}

// visiblePos returns the ordinal of an absolute fork index within vis, or -1
// when that fork is not visible.
func visiblePos(abs int, vis []int) int {
	for p, v := range vis {
		if v == abs {
			return p
		}
	}
	return -1
}

// moveCursorTo places the cursor at a position in *visible* space, clamped to
// the ends. Callers pass ordinals, not absolute indices, so paging arithmetic
// never has to know which forks the filter is hiding.
func (m *Model) moveCursorTo(pos int) {
	vis := m.visibleIdx()
	if len(vis) == 0 {
		m.cursor = -1
		return
	}
	if pos < 0 {
		pos = 0
	}
	if pos >= len(vis) {
		pos = len(vis) - 1
	}
	m.cursor = vis[pos]
}

// moveCursorBy steps the cursor delta rows through visible space. A cursor
// that is somehow not visible is first snapped onto a visible row, so movement
// always starts from a legal position.
func (m *Model) moveCursorBy(delta int) {
	vis := m.visibleIdx()
	if len(vis) == 0 {
		m.cursor = -1
		return
	}
	pos := visiblePos(m.cursor, vis)
	if pos < 0 {
		m.cursor = clampCursorVisible(m.cursor, vis)
		pos = visiblePos(m.cursor, vis)
		if pos < 0 {
			pos = 0
		}
	}
	m.moveCursorTo(pos + delta)
}

// scrollLines returns the [offset, offset+height) line window of body, for the
// detail and help views -- neither of which could scroll at all before.
//
// height <= 0 returns body unchanged: a model that has not received a
// WindowSizeMsg has height 0, and silently rendering nothing there would break
// every existing render test.
func scrollLines(body string, offset, height int) string {
	if height <= 0 {
		return body
	}
	lines := strings.Split(body, "\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	end := offset + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[offset:end], "\n")
}

// maxScrollOffset is the largest offset that still shows content, so a
// clamped offset never scrolls past the end into a blank screen.
func maxScrollOffset(body string, height int) int {
	if height <= 0 {
		return 0
	}
	n := len(strings.Split(body, "\n")) - height
	if n < 0 {
		return 0
	}
	return n
}

// scrollDetail moves the detail view by delta lines, clamped to the body.
// Clamping happens here rather than in viewDetail because View has a value
// receiver -- a clamp applied there could not persist into the model.
func (m *Model) scrollDetail(delta int) {
	max := maxScrollOffset(m.detailBody(), m.detailViewHeight())
	m.detailOffset = clampInt(m.detailOffset+delta, 0, max)
}

// scrollHelp moves the help overlay by delta lines, clamped to the body.
func (m *Model) scrollHelp(delta int) {
	max := maxScrollOffset(helpBody(), m.helpViewHeight())
	m.helpOffset = clampInt(m.helpOffset+delta, 0, max)
}

// clampInt constrains v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
