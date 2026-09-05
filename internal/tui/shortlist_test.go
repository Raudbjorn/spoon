package tui

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forksops"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// shortlistModel returns a table model with four forks of distinct heat,
// two of them at identical heat (a tie band), with the shortlist computed.
func shortlistModel(t *testing.T) *Model {
	t.Helper()
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	m := newClusterTestModel([]ScoredFork{
		makeSF("alice/tool", 90, "", "", 0),
		makeSF("bob/tool", 40, "", "", 0),
		makeSF("carol/tool", 40, "", "", 0),
		makeSF("dave/tool", 10, "", "", 0),
	})
	for i := range m.forks {
		m.forks[i].Heat.Confidence = 0.9
	}
	m.view, m.width, m.height = viewTable, 100, 24
	themed := m.WithTheme(ctx)
	themed.recomputeShortlist()
	return &themed
}

func TestRecomputeShortlist_AttachesRankAndReport(t *testing.T) {
	m := shortlistModel(t)
	for _, sf := range m.forks {
		if sf.Rank == nil {
			t.Fatalf("%s missing Rank", sf.Fork.ID)
		}
	}
	byID := map[string]ScoredFork{}
	for _, sf := range m.forks {
		byID[sf.Fork.ID] = sf
	}
	if byID["alice/tool"].Rank.PScore < byID["bob/tool"].Rank.PScore || byID["bob/tool"].Rank.PScore < byID["dave/tool"].Rank.PScore {
		t.Errorf("pScore not monotone in heat: %+v", byID)
	}
	if !byID["bob/tool"].Rank.TieBand || !byID["carol/tool"].Rank.TieBand || byID["alice/tool"].Rank.TieBand {
		t.Errorf("tie bands: bob=%v carol=%v alice=%v", byID["bob/tool"].Rank.TieBand, byID["carol/tool"].Rank.TieBand, byID["alice/tool"].Rank.TieBand)
	}
	if m.shortlist == nil || m.shortlist.PoolSize != 4 || m.shortlist.ShortlistN != shortlistK {
		t.Errorf("report: %+v", m.shortlist)
	}
	if m.shortlist.POTH <= 0 || m.shortlist.POTH > 1 {
		t.Errorf("POTH %v", m.shortlist.POTH)
	}
}

func TestRecomputeShortlist_EmptyIsNoop(t *testing.T) {
	m := newClusterTestModel(nil)
	m.recomputeShortlist()
	if m.shortlist != nil {
		t.Errorf("report should stay nil on an empty table: %+v", m.shortlist)
	}
}

func TestSortByPScore_RankedFirstNilLast(t *testing.T) {
	m := shortlistModel(t)
	m.forks = append(m.forks, makeSF("erin/tool", 95, "", "", 0)) // added after ranking: Rank nil
	m.sortCol, m.sortAsc = pscoreSortCol, false
	m.sortForks()
	if m.forks[0].Fork.ID != "alice/tool" {
		t.Errorf("first should be the highest pScore, got %s", m.forks[0].Fork.ID)
	}
	if last := m.forks[len(m.forks)-1]; last.Fork.ID != "erin/tool" {
		t.Errorf("unranked fork should sort last, got %s", last.Fork.ID)
	}
}

func TestCycleSortColumn_IncludesPScore(t *testing.T) {
	m := shortlistModel(t)
	m.sortCol = "heat"
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		m.cycleSortColumn()
		seen[m.sortCol] = true
	}
	if !seen[pscoreSortCol] {
		t.Errorf("sort cycle never reached %q: %v", pscoreSortCol, seen)
	}
}

func TestViewTable_PColumnAndTieBadge(t *testing.T) {
	m := shortlistModel(t)
	out := m.viewTable()
	header := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "REPOSITORY") {
			header = l
		}
	}
	if !strings.Contains(header, " P ") && !strings.Contains(header, " P") {
		t.Errorf("header lacks P column: %q", header)
	}
	// The P cell carries the tie mark: "~50" on the tied pair, " 99" on the
	// clear leader (100 clamps to 99 so the cell stays 3 wide).
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(l, "bob/tool"), strings.Contains(l, "carol/tool"):
			if !strings.Contains(l, "~") {
				t.Errorf("tied row lacks tie mark: %q", l)
			}
		case strings.Contains(l, "alice/tool"):
			if !strings.Contains(l, " 99 ") || strings.Contains(l, "~") {
				t.Errorf("leader should read ' 99' unmarked: %q", l)
			}
		}
	}
}

func TestStatusBar_ShowsShortlistPrecision(t *testing.T) {
	m := shortlistModel(t)
	m.width = 120
	if bar := m.renderStatusBar(); !strings.Contains(bar, "POTH") {
		t.Errorf("status bar lacks POTH segment: %q", bar)
	}
}

func TestDetail_ShowsRankBlock(t *testing.T) {
	m := shortlistModel(t)
	m.view = viewDetail
	m.cursor = 0
	body := m.detailBody()
	for _, want := range []string{"Rank", "P-score", "P(top", "95%"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q:\n%s", want, body)
		}
	}
}

func TestExport_IncludesRankAndReport(t *testing.T) {
	m := shortlistModel(t)
	path := filepath.Join(t.TempDir(), "out.json")
	msg := m.doExport(m.forks, path)()
	if dm, ok := msg.(exportDoneMsg); !ok || dm.err != nil {
		t.Fatalf("export failed: %#v", msg)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.RankReport == nil || data.RankReport.PoolSize != 4 {
		t.Errorf("rank_report missing: %+v", data.RankReport)
	}
	for _, f := range data.Forks {
		if f.Rank == nil {
			t.Errorf("%s export lacks rank block", f.FullName)
		}
	}
}

func TestPScoreCellUndefined(t *testing.T) {
	if got := pscoreCell(ScoredFork{Rank: &forksops.RankStats{PScore: math.NaN()}}); got != "  -" {
		t.Fatalf("undefined P-score cell = %q", got)
	}
}

func TestShortlistCacheIncludesConfidence(t *testing.T) {
	m := shortlistModel(t)
	m.forks[0].Heat.Score = 41
	m.recomputeShortlist()
	before := m.forks[0].Rank
	m.recomputeShortlist()
	if m.forks[0].Rank != before {
		t.Fatal("unchanged inputs missed cache")
	}
	m.forks[0].Heat.Confidence = 0.3
	m.recomputeShortlist()
	if m.forks[0].Rank == before || m.forks[0].Rank.PScore == before.PScore {
		t.Fatal("confidence change left stale rank statistics")
	}
}

func TestExportSmallRankPools(t *testing.T) {
	for _, n := range []int{1, 2} {
		m := shortlistModel(t)
		m.forks = m.forks[:n]
		m.recomputeShortlist()
		path := filepath.Join(t.TempDir(), "out.json")
		msg := m.doExport(m.forks, path)()
		if dm, ok := msg.(exportDoneMsg); !ok || dm.err != nil {
			t.Fatalf("n=%d: %#v", n, msg)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var data struct {
			RankReport map[string]any `json:"rank_report"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"poth", "cpothK"} {
			if v, ok := data.RankReport[key]; !ok || v != nil {
				t.Fatalf("n=%d: %s=%v (present=%v)", n, key, v, ok)
			}
		}
	}
}
