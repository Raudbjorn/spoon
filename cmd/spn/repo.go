// cmd/spn/repo.go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/mdg"
	"github.com/svnbjrn/spoon/internal/repo"
)

// repoCentralityFn is indirected so tests can stub it without hitting GitHub.
var repoCentralityFn = func(ctx context.Context, treeSrc repo.TreeSource, commitSrc repo.CommitSource, provider, owner, repoName string, sampleSize int) (repo.DirectoryCentrality, error) {
	return repo.Compute(ctx, treeSrc, commitSrc, provider, owner, repoName, sampleSize)
}

// repoCheckAuthFn is the legacy test seam. Production routing calls
// repoCheckAuthWithEffective with the Bootstrap-owned effective configuration.
var repoCheckAuthFn func() (*gh.Client, gh.AuthStatus, error)

var repoCheckAuthWithEffective = func(effective config.EffectiveConfig) (*gh.Client, gh.AuthStatus, error) {
	if repoCheckAuthFn != nil {
		return repoCheckAuthFn()
	}
	return gh.CheckAuthWithEffective(effective)
}

// repoMDGCentralityFn is the test-stubbable MDG centrality entry. Production
// flow: clone upstream to tempdir → mdg.BuildCentrality → return as
// repo.Centrality interface.
var repoMDGCentralityFn = func(ctx context.Context, provider, owner, repoName string) (repo.Centrality, error) {
	tmp, err := os.MkdirTemp("", "spn-mdg-")
	if err != nil {
		return nil, fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := mdg.ShallowClone(ctx, provider, owner, repoName, tmp); err != nil {
		return nil, fmt.Errorf("shallow clone: %w", err)
	}
	c, err := mdg.BuildCentrality(ctx, tmp, provider, owner, repoName, "", mdg.BuildOptions{})
	if err != nil {
		return nil, fmt.Errorf("build centrality: %w", err)
	}
	return c, nil
}

func runRepo(args []string) int {
	boot := config.Bootstrap(os.Stderr)
	return runRepoWithEffective(args, os.Stdout, os.Stderr, config.ResolveEffectiveConfig(boot.Config, nil, config.EnvironmentSnapshot()))
}

// runRepoWith is the package test seam. Production dispatch calls
// runRepoWithEffective with its startup-owned configuration.
func runRepoWith(args []string, stdout, stderr io.Writer) int {
	return runRepoWithEffective(args, stdout, stderr, config.ResolveEffectiveConfig(nil, nil, config.EnvironmentSnapshot()))
}

func runRepoWithEffective(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (centrality|search)", agentio.RemediationBadInput("repo", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "centrality":
		return doRepoCentrality(rest, stdout, stderr, effective)
	case "search":
		return doRepoSearch(rest, stdout, stderr, effective)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("repo", "")).Emit(stderr)
	}
}

func doRepoCentrality(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig) int {
	var repoArg, forgeFlag string
	var fullMDG bool
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
		case "--full-mdg":
			fullMDG = true
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

	ctx := context.Background()

	if fullMDG {
		c, err := repoMDGCentralityFn(ctx, "github", owner, repoName)
		if err != nil {
			return agentio.NewError(agentio.CodeUpstream, "mdg centrality: "+err.Error(), agentio.RemediationUpstream()).Emit(stderr)
		}
		if err := agentio.WriteJSON(stdout, c); err != nil {
			return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
		return 0
	}

	client, _, err := repoCheckAuthWithEffective(effective)
	if err != nil {
		return agentio.NewError(agentio.CodeAuthRequired, "GitHub auth: "+err.Error(), agentio.RemediationAuthRequired()).Emit(stderr)
	}

	treeSrc := &gh.TreeSourceForRepo{Client: client}
	commitSrc := &gh.CommitSourceForRepo{Client: client}

	dc, err := repoCentralityFn(ctx, treeSrc, commitSrc, "github", owner, repoName, 0)
	if err != nil {
		return agentio.NewError(agentio.CodeUpstream, "repo centrality: "+err.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}

	if err := agentio.WriteJSON(stdout, dc); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
