package threads

import (
	"fmt"
	"strings"
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
	fmt.Fprintf(&b, "PR #%d — %d unresolved\n\n", m.number, len(m.threads))
	for i, t := range m.threads {
		marker := "  "
		if i == m.cursor {
			marker = "> "
		}
		reviewer := "unknown"
		if len(t.Comments) > 0 {
			reviewer = fmt.Sprintf("%s (%s)", t.Comments[0].Author, t.Comments[0].AuthorType)
		}
		fmt.Fprintf(&b, "%s%-16s  %s:%d\n", marker, reviewer, t.Path, t.Line)
	}
	if m.cursor < len(m.threads) {
		t := m.threads[m.cursor]
		b.WriteString("\n")
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
