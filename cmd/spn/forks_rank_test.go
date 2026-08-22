// Tests for cmd/spn JSON emission of the shortlist rank summary
// (expectedRank, pScore, pTopK, pFirst, rankLo, rankHi). Contract: fields
// appear only when Stream computed them (Result.Rank != nil); the legacy
// expectedRank/rankConfidence pair keeps emitting alongside.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
)

func TestForkToJSON_EmitsRankStats(t *testing.T) {
	r := forksops.Result{
		ExpectedRank:   1.25,
		RankConfidence: 0.9,
		Rank: &forksops.RankStats{
			ExpectedRank: 1.25, PScore: 0.9375, PTopK: 0.98, PFirst: 0.8, Lo: 1, Hi: 2, TieBand: true,
		},
	}
	out := forkToJSON(r)
	want := map[string]any{
		"tieBand":        true,
		"expectedRank":   1.25,
		"rankConfidence": 0.9,
		"pScore":         0.9375,
		"pTopK":          0.98,
		"pFirst":         0.8,
		"rankLo":         1,
		"rankHi":         2,
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s: got %#v want %#v", k, out[k], v)
		}
	}
}

func TestForkToJSON_OmitsRankStats_WhenNotComputed(t *testing.T) {
	out := forkToJSON(forksops.Result{})
	for _, k := range []string{"expectedRank", "rankConfidence", "pScore", "pTopK", "pFirst", "rankLo", "rankHi", "tieBand"} {
		if _, ok := out[k]; ok {
			t.Errorf("%s should be omitted without a shortlist, got %#v", k, out[k])
		}
	}
}

func TestSpnForksList_shortlistRule_rejectsUnknownValue(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream",
		"--shortlist", "3",
		"--shortlist-rule", "bogus",
	}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	var env map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr is not a JSON error envelope: %v\n%s", err, stderr.String())
	}
	e, _ := env["error"].(map[string]any)
	if e == nil || e["code"] != "bad_input" {
		t.Errorf("expected bad_input error, got: %s", stderr.String())
	}
	if msg, _ := e["message"].(string); !strings.Contains(msg, "--shortlist-rule must be expected or membership") {
		t.Errorf("unexpected message: %s", stderr.String())
	}
}

func TestSpnForksList_shortlistRule_requiresShortlist(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream",
		"--shortlist-rule", "membership",
	}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--shortlist-rule requires --shortlist") {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}
}

func rankReportDetails(t *testing.T, stderr string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if line == "" {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		info, _ := env["info"].(map[string]any)
		if info == nil || info["code"] != "rank_report" {
			continue
		}
		d, _ := info["details"].(map[string]any)
		out = append(out, d)
	}
	return out
}

func shortlistFakeProvider() func(context.Context, string, string, string) (forge.Forge, string, *agentio.Error) {
	now := time.Now()
	return func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
				{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
				{ID: "o/c", Owner: "o", Name: "c", DefaultBranch: "main", PushedAt: now},
				{ID: "o/d", Owner: "o", Name: "d", DefaultBranch: "main", PushedAt: now},
			},
			t2: map[string]forge.T2Data{
				"o/a": {AheadCount: 1, MNA: 500},
				"o/b": {AheadCount: 1, MNA: 5},
				"o/c": {AheadCount: 1, MNA: 50},
				"o/d": {AheadCount: 1, MNA: 1},
			},
		}, "o/r", nil
	}
}

func TestSpnForksList_shortlistEmitsRankReport_NDJSON(t *testing.T) {
	isolateSpoonRun(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = shortlistFakeProvider()

	var stdout, stderr bytes.Buffer
	if exit := runForksWith([]string{"list", "o/r", "--tier", "2", "--no-cluster", "--shortlist", "3", "--rank-diagnostics"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	reports := rankReportDetails(t, stderr.String())
	if len(reports) != 1 {
		t.Fatalf("rank reports = %d, want 1: %s", len(reports), stderr.String())
	}
	if reports[0]["poolSize"] != float64(4) || reports[0]["shortlistN"] != float64(3) || reports[0]["shortlistRule"] != "expected" {
		t.Errorf("report details: %v", reports[0])
	}
	if _, ok := reports[0]["poth"].(float64); !ok {
		t.Errorf("poth missing or not numeric: %v", reports[0])
	}
	if strings.Contains(stdout.String(), "rank_report") {
		t.Errorf("stdout must not carry the report: %s", stdout.String())
	}
	// Records carry pothResidual under --rank-diagnostics.
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad record %q: %v", line, err)
		}
		if _, ok := rec["pothResidual"]; !ok {
			t.Errorf("record missing pothResidual: %s", line)
		}
	}
}

func TestSpnForksList_shortlistCSV_AddsRankColumnsAndReport(t *testing.T) {
	isolateSpoonRun(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = shortlistFakeProvider()

	var stdout, stderr bytes.Buffer
	if exit := runForksWith([]string{"list", "o/r", "--tier", "2", "--no-cluster", "--shortlist", "3", "--csv"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	header := strings.SplitN(stdout.String(), "\n", 2)[0]
	for _, col := range []string{"expected_rank", "p_score", "p_top_k", "p_first", "rank_lo", "rank_hi"} {
		if !strings.Contains(header, col) {
			t.Errorf("CSV header missing %s: %s", col, header)
		}
	}
	if len(rankReportDetails(t, stderr.String())) != 1 {
		t.Errorf("CSV run should emit one rank_report: %s", stderr.String())
	}
}

func TestSpnForksList_noShortlist_NoRankReport(t *testing.T) {
	isolateSpoonRun(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = shortlistFakeProvider()
	var stdout, stderr bytes.Buffer
	if exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if n := len(rankReportDetails(t, stderr.String())); n != 0 {
		t.Errorf("rank reports = %d, want 0", n)
	}
}

func TestSpnForksList_rankDiagnostics_requiresShortlist(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if exit := runForksWith([]string{"list", "up/stream", "--rank-diagnostics"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--rank-diagnostics requires --shortlist") {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}
}

func TestSpnForksList_priorScale_requiresEB(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if exit := runForksWith([]string{"list", "up/stream", "--shortlist", "3", "--prior-scale", "5"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--prior-scale requires --eb") {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}
}

func TestSpnForksList_eb_requiresShortlistAndPositiveScale(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if exit := runForksWith([]string{"list", "up/stream", "--eb"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--eb requires --shortlist") {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exit := runForksWith([]string{"list", "up/stream", "--shortlist", "3", "--eb", "--prior-scale", "-1"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--prior-scale must be a positive number") {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}
}

func TestSpnForksList_ebEmitsFieldsAndReport(t *testing.T) {
	isolateSpoonRun(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	now := time.Now()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		ff := &fakeForge{parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)}, t2: map[string]forge.T2Data{}}
		for i, mna := range []int{500, 5, 50, 1, 120, 30} {
			id := "o/f" + string(rune('a'+i))
			ff.forks = append(ff.forks, forge.T1Data{ID: id, Owner: "o", Name: id[2:], DefaultBranch: "main", PushedAt: now})
			ff.t2[id] = forge.T2Data{AheadCount: 1, MNA: mna}
		}
		return ff, "o/r", nil
	}
	var stdout, stderr bytes.Buffer
	if exit := runForksWith([]string{"list", "o/r", "--tier", "2", "--no-cluster", "--shortlist", "6", "--eb", "--prior-scale", "100"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	reports := rankReportDetails(t, stderr.String())
	if len(reports) != 1 {
		t.Fatalf("rank reports = %d: %s", len(reports), stderr.String())
	}
	for _, k := range []string{"ebRegime", "tauHat", "ebMean", "ebPool", "dBarOverK", "pD", "priorScale"} {
		if _, ok := reports[0][k]; !ok {
			t.Errorf("rank_report missing %s: %v", k, reports[0])
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad record %q: %v", line, err)
		}
		for _, k := range []string{"ebTheta", "ebSigma", "ebResidual", "ebLeverage", "ebFlag"} {
			if _, ok := rec[k]; !ok {
				t.Errorf("record missing %s: %s", k, line)
			}
		}
	}
}
