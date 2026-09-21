package tui

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// badgeDetail renders the detail body for one fork with no heat or T2 data, so
// the only relationship text in it comes from the row and badge block.
func badgeDetail(fork forge.T1Data) string {
	m := newTestModel()
	m.forks = []ScoredFork{{Fork: fork}}
	m.cursor = 0
	return m.detailBody()
}

// OpenPRCount comes from the fork's own GraphQL pullRequests(states: OPEN), so
// these are PRs targeting the fork, never PRs the fork opened against upstream.
func TestDetailOpenPRBadgeNamesTheFork(t *testing.T) {
	body := badgeDetail(forge.T1Data{ID: "o/f", OpenPRCount: 2})
	if !strings.Contains(body, "2 open PR(s) on this fork") {
		t.Errorf("detail missing open-PR badge:\n%s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "open PR") && strings.Contains(line, "upstream") {
			t.Errorf("open-PR badge claims an upstream target: %q", line)
		}
	}
}

// SubForkCount counts descendants, not ancestry: the "N forks" row already
// reports it, so it must not also be labelled "Fork of fork".
func TestDetailNoAncestryBadge(t *testing.T) {
	cases := []struct {
		name string
		fork forge.T1Data
	}{
		{"direct child with forks of its own", forge.T1Data{ID: "o/f", SubForkCount: 3, DepthFromRoot: 1, ParentFullPath: "owner/parent"}},
		// IsForkOfFork is `seed != root` on the REST fallback, so it is true for
		// every fork there; only a known depth may drive the badge.
		{"unknown depth despite IsForkOfFork", forge.T1Data{ID: "o/f", IsForkOfFork: true, ParentFullPath: "owner/parent"}},
		{"deep fork with unknown parent", forge.T1Data{ID: "o/f", DepthFromRoot: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := badgeDetail(tc.fork)
			if strings.Contains(body, "Fork of") {
				t.Errorf("unexpected ancestry badge:\n%s", body)
			}
		})
	}
}

func TestDetailNoAncestryBadgeKeepsForkCountRow(t *testing.T) {
	body := badgeDetail(forge.T1Data{ID: "o/f", SubForkCount: 3, DepthFromRoot: 1})
	if !strings.Contains(body, "3 forks") {
		t.Errorf("detail lost the descendant count row:\n%s", body)
	}
}

func TestDetailAncestryBadgeForDeepFork(t *testing.T) {
	body := badgeDetail(forge.T1Data{ID: "o/f", DepthFromRoot: 2, ParentFullPath: "owner/parent"})
	if !strings.Contains(body, "Fork of owner/parent") {
		t.Errorf("detail missing ancestry badge:\n%s", body)
	}
}

func TestDetailKeepsReleaseBadge(t *testing.T) {
	body := badgeDetail(forge.T1Data{ID: "o/f", ReleaseCount: 4})
	if !strings.Contains(body, "4 release(s)") {
		t.Errorf("detail missing release badge:\n%s", body)
	}
}

func TestDetailZeroCountsShowNoBadges(t *testing.T) {
	body := badgeDetail(forge.T1Data{ID: "o/f"})
	for _, banned := range []string{"open PR", "release(s)", "Fork of"} {
		if strings.Contains(body, banned) {
			t.Errorf("badge %q rendered for a fork with no counts:\n%s", banned, body)
		}
	}
}
