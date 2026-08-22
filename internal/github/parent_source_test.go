package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepoInfoUnmarshalsParentAndSource(t *testing.T) {
	raw := []byte(`{
		"full_name":"mid/child","default_branch":"dev","fork":true,
		"parent":{"full_name":"mid/child","default_branch":"dev"},
		"source":{"full_name":"root/orig","default_branch":"main"}
	}`)
	var info RepoInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	if info.Source == nil || info.Source.FullName != "root/orig" || info.Source.DefaultBranch != "main" {
		t.Fatalf("source: %+v", info.Source)
	}
}

func TestRepoCompareBaseline(t *testing.T) {
	root := RepoInfo{DefaultBranch: "main", Fork: false}
	o, r, b := repoCompareBaseline(root, "root", "orig")
	if o != "root" || r != "orig" || b != "main" {
		t.Fatalf("non-fork: %s/%s %s", o, r, b)
	}

	mid := RepoInfo{
		DefaultBranch: "dev", Fork: true,
		Source: &RepoRef{FullName: "root/orig", DefaultBranch: "main"},
	}
	o, r, b = repoCompareBaseline(mid, "mid", "child")
	if o != "root" || r != "orig" || b != "main" {
		t.Fatalf("mid-chain: %s/%s %s", o, r, b)
	}

	missing := RepoInfo{DefaultBranch: "dev", Fork: true}
	o, r, b = repoCompareBaseline(missing, "mid", "child")
	if o != "mid" || r != "child" || b != "dev" {
		t.Fatalf("fork without source must no-op: %s/%s %s", o, r, b)
	}

	bad := RepoInfo{DefaultBranch: "dev", Fork: true, Source: &RepoRef{FullName: "noslash"}}
	o, r, b = repoCompareBaseline(bad, "mid", "child")
	if o != "mid" || r != "child" {
		t.Fatalf("malformed source must no-op: %s/%s", o, r)
	}
}

func TestGHProviderParent_MidChainLatchesSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/mid/child") {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"full_name": "mid/child", "default_branch": "dev", "fork": true,
			"html_url": "https://github.com/mid/child",
			"source":   map[string]string{"full_name": "root/orig", "default_branch": "main"},
		})
	}))
	defer srv.Close()
	p := NewGHProvider(newTestClient(t, srv), AuthStatus{})
	got, err := p.Parent(context.Background(), "mid", "child")
	if err != nil {
		t.Fatal(err)
	}
	if got.FullName != "mid/child" || got.DefaultBranch != "dev" {
		t.Fatalf("display parent must stay the seed: %+v", got)
	}
	if got.SourceFullPath != "root/orig" || got.SourceDefaultBranch != "main" {
		t.Fatalf("source fields: %+v", got)
	}
	if p.sourceOwner != "root" || p.sourceRepo != "orig" || p.sourceDefaultBranch != "main" {
		t.Fatalf("latched %s/%s %s", p.sourceOwner, p.sourceRepo, p.sourceDefaultBranch)
	}
}

func TestGHProviderParent_NonForkLeavesSourceEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"full_name": "root/orig", "default_branch": "main", "fork": false,
			"html_url": "https://github.com/root/orig",
		})
	}))
	defer srv.Close()
	p := NewGHProvider(newTestClient(t, srv), AuthStatus{})
	got, err := p.Parent(context.Background(), "root", "orig")
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceFullPath != "" || p.sourceOwner != "root" || p.sourceRepo != "orig" {
		t.Fatalf("non-fork: parent=%+v latch=%s/%s", got, p.sourceOwner, p.sourceRepo)
	}
}

func TestForkInfoToT1_MarksForkOfForkWhenSourceDiffers(t *testing.T) {
	t1 := forkInfoToT1(ForkInfo{FullName: "a/f", Name: "f", Owner: OwnerInfo{Login: "a"}}, nil, "mid/child", "root/orig")
	if t1.ParentFullPath != "mid/child" || t1.SourceFullPath != "root/orig" || !t1.IsForkOfFork {
		t.Fatalf("%+v", t1)
	}
	t1 = forkInfoToT1(ForkInfo{FullName: "a/f", Name: "f", Owner: OwnerInfo{Login: "a"}}, nil, "root/orig", "")
	if t1.SourceFullPath != "root/orig" || t1.IsForkOfFork {
		t.Fatalf("empty source must collapse to parent: %+v", t1)
	}
}
