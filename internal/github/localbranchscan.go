package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// gitRunner is the seam between local branch-scan logic and actual git
// subprocess execution. Production wires it to execGitRunner{}; tests
// substitute a fake. Mirrors internal/mdg/clone.go's cloneRunner.
type gitRunner interface {
	Run(ctx context.Context, dir string, args ...string) ([]byte, error) // stdout
}

type execGitRunner struct{}

func (execGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// gitOnPath reports whether the git binary is reachable via PATH.
func gitOnPath() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// remoteBranch is one ref returned by ls-remote.
type remoteBranch struct {
	Name string
	SHA  string
}

// listRemoteBranches runs `git ls-remote --heads <cloneURL>` and returns every
// branch tip, unfiltered and uncapped -- unlike the GraphQL refs query
// (forksGraphQLQuery in graphql.go), which fetches only 10 branches ordered
// alphabetically (GitHub's schema has no commit-date ordering for refs) and
// can silently miss a repo's true most-recently-active branches when there
// are more than 10. ls-remote costs nothing against the REST/GraphQL budget.
func listRemoteBranches(ctx context.Context, runner gitRunner, cloneURL string) ([]remoteBranch, error) {
	out, err := runner.Run(ctx, "", "ls-remote", "--heads", cloneURL)
	if err != nil {
		return nil, err
	}
	var branches []remoteBranch
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "refs/heads/")
		branches = append(branches, remoteBranch{Name: name, SHA: fields[0]})
	}
	return branches, nil
}

const (
	// maxLocalScanBranches caps how many candidates probeBranchAhead runs
	// against, now that ls-remote's uncapped listing replaces the GraphQL
	// top-10/5 cap -- a deliberate choice this time, not an accidental side
	// effect of a query limit.
	maxLocalScanBranches = 20

	// Shallow-fetch depth for the first attempt, and the deepen step/attempt
	// ceiling used when that depth doesn't reach a common ancestor with
	// upstream. Most forks diverge shallowly; deepening is the exception path.
	initialFetchDepth = 50
	deepenStep        = 200
	maxDeepenAttempts = 4

	// localScanTimeout bounds the whole per-fork local scan (every candidate
	// branch), not any single git subcommand -- mirrors
	// internal/embed/fastembed_provision.go's per-call-timeout-at-the-risky-
	// boundary pattern. On expiry the caller falls back to REST ScanBranches.
	localScanTimeout = 30 * time.Second
)

// branchProbe is one candidate branch's locally-derived divergence.
type branchProbe struct {
	Name    string
	Ahead   int
	TipSHA  string
	TipDate time.Time
	BaseSHA string // merge-base with upstream
}

// probeBranchAhead computes ahead-count and the real tip commit date for one
// branch, from local git state in scratchDir (which already has "upstream"
// and "fork" remotes configured and upstreamBranch fetched to startDepth).
// It deepens on merge-base failure (up to maxDeepenAttempts) before giving up
// -- a fork that diverged from upstream long ago needs more history than the
// shallow default to find a common ancestor. Returns the depth actually
// reached so the caller can carry it forward as the starting point for the
// next candidate, since deepening upstream tends to help every candidate.
func probeBranchAhead(ctx context.Context, runner gitRunner, scratchDir, upstreamBranch, forkBranch string, startDepth int) (branchProbe, int, error) {
	depth := startDepth
	if _, err := runner.Run(ctx, scratchDir, "fetch", "-q", fmt.Sprintf("--depth=%d", depth), "fork", forkBranch); err != nil {
		return branchProbe{}, depth, err
	}

	var mergeBase string
	for attempt := 0; ; attempt++ {
		out, err := runner.Run(ctx, scratchDir, "merge-base", "upstream/"+upstreamBranch, "fork/"+forkBranch)
		if err == nil {
			mergeBase = strings.TrimSpace(string(out))
			break
		}
		if attempt >= maxDeepenAttempts {
			return branchProbe{}, depth, fmt.Errorf("no common ancestor with upstream after %d deepen attempts: %w", attempt, err)
		}
		depth += deepenStep
		if _, err := runner.Run(ctx, scratchDir, "fetch", "-q", fmt.Sprintf("--deepen=%d", deepenStep), "upstream", upstreamBranch); err != nil {
			return branchProbe{}, depth, err
		}
		if _, err := runner.Run(ctx, scratchDir, "fetch", "-q", fmt.Sprintf("--deepen=%d", deepenStep), "fork", forkBranch); err != nil {
			return branchProbe{}, depth, err
		}
	}

	aheadOut, err := runner.Run(ctx, scratchDir, "rev-list", "--count", mergeBase+"..fork/"+forkBranch)
	if err != nil {
		return branchProbe{}, depth, err
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(string(aheadOut)))
	if err != nil {
		return branchProbe{}, depth, fmt.Errorf("parse ahead count: %w", err)
	}

	// The tip's real committer date, read off the object we already fetched
	// for the ahead-count -- zero marginal cost, and bound into the object's
	// own hash rather than merely reported by an API, unlike GraphQL's
	// committedDate field.
	dateOut, err := runner.Run(ctx, scratchDir, "log", "-1", "--format=%cI", "fork/"+forkBranch)
	if err != nil {
		return branchProbe{}, depth, err
	}
	tipDate, err := time.Parse(time.RFC3339, strings.TrimSpace(string(dateOut)))
	if err != nil {
		return branchProbe{}, depth, fmt.Errorf("parse tip date: %w", err)
	}

	shaOut, err := runner.Run(ctx, scratchDir, "rev-parse", "fork/"+forkBranch)
	if err != nil {
		return branchProbe{}, depth, err
	}

	return branchProbe{
		Name:    forkBranch,
		Ahead:   ahead,
		TipSHA:  strings.TrimSpace(string(shaOut)),
		TipDate: tipDate,
		BaseSHA: mergeBase,
	}, depth, nil
}

// ScanBranchesLocal is the local-git alternative to ScanBranches: it
// discovers every branch on the fork via ls-remote (no cap, no GraphQL/REST
// budget spent) and computes each candidate's ahead-count and real tip date
// locally, then walks survivors exactly like ScanBranches does -- most-recent
// first, first genuine (non-upstreamed) branch wins, REST tipUpstreamed
// unchanged (just fed a locally-derived tip SHA via a synthetic
// CompareResult instead of one parsed out of a REST compare response).
//
// The winning branch's rich CompareResult (diffs, MNA-feeding data) still
// comes from one REST FetchCompare call once a genuine winner is confirmed --
// local git replaces branch discovery and the ahead>0 filter, not diff-stat
// computation, which stays out of scope. That REST call only ever fires once
// per fork (for the confirmed winner), where today's REST-only ScanBranches
// can spend one per candidate scanned.
func (c *Client) ScanBranchesLocal(
	ctx context.Context,
	parentOwner, parentRepo, parentBranch string,
	fork ForkInfo,
) (*BranchScan, error) {
	return c.scanBranchesLocalWith(ctx, parentOwner, parentRepo, parentBranch, fork, localScanOpts{
		runner:       execGitRunner{},
		gitAvailable: gitOnPath(),
	})
}

// localScanOpts is the test seam for scanBranchesLocalWith, mirroring
// internal/mdg/clone.go's cloneOpts: production wires runner to
// execGitRunner{} and gitAvailable to a real PATH lookup; tests substitute a
// fake runner and force gitAvailable so no real subprocess ever runs.
type localScanOpts struct {
	runner       gitRunner
	gitAvailable bool
}

func (c *Client) scanBranchesLocalWith(
	ctx context.Context,
	parentOwner, parentRepo, parentBranch string,
	fork ForkInfo,
	opts localScanOpts,
) (*BranchScan, error) {
	if !opts.gitAvailable {
		return nil, errors.New("git not available on PATH")
	}

	ctx, cancel := context.WithTimeout(ctx, localScanTimeout)
	defer cancel()

	runner := opts.runner
	upstreamURL := fmt.Sprintf("https://github.com/%s/%s.git", parentOwner, parentRepo)
	forkURL := fmt.Sprintf("https://github.com/%s/%s.git", fork.Owner.Login, fork.Name)

	remoteBranches, err := listRemoteBranches(ctx, runner, forkURL)
	if err != nil {
		return nil, err
	}

	scratchDir, err := os.MkdirTemp("", "spn-branchscan-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratchDir)

	if _, err := runner.Run(ctx, scratchDir, "init", "-q"); err != nil {
		return nil, err
	}
	if _, err := runner.Run(ctx, scratchDir, "remote", "add", "upstream", upstreamURL); err != nil {
		return nil, err
	}
	if _, err := runner.Run(ctx, scratchDir, "remote", "add", "fork", forkURL); err != nil {
		return nil, err
	}
	depth := initialFetchDepth
	if _, err := runner.Run(ctx, scratchDir, "fetch", "-q", fmt.Sprintf("--depth=%d", depth), "upstream", parentBranch); err != nil {
		return nil, err
	}

	var probes []branchProbe
	scanned := 0
	for _, rb := range remoteBranches {
		if rb.Name == fork.DefaultBranch {
			continue
		}
		if scanned >= maxLocalScanBranches {
			break
		}
		scanned++

		probe, reachedDepth, perr := probeBranchAhead(ctx, runner, scratchDir, parentBranch, rb.Name, depth)
		if perr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			continue // skip branches whose probe fails, mirrors ScanBranches' REST-error handling
		}
		depth = reachedDepth
		if probe.Ahead <= 0 {
			continue
		}
		probes = append(probes, probe)
	}

	sort.SliceStable(probes, func(i, j int) bool {
		return probes[i].TipDate.After(probes[j].TipDate)
	})

	upstream := parentOwner + "/" + parentRepo
	var fallback *BranchScan
	for _, p := range probes {
		select {
		case <-ctx.Done():
			return fallback, ctx.Err()
		default:
		}

		synthetic := CompareResult{
			Performed:    true,
			AheadBy:      p.Ahead,
			TotalCommits: 1,
			Commits:      []Commit{{SHA: p.TipSHA}},
			BaseSHA:      p.BaseSHA,
			HeadSHA:      p.TipSHA,
		}
		up, pr, upErr := c.tipUpstreamed(ctx, upstream, fork.Owner.Login, fork.Name, synthetic)
		if upErr != nil {
			return fallback, upErr
		}
		if up {
			// Remember the most-recent upstreamed branch, but keep looking for
			// genuine work on an older branch -- mirrors ScanBranches.
			if fallback == nil {
				fallback = &BranchScan{Compare: synthetic, Branch: p.Name, Upstreamed: true, UpstreamedPR: pr}
			}
			continue
		}

		cmp, cerr := c.FetchCompare(ctx, parentOwner, parentRepo, parentBranch, fork.Owner.Login, p.Name)
		if cerr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fallback, ctxErr
			}
			// REST compare failed for the confirmed winner -- fall back to the
			// synthetic (ahead-count only) result rather than losing the signal.
			return &BranchScan{Compare: synthetic, Branch: p.Name}, nil
		}
		return &BranchScan{Compare: cmp, Branch: p.Name}, nil
	}

	return fallback, nil
}
