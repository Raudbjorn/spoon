package embed

import "testing"

// The ONNX environment is process-global; Destroy() tears it down for every
// live embedder. releaseORT must report teardown ownership only to the last
// closer, so two concurrently-live embedders don't segfault each other (#89).
func TestORTRefcountOnlyLastCloserTearsDown(t *testing.T) {
	ortMu.Lock()
	saved := ortLiveRefs
	ortLiveRefs = 0
	ortMu.Unlock()
	t.Cleanup(func() {
		ortMu.Lock()
		ortLiveRefs = saved
		ortMu.Unlock()
	})

	// Two live embedders.
	acquireORT()
	acquireORT()

	if releaseORT() {
		t.Fatal("first close claimed teardown while a second embedder is still live")
	}
	if !releaseORT() {
		t.Fatal("last close did not claim teardown of the shared environment")
	}

	// Defensive: an extra release must not go negative or re-claim teardown
	// incorrectly (it stays at zero, so it reports true but never underflows).
	if got := func() bool { releaseORT(); ortMu.Lock(); defer ortMu.Unlock(); return ortLiveRefs >= 0 }(); !got {
		t.Fatal("refcount underflowed below zero")
	}
}
