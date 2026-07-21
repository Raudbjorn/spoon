package github

import (
	"errors"
	"testing"
	"time"
)

// When every configured identity is permanently rejected (401), the pool must
// report an auth failure, NOT a rate limit. A RateLimitError here carries a zero
// ResetAt, which downstream renders as retry_after_seconds=0 and drives an agent
// into a tight retry loop against credentials that will never work (#79).
func TestNextBackendAllPermanentReturnsAuthFailure(t *testing.T) {
	p := &backendPool{backends: []*backend{
		{Login: "a", Permanent: true},
		{Login: "b", Permanent: true},
	}}
	_, err := p.nextBackend(time.Now())

	var rejected *AllBackendsRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("want *AllBackendsRejectedError, got %T: %v", err, err)
	}
	if rejected.Rejected != 2 {
		t.Fatalf("Rejected = %d, want 2", rejected.Rejected)
	}
	var rl *RateLimitError
	if errors.As(err, &rl) {
		t.Fatalf("must not also be a *RateLimitError (would drive a tight retry loop): %v", err)
	}
}

// An empty pool is likewise "no usable identity", not a rate limit.
func TestNextBackendEmptyPoolIsAuthFailure(t *testing.T) {
	p := &backendPool{}
	_, err := p.nextBackend(time.Now())
	var rejected *AllBackendsRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("want *AllBackendsRejectedError, got %T: %v", err, err)
	}
}

// A genuine timed cooldown (rate limit with a real reset time) must still
// surface as a *RateLimitError so the caller waits for the window.
func TestNextBackendTimedCooldownStillRateLimit(t *testing.T) {
	future := time.Now().Add(30 * time.Second)
	p := &backendPool{backends: []*backend{{Login: "a", DisabledUntil: future}}}
	_, err := p.nextBackend(time.Now())

	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want *RateLimitError, got %T: %v", err, err)
	}
	if rl.ResetAt.IsZero() {
		t.Fatal("ResetAt must be the cooldown time, not the zero value")
	}
	var rejected *AllBackendsRejectedError
	if errors.As(err, &rejected) {
		t.Fatalf("a timed cooldown must not surface as an auth failure: %v", err)
	}
}
