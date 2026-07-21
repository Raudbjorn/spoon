package embed

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// github_web patches are scraped as bare +/- lines with no ---/+++ or @@ hunk
// header, while REST patches carry both. NormalizeDiff rewrites a real hunk
// header to "@@@@", so without canonicalization the same change embeds
// differently depending on which source fetched it — and DiffChunk feeds
// BuildDocument -> content_hash -> embeddings, so the divergence reaches
// cluster and novelty output.
func TestDiffChunkCanonicalizesHeaderlessPatches(t *testing.T) {
	body := "+added line\n-removed line\n context line\n"

	rest := forge.FileDiff{
		Path: "internal/a.go", Additions: 1, Deletions: 1,
		PatchSource: "compare_rest",
		Patch:       "--- a/internal/a.go\n+++ b/internal/a.go\n@@ -10,3 +10,3 @@\n" + body,
	}
	web := forge.FileDiff{
		Path: "internal/a.go", Additions: 1, Deletions: 1,
		PatchSource: "github_web",
		Patch:       body,
	}

	restChunk, _ := buildDiffChunk([]forge.FileDiff{rest}, 4096)
	webChunk, _ := buildDiffChunk([]forge.FileDiff{web}, 4096)
	gotRest := NormalizeDiff(restChunk)
	gotWeb := NormalizeDiff(webChunk)

	if !strings.Contains(gotWeb, "@@@@") {
		t.Fatalf("web patch has no normalized hunk marker:\n%s", gotWeb)
	}
	if !strings.Contains(gotWeb, "--- a/internal/a.go") || !strings.Contains(gotWeb, "+++ b/internal/a.go") {
		t.Fatalf("web patch lacks canonical file header:\n%s", gotWeb)
	}
	if gotRest != gotWeb {
		t.Fatalf("same change embeds differently by source:\nrest=%q\nweb =%q", gotRest, gotWeb)
	}
}
