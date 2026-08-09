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

// gitEnv strips inherited GIT_* environment variables -- GIT_DIR in
// particular would make cmd.Dir a no-op and point every operation at
// whatever repository the calling process happens to be inside (a git hook,
// `rebase --exec`, etc), silently mutating it instead of the scratch repo --
// and disables interactive terminal credential prompting, which would
// otherwise block the subprocess on /dev/tty for a private or deleted fork
// until localScanTimeout kills it. See noPromptArgs for the other two ways
// git can still ask for a password once the terminal is closed off.
func gitEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0")
}

// noPromptArgs closes the two credential paths GIT_TERMINAL_PROMPT=0 does
// not: an askpass helper (git prompt.c consults GIT_ASKPASS, then
// core.askPass, then SSH_ASKPASS, and treats an empty value as unset -- so
// setting core.askPass empty short-circuits SSH_ASKPASS too), and credential
// helpers, where an interactive one (Git Credential Manager) can pop a GUI
// or browser and sit there. An empty credential.helper value resets the
// whole helper list, per git-config(1).
//
// This is deliberately a hard reset rather than a terminal-only block: a
// private or credential-gated repo is out of scope for the local path, which
// clones over anonymous https and never carries the API token. Failing fast
// here drops that fork to REST ScanBranches -- which does have the token and
// gets the right answer -- instead of hanging out the full localScanTimeout
// for a scan that could not have worked anyway.
var noPromptArgs = []string{"-c", "credential.helper=", "-c", "core.askPass="}

func (execGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	// Prepended here rather than at the call sites so the scripted keys in
	// localbranchscan_test.go's fakeGitRunner stay readable, and so the
	// error message below quotes the caller's own argv without this noise.
	argv := append(append([]string{}, noPromptArgs...), args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = gitEnv()
	// Bounds how long Run waits for stdout/stderr pipes to close after ctx
	// cancellation kills the process -- without it a subprocess that leaves
	// a descendant holding those pipes open can wedge this call (and the
	// worker-pool goroutine calling it) forever, past both the context
	// cancellation and the process's own death.
	cmd.WaitDelay = 10 * time.Second
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

const (
	// maxLocalScanBranches caps how many candidates probeAhead runs against.
	// Applied AFTER sorting by real tip date (see listForkTips) -- applying
	// it before the sort would recreate the exact alphabetical-cap coverage
	// bug (forksGraphQLQuery/sortBranches) this feature exists to fix, since
	// git fetch's default refspec (and ls-remote) advertise refs in
	// alphabetical order, not recency order.
	maxLocalScanBranches = 20

	// Shallow-fetch depth for the upstream baseline, and the deepen
	// step/attempt ceiling used when that depth doesn't reach a common
	// ancestor with a candidate branch. Most forks diverge shallowly;
	// deepening is the exception path.
	initialFetchDepth = 50
	deepenStep        = 200
	maxDeepenAttempts = 4

	// localScanTimeout bounds only the local git subprocess work (bulk
	// fetch, tip listing, per-candidate merge-base probing) -- not the REST
	// calls afterward (tipUpstreamed, the winner's FetchCompare), which run
	// under the caller's own context instead. Sharing one deadline let slow
	// git work eat the REST leg's budget, so a slow-but-successful local
	// scan looked identical to a dead one and forced a full REST re-scan of
	// work already done.
	localScanTimeout = 30 * time.Second
)

// branchTip is one fork branch's identity as read from local refs after
// fetchAllForkHeads -- name, tip SHA, and the tip's real committer date.
type branchTip struct {
	Name string
	SHA  string
	Date time.Time
}

// fetchAllForkHeads bulk-fetches every branch on the fork to depth 1 in one
// call with no per-branch arguments: the bare remote name uses the default
// refspec `remote add` configured (+refs/heads/*:refs/remotes/fork/*), so no
// untrusted branch name is ever interpolated into this command's argv. This
// is what actually closes the git-fetch argument-injection surface -- a
// hostile branch literally named e.g. "--upload-pack=..." never appears as a
// positional arg here or anywhere else in this file: the only other place a
// bare branch name would otherwise appear is prefixed with "fork/" or
// "upstream/" first (see probeAhead), which can't start with "-" either.
func fetchAllForkHeads(ctx context.Context, runner gitRunner, scratchDir string) error {
	_, err := runner.Run(ctx, scratchDir, "fetch", "-q", "--depth=1", "--filter=blob:none", "--", "fork")
	return err
}

// listForkTips enumerates every branch fetched by fetchAllForkHeads, purely
// from local refs -- no network call, and (unlike git ls-remote, which
// returns only name+SHA) gives each tip's real committer date for free, read
// off the object already fetched. That date is what lets candidates be
// sorted by actual recency BEFORE maxLocalScanBranches discards anything.
func listForkTips(ctx context.Context, runner gitRunner, scratchDir string) ([]branchTip, error) {
	out, err := runner.Run(ctx, scratchDir, "for-each-ref",
		"--format=%(refname:short)|%(objectname)|%(committerdate:iso-strict)",
		"--", "refs/remotes/fork/")
	if err != nil {
		return nil, err
	}
	var tips []branchTip
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "|", 3)
		if len(fields) != 3 {
			continue
		}
		name := strings.TrimPrefix(fields[0], "fork/")
		if name == "" || name == "HEAD" {
			continue // defensive: skip the remote's own HEAD pseudo-ref, if present
		}
		date, derr := time.Parse(time.RFC3339, fields[2])
		if derr != nil {
			continue
		}
		tips = append(tips, branchTip{Name: name, SHA: fields[1], Date: date})
	}
	return tips, nil
}

// probeAhead computes one candidate's ahead-count via merge-base + rev-list,
// deepening both remotes together (again: no branch name as a bare
// positional arg, ever) when the current depth doesn't reach a common
// ancestor. Returns the depth actually reached so the caller can carry it
// forward as the next candidate's starting point -- deepening tends to help
// every remaining candidate, since it's the same upstream graph being
// widened each time.
func probeAhead(ctx context.Context, runner gitRunner, scratchDir, upstreamBranch, forkBranch string, startDepth int) (ahead int, mergeBase string, reachedDepth int, err error) {
	depth := startDepth
	for attempt := 0; ; attempt++ {
		out, merr := runner.Run(ctx, scratchDir, "merge-base", "upstream/"+upstreamBranch, "fork/"+forkBranch)
		if merr == nil {
			mergeBase = strings.TrimSpace(string(out))
			break
		}
		if attempt >= maxDeepenAttempts {
			return 0, "", depth, fmt.Errorf("no common ancestor with upstream after %d deepen attempts: %w", attempt, merr)
		}
		depth += deepenStep
		if _, derr := runner.Run(ctx, scratchDir, "fetch", "-q", fmt.Sprintf("--deepen=%d", deepenStep), "--filter=blob:none", "--", "upstream"); derr != nil {
			return 0, "", depth, derr
		}
		if _, derr := runner.Run(ctx, scratchDir, "fetch", "-q", fmt.Sprintf("--deepen=%d", deepenStep), "--filter=blob:none", "--", "fork"); derr != nil {
			return 0, "", depth, derr
		}
	}

	aheadOut, err := runner.Run(ctx, scratchDir, "rev-list", "--count", mergeBase+"..fork/"+forkBranch)
	if err != nil {
		return 0, "", depth, err
	}
	ahead, err = strconv.Atoi(strings.TrimSpace(string(aheadOut)))
	if err != nil {
		return 0, "", depth, fmt.Errorf("parse ahead count: %w", err)
	}
	return ahead, mergeBase, depth, nil
}

// branchProbe is one candidate branch's confirmed local divergence.
type branchProbe struct {
	Name    string
	Ahead   int
	TipSHA  string
	TipDate time.Time
	BaseSHA string // merge-base with upstream
}

// ScanBranchesLocal is the local-git alternative to ScanBranches: it
// discovers every branch on the fork via a bulk depth-1 fetch (no cap, no
// GraphQL/REST budget spent) and computes each candidate's ahead-count
// locally via merge-base/rev-list, then walks survivors exactly like
// ScanBranches does -- most-recent first (by each tip's real committer
// date), first genuine (non-upstreamed) branch wins, REST tipUpstreamed
// unchanged (just fed a locally-derived tip SHA via a synthetic
// CompareResult instead of one parsed out of a REST compare response).
//
// The winning branch's rich CompareResult (diffs, MNA-feeding data) still
// comes from one REST FetchCompare call once a genuine winner is confirmed:
// local git replaces branch discovery and the ahead>0 filter, not diff-stat
// computation. That REST call fires at most once per fork (for the confirmed
// winner) -- but if it fails or the fork is gone, this returns an error
// rather than a synthetic placeholder, so the caller (scanSideBranches) can
// fall back to a full REST ScanBranches instead of letting a thin,
// Performed=true stand-in escape into the cache as if it were real.
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
	// Mirrors ScanBranches' own reserve gate (branches.go) -- this path
	// still ends in REST calls (tipUpstreamed, the winner's FetchCompare)
	// and must not spend budget below the same protected reserve the
	// REST-only path respects.
	if c.Headroom() < minHeadroom {
		return nil, nil
	}

	runner := opts.runner
	upstreamURL := fmt.Sprintf("https://github.com/%s/%s.git", parentOwner, parentRepo)
	forkURL := fmt.Sprintf("https://github.com/%s/%s.git", fork.Owner.Login, fork.Name)

	scratchDir, err := os.MkdirTemp("", "spn-branchscan-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratchDir)

	gitCtx, cancel := context.WithTimeout(ctx, localScanTimeout)
	defer cancel()

	if _, err := runner.Run(gitCtx, scratchDir, "init", "-q"); err != nil {
		return nil, err
	}
	if _, err := runner.Run(gitCtx, scratchDir, "remote", "add", "--", "upstream", upstreamURL); err != nil {
		return nil, err
	}
	if _, err := runner.Run(gitCtx, scratchDir, "remote", "add", "--", "fork", forkURL); err != nil {
		return nil, err
	}

	depth := initialFetchDepth
	if _, err := runner.Run(gitCtx, scratchDir, "fetch", "-q", fmt.Sprintf("--depth=%d", depth), "--filter=blob:none", "--", "upstream", parentBranch); err != nil {
		return nil, err
	}
	if err := fetchAllForkHeads(gitCtx, runner, scratchDir); err != nil {
		return nil, err
	}
	tips, err := listForkTips(gitCtx, runner, scratchDir)
	if err != nil {
		return nil, err
	}

	sort.SliceStable(tips, func(i, j int) bool {
		if !tips[i].Date.Equal(tips[j].Date) {
			return tips[i].Date.After(tips[j].Date)
		}
		return tips[i].SHA < tips[j].SHA // deterministic tie-break only, not a recency signal
	})

	var probes []branchProbe
	attempted, failed := 0, 0
	// Whether the cap actually cut candidates off, which is not the same as
	// attempted == maxLocalScanBranches: a fork with exactly that many
	// eligible branches exhausts the list without anything being skipped.
	truncated := false
	for _, tip := range tips {
		if tip.Name == fork.DefaultBranch {
			continue
		}
		if attempted >= maxLocalScanBranches {
			truncated = true
			break
		}
		attempted++

		ahead, mergeBase, reachedDepth, perr := probeAhead(gitCtx, runner, scratchDir, parentBranch, tip.Name, depth)
		if perr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			failed++
			continue // this one candidate's probe failed; keep trying the rest
		}
		depth = reachedDepth
		if ahead <= 0 {
			continue
		}
		probes = append(probes, branchProbe{Name: tip.Name, Ahead: ahead, TipSHA: tip.SHA, TipDate: tip.Date, BaseSHA: mergeBase})
	}

	// Every candidate actually attempted failed to probe (deepen exhausted,
	// subprocess error) -- that is "could not scan", not "scanned and found
	// nothing". Returning (nil, nil) here would look identical to a clean
	// scan to scanSideBranches and skip the REST fallback entirely, even
	// though GitHub's server-side compare (with full history) might still
	// find real divergence these probes gave up on.
	if attempted > 0 && failed == attempted {
		return nil, fmt.Errorf("all %d local branch probes failed", failed)
	}

	// The cap cut candidates off and nothing divergent turned up in the ones
	// that survived it. Unlike an untruncated empty result, this is not "the
	// fork has no side-branch work": the skipped candidates were never
	// looked at, so the honest answer is
	// inconclusive. Returning (nil, nil) here would tell scanSideBranches the
	// scan succeeded and suppress the REST fallback, which is exactly the
	// coverage regression this path promises never to cause -- REST
	// ScanBranches walks a different (GraphQL-supplied, alphabetical)
	// candidate set and may well hold a divergent branch this cap excluded.
	// An error, not (nil, nil): the headroom bail-out above deliberately
	// returns (nil, nil) to keep REST from spending the reserve, and these
	// two cases must not collapse into one.
	if truncated && len(probes) == 0 {
		return nil, fmt.Errorf("hit the %d-candidate cap with no divergent branch found; inconclusive", maxLocalScanBranches)
	}

	sort.SliceStable(probes, func(i, j int) bool {
		if !probes[i].TipDate.Equal(probes[j].TipDate) {
			return probes[i].TipDate.After(probes[j].TipDate)
		}
		return probes[i].TipSHA < probes[j].TipSHA
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
			Performed:       true,
			AheadBy:         p.Ahead,
			TotalCommits:    1,
			Commits:         []Commit{{SHA: p.TipSHA}},
			BaseSHA:         p.BaseSHA,
			HeadSHA:         p.TipSHA,
			MergeBaseCommit: Commit{SHA: p.BaseSHA},
		}
		up, pr, upErr := c.tipUpstreamed(ctx, upstream, fork.Owner.Login, fork.Name, synthetic)
		if upErr != nil {
			return fallback, upErr
		}
		if up {
			// Remember the most-recent upstreamed branch, but keep looking
			// for genuine work on an older branch -- mirrors ScanBranches.
			// Upstreamed forks get a dedicated scoring penalty regardless of
			// diff richness (heat.ApplyPenalties), so the thin synthetic is
			// fine here; it never needs to be REST-verified.
			if fallback == nil {
				fallback = &BranchScan{Compare: synthetic, Branch: p.Name, Upstreamed: true, UpstreamedPR: pr}
			}
			continue
		}

		// Confirmed genuine work: the synthetic never becomes the final
		// answer for a real winner -- only a REST-verified CompareResult
		// does. A failure here (network, or a 404 for a fork gone between
		// the bulk fetch and now -- FetchCompare returns Performed=false,
		// nil error on a 404) is not masked with the thin placeholder: it
		// errors so scanSideBranches falls back to REST ScanBranches
		// entirely, instead of letting a Performed=true, zero-diff stand-in
		// escape into the cache as if it were a real compare.
		cmp, cerr := c.FetchCompare(ctx, parentOwner, parentRepo, parentBranch, fork.Owner.Login, p.Name)
		if cerr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fallback, ctxErr
			}
			return nil, fmt.Errorf("REST compare failed for confirmed local winner %s: %w", p.Name, cerr)
		}
		if !cmp.Performed {
			return nil, fmt.Errorf("REST compare for confirmed local winner %s did not run (fork gone or made private?)", p.Name)
		}
		return &BranchScan{Compare: cmp, Branch: p.Name}, nil
	}

	return fallback, nil
}
