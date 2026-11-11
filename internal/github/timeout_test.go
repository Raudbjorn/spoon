package github

import (
	"net/url"
	"testing"
)

// A backend that completes the handshake then never sends response headers
// otherwise hangs spn forever: the gateway-retry loop never fires because no
// error is returned, and the proxy is never scored unhealthy because scoring
// happens only after RoundTrip returns. http.DefaultTransport leaves
// ResponseHeaderTimeout unset, so a cloned proxy transport inherits no header
// deadline and must have one set explicitly.
func TestProxyTransportsHaveResponseHeaderTimeout(t *testing.T) {
	u, err := url.Parse("http://proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	pool := newStaticProxyPool([]*url.URL{u})
	if len(pool.entries) != 1 {
		t.Fatalf("got %d pool entries, want 1", len(pool.entries))
	}
	if got := pool.entries[0].transport.ResponseHeaderTimeout; got <= 0 {
		t.Errorf("pooled proxy transport ResponseHeaderTimeout = %v; a stalled proxy would hang forever", got)
	}

	rot := newRotatingProxyTransport(pool)
	if got := rot.direct.ResponseHeaderTimeout; got <= 0 {
		t.Errorf("direct transport ResponseHeaderTimeout = %v; a stalled backend would hang forever", got)
	}
}

// Both API clients set http.Client.Timeout; a zero
// value means no deadline at all.
func TestRequestTimeoutsAreSet(t *testing.T) {
	if requestTimeout <= 0 {
		t.Errorf("requestTimeout = %v, want a positive bound", requestTimeout)
	}
	if responseHeaderTimeout <= 0 {
		t.Errorf("responseHeaderTimeout = %v, want a positive bound", responseHeaderTimeout)
	}
	if responseHeaderTimeout > requestTimeout {
		t.Errorf("responseHeaderTimeout (%v) exceeds requestTimeout (%v); the header guard would never fire",
			responseHeaderTimeout, requestTimeout)
	}
}
