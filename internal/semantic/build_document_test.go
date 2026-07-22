package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// The diff is the most discriminative section and must sit before the commit
// list, so a fork with a long commit history can no longer push it out of the
// embedding window; and the content hash must be body-only so coexisting models
// share the single documents row without orphaning each other's embeddings (#89).
func TestBuildDocumentDiffPrecedesCommitsAndHashesBodyOnly(t *testing.T) {
	t2 := &forge.T2Data{
		Diffs: []forge.FileDiff{{
			Path: "engine.go", Additions: 5, Deletions: 5,
			Patch: "@@ -1 +1 @@\n-oldengine\n+newengine",
		}},
		Commits: []forge.AheadCommit{{Message: "committokenmarker"}},
	}
	fork := forge.T1Data{Owner: "o", Name: "n", Description: "desc", Language: "Go"}

	rec, truncated := BuildDocument("fk", fork, t2)
	if truncated {
		t.Fatal("small diff should not report truncation")
	}

	diffAt := strings.Index(rec.Body, "newengine")
	commitAt := strings.Index(rec.Body, "committokenmarker")
	if diffAt < 0 || commitAt < 0 {
		t.Fatalf("body missing diff or commit content:\n%s", rec.Body)
	}
	if diffAt > commitAt {
		t.Fatalf("diff must precede commits in the body (diff@%d commit@%d):\n%s", diffAt, commitAt, rec.Body)
	}

	// content_hash = sha256(body), independent of any model identity.
	sum := sha256.Sum256([]byte(rec.Body))
	if rec.ContentHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("content_hash is not the body-only hash; model identity leaked in")
	}
}
