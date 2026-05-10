package threadsops

import (
	"github.com/svnbjrn/spoon/internal/github"
)

// ReviewThreadWithPolicy embeds github.ReviewThread plus a derived requiresBody
// field for agent-facing JSON output.
type ReviewThreadWithPolicy struct {
	github.ReviewThread
	RequiresBody bool `json:"requiresBody"`
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
type BulkResult struct {
	Succeeded []string      `json:"succeeded"`
	Failed    []BulkFailure `json:"failed"`
	Skipped   []BulkSkip    `json:"skipped"`
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
