package github

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDoWithRetry_WithinCap_SleepsThenRetriesOnce(t *testing.T) {
	c := &Client{maxRateWait: 30 * time.Second}
	var slept time.Duration
	c.sleepFn = func(_ context.Context, d time.Duration) error { slept = d; return nil }

	calls := 0
	err := c.doWithRetry(context.Background(), func() error {
		calls++
		if calls == 1 {
			return &RateLimitError{ResetAt: time.Now().Add(2 * time.Second)}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if calls != 2 {
		t.Errorf("calls=%d want 2 (one retry)", calls)
	}
	if slept <= 0 {
		t.Errorf("expected a Retry-After sleep, slept=%v", slept)
	}
}

func TestDoWithRetry_BeyondCap_FailsFastNoSleep(t *testing.T) {
	c := &Client{maxRateWait: 30 * time.Second}
	slept := false
	c.sleepFn = func(_ context.Context, _ time.Duration) error { slept = true; return nil }

	calls := 0
	err := c.doWithRetry(context.Background(), func() error {
		calls++
		return &RateLimitError{ResetAt: time.Now().Add(time.Hour)} // 3600s > 30s cap
	})
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	if calls != 1 {
		t.Errorf("calls=%d want 1 (no retry beyond cap)", calls)
	}
	if slept {
		t.Error("must not sleep when wait exceeds cap")
	}
}

func TestDoWithRetry_NonRateLimitPassthrough(t *testing.T) {
	c := &Client{maxRateWait: 30 * time.Second}
	want := errors.New("boom")
	if got := c.doWithRetry(context.Background(), func() error { return want }); !errors.Is(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestDoWithRetry_CtxCancelDuringSleep(t *testing.T) {
	c := &Client{maxRateWait: 30 * time.Second}
	c.sleepFn = func(_ context.Context, _ time.Duration) error { return context.Canceled }
	err := c.doWithRetry(context.Background(), func() error {
		return &RateLimitError{ResetAt: time.Now().Add(2 * time.Second)}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v want context.Canceled", err)
	}
}
