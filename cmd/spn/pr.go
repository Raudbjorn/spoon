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
		return github.PullRequestStatus{}, agentio.NewError(agentio.Code(opErr.Code), opErr.Message, agentio.RemediationUpstream())
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
	owner, repo, number, ok := resolvePRRef(args[0], "pr", "status", stderr)
	if !ok {
		return 2
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
