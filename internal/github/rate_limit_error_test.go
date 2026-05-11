package github

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRateLimitError_Error(t *testing.T) {
	rl := &RateLimitError{ResetAt: time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)}
	if !strings.Contains(rl.Error(), "rate limit exceeded") {
		t.Errorf("missing prefix: %q", rl.Error())
	}
}

func TestRateLimitError_Unwrap(t *testing.T) {
	cause := errors.New("upstream")
	rl := &RateLimitError{cause: cause}
	if errors.Unwrap(rl) != cause {
		t.Error("Unwrap returned wrong cause")
	}
}

func TestRateLimitError_RetryAfterSeconds_clampsNegative(t *testing.T) {
	rl := &RateLimitError{ResetAt: time.Now().Add(-1 * time.Hour)}
	if rl.RetryAfterSeconds() != 0 {
		t.Errorf("expected clamped 0, got %d", rl.RetryAfterSeconds())
	}
}

func TestRateLimitError_RetryAfterSeconds_positive(t *testing.T) {
	rl := &RateLimitError{ResetAt: time.Now().Add(120 * time.Second)}
	got := rl.RetryAfterSeconds()
	if got < 110 || got > 130 {
		t.Errorf("expected ~120s, got %d", got)
	}
}

func TestRateLimitError_errorsAs(t *testing.T) {
	rl := &RateLimitError{ResetAt: time.Now().Add(time.Minute)}
	var target *RateLimitError
	if !errors.As(error(rl), &target) {
		t.Error("errors.As failed on direct *RateLimitError")
	}
}
