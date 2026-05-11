package threadsops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

// bulkFake collects per-thread resolve calls.
type bulkFake struct {
	fakeAPI
	resolved map[string]bool
	failOn   map[string]error
}

func newBulkFake(threads []github.ReviewThread) *bulkFake {
	return &bulkFake{
		fakeAPI:  fakeAPI{threads: threads},
		resolved: map[string]bool{},
		failOn:   map[string]error{},
	}
}

func (b *bulkFake) ResolveThread(_ context.Context, id string) error {
	if err, ok := b.failOn[id]; ok {
		return err
	}
	b.resolved[id] = true
	return nil
}
func (b *bulkFake) UnresolveThread(_ context.Context, id string) error {
	b.resolved[id] = false
	return nil
}

func TestResolveAll_skipsHumanThreads(t *testing.T) {
	threads := []github.ReviewThread{
		{ID: "PRRT_bot", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_user", Comments: []github.ThreadComment{{AuthorType: "User"}}},
	}
	b := newBulkFake(threads)
	res, opErr := ResolveAll(context.Background(), b, "o", "r", 1, true)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !equalUnordered(res.Succeeded, []string{"PRRT_bot"}) {
		t.Errorf("succeeded: %+v", res.Succeeded)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ID != "PRRT_user" || res.Skipped[0].Reason != "requires_body" {
		t.Errorf("skipped: %+v", res.Skipped)
	}
	if b.resolved["PRRT_user"] {
		t.Errorf("must not resolve human thread")
	}
}

func TestResolveAll_legacyMode_resolvesHumanThreads(t *testing.T) {
	threads := []github.ReviewThread{{ID: "PRRT_user", Comments: []github.ThreadComment{{AuthorType: "User"}}}}
	b := newBulkFake(threads)
	res, opErr := ResolveAll(context.Background(), b, "o", "r", 1, false)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("legacy mode should not skip, got %+v", res.Skipped)
	}
	if !b.resolved["PRRT_user"] {
		t.Errorf("legacy mode should resolve human thread")
	}
}

func TestResolveAll_perThreadFailure(t *testing.T) {
	threads := []github.ReviewThread{
		{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_b", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}
	b := newBulkFake(threads)
	b.failOn["PRRT_b"] = errors.New("boom")
	res, _ := ResolveAll(context.Background(), b, "o", "r", 1, true)
	if !equalUnordered(res.Succeeded, []string{"PRRT_a"}) {
		t.Errorf("succeeded: %+v", res.Succeeded)
	}
	if len(res.Failed) != 1 || res.Failed[0].ID != "PRRT_b" {
		t.Errorf("failed: %+v", res.Failed)
	}
}

func TestResolveAll_ctxCancellation(t *testing.T) {
	threads := make([]github.ReviewThread, 50)
	for i := range threads {
		threads[i] = github.ReviewThread{ID: fmt.Sprintf("PRRT_%d", i), Comments: []github.ThreadComment{{AuthorType: "Bot"}}}
	}
	b := newBulkFake(threads)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, opErr := ResolveAll(ctx, b, "o", "r", 1, true)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	// Pre-cancelled — call must return without hanging. We don't assert on
	// res.Succeeded length because the sender races the cancel.
}

func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
