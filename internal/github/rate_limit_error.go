package github

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
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
// *ghAPI.HTTPError signalling a rate-limit condition; nil otherwise.
//
// Detection rules:
//   - HTTP 429 (Too Many Requests): parse Retry-After header
//   - HTTP 403 with X-RateLimit-Remaining: 0: parse X-RateLimit-Reset header
//
// GraphQL errors do not expose response headers via ghAPI.GraphQLError, so
// rate-limit detection applies to REST endpoints only.
func detectRateLimitFromHTTPError(err error) *RateLimitError {
	if err == nil {
		return nil
	}
	var httpErr *ghAPI.HTTPError
	if !asHTTPError(err, &httpErr) {
		return nil
	}
	h := httpErr.Headers

	if httpErr.StatusCode == http.StatusTooManyRequests {
		ra := h.Get("Retry-After")
		reset := time.Now().Add(60 * time.Second)
		if secs, err := strconv.Atoi(ra); err == nil {
			reset = time.Now().Add(time.Duration(secs) * time.Second)
		} else if t, parseErr := http.ParseTime(ra); parseErr == nil {
			reset = t
		}
		return &RateLimitError{ResetAt: reset, cause: err}
	}

	if httpErr.StatusCode == http.StatusForbidden && h.Get("X-RateLimit-Remaining") == "0" {
		rl := &RateLimitError{cause: err}
		if v := h.Get("X-RateLimit-Reset"); v != "" {
			if epoch, parseErr := strconv.ParseInt(v, 10, 64); parseErr == nil {
				rl.ResetAt = time.Unix(epoch, 0)
			}
		}
		if v := h.Get("X-RateLimit-Remaining"); v != "" {
			if rem, perr := strconv.Atoi(v); perr == nil {
				rl.Remaining = rem
			}
		}
		return rl
	}

	return nil
}
