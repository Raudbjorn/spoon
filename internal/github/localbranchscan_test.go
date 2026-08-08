package github

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGitRunner scripts responses by command (joined args, space-separated,
// after stripping the leading "-q" quiet flag so scripting doesn't have to
// care about it). Mirrors internal/mdg/clone_test.go's fakeRunner.
type fakeGitRunner struct {
	cmds      [][]string
	responses map[string]fakeResponse
	// failMergeBaseTimes, when >0, makes the first N merge-base calls fail
	// before succeeding -- exercises the deepen-retry loop.
	failMergeBaseTimes int
	mergeBaseCalls     int
}

type fakeResponse struct {
	out []byte
	err error
}

func (f *fakeGitRunner) key(args []string) string {
	filtered := make([]string, 0, len(args))
	for _, a := range args {
		if a == "-q" {
			continue
		}
		filtered = append(filtered, a)
	}
	return strings.Join(filtered, " ")
}

func (f *fakeGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	// A real exec.CommandContext fails immediately against an already-done
	// context; mirror that so cancellation-propagation tests exercise the
	// real failure path instead of silently succeeding.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f.cmds = append(f.cmds, append([]string{dir}, args...))

	if len(args) > 0 && args[0] == "merge-base" {
		f.mergeBaseCalls++
		if f.mergeBaseCalls <= f.failMergeBaseTimes {
			return nil, errors.New("fatal: no common ancestor")
		}
	}

	if resp, ok := f.responses[f.key(args)]; ok {
		return resp.out, resp.err
	}
	return []byte{}, nil
}

func TestListRemoteBranches_ParsesLsRemoteOutput(t *testing.T) {
	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		"ls-remote --heads https://github.com/o/r.git": {
			out: []byte("aaa111\trefs/heads/main\nbbb222\trefs/heads/feature/quiet-work\n"),
		},
	}}
	branches, err := listRemoteBranches(context.Background(), fr, "https://github.com/o/r.git")
	if err != nil {
		t.Fatalf("listRemoteBranches: %v", err)
	}
	want := []remoteBranch{{Name: "main", SHA: "aaa111"}, {Name: "feature/quiet-work", SHA: "bbb222"}}
	if len(branches) != len(want) || branches[0] != want[0] || branches[1] != want[1] {
		t.Errorf("branches = %+v, want %+v", branches, want)
	}
}

func TestListRemoteBranches_EmptyOutput(t *testing.T) {
	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		"ls-remote --heads https://github.com/o/r.git": {out: []byte("")},
	}}
	branches, err := listRemoteBranches(context.Background(), fr, "https://github.com/o/r.git")
	if err != nil {
		t.Fatalf("listRemoteBranches: %v", err)
	}
	if len(branches) != 0 {
		t.Errorf("want no branches, got %+v", branches)
	}
}

func TestListRemoteBranches_RunnerErrorPropagates(t *testing.T) {
	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		"ls-remote --heads https://github.com/o/r.git": {err: errors.New("network unreachable")},
	}}
	if _, err := listRemoteBranches(context.Background(), fr, "https://github.com/o/r.git"); err == nil {
		t.Error("want error, got nil")
	}
}

func TestProbeBranchAhead_DeepensOnMergeBaseFailure(t *testing.T) {
	fr := &fakeGitRunner{
		failMergeBaseTimes: 2,
		responses: map[string]fakeResponse{
			"rev-list --count sha_mb..fork/feature": {out: []byte("3\n")},
			"log -1 --format=%cI fork/feature":      {out: []byte("2026-01-02T00:00:00Z\n")},
			"rev-parse fork/feature":                {out: []byte("sha_tip\n")},
		},
	}
	// merge-base succeeds on the 3rd call; script that call's output via the
	// runner's generic key match won't distinguish attempts, so give the
	// success response through the default (empty) path instead: patch fake
	// to return sha_mb once mergeBaseCalls > failMergeBaseTimes.
	fr.responses["merge-base upstream/main fork/feature"] = fakeResponse{out: []byte("sha_mb\n")}

	probe, depth, err := probeBranchAhead(context.Background(), fr, "/scratch", "main", "feature", 50)
	if err != nil {
		t.Fatalf("probeBranchAhead: %v", err)
	}
	if probe.Ahead != 3 || probe.TipSHA != "sha_tip" || probe.BaseSHA != "sha_mb" {
		t.Errorf("probe = %+v", probe)
	}
	wantDate := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if !probe.TipDate.Equal(wantDate) {
		t.Errorf("TipDate = %v, want %v", probe.TipDate, wantDate)
	}
	if depth != 50+2*deepenStep {
		t.Errorf("depth = %d, want %d (2 deepen rounds)", depth, 50+2*deepenStep)
	}
	if fr.mergeBaseCalls != 3 {
		t.Errorf("merge-base called %d times, want 3 (2 failures + 1 success)", fr.mergeBaseCalls)
	}
}

func TestProbeBranchAhead_GivesUpAfterMaxDeepenAttempts(t *testing.T) {
	fr := &fakeGitRunner{failMergeBaseTimes: maxDeepenAttempts + 5} // never succeeds
	_, _, err := probeBranchAhead(context.Background(), fr, "/scratch", "main", "feature", 50)
	if err == nil {
		t.Fatal("want error after exhausting deepen attempts, got nil")
	}
	if fr.mergeBaseCalls != maxDeepenAttempts+1 {
		t.Errorf("merge-base called %d times, want %d (initial + %d retries)", fr.mergeBaseCalls, maxDeepenAttempts+1, maxDeepenAttempts)
	}
}

// End-to-end orchestration test: fake git runner for the local side, the
// existing scanTestServer (branches_test.go) for the REST tipUpstreamed/
// FetchCompare leg. Two branches diverge; the newer one (by real committer
// date, not GraphQL metadata) must win, exactly like the REST-only
// ScanBranches tests assert for their own ordering signal.
func TestScanBranchesLocal_PicksNewestGenuineBranch(t *testing.T) {
	srv := scanTestServer(t, "up/stream", map[string]branchMeta{
		"feature-new": {ahead: 2, tipSHA: "sha_new", mergedPR: 0},
	})
	defer srv.Close()
	c := newTestClient(t, srv)

	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		"ls-remote --heads https://github.com/maint/proj.git": {
			out: []byte("sha_old\trefs/heads/feature-old\nsha_new\trefs/heads/feature-new\n"),
		},
		"rev-list --count sha_mb_old..fork/feature-old": {out: []byte("1\n")},
		"log -1 --format=%cI fork/feature-old":          {out: []byte("2025-01-01T00:00:00Z\n")},
		"rev-parse fork/feature-old":                    {out: []byte("sha_old\n")},
		"merge-base upstream/main fork/feature-old":     {out: []byte("sha_mb_old\n")},
		"rev-list --count sha_mb_new..fork/feature-new": {out: []byte("2\n")},
		"log -1 --format=%cI fork/feature-new":          {out: []byte("2026-06-01T00:00:00Z\n")},
		"rev-parse fork/feature-new":                    {out: []byte("sha_new\n")},
		"merge-base upstream/main fork/feature-new":     {out: []byte("sha_mb_new\n")},
	}}

	scan, err := c.scanBranchesLocalWith(context.Background(), "up", "stream", "main", testFork(),
		localScanOpts{runner: fr, gitAvailable: true})
	if err != nil {
		t.Fatalf("scanBranchesLocalWith: %v", err)
	}
	if scan == nil {
		t.Fatal("want a scan result, got nil")
	}
	if scan.Branch != "feature-new" {
		t.Errorf("selected branch = %q, want feature-new (newer tip date)", scan.Branch)
	}
	if scan.Upstreamed {
		t.Errorf("feature-new is genuine work, want Upstreamed=false")
	}
	// The winning branch's Compare must come from the REST FetchCompare call
	// (rich diff data), not the synthetic ahead-only placeholder.
	if scan.Compare.AheadBy != 2 {
		t.Errorf("AheadBy = %d, want 2 (from REST compare, not local probe)", scan.Compare.AheadBy)
	}
}

func TestScanBranchesLocal_GitUnavailableReturnsError(t *testing.T) {
	c := &Client{}
	_, err := c.scanBranchesLocalWith(context.Background(), "up", "stream", "main", testFork(),
		localScanOpts{runner: &fakeGitRunner{}, gitAvailable: false})
	if err == nil {
		t.Error("want error when git is unavailable, got nil")
	}
}

func TestScanBranchesLocal_TimeoutFallsBackGracefully(t *testing.T) {
	c := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		"ls-remote --heads https://github.com/maint/proj.git": {out: []byte("")},
	}}
	_, err := c.scanBranchesLocalWith(ctx, "up", "stream", "main", testFork(),
		localScanOpts{runner: fr, gitAvailable: true})
	if err == nil {
		t.Error("want context error to propagate so the caller falls back to REST, got nil")
	}
}

// Real-git integration test: builds a synthetic upstream+fork pair on disk
// (frozen default branch, one older side branch, one newer side branch --
// same shape validated by the original ls-remote POC script) and runs the
// real execGitRunner end to end. Skipped when git isn't on PATH.
func TestScanBranchesLocal_RealGit_PicksNewestBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	dir := t.TempDir()
	upstreamPath := filepath.Join(dir, "upstream.git")
	forkPath := filepath.Join(dir, "fork.git")
	workPath := filepath.Join(dir, "work")

	run := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=poc", "GIT_AUTHOR_EMAIL=poc@example.com",
			"GIT_COMMITTER_NAME=poc", "GIT_COMMITTER_EMAIL=poc@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commitAt := func(d, date, msg string) {
		t.Helper()
		cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", msg, "--date="+date)
		cmd.Dir = d
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=poc", "GIT_AUTHOR_EMAIL=poc@example.com", "GIT_AUTHOR_DATE="+date,
			"GIT_COMMITTER_NAME=poc", "GIT_COMMITTER_EMAIL=poc@example.com", "GIT_COMMITTER_DATE="+date)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}

	run(dir, "init", "-q", "--bare", "upstream.git")
	run(dir, "init", "-q", "-b", "main", "work")
	commitAt(workPath, "2024-01-01T00:00:00", "c1: upstream initial")
	run(workPath, "remote", "add", "origin", upstreamPath)
	run(workPath, "push", "-q", "origin", "main")

	run(dir, "init", "-q", "--bare", "fork.git")
	run(workPath, "remote", "add", "fork", forkPath)
	run(workPath, "push", "-q", "fork", "main") // fork's default branch == upstream, frozen

	run(workPath, "checkout", "-q", "-b", "feature-old")
	commitAt(workPath, "2025-01-01T00:00:00", "c2: older attempt")
	run(workPath, "push", "-q", "fork", "feature-old")

	run(workPath, "checkout", "-q", "main")
	run(workPath, "checkout", "-q", "-b", "feature-new")
	commitAt(workPath, "2026-06-01T00:00:00", "c3: newer work")
	run(workPath, "push", "-q", "fork", "feature-new")

	// The local git leg is real (fileURLRunner + execGitRunner); tipUpstreamed
	// and the winning branch's FetchCompare still go over REST, so they need a
	// working client. No branch is scripted as merged, matching the fixture.
	srv := scanTestServer(t, "upstream/repo", map[string]branchMeta{"feature-new": {ahead: 1, mergedPR: 0}})
	defer srv.Close()
	c := newTestClient(t, srv)

	scan, err := c.scanBranchesLocalWith(context.Background(), "upstream", "repo", "main",
		ForkInfo{Owner: OwnerInfo{Login: "fork"}, Name: "repo", DefaultBranch: "main"},
		localScanOpts{runner: fileURLRunner{base: dir}, gitAvailable: true})
	if err != nil {
		t.Fatalf("scanBranchesLocalWith: %v", err)
	}
	if scan == nil {
		t.Fatal("want a scan result, got nil")
	}
	if scan.Branch != "feature-new" {
		t.Errorf("selected branch = %q, want feature-new", scan.Branch)
	}
	if scan.Compare.AheadBy != 1 {
		t.Errorf("AheadBy = %d, want 1", scan.Compare.AheadBy)
	}
}

// fileURLRunner rewrites the https://github.com/<owner>/<repo>.git clone URLs
// scanBranchesLocalWith constructs into file:// paths under a local test
// fixture directory, so the real-git integration test never touches the
// network. owner/repo map 1:1 onto <base>/<repo-without-".git"-adjustment>.
type fileURLRunner struct {
	base string
}

func (f fileURLRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	rewritten := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "https://github.com/upstream/") {
			rewritten[i] = filepath.Join(f.base, "upstream.git")
		} else if strings.HasPrefix(a, "https://github.com/fork/") {
			rewritten[i] = filepath.Join(f.base, "fork.git")
		} else {
			rewritten[i] = a
		}
	}
	return execGitRunner{}.Run(ctx, dir, rewritten...)
}
