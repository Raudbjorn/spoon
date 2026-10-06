package github

import (
	"errors"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// A truncated or failed scrape must never reach the store as a whole diff:
// a file whose hunks spanned the failed page would be persisted as a leading
// fragment with an empty skip reason, and the embedder would index that
// fragment as the file's complete diff. This is the corruption #83 describes,
// and it happens in the adapter, not in the scraper.
func TestApplyWebDiffPatchesDiscardsUntrustedScrape(t *testing.T) {
	partial := map[string]string{"a.go": "+first half\n"}
	for _, tc := range []struct {
		name      string
		truncated bool
		webErr    error
	}{
		{name: "truncated pagination", truncated: true},
		{name: "fetch error", webErr: errors.New("GitHub web diff returned HTTP 500")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t2 := forge.T2Data{Diffs: []forge.FileDiff{{Path: "a.go", Patch: ""}}}
			applyWebDiffPatches(&t2, partial, tc.truncated, tc.webErr)
			if t2.Diffs[0].Patch != "" {
				t.Errorf("patch = %q, want it left empty — a fragment is not a whole diff", t2.Diffs[0].Patch)
			}
			if t2.Diffs[0].PatchSource != "" {
				t.Errorf("patchSource = %q, want it unset so nothing downstream treats the fragment as web-diff-sourced", t2.Diffs[0].PatchSource)
			}
			if t2.PatchSkipReason == "" {
				t.Error("no skip reason recorded; the store cannot tell a missing patch from a fetched one")
			}
		})
	}
}

func TestApplyWebDiffPatchesFillsOnlyEmptyPatches(t *testing.T) {
	t2 := forge.T2Data{Diffs: []forge.FileDiff{
		{Path: "a.go", Patch: ""},
		{Path: "b.go", Patch: "-already from compare\n"},
		{Path: "c.go", Patch: ""},
	}}
	applyWebDiffPatches(&t2, map[string]string{"a.go": "+from web\n", "b.go": "+overwrite\n"}, false, nil)

	if got := t2.Diffs[0].Patch; got != "+from web\n" {
		t.Errorf("a.go patch = %q, want the web-diff content", got)
	}
	if t2.Diffs[0].PatchSource != "github_web" {
		t.Errorf("a.go patchSource = %q, want github_web", t2.Diffs[0].PatchSource)
	}
	if got := t2.Diffs[1].Patch; got != "-already from compare\n" {
		t.Errorf("b.go patch = %q, want the compare-supplied patch untouched", got)
	}
	if got := t2.Diffs[2].Patch; got != "" {
		t.Errorf("c.go patch = %q, want it left empty (no web diff for it)", got)
	}
	if t2.PatchSkipReason != "" {
		t.Errorf("skipReason = %q, want none on a clean scrape", t2.PatchSkipReason)
	}
}
