package threadsops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/github"
)

// bulkFake collects per-thread resolve calls. Writes are mutex-guarded so the
// fake is safe under concurrent ResolveThread/UnresolveThread invocations from
// runBulk's worker pool.
type bulkFake struct {
	fakeAPI
	mu       sync.Mutex
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
	b.mu.Lock()
	defer b.mu.Unlock()
	if err, ok := b.failOn[id]; ok {
		return err
	}
	b.resolved[id] = true
	return nil
}
func (b *bulkFake) UnresolveThread(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
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

func TestResolveAll_routesRateLimited(t *testing.T) {
	reset := time.Now().Add(45 * time.Second)
	rlErr := &github.RateLimitError{ResetAt: reset}
	b := newBulkFake(nil)
	b.fakeAPI.err = rlErr
	_, opErr := ResolveAll(context.Background(), b, "o", "r", 1, true)
	if opErr == nil || opErr.Code != OpCodeRateLimited {
		t.Fatalf("expected OpCodeRateLimited, got %+v", opErr)
	}
	if !opErr.Retryable {
		t.Error("expected Retryable=true")
	}
	if _, ok := opErr.Details["reset_at"].(string); !ok {
		t.Error("missing details.reset_at")
	}
	if secs, ok := opErr.Details["retry_after_seconds"].(int); !ok || secs < 30 {
		t.Errorf("expected retry_after_seconds ~45, got %v", opErr.Details["retry_after_seconds"])
	}
}

// --- G3: bulk-resolve outdated-only --------------------------------------

func TestResolveAll_OutdatedOnly_OnlyResolvesOutdated(t *testing.T) {
	threads := []github.ReviewThread{
		{ID: "PRRT_o1", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_a1", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_o2", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_a2", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}
	b := newBulkFake(threads)
	res, opErr := ResolveAllWithOptions(context.Background(), b, "o", "r", 1, ResolveAllOptions{
		SkipHumanThreads: true,
		OutdatedOnly:     true,
	})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !equalUnordered(res.Succeeded, []string{"PRRT_o1", "PRRT_o2"}) {
		t.Errorf("succeeded: %+v want both outdated bot threads", res.Succeeded)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("expected 2 skipped, got %+v", res.Skipped)
	}
	skipByID := map[string]string{}
	for _, s := range res.Skipped {
		skipByID[s.ID] = s.Reason
	}
	for _, want := range []string{"PRRT_a1", "PRRT_a2"} {
		if got := skipByID[want]; got != "not_outdated" {
			t.Errorf("skip %s reason=%q want not_outdated", want, got)
		}
	}
	if len(res.Failed) != 0 {
		t.Errorf("expected no failures, got %+v", res.Failed)
	}
	// The non-outdated threads must not have been issued resolve calls.
	if b.resolved["PRRT_a1"] || b.resolved["PRRT_a2"] {
		t.Errorf("must not resolve non-outdated threads, resolved=%+v", b.resolved)
	}
}

func TestResolveAll_OutdatedOnly_RespectsHumanSkip(t *testing.T) {
	threads := []github.ReviewThread{
		{ID: "PRRT_bot1", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_bot2", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_user", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}},
	}
	b := newBulkFake(threads)
	res, opErr := ResolveAllWithOptions(context.Background(), b, "o", "r", 1, ResolveAllOptions{
		SkipHumanThreads: true,
		OutdatedOnly:     true,
	})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !equalUnordered(res.Succeeded, []string{"PRRT_bot1", "PRRT_bot2"}) {
		t.Errorf("succeeded: %+v", res.Succeeded)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("expected 1 skipped, got %+v", res.Skipped)
	}
	if res.Skipped[0].ID != "PRRT_user" || res.Skipped[0].Reason != "requires_body" {
		t.Errorf("skipped[0]=%+v want id=PRRT_user reason=requires_body", res.Skipped[0])
	}
	if b.resolved["PRRT_user"] {
		t.Errorf("must not resolve human-only thread")
	}
}

func TestResolveAll_OutdatedOnly_NoOutdated(t *testing.T) {
	threads := []github.ReviewThread{
		{ID: "PRRT_1", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_2", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_3", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_4", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_5", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}
	b := newBulkFake(threads)
	res, opErr := ResolveAllWithOptions(context.Background(), b, "o", "r", 1, ResolveAllOptions{
		SkipHumanThreads: true,
		OutdatedOnly:     true,
	})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if len(res.Succeeded) != 0 {
		t.Errorf("expected no successes, got %+v", res.Succeeded)
	}
	if len(res.Failed) != 0 {
		t.Errorf("expected no failures, got %+v", res.Failed)
	}
	if len(res.Skipped) != 5 {
		t.Fatalf("expected 5 skipped, got %+v", res.Skipped)
	}
	for _, s := range res.Skipped {
		if s.Reason != "not_outdated" {
			t.Errorf("skip %s reason=%q want not_outdated", s.ID, s.Reason)
		}
	}
	if len(b.resolved) != 0 {
		t.Errorf("no thread should have been resolved, got %+v", b.resolved)
	}
}

func TestResolveAll_OutdatedOnly_AllOutdatedSucceed(t *testing.T) {
	threads := []github.ReviewThread{
		{ID: "PRRT_a", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_b", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_c", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}
	b := newBulkFake(threads)
	res, opErr := ResolveAllWithOptions(context.Background(), b, "o", "r", 1, ResolveAllOptions{
		SkipHumanThreads: true,
		OutdatedOnly:     true,
	})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !equalUnordered(res.Succeeded, []string{"PRRT_a", "PRRT_b", "PRRT_c"}) {
		t.Errorf("succeeded: %+v", res.Succeeded)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("expected no skips, got %+v", res.Skipped)
	}
	if len(res.Failed) != 0 {
		t.Errorf("expected no failures, got %+v", res.Failed)
	}
}

// --- G4: --dry-run bulk tests ---------------------------------------------

// bulkMutationCounter tracks total mutation calls (resolve + unresolve) for
// dry-run assertions. Embeds bulkFake to inherit FetchPR / threads behavior.
type bulkMutationCounter struct {
	bulkFake
	resolveCalls   int
	unresolveCalls int
}

func (b *bulkMutationCounter) ResolveThread(ctx context.Context, id string) error {
	b.mu.Lock()
	b.resolveCalls++
	b.mu.Unlock()
	return b.bulkFake.ResolveThread(ctx, id)
}

func (b *bulkMutationCounter) UnresolveThread(ctx context.Context, id string) error {
	b.mu.Lock()
	b.unresolveCalls++
	b.mu.Unlock()
	return b.bulkFake.UnresolveThread(ctx, id)
}

func newBulkMutationCounter(threads []github.ReviewThread) *bulkMutationCounter {
	return &bulkMutationCounter{bulkFake: *newBulkFake(threads)}
}

func TestResolveAll_DryRun_NoMutations(t *testing.T) {
	// 3 bot threads + 1 human thread; SkipHumanThreads=true, DryRun=true.
	// Expect: succeeded=3 (bot IDs, all marked dryRun=true), skipped=1
	// (human → requires_body), failed=0, and zero mutation calls.
	threads := []github.ReviewThread{
		{ID: "PRRT_bot1", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_bot2", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_bot3", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_user", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}},
	}
	b := newBulkMutationCounter(threads)
	res, opErr := ResolveAllWithOptions(context.Background(), b, "o", "r", 1, ResolveAllOptions{
		SkipHumanThreads: true,
		DryRun:           true,
	})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.DryRun {
		t.Errorf("BulkResult.DryRun should be true, got %+v", res)
	}
	if !equalUnordered(res.Succeeded, []string{"PRRT_bot1", "PRRT_bot2", "PRRT_bot3"}) {
		t.Errorf("succeeded=%+v want all 3 bot IDs", res.Succeeded)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ID != "PRRT_user" || res.Skipped[0].Reason != "requires_body" {
		t.Errorf("skipped=%+v want [{PRRT_user requires_body}]", res.Skipped)
	}
	if len(res.Failed) != 0 {
		t.Errorf("failed=%+v want 0", res.Failed)
	}
	if b.resolveCalls != 0 {
		t.Errorf("ResolveThread must NOT be called on dry-run; got %d", b.resolveCalls)
	}
}

func TestResolveAll_DryRun_WithOutdated(t *testing.T) {
	// Mix of outdated and current threads, OutdatedOnly=true + DryRun=true.
	// Expect: succeeded contains the outdated bot threads (dryRun=true on
	// BulkResult), skipped contains the non-outdated ones, no mutations.
	threads := []github.ReviewThread{
		{ID: "PRRT_o1", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_c1", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_o2", IsOutdated: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_c2", IsOutdated: false, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}
	b := newBulkMutationCounter(threads)
	res, opErr := ResolveAllWithOptions(context.Background(), b, "o", "r", 1, ResolveAllOptions{
		SkipHumanThreads: true,
		OutdatedOnly:     true,
		DryRun:           true,
	})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.DryRun {
		t.Errorf("BulkResult.DryRun should be true")
	}
	if !equalUnordered(res.Succeeded, []string{"PRRT_o1", "PRRT_o2"}) {
		t.Errorf("succeeded=%+v want outdated bot IDs", res.Succeeded)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped=%+v want 2", res.Skipped)
	}
	for _, sk := range res.Skipped {
		if sk.Reason != "not_outdated" {
			t.Errorf("skip %s reason=%q want not_outdated", sk.ID, sk.Reason)
		}
	}
	if b.resolveCalls != 0 {
		t.Errorf("ResolveThread must NOT be called on dry-run; got %d", b.resolveCalls)
	}
}

func TestUnresolveAll_DryRun_NoMutations(t *testing.T) {
	// 3 resolved threads + DryRun=true. The fixture is what FetchPR returns
	// for ThreadStateResolved (the fake passes through threads regardless of
	// the state filter). Expect succeeded=3, no mutation calls, dryRun=true.
	threads := []github.ReviewThread{
		{ID: "PRRT_r1", IsResolved: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_r2", IsResolved: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_r3", IsResolved: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
	}
	b := newBulkMutationCounter(threads)
	res, opErr := UnresolveAllWithOptions(context.Background(), b, "o", "r", 1, UnresolveAllOptions{DryRun: true})
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !res.DryRun {
		t.Errorf("BulkResult.DryRun should be true")
	}
	if !equalUnordered(res.Succeeded, []string{"PRRT_r1", "PRRT_r2", "PRRT_r3"}) {
		t.Errorf("succeeded=%+v want all 3", res.Succeeded)
	}
	if len(res.Failed) != 0 {
		t.Errorf("failed=%+v want 0", res.Failed)
	}
	if b.unresolveCalls != 0 {
		t.Errorf("UnresolveThread must NOT be called on dry-run; got %d", b.unresolveCalls)
	}
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
