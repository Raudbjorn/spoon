// internal/threadsops/integration_test.go
//
// Library-level integration tests that compose multiple threadsops helpers
// together — e.g. PopulateSuggestions → Filter, Filter → FetchCodeContext,
// StripVerboseFields preserves CodeContext. These don't exercise the CLI but
// catch bugs in the contracts between helpers that per-feature tests can miss.
package threadsops

import (
	"context"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

// TestIntegration_PopulateSuggestionsThenFilter feeds threads with suggestion
// blocks, populates them, then runs Filter for various modes. The invariant
// being checked: parsed Suggestions survive the Filter pass (Filter doesn't
// strip them — it only includes/excludes whole threads).
func TestIntegration_PopulateSuggestionsThenFilter(t *testing.T) {
	threads := []ReviewThreadWithPolicy{
		{
			ReviewThread: github.ReviewThread{
				ID: "T_unres", IsResolved: false, IsOutdated: false,
				Path: "a.go", Line: 3,
				Comments: []github.ThreadComment{{
					ID:   "PRC_1",
					Body: "```suggestion\nNEW1\n```",
				}},
			},
			RequiresBody: false,
		},
		{
			ReviewThread: github.ReviewThread{
				ID: "T_unres_outdated", IsResolved: false, IsOutdated: true,
				Path: "a.go", Line: 5,
				Comments: []github.ThreadComment{{
					ID:   "PRC_2",
					Body: "use this:\n\n```suggestion\nNEW2\n```",
				}},
			},
		},
		{
			ReviewThread: github.ReviewThread{
				ID: "T_resolved", IsResolved: true, IsOutdated: false,
				Path: "a.go", Line: 7,
				Comments: []github.ThreadComment{{
					ID:   "PRC_3",
					Body: "```suggestion\nNEW3\n```",
				}},
			},
		},
	}
	PopulateSuggestions(threads)

	// Sanity: every thread has exactly one parsed suggestion before filtering.
	for _, th := range threads {
		if len(th.Suggestions) != 1 {
			t.Fatalf("setup: thread %s expected 1 suggestion, got %d", th.ID, len(th.Suggestions))
		}
	}

	cases := []struct {
		mode     FilterMode
		wantIDs  []string
		wantSugs map[string]string // id → expected suggestion body
	}{
		{FilterAll, []string{"T_unres", "T_unres_outdated", "T_resolved"},
			map[string]string{"T_unres": "NEW1", "T_unres_outdated": "NEW2", "T_resolved": "NEW3"}},
		{FilterUnresolved, []string{"T_unres", "T_unres_outdated"},
			map[string]string{"T_unres": "NEW1", "T_unres_outdated": "NEW2"}},
		{FilterUnresolvedOutdated, []string{"T_unres_outdated"},
			map[string]string{"T_unres_outdated": "NEW2"}},
		{FilterCurrentUnresolved, []string{"T_unres"},
			map[string]string{"T_unres": "NEW1"}},
		{FilterResolvedActive, []string{"T_resolved"},
			map[string]string{"T_resolved": "NEW3"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			got := Filter(threads, tc.mode)
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("len=%d want %d (ids: %+v)", len(got), len(tc.wantIDs), got)
			}
			for _, th := range got {
				if len(th.Suggestions) != 1 {
					t.Errorf("thread %s lost its suggestion: %+v", th.ID, th.Suggestions)
					continue
				}
				wantBody, ok := tc.wantSugs[th.ID]
				if !ok {
					t.Errorf("unexpected thread %s in filter %s output", th.ID, tc.mode)
					continue
				}
				if th.Suggestions[0].Body != wantBody {
					t.Errorf("thread %s suggestion body=%q want %q",
						th.ID, th.Suggestions[0].Body, wantBody)
				}
			}
		})
	}
}

// TestIntegration_FetchCodeContextAfterFilter filters to outdated threads,
// then fetches code context for each. Verifies that the IsOutdated flag flows
// through into CodeContext.Outdated for every result.
func TestIntegration_FetchCodeContextAfterFilter(t *testing.T) {
	all := []ReviewThreadWithPolicy{
		{ReviewThread: github.ReviewThread{
			ID: "T_current", IsResolved: false, IsOutdated: false,
			Path: "a.go", Line: 3,
		}},
		{ReviewThread: github.ReviewThread{
			ID: "T_outdated_a", IsResolved: false, IsOutdated: true,
			Path: "a.go", Line: 5,
		}},
		{ReviewThread: github.ReviewThread{
			ID: "T_outdated_b", IsResolved: false, IsOutdated: true,
			Path: "a.go", Line: 7,
		}},
	}
	filtered := Filter(all, FilterUnresolvedOutdated)
	if len(filtered) != 2 {
		t.Fatalf("filter outdated → %d threads, want 2", len(filtered))
	}

	fetcher := &stubFetcher{content: tenLineFile()}
	for i := range filtered {
		cc, err := FetchCodeContext(context.Background(), fetcher, "abc123", "o", "r", filtered[i], 2)
		if err != nil {
			t.Fatalf("fetch %s: %v", filtered[i].ID, err)
		}
		if cc == nil {
			t.Fatalf("nil CodeContext for %s", filtered[i].ID)
		}
		if !cc.Outdated {
			t.Errorf("thread %s: CodeContext.Outdated=%v want true", filtered[i].ID, cc.Outdated)
		}
		if cc.Ref != "abc123" {
			t.Errorf("thread %s: Ref=%q want abc123", filtered[i].ID, cc.Ref)
		}
		if len(cc.Lines) == 0 {
			t.Errorf("thread %s: no lines fetched", filtered[i].ID)
		}
	}
}

// TestIntegration_StripVerboseFieldsDoesntAffectCodeContext ensures that
// StripVerboseFields, which only zeros per-comment fields, leaves the
// per-thread CodeContext attached and intact.
func TestIntegration_StripVerboseFieldsDoesntAffectCodeContext(t *testing.T) {
	threads := []ReviewThreadWithPolicy{
		{
			ReviewThread: github.ReviewThread{
				ID: "T_x", Path: "a.go", Line: 5,
				Comments: []github.ThreadComment{{
					ID:        "PRC_1",
					AuthorType: "Bot",
					CreatedAt: "2026-05-10T09:01:23Z",
					UpdatedAt: "2026-05-10T09:05:00Z",
					AuthorURL: "https://github.com/apps/bot",
				}},
			},
			CodeContext: &CodeContext{
				Path:      "a.go",
				Ref:       "abc1234567890",
				StartLine: 3,
				EndLine:   7,
				Lines:     []string{"L3", "L4", "L5", "L6", "L7"},
				Outdated:  false,
			},
		},
	}

	// Pre-condition: verbose fields populated.
	if threads[0].Comments[0].CreatedAt == "" {
		t.Fatalf("setup: createdAt expected non-empty")
	}

	StripVerboseFields(threads)

	// Verbose fields should now be empty.
	c := threads[0].Comments[0]
	if c.CreatedAt != "" || c.UpdatedAt != "" || c.AuthorURL != "" {
		t.Errorf("verbose fields not stripped: %+v", c)
	}

	// CodeContext should be untouched.
	cc := threads[0].CodeContext
	if cc == nil {
		t.Fatal("CodeContext was wiped!")
	}
	if cc.Path != "a.go" || cc.Ref != "abc1234567890" {
		t.Errorf("CodeContext metadata mutated: %+v", cc)
	}
	if cc.StartLine != 3 || cc.EndLine != 7 {
		t.Errorf("CodeContext range changed: [%d,%d]", cc.StartLine, cc.EndLine)
	}
	wantLines := []string{"L3", "L4", "L5", "L6", "L7"}
	if len(cc.Lines) != len(wantLines) {
		t.Fatalf("CodeContext.Lines len=%d want %d", len(cc.Lines), len(wantLines))
	}
	for i := range wantLines {
		if cc.Lines[i] != wantLines[i] {
			t.Errorf("cc.Lines[%d]=%q want %q", i, cc.Lines[i], wantLines[i])
		}
	}
}
