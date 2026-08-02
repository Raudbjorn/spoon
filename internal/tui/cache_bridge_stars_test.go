package tui

import (
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// The all-zeros bug report also flagged the ★ column. Stars turned out to be
// correct there only because that repo's forks genuinely have none, which
// proves nothing about repos whose forks are starred. Pin the round trip so
// the ★ column is trustworthy at any count, and so a future edit to
// forgeT1ToGHForkInfo / ghForkInfoToForge cannot drop the field silently.
func TestT1CacheRoundTrip_PreservesStarsAtAnyCount(t *testing.T) {
	for _, stars := range []int{0, 1, 7, 4231} {
		in := forge.T1Data{
			ID:            "owner/repo",
			Owner:         "owner",
			Name:          "repo",
			DefaultBranch: "main",
			Stars:         stars,
			SubForkCount:  3,
			OpenIssues:    2,
			PushedAt:      time.Now().UTC().Truncate(time.Second),
			CreatedAt:     time.Now().UTC().Truncate(time.Second),
		}

		out := ghForkInfoToForge(forgeT1ToGHForkInfo(in), nil, "parent/repo")

		if out.Stars != stars {
			t.Errorf("stars %d survived the cache round trip as %d", stars, out.Stars)
		}
		if out.SubForkCount != in.SubForkCount {
			t.Errorf("sub-fork count %d survived as %d", in.SubForkCount, out.SubForkCount)
		}
		if out.OpenIssues != in.OpenIssues {
			t.Errorf("open issues %d survived as %d", in.OpenIssues, out.OpenIssues)
		}
	}
}

// The sibling key degrades from an exact SHA to the fuzzy diff shape if the
// cache loses HeadSHA, so a cold run and a warm run would group differently.
// Same class of bug as the Performed loss.
func TestT2CacheRoundTrip_PreservesIdentitySHAs(t *testing.T) {
	in := forge.T2Data{
		Performed:   true,
		AheadCount:  10,
		BehindCount: 356,
		BaseSHA:     "bbb07e08cd16",
		HeadSHA:     "57e2f4aecb48",
	}

	out := ghCompareToForgeT2(forgeT2ToGHCompare(in))

	if out.BaseSHA != in.BaseSHA {
		t.Errorf("BaseSHA %q survived as %q", in.BaseSHA, out.BaseSHA)
	}
	if out.HeadSHA != in.HeadSHA {
		t.Errorf("HeadSHA %q survived as %q — duplicate grouping would fall back to diff shape",
			in.HeadSHA, out.HeadSHA)
	}
	if !out.Performed {
		t.Error("Performed did not survive the round trip")
	}
}

// A fork's divergent-work fingerprint must survive so a warm run neither
// re-sweeps nor loses its duplicate grouping.
func TestT1CacheRoundTrip_PreservesBranchFingerprint(t *testing.T) {
	n := 3
	in := forge.T1Data{
		ID:                "emtee40/nonraid",
		Owner:             "emtee40",
		Name:              "nonraid",
		BranchFingerprint: "a1b2c3d4e5f60718",
		DivergentBranches: &n,
	}

	extra := forgeT1ToGHExtra(in)
	out := ghForkInfoToForge(forgeT1ToGHForkInfo(in), &extra, "qvr/nonraid")

	if out.BranchFingerprint != in.BranchFingerprint {
		t.Errorf("fingerprint %q survived as %q", in.BranchFingerprint, out.BranchFingerprint)
	}
	if out.DivergentBranches == nil || *out.DivergentBranches != n {
		t.Errorf("DivergentBranches = %v, want %d", out.DivergentBranches, n)
	}
}
