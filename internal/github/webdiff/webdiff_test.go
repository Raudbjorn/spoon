package webdiff

import (
	"strings"
	"testing"
)

func TestWebDiffParsesFilesAndPagination(t *testing.T) {
	fixture := `<html><body>
<div class="js-file-header" data-path="internal/auth.go"></div>
<table><tr><td class="blob-code blob-code-deletion">old()</td></tr><tr><td class="blob-code blob-code-addition">new()</td></tr></table>
<a rel="next" href="?start_entry=25">Next</a>
</body></html>`
	patches, next, err := ParseHTML(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if next != 25 {
		t.Fatalf("next=%d want=25", next)
	}
	if got := patches["internal/auth.go"]; got != "-old()\n+new()\n" {
		t.Fatalf("patch=%q", got)
	}
}

func TestWebDiffMarkupChangeIsGracefulError(t *testing.T) {
	if _, _, err := ParseHTML(strings.NewReader(`<html><body>changed</body></html>`)); err == nil {
		t.Fatal("expected parse error for unrecognized markup")
	}
}
