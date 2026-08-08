package tui

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/heat"
)

// The enrichment-derived divergence signals (Upstreamed, MNA, feature ratio,
// branch-work flags) used to be dropped by the cache bridge in both
// directions: forgeT2ToGHCompare had no fields to put them in, so a warm run
// within the compare TTL rehydrated every cached compare with zeros — heat
// re-scored without the upstreamed penalty or MNA component, and the export
// lost the corresponding keys via omitempty. Same cold-vs-warm drift the
// Performed gate closed, one layer up.
func TestT2CacheBridge_RoundTripsEnrichmentFields(t *testing.T) {
	orig := forge.T2Data{
		Performed:          true,
		AheadCount:         7,
		BehindCount:        3,
		MNA:                42,
		FeatureCommitRatio: 0.75,
		IsBranchWork:       true,
		ActiveBranch:       "feature/x",
		Upstreamed:         true,
		UpstreamedPR:       123,
		BaseSHA:            "base000",
		HeadSHA:            "head111",
	}

	got := ghCompareToForgeT2(forgeT2ToGHCompare(orig))

	if got.MNA != orig.MNA {
		t.Errorf("MNA = %d, want %d", got.MNA, orig.MNA)
	}
	if got.FeatureCommitRatio != orig.FeatureCommitRatio {
		t.Errorf("FeatureCommitRatio = %v, want %v", got.FeatureCommitRatio, orig.FeatureCommitRatio)
	}
	if got.IsBranchWork != orig.IsBranchWork {
		t.Errorf("IsBranchWork = %v, want %v", got.IsBranchWork, orig.IsBranchWork)
	}
	if got.ActiveBranch != orig.ActiveBranch {
		t.Errorf("ActiveBranch = %q, want %q", got.ActiveBranch, orig.ActiveBranch)
	}
	if got.Upstreamed != orig.Upstreamed {
		t.Errorf("Upstreamed = %v, want %v", got.Upstreamed, orig.Upstreamed)
	}
	if got.UpstreamedPR != orig.UpstreamedPR {
		t.Errorf("UpstreamedPR = %d, want %d", got.UpstreamedPR, orig.UpstreamedPR)
	}

	// The export must see the same values through the bridge output, since
	// forgeT2ToExportDiv reads exactly these fields.
	div := forgeT2ToExportDiv(&got)
	if div == nil || !div.Upstreamed || div.MNA != orig.MNA || div.ActiveBranch != orig.ActiveBranch {
		t.Errorf("export div lost bridged fields: %+v", div)
	}
}

// persistForkListCounts rewrites the on-disk fork list from m.forks — but
// m.forks has been ghost-filtered by scoreForks, while the list on disk was
// written pre-filter by fetchForks. Rebuilding the cache from the filtered
// slice silently evicted every ghost fork from disk on the first divergence
// sweep. The persist must merge over the stored list instead.
func TestPersistForkListCounts_PreservesGhostForks(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	parent := gh.RepoInfo{FullName: "owner/repo", DefaultBranch: "main"}
	live := forge.T1Data{ID: "live/repo", Owner: "live", Name: "repo"}
	ghost := forge.T1Data{ID: "ghost/repo", Owner: "ghost", Name: "repo"}

	// Disk state as fetchForks writes it: full, unfiltered list.
	if err := gh.SaveForkList("owner", "repo", parent,
		[]gh.ForkInfo{forgeT1ToGHForkInfo(live), forgeT1ToGHForkInfo(ghost)},
		nil); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	// Model state after scoreForks: the ghost is gone.
	n := 2
	liveWithCount := live
	liveWithCount.DivergentBranches = &n
	m := &Model{
		auth:   forge.AuthInfo{Provider: forge.ProviderGitHub},
		parent: &forge.ParentData{FullName: "owner/repo", DefaultBranch: "main"},
		forks:  []ScoredFork{{Fork: liveWithCount, Heat: heat.HeatResult{Score: 10}}},
	}
	m.persistForkListCounts()

	cache := gh.LoadCache("owner", "repo")
	if cache == nil {
		t.Fatal("cache entry vanished")
	}
	var haveGhost, haveLive bool
	for _, f := range cache.Forks {
		switch f.FullName {
		case "ghost/repo":
			haveGhost = true
		case "live/repo":
			haveLive = true
		}
	}
	if !haveGhost {
		t.Error("ghost fork was evicted from the cached fork list by the sweep persist")
	}
	if !haveLive {
		t.Error("live fork missing from the cached fork list")
	}
	// And the sweep's count actually landed for the live fork.
	extra, ok := cache.T1Extras["live/repo"]
	if !ok || extra.DivergentBranches == nil || *extra.DivergentBranches != n {
		t.Errorf("live fork's divergent-branch count not persisted: %+v", extra)
	}
}
