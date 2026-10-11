package github

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// A capped commit list ends before the fork tip. Its last commit cannot be
// used to fetch a supposedly complete diff, even if the HTML is readable.
func TestFinishT2WebDiffSkipsUnverifiedHead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total int
	}{
		{"capped", 2},
		{"unknown total", 0},
		{"complete", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{}
			c.EnableWebDiff("cookie")
			p := &GHProvider{client: c}
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // A fetch must fail locally rather than contact GitHub.
			scan := BranchScan{Compare: CompareResult{
				BaseCommit:   Commit{SHA: "base"},
				Commits:      []Commit{{SHA: "head"}},
				TotalCommits: tc.total,
				Files:        []FileChange{{Filename: "a.go", Additions: 1}},
			}}
			t2 := p.finishT2(ctx, "o", "r", scan, forge.T1Data{})
			if tc.total == 1 {
				if t2.PatchSkipReason != context.Canceled.Error() {
					t.Fatalf("verified head did not reach the fetcher: %q", t2.PatchSkipReason)
				}
			} else if !strings.Contains(t2.PatchSkipReason, "base/head SHA") {
				t.Fatalf("unverified head reached the fetcher: %q", t2.PatchSkipReason)
			}
		})
	}
}

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
		{Path: "a.go", Additions: 1, Patch: ""}, // REST statistics must match the complete web patch.
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

// Successful pagination alone cannot prove that all hunks survived HTML
// parsing. Compare against the REST counts before a fragment reaches storage.
func TestApplyWebDiffPatchesChecksRESTCounts(t *testing.T) {
	for _, tc := range []struct {
		name, patch string
		complete    bool
	}{
		{"absent", "", false},
		{"missing additions", "+first\n-old\n", false},
		{"missing deletions", "+first\n+second\n", false},
		{"duplicate lines", "+first\n+second\n+duplicate\n-old\n", false},
		{"complete", "+first\n context\n-old\n+second\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t2 := forge.T2Data{Diffs: []forge.FileDiff{{Path: "a.go", Additions: 2, Deletions: 1}}}
			applyWebDiffPatches(&t2, map[string]string{"a.go": tc.patch}, false, nil)
			if tc.complete {
				if t2.Diffs[0].Patch != tc.patch || t2.Diffs[0].PatchSource != "github_web" || t2.PatchSkipReason != "" {
					t.Fatalf("complete patch rejected: %+v, skip reason %q", t2.Diffs[0], t2.PatchSkipReason)
				}
				return
			}
			if t2.Diffs[0].Patch != "" || t2.Diffs[0].PatchSource != "" {
				t.Fatalf("incomplete patch accepted: %+v", t2.Diffs[0])
			}
			if t2.PatchSkipReason == "" {
				t.Fatal("missing skip reason for incomplete patch")
			}
		})
	}
}

func TestApplyWebDiffPatchesKeepsCompleteSibling(t *testing.T) {
	t2 := compareToT2(CompareResult{Files: []FileChange{
		{Filename: "partial.go", Additions: 2},
		{Filename: "complete.go", Additions: 1, Deletions: 1},
		{Filename: "rest.go", Additions: 1, Patch: "+rest\n"},
	}})
	applyWebDiffPatches(&t2, map[string]string{
		"partial.go":  "+first\n",
		"complete.go": "-old\n+new\n",
		"rest.go":     "+overwrite\n",
	}, false, nil)
	if t2.Diffs[0].Patch != "" || t2.Diffs[0].PatchSource == "github_web" {
		t.Fatalf("partial patch accepted: %+v", t2.Diffs[0])
	}
	if t2.Diffs[1].Patch != "-old\n+new\n" || t2.Diffs[1].PatchSource != "github_web" {
		t.Fatalf("complete sibling lost: %+v", t2.Diffs[1])
	}
	if t2.Diffs[2].Patch != "+rest\n" || t2.Diffs[2].PatchSource != "compare_rest" {
		t.Fatalf("REST patch changed: %+v", t2.Diffs[2])
	}
	if !strings.Contains(t2.PatchSkipReason, `"partial.go"`) {
		t.Fatalf("missing partial-file diagnostic: %q", t2.PatchSkipReason)
	}
}
