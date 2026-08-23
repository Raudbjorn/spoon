package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/svnbjrn/spoon/internal/forksops"
)

// Shortlist ranking in the TUI: the same Gaussian utility model
// `spn forks list --shortlist` uses, run over the table's rows. The TUI has
// its own scoring pipeline (it never calls forksops.Stream), so the bridge
// is forksops.RankResults, which needs only each row's heat score and tier
// confidence. Every ranked row carries a P-score (= SUCRA, probability-of-
// being-better averaged over the pool), a tie-band flag, and the pool-level
// precision-of-hierarchy numbers land on Model.shortlist for the status bar.
// See docs/ranking.md. Empirical-Bayes shrinkage (--eb) stays CLI-only.

// shortlistK is the k behind P(rank ≤ k) and cPOTH_k: the size of the
// decision set the precision numbers describe ("the top ten").
const shortlistK = 10

// pscoreSortCol is the sort column keyed on P-score (higher is better);
// rows that were not ranked (added after the last recompute) sort last.
const pscoreSortCol = "pscore"

// recomputeShortlist ranks the current rows and attaches the result. It is
// called after every reapplySort — including a plain resort with unchanged
// heat (cycling the sort column, reversing direction) — so it first checks
// a signature of the fork set against the last ranked one and skips the
// O(n³) pass when nothing has actually changed. The signature sorts fork
// IDs first: reapplySort calls recomputeShortlist before reordering
// m.forks, so consecutive calls see the rows in whatever order the
// previous sort left them, and an order-dependent signature would treat
// every resort as a change. Empty tables leave the report nil. Rows beyond
// forksops.RankPoolCap keep Rank nil and render "-".
func (m *Model) recomputeShortlist() {
	ids := make([]string, len(m.forks))
	heat := make(map[string]float64, len(m.forks))
	for i, sf := range m.forks {
		ids[i] = sf.Fork.ID
		heat[sf.Fork.ID] = sf.Heat.Score
	}
	sort.Strings(ids)
	var sb strings.Builder
	for _, id := range ids {
		sb.WriteString(id)
		fmt.Fprintf(&sb, ":%.6f;", heat[id])
	}
	sig := sb.String()
	if sig == m.shortlistHash {
		return
	}
	m.shortlistHash = sig

	if len(m.forks) == 0 {
		m.shortlist = nil
		return
	}
	pool := make([]forksops.Result, len(m.forks))
	for i, sf := range m.forks {
		pool[i] = forksops.Result{Fork: sf.Fork, Heat: sf.Heat}
	}
	ranked, report := forksops.RankResults(pool, forksops.Options{ShortlistN: shortlistK, RankKeepAll: true})
	byID := make(map[string]*forksops.RankStats, len(ranked))
	for i := range ranked {
		byID[ranked[i].Fork.ID] = ranked[i].Rank
	}
	for i := range m.forks {
		m.forks[i].Rank = byID[m.forks[i].Fork.ID]
	}
	m.shortlist = &report
}

// pscoreCell renders the 3-cell P column: a tie mark ("~" when the row is
// statistically indistinguishable from its neighbour in shortlist order,
// blank otherwise) followed by the P-score as a two-digit percentage, so a
// certain winner reads " 99" and a tied mid-table pair "~50". 100 is
// clamped to 99 to keep the cell width fixed in both table variants; the
// non-compare variant has no badge column, which is why the mark lives
// here. Unranked rows render "  -".
func pscoreCell(sf ScoredFork) string {
	if sf.Rank == nil {
		return "  -"
	}
	pct := int(sf.Rank.PScore*100 + 0.5)
	if pct > 99 {
		pct = 99
	}
	mark := " "
	if sf.Rank.TieBand {
		mark = "~"
	}
	return fmt.Sprintf("%s%2d", mark, pct)
}

// shortlistStatus is the status-bar segment: hierarchy precision over the
// pool and within the top-k, both in [0,1]. Empty when nothing is ranked.
func (m Model) shortlistStatus() string {
	r := m.shortlist
	if r == nil || r.PoolSize == 0 {
		return ""
	}
	seg := fmt.Sprintf("POTH %s", fmtPrecision(r.POTH))
	if c := fmtPrecision(r.CPOTHk); c != "-" {
		seg += fmt.Sprintf(" top%d %s", m.shortlistTopK(), c)
	}
	return seg
}

func fmtPrecision(v float64) string {
	if math.IsNaN(v) { // set below the POTH minimum
		return "-"
	}
	return fmt.Sprintf("%.2f", v)
}

// shortlistPoolSize is the number of rows the current rank numbers are
// relative to (0 before the first ranking).
func (m Model) shortlistPoolSize() int {
	if m.shortlist == nil {
		return 0
	}
	return m.shortlist.PoolSize
}

// shortlistTopK is the k the precision and P(top k) numbers describe:
// shortlistK, or the pool size when fewer rows are ranked.
func (m Model) shortlistTopK() int {
	if n := m.shortlistPoolSize(); n > 0 && n < shortlistK {
		return n
	}
	return shortlistK
}
