// cmd/spn/repo.go
package main

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/repo"
)

// repoCentralityFn is indirected so tests can stub it without hitting GitHub.
var repoCentralityFn = func(ctx context.Context, treeSrc repo.TreeSource, commitSrc repo.CommitSource, provider, owner, repoName string, sampleSize int) (repo.DirectoryCentrality, error) {
	return repo.Compute(ctx, treeSrc, commitSrc, provider, owner, repoName, sampleSize)
}

// repoCheckAuthFn is indirected so tests can stub GitHub auth.
var repoCheckAuthFn = func() (*gh.Client, gh.AuthStatus, error) {
	return gh.CheckAuth()
}

func runRepo(args []string) int { return runRepoWith(args, os.Stdout, os.Stderr) }

func runRepoWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (centrality)", agentio.RemediationBadInput("repo", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "centrality":
		return doRepoCentrality(rest, stdout, stderr)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("repo", "")).Emit(stderr)
	}
}

func doRepoCentrality(args []string, stdout, stderr io.Writer) int {
	var repoArg, forgeFlag string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--forge":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--forge requires a value", agentio.RemediationBadInput("repo", "centrality")).Emit(stderr)
			}
			i++
			forgeFlag = strings.ToLower(args[i])
		case "--forge-host":
			// Accept --forge-host but we don't use it; only GitHub is supported.
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--forge-host requires a value", agentio.RemediationBadInput("repo", "centrality")).Emit(stderr)
			}
			i++ // consume the value
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("repo", "centrality")).Emit(stderr)
			}
			if repoArg != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("repo", "centrality")).Emit(stderr)
			}
			repoArg = args[i]
		}
	}
	if repoArg == "" {
		return agentio.NewError(agentio.CodeBadInput, "missing repository argument (owner/repo)", agentio.RemediationBadInput("repo", "centrality")).Emit(stderr)
	}
	if forgeFlag != "" && forgeFlag != "github" {
		return agentio.NewError(agentio.CodeBadInput, "repo centrality only supports GitHub; got --forge="+forgeFlag, agentio.RemediationBadInput("repo", "centrality")).Emit(stderr)
	}

	owner, repoName := splitRepoArg(repoArg)
	if owner == "" || repoName == "" {
		return agentio.NewError(agentio.CodeBadInput, "invalid repo format: use owner/repo", agentio.RemediationBadInput("repo", "centrality")).Emit(stderr)
	}

	client, _, err := repoCheckAuthFn()
	if err != nil {
		return agentio.NewError(agentio.CodeAuthRequired, "GitHub auth: "+err.Error(), agentio.RemediationAuthRequired()).Emit(stderr)
	}

	treeSrc := &gh.TreeSourceForRepo{Client: client}
	commitSrc := &gh.CommitSourceForRepo{Client: client}

	ctx := context.Background()
	dc, err := repoCentralityFn(ctx, treeSrc, commitSrc, "github", owner, repoName, 0)
	if err != nil {
		return agentio.NewError(agentio.CodeUpstream, "repo centrality: "+err.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}

	if err := agentio.WriteJSON(stdout, dc); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
