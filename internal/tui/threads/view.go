package threads

import (
	"fmt"
	"strings"

	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

func renderModel(m Model) string {
	ctx := m.themeContext()
	if m.showHelp {
		return renderHelp(ctx)
	}
	if m.err != nil {
		return ui.Alert(ctx, ui.AlertError, m.err.Error(), m.width) + "\n\npress q to quit"
	}
	if !m.loaded {
		return "loading threads..."
	}
	if len(m.threads) == 0 {
		return "no unresolved threads on this PR\n\npress q to quit"
	}
	var b strings.Builder
	b.WriteString(renderTUIStatus(m.prStatus, m.number, ctx))
	b.WriteString("\n")
	for i, thread := range m.threads {
		marker := "  "
		if i == m.cursor {
			marker = ctx.Glyph(theme.Selected) + " "
		}
		reviewer := "unknown"
		if len(thread.Comments) > 0 {
			reviewer = fmt.Sprintf("%s (%s)", thread.Comments[0].Author, thread.Comments[0].AuthorType)
		}
		outdated := ""
		if thread.IsOutdated {
			outdated = " (outdated)"
		}
		fmt.Fprintf(&b, "%s%-16s  %s:%d%s\n", marker, reviewer, thread.Path, thread.Line, outdated)
	}
	if m.cursor < len(m.threads) {
		thread := m.threads[m.cursor]
		b.WriteString("\n")
		fmt.Fprintf(&b, "%s\n", threadStateLabel(thread, ctx))
		if len(thread.Comments) > 0 {
			if m.Verbose && thread.Comments[0].CreatedAt != "" {
				fmt.Fprintf(&b, "  Created: %s\n", thread.Comments[0].CreatedAt)
			}
			b.WriteString(renderCommentBody(thread.Comments[0].Body))
			b.WriteString("\n")
			for _, comment := range thread.Comments {
				if len(threadsops.ParseSuggestions(comment.ID, comment.Body)) > 0 {
					fmt.Fprintf(&b, "\nSuggestion available (a to apply)\n")
					break
				}
			}
		}
		if codeContext, ok := m.codeContexts[thread.ID]; ok && codeContext != nil {
			b.WriteString("\n")
			b.WriteString(renderCodeContext(thread, *codeContext, ctx))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n[r/Enter] reply  [R] resolve  [a] apply-suggestion  [c] counter-propose  [Ctrl+A] resolve-all  [A] unresolve-all  [o] open  [?] help  [q] quit\n")
	if m.status != "" {
		fmt.Fprintf(&b, "\n%s\n", m.status)
	}
	if m.confirm != "" {
		fmt.Fprintf(&b, "\n[%s] press y to confirm, any other key to cancel\n", m.confirm)
	}
	if m.composing {
		fmt.Fprintf(&b, "\n--- compose (%s) %s Ctrl+S to send, Esc to cancel ---\n%s_\n", m.composeFor, ctx.Glyph(theme.EmDash), string(m.composeBuf))
	}
	return b.String()
}

// renderTUIStatus formats the PR status header for the TUI panel.
// The TUI always uses dash markers; signal coloring is via lipgloss styles
// applied by the caller if needed (currently plain text).
func renderTUIStatus(s gh.PullRequestStatus, number int, ctx theme.Context) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PR #%d %s %s\n", number, ctx.Glyph(theme.EmDash), s.Title)
	fmt.Fprintf(&b, "  Mergeable: %s\n", statusOrDash(s.MergeStateStatus, ctx))
	fmt.Fprintf(&b, "  Reviews:   %s\n", statusOrDash(s.ReviewDecision, ctx))
	fmt.Fprintf(&b, "  Checks:    %s\n", statusOrDash(s.ChecksState, ctx))
	if s.OutdatedThreads > 0 {
		fmt.Fprintf(&b, "  Threads:   %d unresolved, %d outdated\n", s.UnresolvedThreads, s.OutdatedThreads)
	} else {
		fmt.Fprintf(&b, "  Threads:   %d unresolved\n", s.UnresolvedThreads)
	}
	return b.String()
}

// renderCommentBody renders the comment body for the detail pane. Suggestion
// blocks are transformed for visual separation: the opening fence becomes a
// "💡 Suggestion:" marker, the closing fence becomes "    (end suggestion)",
// and each suggestion-content line is prefixed with "  | ". All other lines
// (outside any suggestion block) are unchanged.
func renderCommentBody(body string) string {
	if body == "" {
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if !inBlock {
			if strings.HasPrefix(strings.ToLower(trimmed), "```suggestion") || strings.HasPrefix(strings.ToLower(trimmed), "~~~suggestion") {
				inBlock = true
				out = append(out, "Suggestion:")
				continue
			}
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inBlock = false
			out = append(out, "    (end suggestion)")
			continue
		}
		out = append(out, "  | "+line)
	}
	return strings.Join(out, "\n")
}

func statusOrDash(value string, ctx theme.Context) string {
	if value == "" {
		return ctx.Glyph(theme.EmDash)
	}
	return value
}

// threadStateLabel renders the resolve/outdated state pair as a single line,
// matching the gh-pr-display behaviour:
//
//	Unresolved + active   -> "⚠️ Unresolved (active)"
//	Unresolved + outdated -> "⚠️ Unresolved (outdated — code changed)"
//	Resolved + active     -> "✓ Resolved (active)"
//	Resolved + outdated   -> "✓ Resolved (outdated — code changed)"
func threadStateLabel(thread gh.ReviewThread, ctx theme.Context) string {
	glyph, label := ctx.Glyph(theme.Warning), "Unresolved"
	if thread.IsResolved {
		glyph, label = ctx.Glyph(theme.Check), "Resolved"
	}
	suffix := "active"
	if thread.IsOutdated {
		suffix = "outdated " + ctx.Glyph(theme.EmDash) + " code changed"
	}
	return fmt.Sprintf("%s %s (%s)", glyph, label, suffix)
}

// renderCodeContext formats a CodeContext into a plain-text framed block for
// the TUI detail pane. The arrow marker points to the thread's anchored line
// range. Outdated threads get a small warning suffix.
func renderCodeContext(thread gh.ReviewThread, cc threadsops.CodeContext, ctx theme.Context) string {
	var b strings.Builder
	refShort := cc.Ref
	if len(refShort) > 7 {
		refShort = refShort[:7]
	}
	outdatedTag := ""
	if cc.Outdated {
		outdatedTag = " (outdated)"
	}
	h := ctx.Glyph(theme.BoxHorizontal)
	v := ctx.Glyph(theme.BoxVertical)
	fmt.Fprintf(&b, "%s%s code at %s:%d-%d [ref %s]%s %s%s\n", ctx.Glyph(theme.BoxTopLeft), h+h, cc.Path, cc.StartLine, cc.EndLine, refShort, outdatedTag, h+h, "")
	hlStart, hlEnd := thread.Line, thread.Line
	if thread.StartLine != nil && *thread.StartLine > 0 {
		hlStart = *thread.StartLine
	}
	if hlEnd < hlStart {
		hlEnd = hlStart
	}
	for i, line := range cc.Lines {
		marker := " "
		if n := cc.StartLine + i; n >= hlStart && n <= hlEnd {
			marker = ctx.Glyph(theme.ArrowLeft)
		}
		fmt.Fprintf(&b, "%s %4d %s %s %s\n", v, cc.StartLine+i, v, marker, line)
	}
	b.WriteString(ctx.Glyph(theme.BoxBottomLeft) + strings.Repeat(h, 5))
	if cc.Outdated {
		b.WriteString("\n  (heads up: this thread is marked outdated " + ctx.Glyph(theme.EmDash) + " snippet shows current code at these lines)")
	}
	return b.String()
}

func renderHelp(ctx theme.Context) string {
	return "spoon threads " + ctx.Glyph(theme.EmDash) + ` keybindings

  ` + ctx.Glyph(theme.ArrowUp) + `/` + ctx.Glyph(theme.ArrowDown) + `, j/k     Navigate threads
  Enter, r     Reply (opens textarea)
  R            Resolve current thread
  a            Apply suggestion on current thread (no-op if thread has none)
  c            Counter-propose: opens $EDITOR with a temp file; content is
               wrapped in a suggestion block and posted as a reply.
               Empty content (or no edits) cancels.
  Ctrl+A       Resolve all (with confirm)
  A            Unresolve all (with confirm)
  o            Open PR in browser
  ?            Toggle this help
  q            Quit

In the composer:
  Ctrl+S       Submit
  Esc          Cancel

Press ? again to return.
`
}
