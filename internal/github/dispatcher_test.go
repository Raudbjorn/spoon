package github

import (
	"net/url"
	"testing"
	"time"
)

func TestBackendPoolRoundRobinAndRehabilitation(t *testing.T) {
	a, b, c := &backend{Login: "A"}, &backend{Login: "B"}, &backend{Login: "C"}
	pool := &backendPool{backends: []*backend{a, b, c}}
	var got []string
	for range 4 {
		next, err := pool.nextBackend(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, next.Login)
	}
	want := []string{"A", "B", "C", "A"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v want=%v", got, want)
		}
	}
	for range 6 {
		pool.reportFailure(a)
	}
	if !a.Disabled {
		t.Fatal("backend was not disabled after six failures")
	}
	pool.mu.Lock()
	pool.dispatches = 49
	pool.mu.Unlock()
	if _, err := pool.nextBackend(time.Now()); err != nil {
		t.Fatal(err)
	}
	if a.Disabled || a.Failures > 5 {
		t.Fatalf("backend not rehabilitated: disabled=%v failures=%d", a.Disabled, a.Failures)
	}
}

func TestProxyPoolRoundRobinAndRehabilitation(t *testing.T) {
	var urls []*url.URL
	for _, raw := range []string{"http://127.0.0.1:1", "http://127.0.0.2:2", "http://127.0.0.3:3"} {
		u, _ := url.Parse(raw)
		urls = append(urls, u)
	}
	pool := newStaticProxyPool(urls)
	want := []string{urls[0].String(), urls[1].String(), urls[2].String(), urls[0].String()}
	for i := range want {
		if got := pool.next().url.String(); got != want[i] {
			t.Fatalf("dispatch %d=%s want=%s", i, got, want[i])
		}
	}
	entry := pool.entries[0]
	for range 6 {
		pool.report(entry, false)
	}
	if !entry.stats.disabled {
		t.Fatal("proxy was not disabled after six failures")
	}
	pool.mu.Lock()
	pool.dispatches = 49
	pool.mu.Unlock()
	pool.next()
	if entry.stats.disabled || entry.stats.failures > 5 {
		t.Fatalf("proxy not rehabilitated: %+v", entry.stats)
	}
}

func TestDuplicateIdentityCollapsesSameLogin(t *testing.T) {
	first := &backend{Login: "octocat"}
	second := &backend{Login: "OCTOCAT"}
	third := &backend{Login: "hubot"}
	unique, duplicates := dedupeBackendsByLogin([]*backend{first, second, third})
	if len(unique) != 2 || duplicates != 1 {
		t.Fatalf("unique=%d duplicates=%d", len(unique), duplicates)
	}
	if !second.Disabled || first.Disabled || third.Disabled {
		t.Fatalf("unexpected disabled states: first=%v second=%v third=%v", first.Disabled, second.Disabled, third.Disabled)
	}
}
