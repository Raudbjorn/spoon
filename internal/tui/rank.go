package tui

// rank.go implements the `R` intent prompt: the user types a free-text intent and
// the fork table is re-ordered by relevance to it. Scoring uses the same
// embed.QueryScorer seam as `spn forks list --query` — the Voyage cross-encoder
// when an API key is configured, the built-in lexical scorer otherwise — over the
// same per-fork digests, so the TUI and the CLI cannot disagree about which fork
// better matches a query.
//
// This is deliberately NOT the `/` filter (filter.go), and the two compose:
// `/` narrows by a substring of owner/name, `R` orders what survives by intent.
// Keeping them separate is what filter.go argues for — a substring match is free,
// local and order-preserving, while ranking costs a network call and replaces the
// sort column, so folding them into one key would hide a paid operation behind a
// free one. Ranking only ever considers forks the active filter admits.
//
// Ranking, not hiding: a relevance floor would differ per scorer and per query,
// so dropping rows would lose forks with no way for the user to tell. Re-ordering
// surfaces the same judgment without losing data.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/embed"
)

// relevanceSortCol is the sort column the ranking installs. It is deliberately
// absent from cycleSortColumn's list, so pressing `s` leaves relevance ordering
// rather than cycling back into it.
const relevanceSortCol = "relevance"

// rankResultMsg carries a completed ranking back to the Update loop. seq
// guards against a slow request landing after the user has moved on to a newer
// query.
type rankResultMsg struct {
	seq    int
	query  string
	method string
	scores map[string]float64
	err    error
}

// WithQueryScorer returns a copy of the model using the given scorer for `R`
// intent ranking. Nil (the default) means the built-in lexical scorer, which
// needs no key and no network — the TUI must stay usable with neither.
func (m Model) WithQueryScorer(scorer embed.QueryScorer) Model {
	m.queryScorer = scorer
	return m
}

// promptRank opens the intent prompt, seeded with the active query so an
// existing ranking can be edited rather than retyped.
func (m *Model) promptRank() {
	m.rankQuery = m.rankApplied
	m.rankCursor = len([]rune(m.rankQuery))
	m.view = viewRank
}

// handleRankKey handles input in the intent prompt. Typing never touches the
// network: the request is issued on Enter only, matching how every other prompt in
// this TUI applies. Scoring per keystroke would put an API call on every
// character, needing debounce and cancellation to show the user rankings they did
// not ask for yet.
//
// Enter on an empty query clears the ranking, mirroring `/`'s "empty clears the
// filter". Esc cancels the edit and leaves any active ranking alone — also
// matching `/`, whose Esc-on-the-table is what clears.
func (m *Model) handleRankKey(key, typed string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		query := strings.TrimSpace(m.rankQuery)
		m.view = viewTable
		if query == "" {
			m.clearRank()
			return m, nil
		}
		return m, m.startRankScoring(query)
	case "esc":
		m.view = viewTable
		return m, nil
	default:
		m.rankQuery, m.rankCursor, _ = lineEdit(m.rankQuery, m.rankCursor, key, typed)
	}
	return m, nil
}

// clearRank drops the ranking and restores the heat ordering.
func (m *Model) clearRank() {
	if m.rankApplied == "" && !m.rankPending {
		return
	}
	m.rankApplied = ""
	m.rankQuery = ""
	m.rankMethod = ""
	m.rankScores = nil
	m.rankPending = false
	// Invalidate any in-flight request so its result is discarded on arrival.
	m.rankSeq++
	if m.sortCol == relevanceSortCol {
		m.sortCol = "heat"
		m.sortAsc = false
		m.reapplySort()
	}
}

// startRankScoring issues the scoring request as a one-shot tea.Cmd. Bubble
// Tea runs it on its own goroutine and delivers the returned message, so no
// channel pump is needed — unlike the cluster pipeline, which streams progress.
func (m *Model) startRankScoring(query string) tea.Cmd {
	m.rankSeq++
	seq := m.rankSeq
	m.rankPending = true
	m.rankQuery = query

	// Snapshot the digests now, on the UI goroutine: m.forks keeps changing as
	// enrichment lands, and the scoring goroutine must not read it concurrently.
	type candidate struct {
		id     string
		digest string
	}
	// Only forks the active `/` filter admits: ranking rows the user has filtered
	// away would pay for scores that cannot be seen.
	candidates := make([]candidate, 0, len(m.forks))
	for _, i := range m.visibleIdx() {
		digest := embed.QueryDigest(m.forks[i].T2)
		if strings.TrimSpace(digest) == "" {
			// No compare data yet: nothing to judge. Matches how the CLI's
			// --query scoring skips forks without a digest.
			continue
		}
		candidates = append(candidates, candidate{id: m.forks[i].Fork.ID, digest: digest})
	}

	scorer := m.queryScorer
	if scorer == nil {
		scorer = embed.LexicalQueryScorer{}
	}
	ctx := m.lifecycleCtx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		if len(candidates) == 0 {
			return rankResultMsg{seq: seq, query: query, method: scorer.Method(),
				err: fmt.Errorf("no forks have compare data to rank yet")}
		}
		docs := make([]string, len(candidates))
		for i, c := range candidates {
			docs[i] = c.digest
		}
		scores, err := scorer.Rerank(ctx, query, docs)
		if err != nil {
			return rankResultMsg{seq: seq, query: query, method: scorer.Method(), err: err}
		}
		if len(scores) != len(docs) {
			return rankResultMsg{seq: seq, query: query, method: scorer.Method(),
				err: fmt.Errorf("scorer returned %d scores, want %d", len(scores), len(docs))}
		}
		byID := make(map[string]float64, len(candidates))
		for i, c := range candidates {
			byID[c.id] = scores[i]
		}
		return rankResultMsg{seq: seq, query: query, method: scorer.Method(), scores: byID}
	}
}

// handleRankResult applies a ranking, or reports why it could not be applied.
// A Voyage outage degrades to a message and the previous ordering; it never
// clears the table or takes the TUI out of service.
func (m *Model) handleRankResult(msg rankResultMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.rankSeq {
		// Superseded by a newer query, or cleared while in flight.
		return m, nil
	}
	m.rankPending = false
	if msg.err != nil {
		m.errMsg = "Rank failed: " + msg.err.Error()
		m.errMsgTime = time.Now()
		return m, nil
	}
	m.rankApplied = msg.query
	m.rankMethod = msg.method
	m.rankScores = msg.scores
	// Anchor the cursor to the selected fork so the user's selection survives
	// the re-sort, matching what the cluster toggle does. The re-sort moves rows
	// the filter may hide, so the cursor is re-clamped to a visible row after.
	selectedID := ""
	if m.cursor >= 0 && m.cursor < len(m.forks) {
		selectedID = m.forks[m.cursor].Fork.ID
	}
	m.sortCol = relevanceSortCol
	m.sortAsc = false
	m.reapplySort()
	m.restoreCursorByID(selectedID)
	m.cursor = clampCursorVisible(m.cursor, m.visibleIdx())
	return m, nil
}

// relevanceScore returns a fork's score under the active ranking. Forks with no
// score (no compare data when the query ran, or enriched afterwards) sort last
// rather than tying with a genuine zero.
func (m *Model) relevanceScore(i int) float64 {
	if m.rankScores == nil {
		return -1
	}
	score, ok := m.rankScores[m.forks[i].Fork.ID]
	if !ok {
		return -1
	}
	return score
}

// rankFooter describes the active ranking, including which scorer produced it —
// a Voyage ranking and a lexical one are not the same judgment, and the user
// should not have to guess which they are looking at.
func (m Model) rankFooter() string {
	if m.rankPending {
		return fmt.Sprintf("Ranking by %q...", strings.TrimSpace(m.rankQuery))
	}
	if m.rankApplied == "" {
		return ""
	}
	return fmt.Sprintf("Ranked by %q (%s, %d fork(s) scored) — R to edit, empty R to clear",
		m.rankApplied, m.rankMethod, len(m.rankScores))
}

// viewRank renders the query prompt.
func (m Model) viewRankPrompt() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  Rank forks by intent\n\n")
	b.WriteString("  Query: " + renderWithCursor(m.rankQuery, m.rankCursor) + "\n\n")
	scorer := "lexical (no Voyage key configured)"
	if m.queryScorer != nil {
		scorer = m.queryScorer.Method()
	}
	b.WriteString("  " + helpStyle.Render("scorer: "+scorer) + "\n")
	b.WriteString("  " + helpStyle.Render("←/→ move  Home/End  Enter rank  Esc clear  Ctrl+U clear line") + "\n")
	return b.String()
}
