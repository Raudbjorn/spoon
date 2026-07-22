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

	// A release with no references held must NOT claim teardown (returning true
	// there would trigger a spurious DestroyEnvironment) and must not underflow.
	if releaseORT() {
		t.Fatal("release at zero refs must not claim teardown")
	}
	ortMu.Lock()
	underflowed := ortLiveRefs < 0
	ortMu.Unlock()
	if underflowed {
		t.Fatal("refcount underflowed below zero")
	}
}
