package threads

import (
	"fmt"
	"strings"

	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

func renderModel(m Model) string {
	if m.showHelp {
		return renderHelp()
	}
	if m.err != nil {
		return fmt.Sprintf("error: %v\n\npress q to quit", m.err)
	}
	if !m.loaded {
		return "loading threads…"
	}
	if len(m.threads) == 0 {
		return "no unresolved threads on this PR\n\npress q to quit"
	}
	var b strings.Builder
	b.WriteString(renderTUIStatus(m.prStatus, m.number))
	b.WriteString("\n")
	for i, t := range m.threads {
		marker := "  "
		if i == m.cursor {
			marker = "> "
		}
		reviewer := "unknown"
		if len(t.Comments) > 0 {
			reviewer = fmt.Sprintf("%s (%s)", t.Comments[0].Author, t.Comments[0].AuthorType)
		}
		outdated := ""
		if t.IsOutdated {
			outdated = " (outdated)"
		}
		fmt.Fprintf(&b, "%s%-16s  %s:%d%s\n", marker, reviewer, t.Path, t.Line, outdated)
	}
	hasSuggestion := false
	if m.cursor < len(m.threads) {
		t := m.threads[m.cursor]
		b.WriteString("\n")
		fmt.Fprintf(&b, "%s\n", threadStateLabel(t))
		if len(t.Comments) > 0 {
			if m.Verbose && t.Comments[0].CreatedAt != "" {
				fmt.Fprintf(&b, "  📅 Created: %s\n", t.Comments[0].CreatedAt)
			}
			b.WriteString(renderCommentBody(t.Comments[0].Body))
			b.WriteString("\n")
			sugs := threadsops.ParseSuggestions("", t.Comments[0].Body)
			if len(sugs) > 0 {
				hasSuggestion = true
				fmt.Fprintf(&b, "\n💡 Suggestion available (a to apply)\n")
			}
		}
		// Code context block (populated only when --show-code N is set).
		if cc, ok := m.codeContexts[t.ID]; ok && cc != nil {
			b.WriteString("\n")
			b.WriteString(renderCodeContext(t, *cc))
			b.WriteString("\n")
		}
	}
	footer := "\n[r/Enter] reply  [R] resolve  [A] unresolve-all  [o] open  [?] help  [q] quit\n"
	if hasSuggestion {
		footer = "\n[r/Enter] reply  [R] resolve  [a] apply-suggestion  [A] unresolve-all  [o] open  [?] help  [q] quit\n"
	}
	b.WriteString(footer)
	if m.status != "" {
		fmt.Fprintf(&b, "\n%s\n", m.status)
	}
	if m.confirm != "" {
		fmt.Fprintf(&b, "\n[%s] press y to confirm, any other key to cancel\n", m.confirm)
	}
	if m.composing {
		fmt.Fprintf(&b, "\n--- compose (%s) — Ctrl+S to send, Esc to cancel ---\n%s_\n", m.composeFor, string(m.composeBuf))
	}
	return b.String()
}

// renderTUIStatus formats the PR status header for the TUI panel.
// The TUI always uses dash markers; signal coloring is via lipgloss styles
// applied by the caller if needed (currently plain text).
func renderTUIStatus(s gh.PullRequestStatus, number int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PR #%d — %s\n", number, s.Title)
	fmt.Fprintf(&b, "  Mergeable: %s\n", statusOrDash(s.MergeStateStatus))
	fmt.Fprintf(&b, "  Reviews:   %s\n", statusOrDash(s.ReviewDecision))
	fmt.Fprintf(&b, "  Checks:    %s\n", statusOrDash(s.ChecksState))
	if s.OutdatedThreads > 0 {
		fmt.Fprintf(&b, "  Threads:   %d unresolved, %d outdated\n", s.UnresolvedThreads, s.OutdatedThreads)
	} else {
		fmt.Fprintf(&b, "  Threads:   %d unresolved\n", s.UnresolvedThreads)
	}
	return b.String()
}

// renderCommentBody renders a comment body. Any embedded ```suggestion blocks
// are visually marked with a "💡 Suggestion:" prefix on the fence lines so a
// human reader can spot them at a glance. The body itself is returned
// otherwise unchanged so the text remains paste-friendly.
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
			if strings.HasPrefix(strings.ToLower(trimmed), "```suggestion") ||
				strings.HasPrefix(strings.ToLower(trimmed), "~~~suggestion") {
				inBlock = true
				out = append(out, "💡 Suggestion:")
				continue
			}
			out = append(out, line)
			continue
		}
		// inBlock: detect close fence
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inBlock = false
			out = append(out, "    (end suggestion)")
			continue
		}
		out = append(out, "  | "+line)
	}
	return strings.Join(out, "\n")
}

func statusOrDash(v string) string {
	if v == "" {
		return "—"
	}
	return v
}

// threadStateLabel renders the resolve/outdated state pair as a single line,
// matching the gh-pr-display behaviour:
//
//	Unresolved + active   -> "⚠️ Unresolved (active)"
//	Unresolved + outdated -> "⚠️ Unresolved (outdated — code changed)"
//	Resolved + active     -> "✓ Resolved (active)"
//	Resolved + outdated   -> "✓ Resolved (outdated — code changed)"
func threadStateLabel(t gh.ReviewThread) string {
	glyph, label := "⚠️", "Unresolved"
	if t.IsResolved {
		glyph, label = "✓", "Resolved"
	}
	suffix := "active"
	if t.IsOutdated {
		suffix = "outdated — code changed"
	}
	return fmt.Sprintf("%s %s (%s)", glyph, label, suffix)
}

// renderCodeContext formats a CodeContext into a plain-text framed block for
// the TUI detail pane. The arrow marker points to the thread's anchored line
// range. Outdated threads get a small warning suffix.
func renderCodeContext(t gh.ReviewThread, cc threadsops.CodeContext) string {
	var b strings.Builder
	refShort := cc.Ref
	if len(refShort) > 7 {
		refShort = refShort[:7]
	}
	outdatedTag := ""
	if cc.Outdated {
		outdatedTag = " (outdated)"
	}
	fmt.Fprintf(&b, "┌── code at %s:%d-%d [ref %s]%s ──\n", cc.Path, cc.StartLine, cc.EndLine, refShort, outdatedTag)
	hlStart, hlEnd := t.Line, t.Line
	if t.StartLine != nil && *t.StartLine > 0 {
		hlStart = *t.StartLine
	}
	if hlEnd < hlStart {
		hlEnd = hlStart
	}
	for i, ln := range cc.Lines {
		n := cc.StartLine + i
		marker := " "
		if n >= hlStart && n <= hlEnd {
			marker = "←"
		}
		fmt.Fprintf(&b, "│ %4d │ %s %s\n", n, marker, ln)
	}
	b.WriteString("└─────")
	if cc.Outdated {
		b.WriteString("\n  (heads up: this thread is marked outdated — snippet shows current code at these lines)")
	}
	return b.String()
}

func renderHelp() string {
	return `spoon threads — keybindings

  ↑/↓, j/k     Navigate threads
  Enter, r     Reply (opens textarea)
  R            Resolve current thread
  a            Apply suggestion (if thread has one) or Resolve all
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
