package threads

import (
	"fmt"
	"strings"

	gh "github.com/svnbjrn/spoon/internal/github"
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
	if m.cursor < len(m.threads) {
		t := m.threads[m.cursor]
		b.WriteString("\n")
		fmt.Fprintf(&b, "%s\n", threadStateLabel(t))
		if len(t.Comments) > 0 {
			b.WriteString(t.Comments[0].Body)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n[r/Enter] reply  [R] resolve  [a] resolve-all  [A] unresolve-all  [o] open  [?] help  [q] quit\n")
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

func renderHelp() string {
	return `spoon threads — keybindings

  ↑/↓, j/k     Navigate threads
  Enter, r     Reply (opens textarea)
  R            Resolve current thread
  a            Resolve all (with confirm)
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
