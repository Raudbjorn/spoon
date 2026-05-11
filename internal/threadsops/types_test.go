package threadsops

import (
	"sort"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

// fourThreads builds the canonical 2x2 (resolved x outdated) fixture used by
// both the helper test and the binary-level integration tests.
func fourThreads() []ReviewThreadWithPolicy {
	return AnnotateWithPolicy([]github.ReviewThread{
		{ID: "T_uu", IsResolved: false, IsOutdated: false,
			Comments: []github.ThreadComment{{CreatedAt: "2026-05-01T00:00:00Z", AuthorType: "User", Author: "a"}}},
		{ID: "T_uo", IsResolved: false, IsOutdated: true,
			Comments: []github.ThreadComment{{CreatedAt: "2026-05-02T00:00:00Z", AuthorType: "User", Author: "a"}}},
		{ID: "T_ra", IsResolved: true, IsOutdated: false,
			Comments: []github.ThreadComment{{CreatedAt: "2026-05-03T00:00:00Z", AuthorType: "Bot"}}},
		{ID: "T_ro", IsResolved: true, IsOutdated: true,
			Comments: []github.ThreadComment{{CreatedAt: "2026-05-04T00:00:00Z", AuthorType: "Bot"}}},
	})
}

func idsOf(ts []ReviewThreadWithPolicy) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	sort.Strings(out)
	return out
}

func TestFilter_BehaviorTable(t *testing.T) {
	in := fourThreads()
	cases := []struct {
		mode FilterMode
		want []string
	}{
		{FilterAll, []string{"T_ra", "T_ro", "T_uo", "T_uu"}},
		{FilterUnresolved, []string{"T_uo", "T_uu"}},
		{FilterResolvedActive, []string{"T_ra"}},
		{FilterUnresolvedOutdated, []string{"T_uo"}},
		{FilterCurrentUnresolved, []string{"T_uu"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			got := idsOf(Filter(in, tc.mode))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("mode=%s got %v want %v", tc.mode, got, tc.want)
			}
		})
	}
}

func TestParseFilterMode_Defaults(t *testing.T) {
	m, err := ParseFilterMode("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != FilterUnresolved {
		t.Errorf("default = %q, want %q", m, FilterUnresolved)
	}
}

func TestParseFilterMode_AllValidValues(t *testing.T) {
	for _, m := range ValidFilterModes {
		got, err := ParseFilterMode(string(m))
		if err != nil {
			t.Errorf("ParseFilterMode(%q) err=%v", m, err)
		}
		if got != m {
			t.Errorf("ParseFilterMode(%q) = %q", m, got)
		}
	}
}

func TestParseFilterMode_Unknown(t *testing.T) {
	_, err := ParseFilterMode("bogus")
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
	// Must enumerate valid choices so callers can echo to the user.
	msg := err.Error()
	for _, m := range ValidFilterModes {
		if !strings.Contains(msg, string(m)) {
			t.Errorf("error message missing %q: %s", m, msg)
		}
	}
}

func TestNeedsResolvedFetch(t *testing.T) {
	cases := map[FilterMode]bool{
		FilterAll:                true,
		FilterResolvedActive:     true,
		FilterUnresolved:         false,
		FilterCurrentUnresolved:  false,
		FilterUnresolvedOutdated: false,
	}
	for m, want := range cases {
		if got := m.NeedsResolvedFetch(); got != want {
			t.Errorf("%s.NeedsResolvedFetch() = %v want %v", m, got, want)
		}
	}
}

func TestSortThreadsForList_StableByTimeThenID(t *testing.T) {
	in := AnnotateWithPolicy([]github.ReviewThread{
		{ID: "z", Comments: []github.ThreadComment{{CreatedAt: "2026-05-10T10:00:00Z"}}},
		{ID: "a", Comments: []github.ThreadComment{{CreatedAt: "2026-05-10T10:00:00Z"}}},
		{ID: "m", Comments: []github.ThreadComment{{CreatedAt: "2026-05-09T10:00:00Z"}}},
	})
	SortThreadsForList(in)
	gotIDs := []string{in[0].ID, in[1].ID, in[2].ID}
	want := []string{"m", "a", "z"}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Errorf("position %d: got %q want %q (full: %v)", i, gotIDs[i], want[i], gotIDs)
		}
	}
}
