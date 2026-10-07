package github

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// RateLimitError is returned by *Client when the GitHub API responds with a
// rate-limit signal: HTTP 403 with X-RateLimit-Remaining: 0, or HTTP 429
// with Retry-After. ResetAt is the absolute time the window resets;
// Remaining is the documented per-window remaining count at the time of
// the failure (typically 0).
type RateLimitError struct {
	ResetAt   time.Time
	Remaining int
	cause     error
}

// Error implements the error interface.
func (e *RateLimitError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("github rate limit exceeded (resets %s): %v", e.ResetAt.UTC().Format(time.RFC3339), e.cause)
	}
	return fmt.Sprintf("github rate limit exceeded (resets %s)", e.ResetAt.UTC().Format(time.RFC3339))
}

// Unwrap supports errors.Is/errors.As walking the cause chain.
func (e *RateLimitError) Unwrap() error { return e.cause }

// AllBackendsRejectedError is returned by the backend pool when there is no
// usable identity to dispatch on: every configured token has been permanently
// rejected (HTTP 401), or the pool is empty. It is deliberately distinct from
// RateLimitError — there is no reset window to wait for, and retrying is futile
// until the operator re-authenticates. Surfacing this case as a rate limit
// yields a zero ResetAt and a retry_after_seconds=0 tight retry loop (#79).
type AllBackendsRejectedError struct {
	Rejected int // number of permanently-rejected (401) identities
}

// Error implements the error interface.
func (e *AllBackendsRejectedError) Error() string {
	switch e.Rejected {
	case 0:
		return "no github identity available for dispatch"
	case 1:
		return "github authentication failed: the configured token was rejected (401)"
	default:
		return fmt.Sprintf("github authentication failed: all %d configured tokens were rejected (401)", e.Rejected)
	}
}

// RetryAfterSeconds returns the number of seconds until ResetAt, clamped
// at zero when ResetAt is in the past.
func (e *RateLimitError) RetryAfterSeconds() int {
	s := int(time.Until(e.ResetAt).Seconds())
	if s < 0 {
		return 0
	}
	return s
}

// detectRateLimitFromHTTPError returns a *RateLimitError if the error is a
// go-github *ErrorResponse (REST or GraphQL transport) signalling a rate-limit condition; nil otherwise.
//
// Detection rules:
//   - HTTP 429 (Too Many Requests): parse Retry-After header
//   - HTTP 403 with X-RateLimit-Remaining: 0: parse X-RateLimit-Reset header
//
// GraphQL-level errors (gqlResponseError) carry no response headers, so for
// GraphQL only a non-2xx HTTP failure can be classified.
func detectRateLimitFromHTTPError(err error) *RateLimitError {
	if err == nil {
		return nil
	}
	status, h, ok := httpFailure(err)
	if !ok {
		return nil
	}

	if status == http.StatusTooManyRequests {
		ra := h.Get("Retry-After")
		reset := time.Now().Add(60 * time.Second)
		if secs, err := strconv.Atoi(ra); err == nil {
			reset = time.Now().Add(time.Duration(secs) * time.Second)
		} else if t, parseErr := http.ParseTime(ra); parseErr == nil {
			reset = t
		}
		return &RateLimitError{ResetAt: reset, Remaining: remainingFromHeaders(h), cause: err}
	}

	if status == http.StatusForbidden && h.Get("X-RateLimit-Remaining") == "0" {
		// Default to now so ResetAt is never the zero value (which would surface
		// as a year-0001 timestamp and retry_after_seconds=0 downstream).
		rl := &RateLimitError{ResetAt: time.Now(), Remaining: remainingFromHeaders(h), cause: err}
		if v := h.Get("X-RateLimit-Reset"); v != "" {
			if epoch, parseErr := strconv.ParseInt(v, 10, 64); parseErr == nil {
				rl.ResetAt = time.Unix(epoch, 0)
			}
		}
		// Retry-After, when present, is the authoritative back-off hint; prefer it.
		if ra := h.Get("Retry-After"); ra != "" {
			if secs, parseErr := strconv.Atoi(ra); parseErr == nil {
				rl.ResetAt = time.Now().Add(time.Duration(secs) * time.Second)
			} else if t, parseErr := http.ParseTime(ra); parseErr == nil {
				rl.ResetAt = t
			}
		}
		return rl
	}

	return nil
}

// remainingFromHeaders parses X-RateLimit-Remaining into an int, returning 0
// when the header is absent or malformed. GitHub reports a non-zero remaining
// count on secondary/abuse limits, so callers surfacing RateLimitError.Remaining
// see the real value rather than a hard-coded 0.
func remainingFromHeaders(h http.Header) int {
	if v := h.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}
