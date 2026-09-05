package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
)

const offlineExport = `{
  "parent": {"full_name": "o/r"},
  "total_count": 6,
  "forks": [
    {"full_name": "o/f0", "heat": {"score": 50, "tier": 3, "confidence": 0.9}},
    {"full_name": "o/f1", "heat": {"score": 32, "tier": 1, "confidence": 0.3}},
    {"full_name": "o/f2", "heat": {"score": 0,  "tier": 2, "confidence": 0.7}},
    {"full_name": "o/f3", "heat": {"score": 30, "tier": 3, "confidence": 0.9}},
    {"full_name": "o/f4", "heat": {"score": 20, "tier": 1, "confidence": 0.3}},
    {"full_name": "o/f5", "heat": {"score": 10, "tier": 3, "confidence": 0.9}}
  ]
}`

const offlineJudgments = `{"forks":[
  {"id":"o/f0","novelty":"novel"},
  {"id":"o/f1","novelty":"established"},
  {"id":"o/f2","novelty":"established"},
  {"id":"o/f3","novelty":"novel"},
  {"id":"o/f4","novelty":"established"},
  {"id":"o/f5","novelty":"established"}
]}`

func writeOfflineFixtures(t *testing.T) (exportPath, judgmentsPath string) {
	t.Helper()
	dir := t.TempDir()
	exportPath = filepath.Join(dir, "export.json")
	judgmentsPath = filepath.Join(dir, "judgments.json")
	if err := os.WriteFile(exportPath, []byte(offlineExport), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(judgmentsPath, []byte(offlineJudgments), 0o644); err != nil {
		t.Fatal(err)
	}
	return exportPath, judgmentsPath
}

func TestSpnForksEval_fromExport_scoresVariantsWithoutNetwork(t *testing.T) {
	isolateSpoonRun(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		t.Fatal("offline eval must not construct a provider")
		return nil, "", nil
	}
	exportPath, judgmentsPath := writeOfflineFixtures(t)
	for _, variant := range []string{"heat", "erank", "pscore", "membership", "eb"} {
		var stdout, stderr bytes.Buffer
		exit := runEvalWith([]string{"o/r", "--from-export", exportPath, "--judgments", judgmentsPath, "--rank-variant", variant, "--shortlist", "3"}, &stdout, &stderr)
		if exit != 0 {
			t.Fatalf("%s: exit=%d stderr=%s", variant, exit, stderr.String())
		}
		var rep map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
			t.Fatalf("%s: bad report JSON: %v\n%s", variant, err, stdout.String())
		}
		if rep["upstream"] != "o/r" || rep["rankVariant"] != variant {
			t.Errorf("%s: report header %v", variant, rep)
		}
		ndcg, ok := rep["rankingNDCG"].(float64)
		if !ok || ndcg <= 0 || ndcg > 1 {
			t.Errorf("%s: rankingNDCG=%v", variant, rep["rankingNDCG"])
		}
		if _, ok := rep["rankReport"]; !ok && variant != "heat" {
			t.Errorf("%s: rankReport missing", variant)
		}
	}
}

func TestSpnForksEval_fromExport_variantChangesOrderingKey(t *testing.T) {
	isolateSpoonRun(t)
	exportPath, judgmentsPath := writeOfflineFixtures(t)
	// f1 (32, tier 1) vs f3 (30, tier 3, novel): raw heat ranks f1 first;
	// EB shrinks the noisy 45 toward the pool mean so the ordering key can
	// reorder them. The report's "ranked" list exposes the per-fork key.
	keys := map[string][]string{}
	for _, variant := range []string{"heat", "eb"} {
		var stdout, stderr bytes.Buffer
		if exit := runEvalWith([]string{"o/r", "--from-export", exportPath, "--judgments", judgmentsPath, "--rank-variant", variant, "--shortlist", "6", "--prior-scale", "2"}, &stdout, &stderr); exit != 0 {
			t.Fatalf("%s: exit=%d stderr=%s", variant, exit, stderr.String())
		}
		var rep struct {
			Ranked []struct {
				ID  string  `json:"id"`
				Key float64 `json:"key"`
			} `json:"ranked"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		for _, r := range rep.Ranked {
			keys[variant] = append(keys[variant], r.ID)
		}
	}
	if len(keys["heat"]) != 6 || keys["heat"][0] != "o/f0" || keys["heat"][1] != "o/f1" {
		t.Errorf("heat ordering %v", keys["heat"])
	}
	if strings.Join(keys["heat"], ",") == strings.Join(keys["eb"], ",") {
		t.Errorf("eb ordering should differ from heat ordering on this fixture: %v", keys["eb"])
	}
}

func TestSpnForksEval_fromExport_flagValidation(t *testing.T) {
	isolateSpoonRun(t)
	exportPath, judgmentsPath := writeOfflineFixtures(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"o/r", "--judgments", judgmentsPath, "--shortlist", "10"}, "--shortlist requires --from-export"},
		{[]string{"o/r", "--judgments", judgmentsPath, "--shortlist", "3"}, "--shortlist requires --from-export"},
		{[]string{"o/r", "--from-export", exportPath, "--judgments", judgmentsPath, "--rank-variant", "bogus"}, "--rank-variant must be one of"},
		{[]string{"o/r", "--judgments", judgmentsPath, "--rank-variant", "pscore"}, "--rank-variant requires --from-export"},
		{[]string{"o/r", "--from-export", filepath.Join(t.TempDir(), "missing.json"), "--judgments", judgmentsPath}, "read export file"},
	}
	for _, c := range cases {
		var stdout, stderr bytes.Buffer
		if exit := runEvalWith(c.args, &stdout, &stderr); exit != 2 {
			t.Errorf("%v: exit=%d want 2 (%s)", c.args, exit, stderr.String())
			continue
		}
		if !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%v: stderr %s lacks %q", c.args, stderr.String(), c.want)
		}
	}
}

func TestSpnForksEval_fromExport_heatVariantUsesSamePoolAsRankModel(t *testing.T) {
	isolateSpoonRun(t)
	dir := t.TempDir()
	n := forksops.RankPoolCap + 25
	var sb strings.Builder
	sb.WriteString(`{"forks":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"full_name":"o/f%d","heat":{"score":%d,"tier":3,"confidence":0.9}}`, i, n-i)
	}
	sb.WriteString(`]}`)
	exportPath := filepath.Join(dir, "export.json")
	judgmentsPath := filepath.Join(dir, "j.json")
	if err := os.WriteFile(exportPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(judgmentsPath, []byte(`{"forks":[{"id":"o/f0","novelty":"novel"},{"id":"o/f1","novelty":"established"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"heat", "erank"} {
		var stdout, stderr bytes.Buffer
		if exit := runEvalWith([]string{"o/r", "--from-export", exportPath, "--judgments", judgmentsPath, "--rank-variant", variant}, &stdout, &stderr); exit != 0 {
			t.Fatalf("%s: exit=%d stderr=%s", variant, exit, stderr.String())
		}
		var rep struct {
			Ranked []struct{ ID string } `json:"ranked"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		if len(rep.Ranked) != forksops.RankPoolCap {
			t.Errorf("%s: ranked rows=%d want %d", variant, len(rep.Ranked), forksops.RankPoolCap)
		}
	}
}

func TestSpnForksEvalSmallRankPoolJSON(t *testing.T) {
	isolateSpoonRun(t)
	exportPath, judgmentsPath := writeOfflineFixtures(t)
	var ex struct {
		Forks []json.RawMessage `json:"forks"`
	}
	if err := json.Unmarshal([]byte(offlineExport), &ex); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2} {
		raw, err := json.Marshal(struct {
			Forks []json.RawMessage `json:"forks"`
		}{ex.Forks[:n]})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exportPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := runEvalWith([]string{"o/r", "--from-export", exportPath, "--judgments", judgmentsPath, "--rank-variant", "erank"}, &stdout, &stderr); code != 0 {
			t.Fatalf("n=%d: exit=%d: %s", n, code, stderr.String())
		}
		var report struct {
			RankReport map[string]any `json:"rankReport"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"poth", "cpothK"} {
			if value, ok := report.RankReport[key]; !ok || value != nil {
				t.Fatalf("%s should be null: %s", key, stdout.String())
			}
		}
	}
}
