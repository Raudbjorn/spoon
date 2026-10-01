package forge

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCompareURL_emptyHostFallsBackToCanonicalHost(t *testing.T) {
	// Regression: AuthStatus.Host was never populated on the GitHub path, so
	// CompareURL interpolated an empty host and produced "https:///owner/repo/
	// compare/..." — a dead link. Every exported fork row carried one. An empty
	// host must degrade to the provider's canonical host instead.
	tests := []struct {
		name     string
		provider Provider
		host     string
		want     string
	}{
		{
			"github with empty host",
			ProviderGitHub, "",
			"https://github.com/qvr/nonraid/compare/main...Raudbjorn:main",
		},
		{
			"gitlab with empty host",
			ProviderGitLab, "",
			"https://gitlab.com/qvr/nonraid/-/compare/main...Raudbjorn:main",
		},
		{
			"gitea with empty host",
			ProviderGitea, "",
			"https://codeberg.org/qvr/nonraid/compare/main...Raudbjorn:main",
		},
		{
			"explicit host is preserved",
			ProviderGitHub, "github.example.com",
			"https://github.example.com/qvr/nonraid/compare/main...Raudbjorn:main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompareURL(tt.provider, tt.host, "qvr/nonraid", "main", "Raudbjorn", "main")
			if got != tt.want {
				t.Errorf("CompareURL() = %q, want %q", got, tt.want)
			}
			if strings.HasPrefix(got, "https:///") {
				t.Errorf("CompareURL() produced a hostless URL: %q", got)
			}
		})
	}
}

func TestDefaultHost(t *testing.T) {
	if got := DefaultHost(ProviderGitLab); got != "gitlab.com" {
		t.Errorf("DefaultHost(GitLab) = %q, want gitlab.com", got)
	}
	if got := DefaultHost(ProviderGitHub); got != "github.com" {
		t.Errorf("DefaultHost(GitHub) = %q, want github.com", got)
	}
	// Gitea has no single canonical instance; codeberg.org matches what
	// persistForkSnapshot already assumed for a hostless Gitea caller.
	if got := DefaultHost(ProviderGitea); got != "codeberg.org" {
		t.Errorf("DefaultHost(Gitea) = %q, want codeberg.org", got)
	}
}

// ValidUTF8 must replace per byte, like encoding/json, so a raw-diff path and
// the same path decoded from a REST response compare equal.
func TestValidUTF8MatchesJSONDecoding(t *testing.T) {
	for _, in := range []string{"", "plain", "café", "a\xffb", "a\xff\xfeb", "\xe2\x82", "ok\xc3"} {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var viaJSON string
		if err := json.Unmarshal(raw, &viaJSON); err != nil {
			t.Fatal(err)
		}
		if got := ValidUTF8(in); got != viaJSON {
			t.Errorf("ValidUTF8(%q) = %q, want %q (encoding/json)", in, got, viaJSON)
		}
	}
}

// inheritedSample mirrors testdata/inherited_branches_sample.json: 104 real
// ggml-org/llama.cpp forks with side branches, from a random 400-fork sample
// taken live on 2026-09-28. tip_matches_upstream records whether the branch
// tip equalled any of upstream's 814 branch tips at sampling time.
type inheritedSample struct {
	Forks []struct {
		Fork      string `json:"fork"`
		CreatedAt string `json:"created_at"`
		Branches  []struct {
			Name               string `json:"name"`
			CommittedDate      string `json:"committed_date"`
			TipMatchesUpstream bool   `json:"tip_matches_upstream"`
		} `json:"branches"`
	} `json:"forks"`
}

// PostForkBranches drops exactly the side branches dated before their fork,
// which on the real sample includes every branch that was a copy of an
// upstream branch tip.
func TestPostForkBranches_RealLlamaCppSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/inherited_branches_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var sample inheritedSample
	if err := json.Unmarshal(raw, &sample); err != nil {
		t.Fatal(err)
	}
	parse := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return v
	}
	var total, kept, upstreamCopiesKept int
	for _, sf := range sample.Forks {
		f := T1Data{ID: sf.Fork, CreatedAt: parse(sf.CreatedAt)}
		upstreamTip := map[string]bool{}
		for _, b := range sf.Branches {
			f.Branches = append(f.Branches, BranchRef{Name: b.Name, CommittedDate: parse(b.CommittedDate)})
			upstreamTip[b.Name] = b.TipMatchesUpstream
		}
		got := PostForkBranches(f)
		for _, b := range got {
			if b.CommittedDate.Before(f.CreatedAt) {
				t.Errorf("%s: kept %s dated %s, before fork creation %s", sf.Fork, b.Name, b.CommittedDate, f.CreatedAt)
			}
			if upstreamTip[b.Name] {
				upstreamCopiesKept++
			}
		}
		total += len(f.Branches)
		kept += len(got)
	}
	if len(sample.Forks) != 104 || total != 343 {
		t.Fatalf("fixture has %d forks / %d branches, want 104 / 343", len(sample.Forks), total)
	}
	if kept != 119 {
		t.Errorf("kept %d of %d side branches, want 119 (224 predate their fork)", kept, total)
	}
	if upstreamCopiesKept != 0 {
		t.Errorf("kept %d branches whose tip was an upstream branch tip, want 0", upstreamCopiesKept)
	}
}

func TestPostForkBranches_UnknownDatesKept(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := created.Add(-time.Hour)
	branches := []BranchRef{{Name: "undated"}, {Name: "old", CommittedDate: old}}
	if got := PostForkBranches(T1Data{Branches: branches}); len(got) != 2 {
		t.Errorf("fork with unknown creation date: kept %d, want all 2", len(got))
	}
	got := PostForkBranches(T1Data{CreatedAt: created, Branches: branches})
	if len(got) != 1 || got[0].Name != "undated" {
		t.Errorf("kept %+v, want only the undated branch", got)
	}
}

func TestHasBranchInventory(t *testing.T) {
	if HasBranchInventory(T1Data{}) {
		t.Error("a row with no tip SHA (REST listing) has no branch inventory")
	}
	if !HasBranchInventory(T1Data{DefaultTipSHA: "abc"}) {
		t.Error("a GraphQL-listed row carries its tip SHA and a real branch inventory")
	}
}
