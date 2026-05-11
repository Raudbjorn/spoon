package agentio

import (
	"encoding/json"
	"io"
)

// Code is the stable error category surfaced in the JSON envelope.
type Code string

const (
	CodeBadInput     Code = "bad_input"
	CodeAuthRequired Code = "auth_required"
	CodeAuthScope    Code = "auth_scope_missing"
	CodePolicy       Code = "policy_violation"
	CodeNotFound     Code = "not_found"
	CodeUpstream     Code = "upstream_error"
	CodeRateLimited  Code = "rate_limited"
	CodeInternal     Code = "internal"
)

// Error is the spn-side error envelope. Build with NewError and call Emit to
// write JSON to stderr and obtain the process exit code.
type Error struct {
	Code              Code
	Message           string
	Remediation       string
	Retryable         bool
	RetryAfterSeconds *int
	Details           map[string]any
}

// NewError constructs an Error. Retryable defaults from the code.
func NewError(code Code, message, remediation string) *Error {
	return &Error{
		Code:        code,
		Message:     message,
		Remediation: remediation,
		Retryable:   defaultRetryable(code),
	}
}

// WithDetails attaches a details map. Returns the receiver for chaining.
func (e *Error) WithDetails(d map[string]any) *Error {
	e.Details = d
	return e
}

// WithRetryAfter attaches retry_after_seconds. Negative values are clamped
// to 0 — agents that consume the envelope shouldn't have to handle negative
// sleeps.
func (e *Error) WithRetryAfter(seconds int) *Error {
	if seconds < 0 {
		seconds = 0
	}
	e.RetryAfterSeconds = &seconds
	return e
}

// Emit writes the envelope as JSON to w and returns the process exit code.
// w is typically os.Stderr — agent callers reserve stdout for success data.
func (e *Error) Emit(w io.Writer) int {
	body := map[string]any{
		"code":        string(e.Code),
		"message":     e.Message,
		"remediation": e.Remediation,
		"retryable":   e.Retryable,
	}
	if e.RetryAfterSeconds != nil {
		body["retry_after_seconds"] = *e.RetryAfterSeconds
	}
	if e.Details != nil {
		body["details"] = e.Details
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Write errors are intentionally discarded: if the writer is unusable
	// (closed pipe, full disk) the caller still gets a meaningful exit code.
	_ = enc.Encode(map[string]any{"error": body})
	return ExitCode(e.Code)
}

// ExitCode returns the process exit code for a given code.
func ExitCode(c Code) int {
	switch c {
	case CodeBadInput, CodeAuthRequired, CodeAuthScope, CodePolicy:
		return 2
	default:
		return 1
	}
}

func defaultRetryable(c Code) bool {
	switch c {
	case CodeRateLimited, CodeUpstream:
		return true
	default:
		return false
	}
}
