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

// TestStripVerboseFields_ClearsTimestamps verifies the helper zeroes every
// per-comment verbose field across every thread, so the `omitempty` JSON tags
// suppress them when the user did not pass --verbose.
func TestStripVerboseFields_ClearsTimestamps(t *testing.T) {
	in := AnnotateWithPolicy([]github.ReviewThread{
		{ID: "T1", Comments: []github.ThreadComment{
			{ID: "c1", CreatedAt: "2026-05-01T00:00:00Z", UpdatedAt: "2026-05-02T00:00:00Z", AuthorURL: "https://github.com/a"},
			{ID: "c2", CreatedAt: "2026-05-03T00:00:00Z", UpdatedAt: "2026-05-04T00:00:00Z", AuthorURL: "https://github.com/b"},
		}},
		{ID: "T2", Comments: []github.ThreadComment{
			{ID: "c3", CreatedAt: "2026-05-05T00:00:00Z", UpdatedAt: "2026-05-06T00:00:00Z", AuthorURL: "https://github.com/c"},
		}},
	})
	StripVerboseFields(in)
	for i, th := range in {
		for j, c := range th.Comments {
			if c.CreatedAt != "" || c.UpdatedAt != "" || c.AuthorURL != "" {
				t.Errorf("thread[%d].Comments[%d] still has verbose fields: %+v", i, j, c)
			}
		}
	}
}

// TestStripVerboseFields_NoOpOnEmpty verifies that the helper is safe to call
// when no verbose fields are populated (the input is unchanged).
func TestStripVerboseFields_NoOpOnEmpty(t *testing.T) {
	in := AnnotateWithPolicy([]github.ReviewThread{
		{ID: "T1", Comments: []github.ThreadComment{
			{ID: "c1", Author: "alice", AuthorType: "User", Body: "hi"},
		}},
	})
	// Snapshot non-verbose fields to ensure they survive.
	wantID := in[0].Comments[0].ID
	wantAuthor := in[0].Comments[0].Author
	wantBody := in[0].Comments[0].Body
	StripVerboseFields(in)
	c := in[0].Comments[0]
	if c.CreatedAt != "" || c.UpdatedAt != "" || c.AuthorURL != "" {
		t.Errorf("verbose fields should remain empty, got %+v", c)
	}
	if c.ID != wantID || c.Author != wantAuthor || c.Body != wantBody {
		t.Errorf("non-verbose fields changed: got %+v want id=%q author=%q body=%q", c, wantID, wantAuthor, wantBody)
	}
}

// TestStripVerboseFieldsOne covers the single-thread helper used by the next /
// resolve verbs.
func TestStripVerboseFieldsOne(t *testing.T) {
	one := &ReviewThreadWithPolicy{
		ReviewThread: github.ReviewThread{ID: "T1", Comments: []github.ThreadComment{
			{ID: "c1", CreatedAt: "2026-05-01T00:00:00Z", UpdatedAt: "2026-05-02T00:00:00Z", AuthorURL: "https://github.com/a"},
		}},
	}
	StripVerboseFieldsOne(one)
	c := one.Comments[0]
	if c.CreatedAt != "" || c.UpdatedAt != "" || c.AuthorURL != "" {
		t.Errorf("expected verbose fields cleared, got %+v", c)
	}
	// Nil safety.
	StripVerboseFieldsOne(nil)
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
