package tui

import (
	"context"
	"fmt"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/forge"
)

// orderFakeForge records the order Compare is called in.
type orderFakeForge struct {
	tierFakeForge
	mu    sync.Mutex
	order []string
}

func (f *orderFakeForge) Compare(_ context.Context, fk forge.T1Data, _ string) (forge.T2Data, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, fk.ID)
	return forge.T2Data{Performed: true, AheadCount: 1}, nil
}

func queueTestModel(fake forge.Forge) *Model {
	m := movementModel(3)
	m.provider = fake
	m.refresh = true // no store fast path
	m.enrichCtx = context.Background()
	m.enrichSem = make(chan struct{}, 1)
	return m
}

// The old dispatch bound each command to a fork when it was built, then let
// tea.Batch launch them all to race for the semaphore: the order compares
// actually ran in was random. Commands now take a fork only once they hold a
// slot, so whichever command wins, forks are compared in priority order.
func TestQueuedCompares_RunInPriorityOrderWhateverWinsTheRace(t *testing.T) {
	fake := &orderFakeForge{tierFakeForge: tierFakeForge{headroom: 1}}
	m := queueTestModel(fake)

	cmds := m.dispatchQueued([]enrichEntry{
		{fork: forge.T1Data{ID: "p1"}, priority: 1},
		{fork: forge.T1Data{ID: "p5"}, priority: 5},
		{fork: forge.T1Data{ID: "p3"}, priority: 3},
		{fork: forge.T1Data{ID: "p4"}, priority: 4},
		{fork: forge.T1Data{ID: "p2"}, priority: 2},
	})
	// Run the commands in reverse build order, one at a time: under the old
	// scheme this would compare p2 first.
	for i := len(cmds) - 1; i >= 0; i-- {
		if msg := cmds[i]().(tier2ResultMsg); msg.err != nil {
			t.Fatalf("compare: %v", msg.err)
		}
	}
	want := []string{"p5", "p4", "p3", "p2", "p1"}
	if len(fake.order) != len(want) {
		t.Fatalf("Compare order = %v, want %v", fake.order, want)
	}
	for i := range want {
		if fake.order[i] != want[i] {
			t.Fatalf("Compare order = %v, want %v", fake.order, want)
		}
	}
}

// Concurrent commands with one slot still pop in priority order.
func TestQueuedCompares_ConcurrentCommandsKeepPriorityOrder(t *testing.T) {
	fake := &orderFakeForge{tierFakeForge: tierFakeForge{headroom: 1}}
	m := queueTestModel(fake)
	var entries []enrichEntry
	for i := 0; i < 50; i++ {
		entries = append(entries, enrichEntry{fork: forge.T1Data{ID: string(rune('A'+i%26)) + string(rune('a'+i/26))}, priority: float64((i * 37) % 50)})
	}
	cmds := m.dispatchQueued(entries)
	var wg sync.WaitGroup
	for _, c := range cmds {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c() }()
	}
	wg.Wait()
	prio := map[string]float64{}
	for _, e := range entries {
		prio[e.fork.ID] = e.priority
	}
	for i := 1; i < len(fake.order); i++ {
		if prio[fake.order[i-1]] < prio[fake.order[i]] {
			t.Fatalf("compare %d (%s, prio %v) ran before higher-priority %s (prio %v)",
				i-1, fake.order[i-1], prio[fake.order[i-1]], fake.order[i], prio[fake.order[i]])
		}
	}
}

// Re-enrichment appended mid-pass is ordered against the forks not yet
// handed out, not queued behind them.
func TestEnrichQueue_PushReordersRemaining(t *testing.T) {
	q := &enrichQueue{}
	q.push(enrichEntry{fork: forge.T1Data{ID: "a"}, priority: 3}, enrichEntry{fork: forge.T1Data{ID: "b"}, priority: 1})
	if e, _ := q.pop(); e.fork.ID != "a" {
		t.Fatalf("first pop = %s, want a", e.fork.ID)
	}
	q.push(enrichEntry{fork: forge.T1Data{ID: "c"}, priority: 2})
	for _, want := range []string{"c", "b"} {
		if e, _ := q.pop(); e.fork.ID != want {
			t.Fatalf("pop = %s, want %s", e.fork.ID, want)
		}
	}
	if _, ok := q.pop(); ok {
		t.Fatal("pop on a drained queue must report !ok")
	}
}

// A fork the GraphQL batch finds has nothing ahead is settled without a live
// compare; only the unresolved one is queued.
func TestHandleBatchDivergence_ZeroAheadNeverReachesCompare(t *testing.T) {
	fake := &orderFakeForge{tierFakeForge: tierFakeForge{headroom: 1}}
	m := queueTestModel(fake)
	m.enrichQueue = &enrichQueue{}
	m.batchResolving = true

	order := []enrichEntry{
		{fork: forge.T1Data{ID: "zero"}, priority: 2},
		{fork: forge.T1Data{ID: "unknown"}, priority: 1},
	}
	msg := batchDivergenceMsg{
		ctx:   m.enrichCtx,
		order: order,
		divergence: map[string]forge.ForkDivergence{
			"zero": {Resolved: true, Default: forge.BranchDivergence{Name: "main", AheadBy: 0, BehindBy: 7, TipSHA: "abc"}},
		},
	}
	_, cmd := m.handleBatchDivergence(msg)
	if m.batchResolving {
		t.Error("batchResolving still set after the batch result was applied")
	}
	if len(m.pendingUpdates) != 1 || m.pendingUpdates[0].forkID != "zero" || m.pendingUpdates[0].fromCache {
		t.Fatalf("pendingUpdates = %+v, want one non-cache update for zero", m.pendingUpdates)
	}
	if got := m.pendingUpdates[0].t2; !got.Performed || got.AheadCount != 0 || got.BehindCount != 7 {
		t.Errorf("synthesised T2 = %+v, want performed, ahead 0, behind 7", got)
	}
	if cmd == nil {
		t.Fatal("no compare dispatched for the unresolved fork")
	}
	runBatch(cmd)
	if len(fake.order) != 1 || fake.order[0] != "unknown" {
		t.Errorf("Compare calls = %v, want only [unknown]", fake.order)
	}
}

// A batch result for a pass that was cancelled must not dispatch anything.
func TestHandleBatchDivergence_StalePassIgnored(t *testing.T) {
	fake := &orderFakeForge{tierFakeForge: tierFakeForge{headroom: 1}}
	m := queueTestModel(fake)
	stale, cancel := context.WithCancel(context.Background())
	cancel()
	_, cmd := m.handleBatchDivergence(batchDivergenceMsg{ctx: stale, order: []enrichEntry{{fork: forge.T1Data{ID: "x"}}}})
	if cmd != nil || len(m.pendingUpdates) != 0 {
		t.Fatalf("stale batch result was applied: cmd=%v pending=%v", cmd != nil, m.pendingUpdates)
	}
}

// Cancelling mid-pass must still report every queued fork, or enrichDone never
// reaches enrichTotal and clustering never starts.
func TestQueuedCompares_CancelledPassReportsEveryFork(t *testing.T) {
	fake := &orderFakeForge{tierFakeForge: tierFakeForge{headroom: 1}}
	m := queueTestModel(fake)
	ctx, cancel := context.WithCancel(context.Background())
	m.enrichCtx = ctx
	cancel()
	cmds := m.dispatchQueued([]enrichEntry{{fork: forge.T1Data{ID: "a"}}, {fork: forge.T1Data{ID: "b"}}})
	seen := map[string]bool{}
	for _, c := range cmds {
		msg := c().(tier2ResultMsg)
		if msg.err == nil || msg.forkID == "" {
			t.Fatalf("cancelled command returned %+v, want an error for a named fork", msg)
		}
		seen[msg.forkID] = true
	}
	if !seen["a"] || !seen["b"] || len(fake.order) != 0 {
		t.Errorf("reported %v, compares %v; want both forks reported, no compares", seen, fake.order)
	}
}

// runBatch executes cmd and, recursively, every command in a tea.BatchMsg.
func runBatch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runBatch(c)
		}
	}
}

// A fork whose repository no longer exists is settled without a compare and
// dropped from the list, counted as unreachable, with the pass still
// finishing its count.
func TestBatchDivergence_MissingRepoDroppedWithoutCompare(t *testing.T) {
	fake := &orderFakeForge{tierFakeForge: tierFakeForge{headroom: 1}}
	m := queueTestModel(fake)
	m.forks = []ScoredFork{
		{Fork: forge.T1Data{ID: "live"}, Enriching: true},
		{Fork: forge.T1Data{ID: "gone"}, Enriching: true},
	}
	m.cursor = 1
	m.enriching, m.enrichTotal = true, 2
	m.enrichQueue = &enrichQueue{}

	_, cmd := m.handleBatchDivergence(batchDivergenceMsg{
		ctx:     m.enrichCtx,
		order:   []enrichEntry{{fork: m.forks[0].Fork}, {fork: m.forks[1].Fork}},
		missing: map[string]bool{"gone": true},
	})
	// Deliver the live fork's compare result the way Update would.
	// tea.Batch of one command returns that command itself, not a BatchMsg.
	var collect func(tea.Msg)
	collect = func(msg tea.Msg) {
		switch v := msg.(type) {
		case tier2ResultMsg:
			m.pendingUpdates = append(m.pendingUpdates, v)
		case tea.BatchMsg:
			for _, c := range v {
				collect(c())
			}
		}
	}
	collect(cmd())
	_, _ = m.processPendingUpdates()

	if len(fake.order) != 1 || fake.order[0] != "live" {
		t.Errorf("Compare calls = %v, want only [live]", fake.order)
	}
	if len(m.forks) != 1 || m.forks[0].Fork.ID != "live" || m.unreachable != 1 {
		t.Errorf("forks = %d (%v), unreachable = %d; want only live kept, 1 unreachable", len(m.forks), m.forks, m.unreachable)
	}
	if m.enrichDone != 2 || m.enriching {
		t.Errorf("enrichDone = %d, enriching = %v; want 2 and finished", m.enrichDone, m.enriching)
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (was on the dropped row)", m.cursor)
	}
}

// batchFake resolves every fork as zero-ahead and records chunk sizes.
type batchFake struct {
	orderFakeForge
	chunks [][]string
}

func (f *batchFake) BatchCompare(_ context.Context, forks []forge.T1Data) (map[string]forge.ForkDivergence, forge.BatchStats, error) {
	ids := make([]string, len(forks))
	div := make(map[string]forge.ForkDivergence, len(forks))
	for i, fk := range forks {
		ids[i] = fk.ID
		div[fk.ID] = forge.ForkDivergence{Resolved: true, Default: forge.BranchDivergence{Name: "main"}}
	}
	f.chunks = append(f.chunks, ids)
	return div, forge.BatchStats{Queries: 1, Cost: 1}, nil
}

// The divergence batch runs in priority-ordered chunks so compares start after
// the first chunk instead of after the whole network (42 minutes on
// llama.cpp); the next chunk is requested alongside each chunk's compares.
func TestBatchDivergence_ResolvesInPriorityChunks(t *testing.T) {
	fake := &batchFake{}
	m := queueTestModel(fake)
	m.enrichQueue = &enrichQueue{}
	var order []enrichEntry
	for i := 0; i < 2*batchChunkForks+7; i++ {
		order = append(order, enrichEntry{fork: forge.T1Data{ID: fmt.Sprintf("f%04d", i)}, priority: float64(-i)})
	}
	m.batchResolving, m.batchProvider, m.batchPending = true, fake, order
	m.batchChunksTotal = 3

	// A zero-ahead chunk dispatches no compares, so the command handed back
	// is just the next chunk (tea.Batch of one command returns it as-is).
	cmd := m.nextBatchChunk()
	for steps := 0; cmd != nil; steps++ {
		if steps > 5 {
			t.Fatal("batch chunks did not terminate")
		}
		msg, ok := cmd().(batchDivergenceMsg)
		if !ok {
			t.Fatalf("expected a chunk result, got %T", msg)
		}
		_, cmd = m.handleBatchDivergence(msg)
	}
	if len(fake.chunks) != 3 || len(fake.chunks[0]) != batchChunkForks || len(fake.chunks[2]) != 7 {
		t.Fatalf("chunk sizes = %d chunks, want 3 of %d,%d,7", len(fake.chunks), batchChunkForks, batchChunkForks)
	}
	if fake.chunks[0][0] != "f0000" || fake.chunks[1][0] != fmt.Sprintf("f%04d", batchChunkForks) {
		t.Errorf("chunks not in priority order: first ids %s, %s", fake.chunks[0][0], fake.chunks[1][0])
	}
	if m.batchResolving || m.batchChunksDone != 3 || len(m.pendingUpdates) != len(order) {
		t.Errorf("after last chunk: resolving=%v done=%d settled=%d; want false, 3, %d",
			m.batchResolving, m.batchChunksDone, len(m.pendingUpdates), len(order))
	}
}

// Lowering the ceiling to T1 mid-pass stops further batch chunks; the forks
// they held are reported as ceiling skips so the pass still completes.
func TestNextBatchChunk_CeilingOneStopsSpending(t *testing.T) {
	fake := &batchFake{}
	m := queueTestModel(fake)
	m.batchProvider = fake
	m.batchPending = []enrichEntry{{fork: forge.T1Data{ID: "a"}}, {fork: forge.T1Data{ID: "b"}}}
	m.setMaxTier(1)
	if cmd := m.nextBatchChunk(); cmd != nil {
		t.Fatal("a chunk was dispatched at ceiling 1")
	}
	if len(fake.chunks) != 0 || len(m.pendingUpdates) != 2 || !m.pendingUpdates[0].tierSkipped {
		t.Errorf("chunks=%d updates=%+v; want no batch spend and two ceiling skips", len(fake.chunks), m.pendingUpdates)
	}
}
