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

func TestWebDiffMarkupChangeIsGracefulError(t *testing.T) {
	if _, _, err := ParseHTML(strings.NewReader(`<html><body>changed</body></html>`)); err == nil {
		t.Fatal("expected parse error for unrecognized markup")
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
