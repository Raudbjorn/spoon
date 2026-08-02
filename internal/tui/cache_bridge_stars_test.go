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
