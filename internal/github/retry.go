package github

import (
	"context"
	"errors"
	"time"
)

// doWithRetry executes fn; if it returns a *RateLimitError whose wait is within
// c.maxRateWait, it sleeps (ctx-aware) until the reset / Retry-After and retries
// once. If the required wait exceeds the cap, it returns the error unchanged so
// the caller surfaces a rate_limited result — fail-fast on a long primary-limit
// exhaustion, transparently absorb short secondary-limit blips.
func (c *Client) doWithRetry(ctx context.Context, fn func() error) error {
	err := fn()
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		return err
	}
	wait := time.Duration(rl.RetryAfterSeconds()) * time.Second
	if wait <= 0 || wait > c.maxRateWait {
		return err // beyond cap (or unknown) → surface it
	}
	if serr := c.sleepFn(ctx, wait); serr != nil {
		return serr
	}
	return fn() // single retry
}
