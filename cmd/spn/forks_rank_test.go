// Tests for cmd/spn JSON emission of the shortlist rank summary
// (expectedRank, pScore, pTopK, pFirst, rankLo, rankHi). Contract: fields
// appear only when Stream computed them (Result.Rank != nil); the legacy
// expectedRank/rankConfidence pair keeps emitting alongside.
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forksops"
)

func TestForkToJSON_EmitsRankStats(t *testing.T) {
	r := forksops.Result{
		ExpectedRank:   1.25,
		RankConfidence: 0.9,
		Rank: &forksops.RankStats{
			ExpectedRank: 1.25, PScore: 0.9375, PTopK: 0.98, PFirst: 0.8, Lo: 1, Hi: 2,
		},
	}
	out := forkToJSON(r)
	want := map[string]any{
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
	for _, k := range []string{"expectedRank", "rankConfidence", "pScore", "pTopK", "pFirst", "rankLo", "rankHi"} {
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
