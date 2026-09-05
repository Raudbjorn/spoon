package tui

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/pathmatch"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestRenderPatchFiltersAndColours(t *testing.T) {
	t2 := forge.T2Data{Diffs: []forge.FileDiff{
		{Path: "a/registry/antipatterns.mjs", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
		{Path: "README.md", Status: "modified", Patch: "@@ -1 +1 @@\n-x\n+y\n"},
	}}
	m, _ := pathmatch.Compile([]string{"**/antipatterns.mjs"})
	out := renderPatch(theme.Context{}, t2, &m, 200_000)
	if !strings.Contains(out, "a/registry/antipatterns.mjs") || strings.Contains(out, "README.md") {
		t.Fatalf("filter not applied:\n%s", out)
	}
	if !strings.Contains(out, "+new") {
		t.Fatal("patch body missing")
	}
	if out2 := renderPatch(theme.Context{}, t2, nil, 10); !strings.Contains(out2, "truncated") {
		t.Fatal("cap must announce truncation")
	}
}

// TestViewPatchKeyNoProviderLeavesDetailView guards the fix in
// handleDetailKey (app.go): fetchPatchCmd returns nil when there is no
// provider (or the cursor is out of range), and switching to viewPatch
// regardless would render whatever patchBody a *previous* fork's fetch left
// behind. With provider nil, "p" must leave the view on detail and must not
// touch the stale patchBody -- there is nothing to fetch, so there is
// nothing to clear either.
func TestViewPatchKeyNoProviderLeavesDetailView(t *testing.T) {
	m := newTestModel()
	m.view = viewDetail
	m.forks = []ScoredFork{{Fork: forge.T1Data{ID: "o/x"}, T2: &forge.T2Data{Performed: true, AheadCount: 1}}}
	m.cursor = 0
	m.patchBody = "stale body from a previous fork"

	_, cmd := m.handleDetailKey("p")

	if m.view != viewDetail {
		t.Fatalf("view = %v, want viewDetail (no provider means no fetch, so no view switch)", m.view)
	}
	if cmd != nil {
		t.Fatal("handleDetailKey returned a non-nil cmd with no provider")
	}
	if m.patchBody != "stale body from a previous fork" {
		t.Fatalf("patchBody = %q, want untouched since no fetch started", m.patchBody)
	}
}

func TestPatchKeymap(t *testing.T) {
	if keymap.Dispatch(keymap.MainDetail, "p") != keymap.ViewPatch {
		t.Fatal("p must open the patch view from detail")
	}
	if keymap.Dispatch(keymap.MainPatch, "esc") != keymap.Back {
		t.Fatal("esc must close the patch view")
	}
}

// A patch result from an older fetch (same fork or not) must neither clear
// the loading flag of the newer in-flight fetch nor overwrite its body
// (PR #125 review: request generation, not just fork identity).
func TestPatchResultStaleSeqIgnored(t *testing.T) {
	m := newTestModel()
	m.view = viewPatch
	m.forks = []ScoredFork{{Fork: forge.T1Data{ID: "o/x"}, T2: &forge.T2Data{Performed: true, AheadCount: 1}}}
	m.cursor = 0
	m.patchSeq = 2 // a second fetch for the same fork is in flight
	m.patchLoading = true
	m.patchBody = "body of fetch #2 (pending)"

	stale := patchResultMsg{seq: 1, forkID: "o/x", t2: forge.T2Data{Performed: true, Diffs: []forge.FileDiff{{Path: "a.go", Patch: "@@ -1 +1 @@\n+x\n"}}}}
	updated, _ := m.Update(stale)
	got := updated.(Model)
	if !got.patchLoading {
		t.Fatal("stale result cleared patchLoading for the newer fetch")
	}
	if got.patchBody != "body of fetch #2 (pending)" {
		t.Fatalf("stale result overwrote patchBody: %q", got.patchBody)
	}

	fresh := stale
	fresh.seq = 2
	updated, _ = got.Update(fresh)
	got = updated.(Model)
	if got.patchLoading {
		t.Fatal("current result must clear patchLoading")
	}
	if !strings.Contains(got.patchBody, "a.go") {
		t.Fatalf("current result not applied: %q", got.patchBody)
	}
}

// One long final line must not carry the rendered body far past the limit:
// the check is on the prospective length, not the length before the line.
func TestRenderPatchLongLineRespectsLimit(t *testing.T) {
	long := "+" + strings.Repeat("x", 10_000)
	t2 := forge.T2Data{Diffs: []forge.FileDiff{{Path: "big.go", Status: "modified", Patch: "@@ -1 +1 @@\n" + long}}}
	out := renderPatch(newTestModel().themeContext(), t2, nil, 200)
	if !strings.Contains(out, "patch truncated at 200") {
		t.Fatalf("expected truncation marker, got %d chars", len(out))
	}
	if len(out) > 600 {
		t.Fatalf("rendered body is %d chars; a single long line escaped the limit", len(out))
	}
}
