package github

import (
	"context"
	"sync"
	"time"
)

// limiter is a minimal mutex-guarded token bucket, safe for concurrent use.
// Tokens refill lazily from elapsed wall-clock; Wait blocks until a token is
// available or the context is cancelled. The clock and sleep are injectable so
// tests can run without real time.
type limiter struct {
	mu     sync.Mutex
	tokens float64
	rate   float64 // tokens per second
	burst  float64 // max tokens
	last   time.Time

	now   func() time.Time                           // injectable; defaults to time.Now
	sleep func(context.Context, time.Duration) error // injectable; defaults to ctxSleep
}

func newLimiter(rate, burst float64) *limiter {
	if burst < 1 {
		burst = 1
	}
	l := &limiter{rate: rate, burst: burst, tokens: burst, now: time.Now, sleep: ctxSleep}
	l.last = l.now()
	return l
}

func newLimiterRPM(rpm, burst float64) *limiter {
	return newLimiter(rpm/60, burst)
}

// ctxSleep sleeps for d or until ctx is cancelled, whichever comes first.
func ctxSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetRate updates the refill rate (tokens/sec) at runtime. Non-positive values
// are ignored.
func (l *limiter) SetRate(r float64) {
	if r <= 0 {
		return
	}
	l.mu.Lock()
	l.rate = r
	l.mu.Unlock()
}

// SetRPM updates the refill rate in requests per minute.
func (l *limiter) SetRPM(rpm float64) {
	l.SetRate(rpm / 60)
}

// Wait blocks until one token is available or ctx is cancelled.
func (l *limiter) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		now := l.now()
		if elapsed := now.Sub(l.last).Seconds(); elapsed > 0 {
			l.tokens = min(l.burst, l.tokens+elapsed*l.rate)
			l.last = now
		}
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		deficit := 1 - l.tokens
		rate := l.rate
		l.mu.Unlock()

		if rate <= 0 {
			rate = 1e-6
		}
		// Sleep outside the lock so other goroutines can refill/proceed. Guard
		// against a zero/negative duration (float→Duration truncation when the
		// bucket is a hair below full), which would otherwise busy-spin.
		wait := time.Duration(deficit / rate * float64(time.Second))
		if wait <= 0 {
			wait = time.Nanosecond
		}
		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
	}
}
