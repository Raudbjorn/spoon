package threadsops

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/github"
)

// ReviewThreadWithPolicy embeds github.ReviewThread plus a derived requiresBody
// field for agent-facing JSON output. Suggestions, when present, holds the
// parsed `suggestion` blocks from this thread's comments (document order).
// DryRun is set on output from a Resolve/UnresolveAll call invoked with
// DryRun=true to let consumers distinguish a previewed mutation from a real one.
type ReviewThreadWithPolicy struct {
	github.ReviewThread
	RequiresBody bool         `json:"requiresBody"`
	Suggestions  []Suggestion `json:"suggestions,omitempty"`
	DryRun       bool         `json:"dryRun,omitempty"`
	CodeContext  *CodeContext `json:"codeContext,omitempty"`
}

// FilterMode selects which review threads to surface in list/JSON output.
// Values mirror gh-pr-display's filter semantics.
type FilterMode string

const (
	// FilterAll shows every thread (resolved + unresolved, active + outdated).
	FilterAll FilterMode = "all"
	// FilterUnresolved shows only unresolved threads (spoon's historical default).
	FilterUnresolved FilterMode = "unresolved"
	// FilterResolvedActive shows resolved threads whose anchored code is still
	// active (not outdated).
	FilterResolvedActive FilterMode = "resolved-active"
	// FilterUnresolvedOutdated shows unresolved threads whose anchored code
	// has shifted (often safe to bulk-resolve).
	FilterUnresolvedOutdated FilterMode = "unresolved-outdated"
	// FilterCurrentUnresolved shows unresolved AND not outdated threads — the
	// most urgent set.
	FilterCurrentUnresolved FilterMode = "current-unresolved"
)

// ValidFilterModes is the canonical list, in stable order, of accepted
// --filter values. Used by callers to construct error remediation hints.
var ValidFilterModes = []FilterMode{
	FilterAll,
	FilterUnresolved,
	FilterResolvedActive,
	FilterUnresolvedOutdated,
	FilterCurrentUnresolved,
}

// ValidFilterModesCSV returns the valid modes as a comma-separated string,
// suitable for inclusion in error remediation text.
func ValidFilterModesCSV() string {
	parts := make([]string, len(ValidFilterModes))
	for i, m := range ValidFilterModes {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}

// ParseFilterMode parses a --filter value. Empty input returns FilterUnresolved
// (the default). Unknown values return an error listing the valid choices.
func ParseFilterMode(s string) (FilterMode, error) {
	if s == "" {
		return FilterUnresolved, nil
	}
	for _, m := range ValidFilterModes {
		if string(m) == s {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown filter mode %q (valid: %s)", s, ValidFilterModesCSV())
}

// NeedsResolvedFetch reports whether the underlying fetch must include
// resolved threads to satisfy this filter mode.
func (m FilterMode) NeedsResolvedFetch() bool {
	switch m {
	case FilterUnresolved, FilterCurrentUnresolved, FilterUnresolvedOutdated:
		return false
	default:
		// FilterAll and FilterResolvedActive require resolved threads.
		return true
	}
}

// Filter applies a FilterMode to a slice of ReviewThreadWithPolicy in O(n),
// preserving the caller-supplied order. This is the single source of truth
// for filtering — both spoon and spn route through here.
//
// Behavior (IsResolved, IsOutdated → included by which modes):
//
//	false, false  → all, unresolved, current-unresolved
//	false, true   → all, unresolved, unresolved-outdated
//	true,  false  → all, resolved-active
//	true,  true   → all
func Filter(in []ReviewThreadWithPolicy, mode FilterMode) []ReviewThreadWithPolicy {
	out := make([]ReviewThreadWithPolicy, 0, len(in))
	for _, t := range in {
		if includeThread(t.IsResolved, t.IsOutdated, mode) {
			out = append(out, t)
		}
	}
	return out
}

func includeThread(isResolved, isOutdated bool, mode FilterMode) bool {
	switch mode {
	case FilterAll:
		return true
	case FilterUnresolved:
		return !isResolved
	case FilterResolvedActive:
		return isResolved && !isOutdated
	case FilterUnresolvedOutdated:
		return !isResolved && isOutdated
	case FilterCurrentUnresolved:
		return !isResolved && !isOutdated
	default:
		// Unknown mode — be conservative and include nothing.
		return false
	}
}

// SortThreadsForList orders threads by first-comment time ASC, with thread ID
// as a deterministic tiebreaker. Callers wishing to expose --filter all should
// route through this so the output order is stable across invocations.
func SortThreadsForList(in []ReviewThreadWithPolicy) {
	sort.SliceStable(in, func(i, j int) bool {
		ai, aj := firstCommentTime(in[i].ReviewThread), firstCommentTime(in[j].ReviewThread)
		if ai != aj {
			return ai < aj
		}
		return in[i].ID < in[j].ID
	})
}

// AnnotateWithPolicy wraps a slice of ReviewThread into the policy-annotated form.
func AnnotateWithPolicy(in []github.ReviewThread) []ReviewThreadWithPolicy {
	out := make([]ReviewThreadWithPolicy, len(in))
	for i, t := range in {
		out[i] = ReviewThreadWithPolicy{ReviewThread: t, RequiresBody: t.RequiresBody()}
	}
	return out
}

// AnnotateOneWithPolicy returns a single annotated thread or nil.
func AnnotateOneWithPolicy(t *github.ReviewThread) *ReviewThreadWithPolicy {
	if t == nil {
		return nil
	}
	return &ReviewThreadWithPolicy{ReviewThread: *t, RequiresBody: t.RequiresBody()}
}

// BulkResult is the spn-facing outcome of resolve-all / unresolve-all.
// DryRun, when true, means the listed Succeeded IDs were NOT actually mutated;
// the operation only previewed what would have happened.
type BulkResult struct {
	Succeeded []string      `json:"succeeded"`
	Failed    []BulkFailure `json:"failed"`
	Skipped   []BulkSkip    `json:"skipped"`
	DryRun    bool          `json:"dryRun,omitempty"`
}

// BulkFailure captures one failure inside a bulk operation.
type BulkFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// BulkSkip captures one thread skipped by policy.
type BulkSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// OpError carries a stable code, retryability flag, and details. Callers
// translate this into the appropriate agentio.Error (spn) or text + exit code
// (spoon).
type OpError struct {
	Code      string
	Message   string
	Retryable bool
	Details   map[string]any
}

// Op error codes — mirror agentio.Code values exactly so translation is trivial.
const (
	OpCodeBadInput     = "bad_input"
	OpCodeAuthRequired = "auth_required"
	OpCodeAuthScope    = "auth_scope_missing"
	OpCodePolicy       = "policy_violation"
	OpCodeNotFound     = "not_found"
	OpCodeUpstream     = "upstream_error"
	OpCodeRateLimited  = "rate_limited"
	OpCodeInternal     = "internal"
)

// rateLimitedOpError converts a *github.RateLimitError into an OpError,
// or returns nil if the underlying error is not a rate-limit error.
func rateLimitedOpError(err error) *OpError {
	var rl *github.RateLimitError
	if !errors.As(err, &rl) {
		return nil
	}
	return &OpError{
		Code:      OpCodeRateLimited,
		Message:   "rate limit exceeded",
		Retryable: true,
		Details: map[string]any{
			"reset_at":            rl.ResetAt.UTC().Format(time.RFC3339),
			"retry_after_seconds": rl.RetryAfterSeconds(),
			"remaining":           rl.Remaining,
		},
	}
}
