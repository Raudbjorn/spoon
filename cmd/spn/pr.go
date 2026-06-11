// cmd/spn/pr.go
package main

import (
	"context"
	"io"
	"os"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

// fetchPRStatus is overridable for tests; production calls threadsops.List
// to obtain the status as a side-output.
var fetchPRStatus = func(ctx context.Context, api threadsops.API, owner, repo string, number int) (github.PullRequestStatus, *agentio.Error) {
	status, _, opErr := threadsops.List(ctx, api, owner, repo, number, false)
	if opErr != nil {
		code := agentio.Code(opErr.Code)
		rem := agentio.RemediationUpstream()
		if code == agentio.CodeRateLimited {
			resetAt, _ := opErr.Details["reset_at"].(string)
			secs, _ := opErr.Details["retry_after_seconds"].(int)
			rem = agentio.RemediationRateLimited(resetAt, secs)
		}
		e := agentio.NewError(code, opErr.Message, rem)
		// Propagate the OpError's Retryable verdict so non-retryable
		// upstream errors don't falsely advertise retryability via the
		// agentio default (which is true for upstream_error).
		e.Retryable = opErr.Retryable
		if opErr.Details != nil {
			e = e.WithDetails(opErr.Details)
		}
		if code == agentio.CodeRateLimited {
			if secs, ok := opErr.Details["retry_after_seconds"].(int); ok && secs > 0 {
				e = e.WithRetryAfter(secs)
			}
		}
		return github.PullRequestStatus{}, e
	}
	return status, nil
}

func runPR(args []string) int { return runPRWith(args, os.Stdout, os.Stderr) }

func runPRWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (status)", agentio.RemediationBadInput("pr", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "status":
		return doPRStatus(rest, stdout, stderr)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("pr", "")).Emit(stderr)
	}
}

func doPRStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn pr status <pr-ref>", agentio.RemediationBadInput("pr", "status")).Emit(stderr)
	}
	owner, repo, number, ec, ok := resolvePRRef(args[0], "pr", "status", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	status, e := fetchPRStatus(context.Background(), api, owner, repo, number)
	if e != nil {
		return e.Emit(stderr)
	}
	if err := agentio.WriteJSON(stdout, status); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
