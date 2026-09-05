package github

// GitHub's compare endpoint caps the returned commit list (250 at time of
// writing) and reports the true total separately as TotalCommits. Above that
// cap, Commits[len-1] is not the fork's actual tip — it's just the deepest
// commit the API happened to return — so trusting it as HeadSHA would let two
// forks that share a base and their first N ahead-commits, then diverge later,
// collide on an identity neither of them actually has at that position.

import (
	"fmt"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestCompareToT2_HeadSHAOmittedWhenCommitListTruncated(t *testing.T) {
	r := CompareResult{
		Performed:    true,
		AheadBy:      400,
		TotalCommits: 400, // the true count
		Commits: []Commit{
			{SHA: "c1"}, {SHA: "c2"}, {SHA: "c3"}, // only 3 returned — capped
		},
	}

	t2 := compareToT2(r)

	if t2.HeadSHA != "" {
		t.Errorf("HeadSHA = %q, want empty — the commit list is truncated (3 of 400) and c3 is not the real tip", t2.HeadSHA)
	}
}

func TestCompareToT2_HeadSHASetWhenCommitListComplete(t *testing.T) {
	r := CompareResult{
		Performed:    true,
		AheadBy:      3,
		TotalCommits: 3,
		Commits: []Commit{
			{SHA: "c1"}, {SHA: "c2"}, {SHA: "c3"},
		},
	}

	t2 := compareToT2(r)

	if t2.HeadSHA != "c3" {
		t.Errorf("HeadSHA = %q, want %q — the full commit list was returned", t2.HeadSHA, "c3")
	}
}

// TotalCommits defaults to zero on a manually constructed CompareResult (e.g.
// an older cache entry, or a test fixture), which must not be mistaken for "3
// of 0" being complete.
func TestCompareToT2_HeadSHAOmittedWhenTotalCommitsUnset(t *testing.T) {
	r := CompareResult{
		Performed: true,
		AheadBy:   3,
		Commits:   []Commit{{SHA: "c1"}, {SHA: "c2"}, {SHA: "c3"}},
	}

	t2 := compareToT2(r)

	if t2.HeadSHA != "" {
		t.Errorf("HeadSHA = %q, want empty — TotalCommits is unset, so completeness is unknown", t2.HeadSHA)
	}
}

func TestCompareToT2FlagsTruncatedFileList(t *testing.T) {
	files := make([]FileChange, forge.CompareFilesCap)
	for i := range files {
		files[i] = FileChange{Filename: fmt.Sprintf("f%d", i), Status: "modified"}
	}
	got := compareToT2(CompareResult{Performed: true, AheadBy: 1, Files: files})
	if !got.FilesTruncated {
		t.Fatal("300 files must set FilesTruncated")
	}
	got = compareToT2(CompareResult{Performed: true, AheadBy: 1, Files: files[:299]})
	if got.FilesTruncated {
		t.Fatal("299 files must not set FilesTruncated")
	}
}
