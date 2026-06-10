package github

import (
	"context"
	"testing"
	"time"
)

func TestLimiter_BurstThenRefill(t *testing.T) {
	l := newLimiter(1.0, 1.0) // 1 token/sec, burst 1
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	l.last = now
	var slept time.Duration
	l.sleep = func(_ context.Context, d time.Duration) error {
		slept += d
		now = now.Add(d) // advance the fake clock so the bucket refills
		return nil
	}
	ctx := context.Background()

	if err := l.Wait(ctx); err != nil { // burst token → no sleep
		t.Fatal(err)
	}
	if slept != 0 {
		t.Errorf("first Wait should not sleep, slept=%v", slept)
	}
	if err := l.Wait(ctx); err != nil { // empty bucket → ~1s
		t.Fatal(err)
	}
	if slept < time.Second {
		t.Errorf("second Wait should sleep ~1s, slept=%v", slept)
	}
}

func TestLimiter_GrantsBoundedByRate(t *testing.T) {
	// Over a 10s window at 2 tokens/s with burst 2, grants must not exceed
	// rate*window + burst = 22.
	l := newLimiter(2.0, 2.0)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	l.last = now
	l.sleep = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	ctx := context.Background()
	grants := 0
	start := now
	for now.Sub(start) < 10*time.Second {
		if err := l.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		grants++
		if grants > 1000 {
			t.Fatal("runaway")
		}
	}
	if max := 2.0*10 + 2; float64(grants) > max+1 {
		t.Errorf("granted %d over window, exceeds rate*window+burst=%v", grants, max)
	}
}

func TestLimiter_CtxCancel(t *testing.T) {
	l := newLimiter(0.001, 1.0)
	_ = l.Wait(context.Background()) // drain the burst token
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestRefillRate(t *testing.T) {
	t.Run("unknown budget → burst (fast)", func(t *testing.T) {
		if r := (&Client{authenticated: true}).refillRate(); r != refillBurst {
			t.Errorf("rate=%v want %v", r, refillBurst)
		}
	})
	t.Run("healthy headroom → burst (fast)", func(t *testing.T) {
		c := &Client{rateLimit: RateLimit{Limit: 5000, Remaining: 4000, Reset: time.Now().Add(1000 * time.Second)}}
		if r := c.refillRate(); r != refillBurst {
			t.Errorf("rate=%v want %v", r, refillBurst)
		}
	})
	t.Run("mid headroom paces over reset", func(t *testing.T) {
		// 1500/5000 = 30% (between 20% and 50%): rate = 1500/1000 = 1.5
		c := &Client{rateLimit: RateLimit{Limit: 5000, Remaining: 1500, Reset: time.Now().Add(1000 * time.Second)}}
		if r := c.refillRate(); r < 1.3 || r > 1.7 {
			t.Errorf("rate=%v want ~1.5", r)
		}
	})
	t.Run("low headroom applies slowdown", func(t *testing.T) {
		// 500/5000 = 10% (<20%): base 500/1000=0.5, ×0.25 = 0.125.
		c := &Client{rateLimit: RateLimit{Limit: 5000, Remaining: 500, Reset: time.Now().Add(1000 * time.Second)}}
		if r := c.refillRate(); r < 0.10 || r > 0.15 {
			t.Errorf("rate=%v want ~0.125 (slowed)", r)
		}
	})
}
