package github

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/embed"
)

func TestForkIntentText_BuildsFromNonEmptyFeatures(t *testing.T) {
	got := forkIntentText(embed.ForkFeatures{
		Paths:     " internal/auth.go ",
		Commits:   "add oauth flow",
		ReadmeDoc: " ",
		DiffChunk: "func auth() {}",
	})
	for _, want := range []string{"internal/auth.go", "add oauth flow", "func auth() {}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("forkIntentText missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("forkIntentText has extra blank separators: %q", got)
	}
}

func TestForkIntentMaxCosine(t *testing.T) {
	got := maxForkIntentSiblingSims(
		[]string{"fork-a", "fork-b"},
		[]embed.Vector{{1, 0}, {0, 1}},
		[]embed.Vector{{0.5, 0}, {0, 0.25}},
	)
	if got["fork-a"] != 0.5 {
		t.Fatalf("fork-a sim=%v want 0.5", got["fork-a"])
	}
	if got["fork-b"] != 0.25 {
		t.Fatalf("fork-b sim=%v want 0.25", got["fork-b"])
	}
}
