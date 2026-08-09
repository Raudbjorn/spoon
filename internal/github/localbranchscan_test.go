package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGitRunner scripts responses by command (joined args, space-separated,
// after stripping the leading "-q" quiet flag and "--" end-of-options marker
// so scripting doesn't have to care about either). Mirrors
// internal/mdg/clone_test.go's fakeRunner. Records every invocation
// verbatim in cmds so tests can assert on exact argv -- in particular, that
// no untrusted branch name is ever passed as a bare positional argument.
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
		if a == "-q" || a == "--" {
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

func TestListForkTips_ParsesForEachRefOutput(t *testing.T) {
	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		"for-each-ref --format=%(refname:short)|%(objectname)|%(committerdate:iso-strict) refs/remotes/fork/": {
			out: []byte("fork/main|aaa111|2026-01-01T00:00:00Z\n" +
				"fork/feature/quiet-work|bbb222|2026-06-01T00:00:00Z\n"),
		},
	}}
	tips, err := listForkTips(context.Background(), fr, "/scratch")
	if err != nil {
		t.Fatalf("listForkTips: %v", err)
	}
	if len(tips) != 2 {
		t.Fatalf("tips = %+v, want 2", tips)
	}
	if tips[0].Name != "main" || tips[0].SHA != "aaa111" {
		t.Errorf("tips[0] = %+v", tips[0])
	}
	if tips[1].Name != "feature/quiet-work" || tips[1].SHA != "bbb222" {
		t.Errorf("tips[1] = %+v", tips[1])
	}
	wantDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !tips[1].Date.Equal(wantDate) {
		t.Errorf("tips[1].Date = %v, want %v", tips[1].Date, wantDate)
	}
}

func TestListForkTips_SkipsHeadPseudoRef(t *testing.T) {
	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		"for-each-ref --format=%(refname:short)|%(objectname)|%(committerdate:iso-strict) refs/remotes/fork/": {
			out: []byte("fork/HEAD|aaa111|2026-01-01T00:00:00Z\nfork/main|bbb222|2026-01-01T00:00:00Z\n"),
		},
	}}
	tips, err := listForkTips(context.Background(), fr, "/scratch")
	if err != nil {
		t.Fatalf("listForkTips: %v", err)
	}
	if len(tips) != 1 || tips[0].Name != "main" {
		t.Errorf("tips = %+v, want only main", tips)
	}
}

func TestFetchAllForkHeads_NoBranchNameInArgs(t *testing.T) {
	fr := &fakeGitRunner{}
	if err := fetchAllForkHeads(context.Background(), fr, "/scratch"); err != nil {
		t.Fatalf("fetchAllForkHeads: %v", err)
	}
	if len(fr.cmds) != 1 {
		t.Fatalf("want 1 command, got %d: %v", len(fr.cmds), fr.cmds)
	}
	got := fr.cmds[0]
	for _, arg := range got {
		if arg == "" {
			continue
		}
		// The only bare, non-flag positional argument allowed here is the
		// literal remote name "fork" -- never a branch name, since none is
		// ever passed to this call.
		if !strings.HasPrefix(arg, "-") && arg != "fetch" && arg != "fork" && arg != "/scratch" {
			t.Errorf("unexpected positional arg %q in %v -- fetchAllForkHeads must never take a branch name", arg, got)
		}
	}
}

// Regression test for the argument-injection finding: a hostile fork branch
// literally named "--upload-pack=evil" must never appear as a bare
// positional argument anywhere probeAhead invokes git. It is only ever
// legitimate when prefixed "fork/" or "upstream/" (merge-base, rev-list),
// which can't be interpreted as an option since the resulting string can't
// start with "-".
func TestProbeAhead_HostileBranchNameNeverBarePositionalArg(t *testing.T) {
	const hostile = "--upload-pack=touch /tmp/pwned"
	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		"merge-base upstream/main fork/" + hostile: {out: []byte("sha_mb\n")},
		"rev-list --count sha_mb..fork/" + hostile: {out: []byte("3\n")},
	}}

	ahead, mergeBase, _, err := probeAhead(context.Background(), fr, "/scratch", "main", hostile, 50)
	if err != nil {
		t.Fatalf("probeAhead: %v", err)
	}
	if ahead != 3 || mergeBase != "sha_mb" {
		t.Errorf("ahead=%d mergeBase=%q", ahead, mergeBase)
	}

	for _, cmd := range fr.cmds {
		for _, arg := range cmd {
			if arg == hostile {
				t.Fatalf("hostile branch name appeared as a bare positional arg in %v", cmd)
			}
			if strings.HasPrefix(arg, "--upload-pack") {
				t.Fatalf("hostile branch name was interpretable as a git option in %v", cmd)
			}
		}
	}
}

func TestProbeAhead_DeepensOnMergeBaseFailure(t *testing.T) {
	fr := &fakeGitRunner{
		failMergeBaseTimes: 2,
		responses: map[string]fakeResponse{
			"rev-list --count sha_mb..fork/feature": {out: []byte("3\n")},
		},
	}
	fr.responses["merge-base upstream/main fork/feature"] = fakeResponse{out: []byte("sha_mb\n")}

	ahead, mergeBase, depth, err := probeAhead(context.Background(), fr, "/scratch", "main", "feature", 50)
	if err != nil {
		t.Fatalf("probeAhead: %v", err)
	}
	if ahead != 3 || mergeBase != "sha_mb" {
		t.Errorf("ahead=%d mergeBase=%q", ahead, mergeBase)
	}
	if depth != 50+2*deepenStep {
		t.Errorf("depth = %d, want %d (2 deepen rounds)", depth, 50+2*deepenStep)
	}
	if fr.mergeBaseCalls != 3 {
		t.Errorf("merge-base called %d times, want 3 (2 failures + 1 success)", fr.mergeBaseCalls)
	}
	// Deepen fetches must not carry a branch name either.
	for _, cmd := range fr.cmds {
		if len(cmd) > 1 && cmd[1] == "fetch" {
			for _, arg := range cmd {
				if arg == "feature" || arg == "main" {
					t.Errorf("deepen fetch %v carries a bare branch name", cmd)
				}
			}
		}
	}
}

func TestProbeAhead_GivesUpAfterMaxDeepenAttempts(t *testing.T) {
	fr := &fakeGitRunner{failMergeBaseTimes: maxDeepenAttempts + 5} // never succeeds
	_, _, _, err := probeAhead(context.Background(), fr, "/scratch", "main", "feature", 50)
	if err == nil {
		t.Fatal("want error after exhausting deepen attempts, got nil")
	}
	if fr.mergeBaseCalls != maxDeepenAttempts+1 {
		t.Errorf("merge-base called %d times, want %d (initial + %d retries)", fr.mergeBaseCalls, maxDeepenAttempts+1, maxDeepenAttempts)
	}
}

func forkTipsResponse(triples ...string) fakeResponse {
	// triples: name, sha, date, name, sha, date, ... (len must be a multiple of 3)
	var b strings.Builder
	for i := 0; i < len(triples); i += 3 {
		b.WriteString("fork/" + triples[i] + "|" + triples[i+1] + "|" + triples[i+2] + "\n")
	}
	return fakeResponse{out: []byte(b.String())}
}

const forkTipsKey = "for-each-ref --format=%(refname:short)|%(objectname)|%(committerdate:iso-strict) refs/remotes/fork/"

// End-to-end orchestration test: fake git runner for the local side, the
// existing scanTestServer (branches_test.go) for the REST tipUpstreamed/
// FetchCompare leg. Two branches diverge; the newer one (by real committer
// date, read off the bulk-fetched tips) must win.
func TestScanBranchesLocal_PicksNewestGenuineBranch(t *testing.T) {
	srv := scanTestServer(t, "up/stream", map[string]branchMeta{
		"feature-new": {ahead: 2, tipSHA: "sha_new", mergedPR: 0},
	})
	defer srv.Close()
	c := newTestClient(t, srv)

	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		forkTipsKey: forkTipsResponse(
			"feature-old", "sha_old", "2025-01-01T00:00:00Z",
			"feature-new", "sha_new", "2026-06-01T00:00:00Z",
		),
		"merge-base upstream/main fork/feature-old":     {out: []byte("sha_mb_old\n")},
		"rev-list --count sha_mb_old..fork/feature-old": {out: []byte("1\n")},
		"merge-base upstream/main fork/feature-new":     {out: []byte("sha_mb_new\n")},
		"rev-list --count sha_mb_new..fork/feature-new": {out: []byte("2\n")},
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

// Regression test for the cap-before-sort finding: a fork with more
// candidates than maxLocalScanBranches must still find the true newest one,
// as long as tip listing (which carries real dates) happens before the cap.
func TestScanBranchesLocal_CapAppliesAfterDateSort(t *testing.T) {
	srv := scanTestServer(t, "up/stream", map[string]branchMeta{
		"zz-newest": {ahead: 1, tipSHA: "sha_zz", mergedPR: 0},
	})
	defer srv.Close()
	c := newTestClient(t, srv)

	// maxLocalScanBranches stale branches, alphabetically ahead of the one
	// true newest branch ("zz-newest") -- ls-remote/for-each-ref's own
	// output order is alphabetical, so this only passes if sorting by real
	// date happens BEFORE the cap discards anything.
	pairs := make([]string, 0, (maxLocalScanBranches+1)*3)
	responses := map[string]fakeResponse{}
	for i := 0; i < maxLocalScanBranches; i++ {
		name := "a-stale-" + string(rune('a'+i))
		pairs = append(pairs, name, "sha_"+name, "2020-01-01T00:00:00Z")
		responses["merge-base upstream/main fork/"+name] = fakeResponse{out: []byte("sha_mb_" + name + "\n")}
		responses["rev-list --count sha_mb_"+name+"..fork/"+name] = fakeResponse{out: []byte("0\n")}
	}
	pairs = append(pairs, "zz-newest", "sha_zz", "2026-06-01T00:00:00Z")
	responses["merge-base upstream/main fork/zz-newest"] = fakeResponse{out: []byte("sha_mb_zz\n")}
	responses["rev-list --count sha_mb_zz..fork/zz-newest"] = fakeResponse{out: []byte("1\n")}
	responses[forkTipsKey] = forkTipsResponse(pairs...)

	fr := &fakeGitRunner{responses: responses}
	scan, err := c.scanBranchesLocalWith(context.Background(), "up", "stream", "main", testFork(),
		localScanOpts{runner: fr, gitAvailable: true})
	if err != nil {
		t.Fatalf("scanBranchesLocalWith: %v", err)
	}
	if scan == nil || scan.Branch != "zz-newest" {
		t.Errorf("scan = %+v, want zz-newest", scan)
	}
}

// Regression test for the "confidently nothing" vs "could not scan"
// distinction: when every attempted probe fails, ScanBranchesLocal must
// error (triggering the REST fallback in scanSideBranches) rather than
// return (nil, nil), which scanSideBranches would treat as a clean,
// authoritative "no divergent branches".
func TestScanBranchesLocal_AllProbesFailReturnsError(t *testing.T) {
	c := &Client{}
	fr := &fakeGitRunner{
		failMergeBaseTimes: 999, // every merge-base call fails, forever
		responses: map[string]fakeResponse{
			forkTipsKey: forkTipsResponse("feature", "sha_f", "2026-01-01T00:00:00Z"),
		},
	}
	_, err := c.scanBranchesLocalWith(context.Background(), "up", "stream", "main", testFork(),
		localScanOpts{runner: fr, gitAvailable: true})
	if err == nil {
		t.Error("want an error when every candidate probe fails, got nil (would look like a clean 'nothing found' to scanSideBranches)")
	}
}

// The same "could not scan" vs "scanned and found nothing" distinction, for
// the other way the local path can come up empty without having looked at
// everything: every probe SUCCEEDS, all of them report ahead==0, and the
// candidate cap cut the list short. Branches past the cap were never
// examined, so this must error into the REST fallback rather than report a
// clean nil -- REST walks a different (alphabetical) candidate set and may
// hold a divergent branch the recency cap excluded.
func TestScanBranchesLocal_CapHitWithNoDivergenceReturnsError(t *testing.T) {
	c := &Client{}

	tips := make([]string, 0, (maxLocalScanBranches+5)*3)
	responses := map[string]fakeResponse{}
	for i := 0; i < maxLocalScanBranches+5; i++ {
		name := fmt.Sprintf("branch-%02d", i)
		tips = append(tips, name, fmt.Sprintf("sha_%02d", i), fmt.Sprintf("2026-01-%02dT00:00:00Z", i+1))
		responses["merge-base upstream/main fork/"+name] = fakeResponse{out: []byte("sha_mb\n")}
		responses["rev-list --count sha_mb..fork/"+name] = fakeResponse{out: []byte("0\n")} // no divergence
	}
	responses[forkTipsKey] = forkTipsResponse(tips...)

	_, err := c.scanBranchesLocalWith(context.Background(), "up", "stream", "main", testFork(),
		localScanOpts{runner: &fakeGitRunner{responses: responses}, gitAvailable: true})
	if err == nil {
		t.Error("want an error when the candidate cap is hit with nothing divergent found, got nil (would suppress the REST fallback)")
	}
}

// The uncapped counterpart: fewer candidates than the cap, every one probed
// successfully, none divergent. That IS an authoritative "no side-branch
// work" -- nothing went unexamined -- so it must stay (nil, nil) and not
// spend a REST scan re-deriving the same answer.
func TestScanBranchesLocal_UncappedNoDivergenceReturnsNilNil(t *testing.T) {
	c := &Client{}
	fr := &fakeGitRunner{responses: map[string]fakeResponse{
		forkTipsKey: forkTipsResponse("only-branch", "sha_a", "2026-01-01T00:00:00Z"),
		"merge-base upstream/main fork/only-branch": {out: []byte("sha_mb\n")},
		"rev-list --count sha_mb..fork/only-branch": {out: []byte("0\n")},
	}}
	scan, err := c.scanBranchesLocalWith(context.Background(), "up", "stream", "main", testFork(),
		localScanOpts{runner: fr, gitAvailable: true})
	if err != nil {
		t.Fatalf("want no error for a complete scan that found nothing, got %v", err)
	}
	if scan != nil {
		t.Errorf("want nil scan, got %+v", scan)
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

	fr := &fakeGitRunner{}
	_, err := c.scanBranchesLocalWith(ctx, "up", "stream", "main", testFork(),
		localScanOpts{runner: fr, gitAvailable: true})
	if err == nil {
		t.Error("want context error to propagate so the caller falls back to REST, got nil")
	}
}

// Exercises scanSideBranches itself (not scanBranchesLocalWith directly), so
// the REST-fallback path production code actually takes is under test, not
// just the local-scan function in isolation.
func TestScanSideBranchesWith_LocalFailureFallsBackToRESTScanBranches(t *testing.T) {
	srv := scanTestServer(t, "up/stream", map[string]branchMeta{
		"main":        {ahead: 0},
		"feature-new": {ahead: 5, tipSHA: "sha_new", mergedPR: 0},
	})
	defer srv.Close()
	c := newTestClient(t, srv)
	c.localBranchScan = true

	// gitAvailable: false forces scanBranchesLocalWith to fail immediately,
	// so scanSideBranchesWith must fall through to the REST ScanBranches
	// path below, using the branches list REST would have used all along.
	branches := []BranchInfo{{Name: "feature-new", LastCommitAt: "2026-06-01T00:00:00Z"}}
	scan, err := c.scanSideBranchesWith(context.Background(), "up", "stream", "main", testFork(), branches,
		localScanOpts{runner: &fakeGitRunner{}, gitAvailable: false})
	if err != nil {
		t.Fatalf("scanSideBranchesWith: %v", err)
	}
	if scan == nil || scan.Branch != "feature-new" {
		t.Errorf("scan = %+v, want REST fallback to find feature-new", scan)
	}
}

func TestScanSideBranchesWith_LocalFlagOffSkipsLocalEntirely(t *testing.T) {
	srv := scanTestServer(t, "up/stream", map[string]branchMeta{
		"feature-new": {ahead: 3, tipSHA: "sha_new", mergedPR: 0},
	})
	defer srv.Close()
	c := newTestClient(t, srv) // localBranchScan left false

	branches := []BranchInfo{{Name: "feature-new", LastCommitAt: "2026-06-01T00:00:00Z"}}
	scan, err := c.scanSideBranchesWith(context.Background(), "up", "stream", "main", testFork(), branches,
		localScanOpts{runner: &fakeGitRunner{}, gitAvailable: true})
	if err != nil {
		t.Fatalf("scanSideBranchesWith: %v", err)
	}
	if scan == nil || scan.Branch != "feature-new" {
		t.Errorf("scan = %+v, want REST path (flag off)", scan)
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
// scanBranchesLocalWith constructs into plain local filesystem paths (git
// accepts a bare path as a remote), so the real-git integration test never
// touches the network. The rewrite keys on the owner segment alone, which is
// all the fixture needs: any .../upstream/... URL becomes <base>/upstream.git
// and any .../fork/... URL becomes <base>/fork.git.
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
