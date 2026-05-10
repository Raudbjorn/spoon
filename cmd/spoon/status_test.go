package main

import (
	"bytes"
	"strings"
	"testing"

	gh "github.com/svnbjrn/spoon/internal/github"
)

func TestRenderStatusBlock(t *testing.T) {
	allGreen := gh.PullRequestStatus{
		Title:             "Add feature",
		MergeStateStatus:  "CLEAN",
		Mergeable:         "MERGEABLE",
		ReviewDecision:    "APPROVED",
		ChecksState:       "SUCCESS",
		UnresolvedThreads: 0,
	}
	allRed := gh.PullRequestStatus{
		Title:             "Add feature",
		MergeStateStatus:  "DIRTY",
		Mergeable:         "CONFLICTING",
		ReviewDecision:    "REVIEW_REQUIRED",
		ChecksState:       "FAILURE",
		UnresolvedThreads: 3,
	}
	missing := gh.PullRequestStatus{
		Title:             "WIP",
		MergeStateStatus:  "UNKNOWN",
		Mergeable:         "UNKNOWN",
		ReviewDecision:    "",
		ChecksState:       "",
		UnresolvedThreads: 0,
	}

	cases := []struct {
		name    string
		status  gh.PullRequestStatus
		number  int
		ascii   bool
		expects []string // substrings the output must contain
	}{
		{
			"all green ASCII",
			allGreen, 1, true,
			[]string{"PR #1", "Add feature", "[OK]", "CLEAN", "APPROVED", "SUCCESS", "0 unresolved"},
		},
		{
			"all red ASCII",
			allRed, 42, true,
			[]string{"PR #42", "[X]", "DIRTY", "REVIEW_REQUIRED", "FAILURE", "3 unresolved"},
		},
		{
			"missing review and checks shown as dash",
			missing, 7, true,
			[]string{"—", "UNKNOWN"},
		},
		{
			"glyph mode emits emoji markers",
			allGreen, 1, false, // useGlyphs=true via "!tc.ascii"
			[]string{"✅", "💬", "CLEAN", "APPROVED", "SUCCESS"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := RenderStatusBlock(&buf, tc.status, tc.number, !tc.ascii); err != nil {
				t.Fatalf("render: %v", err)
			}
			out := buf.String()
			for _, sub := range tc.expects {
				if !strings.Contains(out, sub) {
					t.Errorf("output missing %q\n---\n%s", sub, out)
				}
			}
			// Block ends with a blank line.
			if !strings.HasSuffix(out, "\n\n") {
				t.Errorf("block must end with blank line, got %q", out[len(out)-3:])
			}
		})
	}
}
