package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

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

// unmeasuredPageSize is the row budget for a model that has not received a
// WindowSizeMsg yet -- every model a test builds directly. Picking a usable
// page keeps those tests rendering something rather than nothing.
const unmeasuredPageSize = 10

// chromeHeight is the true rendered height of everything viewTable emits that
// is not a fork row.
//
// It measures rather than counts. The previous constant budgeted one line for
// the keybinding footer, but ui.KeyLegend wraps to as many lines as the
// bindings need -- nine of them at the enforced 80-column floor, four even at
// 200 columns. The frame therefore overran the terminal on every single render
// of the fork table, Bubble Tea's renderer scrolled, and the status bar and
// column header were pushed off the top permanently. Reversing the sort order
// was the only way to read the top-ranked forks.
//
// Anything conditional is measured under the same condition the renderer uses,
// so a chrome line the renderer skips is a chrome line this does not charge for.
func (m Model) chromeHeight() int {
	// Fullscreen hides every piece of chrome except the column header -- and
	// then ends on the last row's newline, so the frame carries one trailing
	// blank line that the other modes absorb into the footer. That blank is a
	// real terminal row, so it is charged for here rather than overrunning.
	if m.fullscreen {
		return 2
	}
	// The status bar and the feedback line are one line each by construction:
	// renderStatusBar elides its variable fields to fit (see the priority loop
	// there), and the feedback line is always emitted, blank when idle.
	height := lipgloss.Height(m.renderStatusBar()) + 1 + 1
	if legend := m.tableLegends(); legend != "" {
		height += lipgloss.Height(m.styles().help.Render(" " + legend))
	}
	height += lipgloss.Height(m.tableKeyLegend())
	return height
}

// rowWindow returns the [start, end) window of visible positions the table
// draws for a budget of n rows with the cursor at cursorPos. The list stays
// pinned to the top until the cursor passes the last row, after which the
// cursor is pinned to the bottom.
func rowWindow(cursorPos, n, total int) (int, int) {
	start := 0
	if cursorPos >= n {
		start = cursorPos - n + 1
	}
	end := start + n
	if end > total {
		end = total
	}
	return start, end
}

// clusterHeadersIn counts the cluster header lines viewTable emits for the
// [start, end) window. It mirrors the emit condition in viewTable exactly; the
// two must agree or the budget is wrong in whichever direction they differ.
func (m Model) clusterHeadersIn(vis []int, start, end int) int {
	if !m.groupByCluster {
		return 0
	}
	prev := ""
	if start > 0 {
		prev = m.forks[vis[start-1]].Heat.ClusterID
	}
	headers := 0
	for p := start; p < end; p++ {
		cur := m.forks[vis[p]].Heat.ClusterID
		if (start == 0 && p == start) || cur != prev {
			headers++
		}
		prev = cur
	}
	return headers
}

// pageSize is how many fork rows one screen holds, and therefore how far
// PgUp/PgDn moves. Single source of truth for the render window and the paging
// keys -- they cannot drift apart if they call the same function.
//
// Cluster headers are budgeted by iterating rather than solving: the header
// count depends on the window and the window depends on the header count, but
// the map is monotone and converges in two passes on real data, so a bounded
// loop is enough. The floor of one row is not optional -- without it a window
// dense in headers could shrink to nothing and hide the cursor.
func (m Model) pageSize() int {
	if m.height <= 0 {
		return unmeasuredPageSize
	}
	budget := m.height - m.chromeHeight()
	if budget < 1 {
		return 1
	}
	if !m.groupByCluster {
		return budget
	}

	vis := m.visibleIdx()
	cursorPos := visiblePos(m.cursor, vis)
	if cursorPos < 0 {
		cursorPos = 0
	}
	rows := budget
	for range 3 {
		start, end := rowWindow(cursorPos, rows, len(vis))
		next := budget - m.clusterHeadersIn(vis, start, end)
		if next < 1 {
			next = 1
		}
		if next == rows {
			break
		}
		rows = next
	}
	return rows
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
	max := maxScrollOffset(helpBody(m.themeContext()), m.helpViewHeight())
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
