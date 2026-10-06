package webdiff

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
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

// ParseHTML tracks the "current file" as a state machine over a pre-order DFS:
// a file header must be visited before its descendant blob-code cells. If the
// traversal visits children in reverse, a cell attaches to the wrong path and
// patches cross-contaminate. A single-file fixture cannot catch that, so assert two
// files whose patches must stay on their own paths.
func TestWebDiffMultiFileKeepsPatchesOnOwnPath(t *testing.T) {
	fixture := `<html><body>
<div class="js-file-header" data-path="a.go"></div>
<table><tr><td class="blob-code blob-code-addition">alpha()</td></tr></table>
<div class="js-file-header" data-path="b.go"></div>
<table><tr><td class="blob-code blob-code-deletion">beta()</td></tr></table>
</body></html>`
	patches, _, err := ParseHTML(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if got := patches["a.go"]; got != "+alpha()\n" {
		t.Fatalf("a.go patch=%q want=+alpha()\\n", got)
	}
	if got := patches["b.go"]; got != "-beta()\n" {
		t.Fatalf("b.go patch=%q want=-beta()\\n", got)
	}
}

func TestWebDiffMarkupChangeIsGracefulError(t *testing.T) {
	if _, _, err := ParseHTML(strings.NewReader(`<html><body>changed</body></html>`)); err == nil {
		t.Fatal("expected parse error for unrecognized markup")
	}
}

// A page whose files are all binary explains its own lack of hunks, so it is a
// valid empty page. Failing it made Fetch report truncation, which cost the
// fork every patch it had already collected (#83).
func TestWebDiffSelfExplainingEmptyPageIsNotAnError(t *testing.T) {
	fixture := `<html><body>
<div class="js-file-header" data-path="assets/logo.png"></div>
<div class="diff-table"><span class="file-info">Binary files a/assets/logo.png and b/assets/logo.png differ</span></div>
</body></html>`
	patches, next, err := ParseHTML(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("empty page rejected: %v", err)
	}
	if len(patches) != 0 {
		t.Errorf("patches = %v, want none (this page has no textual hunks)", patches)
	}
	if next != -1 {
		t.Errorf("next = %d, want -1 (no pagination link on the page)", next)
	}
}

// A header with no cells and no explanation is the dangerous case, not the
// benign one: markup that moved away from blob-code while keeping its headers
// looks exactly like this. Accepting it would let Fetch report a partial
// pagination as complete and the adapter persist a fragment as a whole diff
// (#83).
func TestWebDiffUnexplainedEmptyPageIsStillAnError(t *testing.T) {
	fixture := `<html><body>
<div class="js-file-header" data-path="internal/auth.go"></div>
<table class="diff-table"><tr><td class="diff-hunk-cell">+func New()</td></tr></table>
</body></html>`
	if _, _, err := ParseHTML(strings.NewReader(fixture)); err == nil {
		t.Fatal("expected an error: file headers alone do not explain a page with no hunks")
	}
}

// The rename and mode-only entries this change exists to accept are just as
// common as binary ones, and GitHub words their placeholders differently. A
// page of them must not read as drift, or the fork loses every patch again.
func TestWebDiffAcceptsRenameAndModeOnlyPages(t *testing.T) {
	for _, tc := range []struct{ name, path, note string }{
		{"rename without changes", "old/name.go", "File renamed without changes"},
		{"rename with changes", "new/name.go", "renamed from old/name.go"},
		{"mode only", "bin/tool", "File mode changed from 100644 to 100755"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := `<html><body><div class="js-file-header" data-path="` + tc.path + `"></div>
<span class="file-info">` + tc.note + `</span></body></html>`
			patches, _, err := ParseHTML(strings.NewReader(fixture))
			if err != nil {
				t.Fatalf("%q rejected as drift: %v", tc.note, err)
			}
			if len(patches) != 0 {
				t.Errorf("patches = %v, want none", patches)
			}
		})
	}
}

// Evidence is per file, not per page. One binary entry must not vouch for a
// sibling whose hunks went unparsed — that is how a partial pagination would
// pass as complete (#83).
func TestWebDiffUnexplainedSiblingFileStillFails(t *testing.T) {
	fixture := `<html><body>
<div class="js-file-header" data-path="assets/logo.png"></div>
<span class="file-info">Binary files a/assets/logo.png and b/assets/logo.png differ</span>
<div class="js-file-header" data-path="internal/auth.go"></div>
<table><tr><td class="diff-hunk-cell">+func New()</td></tr></table>
</body></html>`
	if _, _, err := ParseHTML(strings.NewReader(fixture)); err == nil {
		t.Fatal("expected an error: the binary file's explanation must not cover the unexplained sibling")
	}
}

// A source line that merely mentions a rename is not a placeholder. Without the
// length bound, unparsed source text containing "renamed" would accept a page
// whose markup had drifted.
func TestWebDiffLongSourceLineIsNotAPlaceholder(t *testing.T) {
	long := "the migration renamed the column and the index and the constraint and the view and the trigger"
	fixture := `<html><body><div class="js-file-header" data-path="internal/auth.go"></div>
<span class="file-info">` + long + `</span></body></html>`
	if len(long) <= 80 {
		t.Fatalf("fixture must exceed nontextualMaxLen to be meaningful, got %d", len(long))
	}
	if _, _, err := ParseHTML(strings.NewReader(fixture)); err == nil {
		t.Fatal("expected an error: a long source line is not a placeholder")
	}
}

// Concurrent callers must each receive a distinct slot. The bug this guards
// against overwrote c.next with now+interval on every call, so every queued
// caller computed roughly the same wait and they all fired as one burst —
// defeating the pacing entirely.
func TestReserveHandsOutDistinctSlots(t *testing.T) {
	c := New("cookie", nil)
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	// Five callers arriving at the same instant.
	var waits []time.Duration
	for range 5 {
		waits = append(waits, c.reserve(base))
	}

	for i, got := range waits {
		want := time.Duration(i) * minInterval
		if got != want {
			t.Errorf("caller %d waits %v, want %v (slots must not collide)", i, got, want)
		}
	}
}

// An idle client hands out the current slot immediately rather than making the
// first caller wait out a stale reservation.
func TestReserveIdleClientDoesNotWait(t *testing.T) {
	c := New("cookie", nil)
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	if got := c.reserve(base); got != 0 {
		t.Fatalf("first call waits %v, want 0", got)
	}
	// Well past the reserved slot: still immediate.
	if got := c.reserve(base.Add(time.Hour)); got != 0 {
		t.Errorf("call after idle period waits %v, want 0", got)
	}
}

// Slots stay distinct under real concurrency, not just sequential calls.
func TestReserveIsRaceFree(t *testing.T) {
	c := New("cookie", nil)
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	const n = 50
	results := make(chan time.Duration, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- c.reserve(base)
		}()
	}
	wg.Wait()
	close(results)

	seen := map[time.Duration]bool{}
	for d := range results {
		if seen[d] {
			t.Fatalf("duplicate slot %v handed to two callers", d)
		}
		seen[d] = true
	}
	if len(seen) != n {
		t.Errorf("got %d distinct slots, want %d", len(seen), n)
	}
}

// An already-cancelled context must not consume a rate-limit slot — reserving
// one would advance c.next and delay live callers — and on the idle path where
// reserve returns 0, wait must still surface the cancellation, not nil.
func TestWaitCancelledContextReservesNoSlot(t *testing.T) {
	c := New("cookie", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	before := c.next
	if err := c.wait(ctx); err == nil {
		t.Error("wait returned nil for a cancelled context")
	}
	if c.next != before {
		t.Errorf("cancelled wait advanced c.next from %v to %v; it consumed a slot", before, c.next)
	}
}
