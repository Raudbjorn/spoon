package github

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type memberStats struct {
	successes uint64
	failures  uint64
	disabled  bool
}

type proxyEntry struct {
	url       *url.URL
	transport *http.Transport
	stats     memberStats
}

type proxyPool struct {
	mu         sync.Mutex
	entries    []*proxyEntry
	cursor     int
	dispatches uint64
}

func newStaticProxyPool(urls []*url.URL) *proxyPool {
	p := &proxyPool{}
	base, _ := http.DefaultTransport.(*http.Transport)
	for _, u := range urls {
		tr := base.Clone()
		tr.Proxy = http.ProxyURL(u)
		// DefaultTransport sets dial and TLS-handshake deadlines but leaves
		// ResponseHeaderTimeout unset, so a proxy that connects and then goes
		// silent is never timed out at the transport layer.
		tr.ResponseHeaderTimeout = responseHeaderTimeout
		p.entries = append(p.entries, &proxyEntry{url: u, transport: tr})
	}
	return p
}

func (p *proxyPool) next() *proxyEntry {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.entries) == 0 {
		return nil
	}
	p.dispatches++
	if p.dispatches%50 == 0 {
		p.rehabilitateLocked()
	}
	active := make([]*proxyEntry, 0, len(p.entries))
	for _, entry := range p.entries {
		if !entry.stats.disabled {
			active = append(active, entry)
		}
	}
	if len(active) == 0 {
		for _, entry := range p.entries {
			entry.stats.disabled = false
		}
		active = p.entries
	}
	entry := active[p.cursor%len(active)]
	p.cursor = (p.cursor + 1) % len(active)
	return entry
}

func (p *proxyPool) report(entry *proxyEntry, success bool) {
	if p == nil || entry == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if success {
		entry.stats.successes++
		return
	}
	entry.stats.failures++
	total := entry.stats.successes + entry.stats.failures
	if entry.stats.failures > 5 && float64(entry.stats.failures)/float64(total) > 0.70 {
		entry.stats.disabled = true
	}
}

func (p *proxyPool) rehabilitateLocked() {
	for _, entry := range p.entries {
		entry.stats.successes /= 2
		entry.stats.failures /= 2
		if entry.stats.disabled && entry.stats.failures <= 5 {
			entry.stats.disabled = false
		}
	}
}

func (p *proxyPool) closeIdleConnections() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entry := range p.entries {
		entry.transport.CloseIdleConnections()
	}
}

func proxyLabel(u *url.URL) string {
	if u == nil {
		return "direct"
	}
	sum := sha256.Sum256([]byte(u.String()))
	return fmt.Sprintf("%x@%s", sum[:4], u.Hostname())
}

type rotatingProxyTransport struct {
	pool   *proxyPool
	direct *http.Transport
}

func newRotatingProxyTransport(pool *proxyPool) *rotatingProxyTransport {
	base, _ := http.DefaultTransport.(*http.Transport)
	direct := base.Clone()
	direct.ResponseHeaderTimeout = responseHeaderTimeout
	return &rotatingProxyTransport{pool: pool, direct: direct}
}

func (t *rotatingProxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	entry := t.pool.next()
	transport := http.RoundTripper(t.direct)
	if entry != nil {
		transport = entry.transport
	}
	resp, err := transport.RoundTrip(req)
	// Don't charge caller cancellation / deadline to proxy health — that's not
	// the proxy's fault and would wrongly disable good proxies under load.
	if entry != nil && req.Context().Err() == nil {
		success := err == nil && resp.StatusCode != http.StatusProxyAuthRequired && resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusGatewayTimeout
		t.pool.report(entry, success)
	}
	return resp, err
}

func (t *rotatingProxyTransport) CloseIdleConnections() {
	t.direct.CloseIdleConnections()
	t.pool.closeIdleConnections()
}

type backendPool struct {
	mu         sync.Mutex
	backends   []*backend
	cursor     int
	dispatches uint64
}

func (p *backendPool) nextBackend(now time.Time) (*backend, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dispatches++
	if p.dispatches%50 == 0 {
		p.rehabilitateLocked()
	}
	active := make([]*backend, 0, len(p.backends))
	var earliest time.Time
	statisticalOnly := true
	for _, b := range p.backends {
		if b.Permanent {
			continue // confirmed auth failure — never dispatch or recover
		}
		if !b.Disabled && (b.DisabledUntil.IsZero() || !now.Before(b.DisabledUntil)) {
			active = append(active, b)
			continue
		}
		if !b.DisabledUntil.IsZero() && now.Before(b.DisabledUntil) {
			statisticalOnly = false
			if earliest.IsZero() || b.DisabledUntil.Before(earliest) {
				earliest = b.DisabledUntil
			}
		}
	}
	if len(active) == 0 && statisticalOnly {
		// Reset the circuit breaker for statistically-disabled identities only;
		// permanently-rejected (401) credentials stay excluded.
		for _, b := range p.backends {
			if b.Permanent {
				continue
			}
			b.Disabled = false
			active = append(active, b)
		}
	}
	if len(active) == 0 {
		return nil, &RateLimitError{ResetAt: earliest}
	}
	b := active[p.cursor%len(active)]
	p.cursor = (p.cursor + 1) % len(active)
	return b, nil
}

func (p *backendPool) reportSuccess(b *backend) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b.Successes++
}

func (p *backendPool) reportFailure(b *backend) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b.Failures++
	total := b.Successes + b.Failures
	if b.Failures > 5 && float64(b.Failures)/float64(total) > 0.70 {
		b.Disabled = true
	}
}

func (p *backendPool) disableUntil(b *backend, until time.Time, permanent bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b.DisabledUntil = until
	if permanent {
		b.Permanent = true
	}
}

func (p *backendPool) rehabilitateLocked() {
	for _, b := range p.backends {
		if b.Permanent {
			continue // never rehabilitate a confirmed auth failure
		}
		b.Successes /= 2
		b.Failures /= 2
		if b.Disabled && b.DisabledUntil.IsZero() && b.Failures <= 5 {
			b.Disabled = false
		}
	}
}

func proxyBackoff(ctx context.Context, sleep func(context.Context, time.Duration) error, attempt int) error {
	return sleep(ctx, time.Duration(1<<attempt)*time.Second)
}
