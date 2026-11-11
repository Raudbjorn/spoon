package threadsops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/svnbjrn/spoon/internal/github"
)

func TestResolveGitDir_Standard(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	gotGit, gotCommon, err := resolveGitDir(dir)
	if err != nil {
		t.Fatalf("resolveGitDir: %v", err)
	}
	if gotGit != gitDir || gotCommon != gitDir {
		t.Errorf("gotGit=%q gotCommon=%q, want %q for both", gotGit, gotCommon, gitDir)
	}
}

func TestResolveGitDir_Worktree(t *testing.T) {
	tmp := t.TempDir()
	mainGit := filepath.Join(tmp, "main", ".git")
	wtGitDir := filepath.Join(mainGit, "worktrees", "wt1")
	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// commondir inside the worktree points to main .git
	if err := os.WriteFile(filepath.Join(wtGitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wtDir := filepath.Join(tmp, "wt1")
	if err := os.Mkdir(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// .git file in the worktree
	gitContent := fmt.Sprintf("gitdir: %s\n", wtGitDir)
	if err := os.WriteFile(filepath.Join(wtDir, ".git"), []byte(gitContent), 0o644); err != nil {
		t.Fatal(err)
	}

	gotGit, gotCommon, err := resolveGitDir(wtDir)
	if err != nil {
		t.Fatalf("resolveGitDir: %v", err)
	}
	if gotGit != wtGitDir {
		t.Errorf("gotGit=%q want %q", gotGit, wtGitDir)
	}
	wantCommon := filepath.Clean(mainGit)
	if gotCommon != wantCommon {
		t.Errorf("gotCommon=%q want %q", gotCommon, wantCommon)
	}
}

func TestResolveGitDir_Submodule(t *testing.T) {
	tmp := t.TempDir()
	subGitDir := filepath.Join(tmp, "main", ".git", "modules", "sub1")
	if err := os.MkdirAll(subGitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(tmp, "main", "sub1")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// submodule pointer file
	gitContent := fmt.Sprintf("gitdir: %s\n", subGitDir)
	if err := os.WriteFile(filepath.Join(subDir, ".git"), []byte(gitContent), 0o644); err != nil {
		t.Fatal(err)
	}

	gotGit, gotCommon, err := resolveGitDir(subDir)
	if err != nil {
		t.Fatalf("resolveGitDir: %v", err)
	}
	if gotGit != subGitDir || gotCommon != subGitDir {
		t.Errorf("gotGit=%q gotCommon=%q, want %q", gotGit, gotCommon, subGitDir)
	}
}

func TestNormalizeGitURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"git@github.com:owner/repo.git", "https://github.com/owner/repo.git"},
		{"git@gitlab.com:group/subgroup/repo.git", "https://gitlab.com/group/subgroup/repo.git"},
		{"ssh://git@github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"ssh://alice@github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"git@github.com:/owner/repo.git", "https://github.com/owner/repo.git"},
		{"git://github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"https://github.com/owner/repo", "https://github.com/owner/repo"},
		{"http://github.com/owner/repo.git", "http://github.com/owner/repo.git"},
	}
	for _, tt := range tests {
		got := normalizeGitURL(tt.in)
		if got != tt.want {
			t.Errorf("normalizeGitURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseBranch(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"ref: refs/heads/main\n", "main"},
		{"ref: refs/heads/feature/login-v2\n", "feature/login-v2"},
		{"4b825dc642cb6eb9a060e54bf8d69288fbee4904\n", ""}, // detached HEAD
		{"ref: refs/tags/release\n", ""},                   // only a local branch can identify a PR head
		{"", ""},
	}
	for _, tt := range tests {
		got := parseBranch(tt.in)
		if got != tt.want {
			t.Errorf("parseBranch(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestReadRemotes(t *testing.T) {
	cfg := `
[core]
	repositoryformatversion = 0
	filemode = true
[remote "origin"]
	url = git@github.com:alice/spoon.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[remote "upstream"]
	url = https://github.com/svnbjrn/spoon.git
	fetch = +refs/heads/*:refs/remotes/upstream/*
[branch "main"]
	remote = origin
	merge = refs/heads/main
`
	dir := t.TempDir()
	createFakeCheckout(t, dir, "main", "", "")
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	origin, upstream, err := readRemotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "git@github.com:alice/spoon.git" {
		t.Errorf("origin = %q, want git@github.com:alice/spoon.git", origin)
	}
	if upstream != "https://github.com/svnbjrn/spoon.git" {
		t.Errorf("upstream = %q, want https://github.com/svnbjrn/spoon.git", upstream)
	}
}

func createFakeCheckout(t *testing.T, dir, branch, originURL, upstreamURL string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "--initial-branch=main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var headContent string
	if branch != "" {
		headContent = fmt.Sprintf("ref: refs/heads/%s\n", branch)
	} else {
		headContent = "0123456789abcdef0123456789abcdef01234567\n"
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(headContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var cfg strings.Builder
	cfg.WriteString("[core]\n\trepositoryformatversion = 0\n")
	if originURL != "" {
		cfg.WriteString(fmt.Sprintf("[remote \"origin\"]\n\turl = %s\n", originURL))
	}
	if upstreamURL != "" {
		cfg.WriteString(fmt.Sprintf("[remote \"upstream\"]\n\turl = %s\n", upstreamURL))
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanWorkspace_SingleLevel(t *testing.T) {
	root := t.TempDir()

	createFakeCheckout(t, filepath.Join(root, "frontend"), "feat-login", "git@github.com:acme/frontend.git", "")
	createFakeCheckout(t, filepath.Join(root, "backend"), "feat-login", "https://github.com/acme/backend.git", "https://github.com/upstream/backend.git")
	createFakeCheckout(t, filepath.Join(root, "infra"), "", "https://github.com/acme/infra.git", "")

	// Ignored folder
	vendorDir := filepath.Join(root, "vendor")
	createFakeCheckout(t, vendorDir, "main", "https://github.com/acme/vendor.git", "")

	repos, err := ScanWorkspace(root, ScanOptions{Depth: 1})
	if err != nil {
		t.Fatalf("ScanWorkspace: %v", err)
	}

	if len(repos) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(repos))
	}

	byName := make(map[string]WorkspaceRepo)
	for _, r := range repos {
		byName[filepath.Base(r.Dir)] = r
	}

	fe := byName["frontend"]
	if fe.Owner != "acme" || fe.Repo != "frontend" || fe.CurrentBranch != "feat-login" {
		t.Errorf("frontend: %+v", fe)
	}

	be := byName["backend"]
	// Upstream takes precedence
	if be.Owner != "upstream" || be.Repo != "backend" || be.OriginOwner != "acme" || be.CurrentBranch != "feat-login" {
		t.Errorf("backend: %+v", be)
	}

	inf := byName["infra"]
	if inf.CurrentBranch != "" {
		t.Errorf("infra detached HEAD should have empty CurrentBranch, got %q", inf.CurrentBranch)
	}
}

func TestScanWorkspace_Depth(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "services", "auth")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	createFakeCheckout(t, nested, "main", "https://github.com/acme/auth.git", "")

	// Depth 1 should NOT find nested
	r1, err := ScanWorkspace(root, ScanOptions{Depth: 1})
	if err != nil {
		t.Fatalf("ScanWorkspace(depth 1): %v", err)
	}
	if len(r1) != 0 {
		t.Errorf("depth 1 should not find nested repo, got %d", len(r1))
	}

	// Depth 2 should find nested
	r2, err := ScanWorkspace(root, ScanOptions{Depth: 2})
	if err != nil {
		t.Fatalf("ScanWorkspace(depth 2): %v", err)
	}
	if len(r2) != 1 || r2[0].Repo != "auth" {
		t.Errorf("depth 2 should find auth repo, got %+v", r2)
	}
}

type fakePRFetcher struct {
	fn func(ctx context.Context, owner, repo, headOwner, headBranch string) (*gh.PullRequest, error)
}

func (f *fakePRFetcher) Host() string { return "github.com" }

func (f *fakePRFetcher) FindOpenPRForHead(ctx context.Context, owner, repo, headOwner, headBranch string) (*gh.PullRequest, error) {
	return f.fn(ctx, owner, repo, headOwner, headBranch)
}

func TestDiscoverPRsInWorkspace_ConcurrencyAndFallback(t *testing.T) {
	repos := []WorkspaceRepo{
		{
			Dir:      "/path/to/repo1",
			RelDir:   "./repo1",
			Owner:    "acme",
			Repo:     "repo1",
			Provider: "github", OriginHost: "github.com",
			OriginOwner:   "acme",
			OriginRepo:    "repo1",
			CurrentBranch: "feature-1",
		},
		{
			Dir:              "/path/to/fork-upstream",
			RelDir:           "./fork-upstream",
			Owner:            "upstream",
			Repo:             "fork-upstream",
			UpstreamProvider: "github", UpstreamHost: "github.com",
			UpstreamOwner: "upstream",
			UpstreamRepo:  "fork-upstream",
			Provider:      "github", OriginHost: "github.com",
			OriginOwner:   "alice",
			OriginRepo:    "fork-upstream",
			CurrentBranch: "feature-2",
		},
		{
			Dir:              "/path/to/fork-origin-fallback",
			RelDir:           "./fork-origin-fallback",
			Owner:            "upstream",
			Repo:             "fork-origin-fallback",
			UpstreamProvider: "github", UpstreamHost: "github.com",
			UpstreamOwner: "upstream",
			UpstreamRepo:  "fork-origin-fallback",
			Provider:      "github", OriginHost: "github.com",
			OriginOwner:   "bob",
			OriginRepo:    "fork-origin-fallback",
			CurrentBranch: "feature-3",
		},
		{
			Dir:      "/path/to/no-pr",
			RelDir:   "./no-pr",
			Owner:    "acme",
			Repo:     "no-pr",
			Provider: "github", OriginHost: "github.com",
			OriginOwner:   "acme",
			OriginRepo:    "no-pr",
			CurrentBranch: "feature-4",
		},
	}

	var active int32
	var maxActive int32

	fetcher := &fakePRFetcher{
		fn: func(ctx context.Context, owner, repo, headOwner, headBranch string) (*gh.PullRequest, error) {
			cur := atomic.AddInt32(&active, 1)
			defer atomic.AddInt32(&active, -1)
			for {
				old := atomic.LoadInt32(&maxActive)
				if cur <= old || atomic.CompareAndSwapInt32(&maxActive, old, cur) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)

			switch {
			case owner == "acme" && repo == "repo1":
				return &gh.PullRequest{Number: 101, Title: "PR 101"}, nil
			case owner == "upstream" && repo == "fork-upstream" && headOwner == "alice":
				return &gh.PullRequest{Number: 102, Title: "PR 102"}, nil
			case owner == "upstream" && repo == "fork-origin-fallback":
				// Not in upstream; should fall back to origin
				return nil, nil
			case owner == "bob" && repo == "fork-origin-fallback":
				// Found in origin fallback!
				return &gh.PullRequest{Number: 103, Title: "PR 103"}, nil
			default:
				return nil, nil
			}
		},
	}

	start := time.Now()
	discoveries := DiscoverPRsForRepos(context.Background(), fetcher, repos, DiscoverOptions{
		Parallelism: 4,
	})
	elapsed := time.Since(start)

	// Since workers ran concurrently, elapsed time should be well below serial time (4 * 30ms = 120ms+)
	if elapsed > 150*time.Millisecond {
		t.Logf("discovery took %v (bounded concurrency asserted)", elapsed)
	}
	if max := atomic.LoadInt32(&maxActive); max < 2 {
		t.Errorf("max active workers = %d, expected concurrent execution", max)
	}

	// Only repos with an open PR should be returned
	if len(discoveries) != 3 {
		t.Fatalf("expected 3 discoveries with open PRs, got %d", len(discoveries))
	}

	byRepo := make(map[string]WorkspaceDiscovery)
	for _, d := range discoveries {
		byRepo[d.Repo.Repo] = d
	}

	d1 := byRepo["repo1"]
	if d1.PR == nil || d1.PR.Number != 101 || d1.PR.RepoOwner != "acme" {
		t.Errorf("d1 PR = %+v", d1.PR)
	}

	d2 := byRepo["fork-upstream"]
	if d2.PR == nil || d2.PR.Number != 102 || d2.PR.RepoOwner != "upstream" {
		t.Errorf("d2 PR = %+v", d2.PR)
	}

	d3 := byRepo["fork-origin-fallback"]
	if d3.PR == nil || d3.PR.Number != 103 || d3.PR.RepoOwner != "bob" {
		t.Errorf("d3 PR = %+v", d3.PR)
	}
}

func TestDiscoverPRsInWorkspace_NonFatalError(t *testing.T) {
	repos := []WorkspaceRepo{
		{
			Dir:      "/path/to/ok",
			Owner:    "acme",
			Repo:     "ok",
			Provider: "github", OriginHost: "github.com",
			OriginOwner:   "acme",
			OriginRepo:    "ok",
			CurrentBranch: "main",
		},
		{
			Dir:      "/path/to/failing",
			Owner:    "acme",
			Repo:     "failing",
			Provider: "github", OriginHost: "github.com",
			OriginOwner:   "acme",
			OriginRepo:    "failing",
			CurrentBranch: "main",
		},
	}

	fetcher := &fakePRFetcher{
		fn: func(ctx context.Context, owner, repo, headOwner, headBranch string) (*gh.PullRequest, error) {
			if repo == "failing" {
				return nil, errors.New("500 internal server error")
			}
			return &gh.PullRequest{Number: 42, Title: "OK PR"}, nil
		},
	}

	var logBuf strings.Builder
	discoveries := DiscoverPRsForRepos(context.Background(), fetcher, repos, DiscoverOptions{
		Logger: &logBuf,
	})

	// Failed lookups remain distinguishable from a confirmed absence.
	if len(discoveries) != 2 || discoveries[0].PR.Number != 42 || discoveries[1].Err == nil {
		t.Fatalf("expected successful and failed discoveries, got %+v", discoveries)
	}
	if !strings.Contains(logBuf.String(), "warning") {
		t.Errorf("expected warning logged for failing repo, got %q", logBuf.String())
	}
}

func TestScanWorkspace_IncludedRemotesAndWorktree(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	linkedDir := filepath.Join(root, ".worktrees", "topic")
	createFakeCheckout(t, mainDir, "main", "", "")
	gitDir := filepath.Join(mainDir, ".git")
	for name, contents := range map[string]string{
		"remotes.inc": "[remote \"origin\"]\n url = git@github.com:alice/project.git\n",
		"topic.inc":   "[remote \"upstream\"]\n url = https://github.com/acme/project.git\n",
	} {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(gitDir, "config")
	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("\n[include]\n path = remotes.inc\n[includeIf \"onbranch:topic\"]\n path = topic.inc\n")...)
	if err := os.WriteFile(configPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", mainDir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial"},
		{"-C", mainDir, "worktree", "add", "-b", "topic", linkedDir},
		{"-C", mainDir, "config", "extensions.worktreeConfig", "true"},
		{"-C", linkedDir, "config", "--worktree", "--add", "remote.origin.url", ""},
		{"-C", linkedDir, "config", "--worktree", "--add", "remote.origin.url", "https://github.com/bob/project.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	// .worktrees is traversable within the depth limit, unless explicitly ignored.
	for _, opts := range []ScanOptions{{Depth: 1}, {Depth: 2, IgnoreDirs: []string{".worktrees"}}} {
		repos, err := ScanWorkspace(root, opts)
		if err != nil || len(repos) != 1 || repos[0].Dir != mainDir {
			t.Fatalf("limited scan %+v: %+v, %v", opts, repos, err)
		}
	}
	repos, err := ScanWorkspace(root, ScanOptions{Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("repos = %+v", repos)
	}
	for _, repo := range repos {
		wantOrigin := "alice"
		if repo.CurrentBranch == "topic" {
			wantOrigin = "bob" // The worktree clears and replaces the shared URL list.
		}
		if repo.OriginOwner != wantOrigin || repo.OriginHost != "github.com" {
			t.Errorf("included origin: %+v", repo)
		}
		if repo.CurrentBranch == "topic" && (repo.UpstreamOwner != "acme" || repo.UpstreamHost != "github.com") {
			t.Errorf("conditional worktree upstream: %+v", repo)
		}
		if repo.CurrentBranch == "main" && repo.UpstreamOwner != "" {
			t.Errorf("conditional include leaked into main: %+v", repo)
		}
	}
}

func TestDiscoverPRsForRepos_RemoteIdentity(t *testing.T) {
	cases := []struct {
		name, origin, upstream string
		wantCalls              int
		wantErr                bool
	}{
		{"github", "https://github.com/alice/project.git", "", 1, false},
		{"gitlab", "https://gitlab.com/alice/project.git", "", 0, true},
		{"enterprise", "git@git.example.com:alice/project.git", "", 0, true},
		{"gitea", "https://codeberg.org/alice/project.git", "", 0, true},
		{"foreign origin github upstream", "https://gitlab.com/alice/project.git", "https://github.com/acme/project.git", 0, true},
		{"github origin foreign upstream", "https://github.com/alice/project.git", "https://gitlab.com/acme/project.git", 1, true},
		{"relative remote", "alice/project", "", 0, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			createFakeCheckout(t, dir, "feature", tt.origin, tt.upstream)
			repo, err := parseWorkspaceRepo(dir, dir)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			fetcher := &fakePRFetcher{fn: func(_ context.Context, owner, name, headOwner, branch string) (*gh.PullRequest, error) {
				calls++
				// Origin fallback carries the same explicit head owner as upstream queries.
				if owner != "alice" || name != "project" || headOwner != "alice" {
					t.Errorf("unexpected public GitHub identity: %q/%q head=%q", owner, name, headOwner)
				}
				return &gh.PullRequest{Number: 42}, nil
			}}
			got := DiscoverPRsForRepos(context.Background(), fetcher, []WorkspaceRepo{repo}, DiscoverOptions{})
			if calls != tt.wantCalls {
				t.Fatalf("calls=%d want %d", calls, tt.wantCalls)
			}
			if len(got) != 1 || (got[0].Err != nil) != tt.wantErr {
				t.Fatalf("discovery=%+v wantErr=%v", got, tt.wantErr)
			}
			if got[0].PR != nil && got[0].PR.RepoRoot != dir {
				t.Errorf("checkout root=%q want %q", got[0].PR.RepoRoot, dir)
			}
		})
	}
}

func TestDiscoverPRsForRepos_RetainsFallbackErrorsAndSerializesLogger(t *testing.T) {
	upstreamErr := errors.New("upstream unavailable")
	originErr := errors.New("origin unavailable")
	for _, fallbackFound := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallbackFound=%v", fallbackFound), func(t *testing.T) {
			repos := make([]WorkspaceRepo, 32)
			for i := range repos {
				repos[i] = WorkspaceRepo{Dir: fmt.Sprintf("/repo/%d", i), RelDir: fmt.Sprintf("./repo%d\x1b]52;c;X\a", i), Provider: "github", OriginHost: "github.com", OriginOwner: "alice", OriginRepo: "project", UpstreamProvider: "github", UpstreamHost: "github.com", UpstreamOwner: "acme", UpstreamRepo: "project", CurrentBranch: "topic"}
			}
			fetcher := &fakePRFetcher{fn: func(_ context.Context, owner, _, _, _ string) (*gh.PullRequest, error) {
				if owner == "acme" {
					return nil, upstreamErr
				}
				if fallbackFound {
					return &gh.PullRequest{Number: 42}, nil
				}
				return nil, originErr
			}}
			var log strings.Builder
			got := DiscoverPRsForRepos(context.Background(), fetcher, repos, DiscoverOptions{Parallelism: 8, Logger: &log})
			if len(got) != len(repos) {
				t.Fatalf("lost failed lookups: got %d", len(got))
			}
			for _, d := range got {
				if !errors.Is(d.Err, upstreamErr) || (!fallbackFound && !errors.Is(d.Err, originErr)) {
					t.Errorf("lost failure: %+v", d)
				}
				if (d.PR != nil) != fallbackFound {
					t.Errorf("PR=%+v fallbackFound=%v", d.PR, fallbackFound)
				}
			}
			if strings.Count(log.String(), "warning:") != len(repos) || strings.ContainsAny(log.String(), "\x1b\a") {
				t.Errorf("unsafe or lost warnings: %q", log.String())
			}
		})
	}
}

func TestScanWorkspace_ReportsInvalidConfig(t *testing.T) {
	root := t.TempDir()
	createFakeCheckout(t, root, "main", "", "")
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanWorkspace(root, ScanOptions{}); err == nil {
		t.Fatal("invalid Git config was reported as an empty scan")
	}
}

func TestScanWorkspace_NestedCheckoutsRespectDepth(t *testing.T) {
	root := t.TempDir()
	paths := []string{"outer", "outer/nested", "outer/nested/deep", "outer/nested/deep/beyond"}
	for _, path := range paths {
		createFakeCheckout(t, filepath.Join(root, path), "feature", "https://github.com/alice/project.git", "")
	}
	for _, depth := range []int{1, 2, 3, 4} {
		repos, err := ScanWorkspace(root, ScanOptions{Depth: depth})
		if err != nil {
			t.Fatal(err)
		}
		want := min(depth, 3)
		if len(repos) != want {
			t.Fatalf("depth %d found %d checkouts, want %d: %+v", depth, len(repos), want, repos)
		}
		for i, repo := range repos {
			if repo.Dir != filepath.Join(root, paths[i]) {
				t.Errorf("depth %d checkout %d = %q, want %q", depth, i, repo.Dir, paths[i])
			}
		}
	}
}

func TestReadRemotes_UsesEffectiveFirstFetchURL(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("insteadOf=%v", rewrite), func(t *testing.T) {
			dir := t.TempDir()
			createFakeCheckout(t, dir, "feature", "https://github.com/alice/project.git", "https://github.com/acme/project.git")
			for _, remote := range []string{"origin", "upstream"} {
				out, err := exec.Command("git", "-C", dir, "config", "--add", "remote."+remote+".url", "https://gitlab.com/wrong/second.git").CombinedOutput()
				if err != nil {
					t.Fatalf("add URL: %v: %s", err, out)
				}
			}
			if rewrite {
				out, err := exec.Command("git", "-C", dir, "config", "url.https://gitlab.com/.insteadOf", "https://github.com/").CombinedOutput()
				if err != nil {
					t.Fatalf("rewrite config: %v: %s", err, out)
				}
			}
			origin, upstream, err := readRemotes(dir)
			if err != nil {
				t.Fatal(err)
			}
			for remote, got := range map[string]string{"origin": origin, "upstream": upstream} {
				out, err := exec.Command("git", "-C", dir, "remote", "get-url", remote).Output()
				if err != nil {
					t.Fatal(err)
				}
				if got != strings.TrimSuffix(string(out), "\n") || strings.Contains(got, "/wrong/") {
					t.Errorf("%s = %q, Git fetch URL = %q", remote, got, out)
				}
			}
			if rewrite {
				repo, err := parseWorkspaceRepo(dir, dir)
				if err != nil {
					t.Fatal(err)
				}
				fetcher := &fakePRFetcher{fn: func(context.Context, string, string, string, string) (*gh.PullRequest, error) {
					t.Error("rewritten GitLab identity reached public GitHub")
					return nil, nil
				}}
				got := DiscoverPRsForRepos(context.Background(), fetcher, []WorkspaceRepo{repo}, DiscoverOptions{})
				if len(got) != 1 || got[0].Err == nil || repo.OriginHost != "gitlab.com" {
					t.Fatalf("rewritten identity was not rejected: %+v", got)
				}
			}
		})
	}
}

type mockPRFetcher struct {
	mu    sync.Mutex
	calls []string
	fn    func(ctx context.Context, owner, repo, headOwner, headBranch string) (*gh.PullRequest, error)
}

func (m *mockPRFetcher) Host() string { return "github.com" }

func (m *mockPRFetcher) FindOpenPRForHead(ctx context.Context, owner, repo, headOwner, headBranch string) (*gh.PullRequest, error) {
	m.mu.Lock()
	m.calls = append(m.calls, fmt.Sprintf("%s/%s<-%s:%s", owner, repo, headOwner, headBranch))
	m.mu.Unlock()
	if m.fn != nil {
		return m.fn(ctx, owner, repo, headOwner, headBranch)
	}
	return nil, nil
}

func TestResolveGitDir_StandardRepo(t *testing.T) {
	tmp := t.TempDir()
	dotGit := filepath.Join(tmp, ".git")
	if err := os.Mkdir(dotGit, 0o755); err != nil {
		t.Fatal(err)
	}

	gitDir, commonDir, err := resolveGitDir(tmp)
	if err != nil {
		t.Fatalf("resolveGitDir failed: %v", err)
	}
	if gitDir != dotGit {
		t.Errorf("got gitDir %q, want %q", gitDir, dotGit)
	}
	if commonDir != dotGit {
		t.Errorf("got commonDir %q, want %q", commonDir, dotGit)
	}
}

func TestResolveGitDir_LinkedWorktree(t *testing.T) {
	tmp := t.TempDir()
	mainRepo := filepath.Join(tmp, "main")
	mainGit := filepath.Join(mainRepo, ".git")
	wtGitDir := filepath.Join(mainGit, "worktrees", "feature-x")
	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// In worktree, commondir points back to mainGit (relative "../..")
	if err := os.WriteFile(filepath.Join(wtGitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create linked worktree folder
	worktreePath := filepath.Join(tmp, "worktrees", "feature-x")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	// .git file in worktree
	gitfileContent := fmt.Sprintf("gitdir: %s\n", wtGitDir)
	if err := os.WriteFile(filepath.Join(worktreePath, ".git"), []byte(gitfileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	gitDir, commonDir, err := resolveGitDir(worktreePath)
	if err != nil {
		t.Fatalf("resolveGitDir failed: %v", err)
	}
	if gitDir != wtGitDir {
		t.Errorf("got gitDir %q, want %q", gitDir, wtGitDir)
	}
	if commonDir != mainGit {
		t.Errorf("got commonDir %q, want %q", commonDir, mainGit)
	}
}

// Preserve main's alternate submodule layout alongside the original pointer-file coverage.
func TestResolveGitDir_Submodule_NestedMainLayout(t *testing.T) {
	tmp := t.TempDir()
	mainRepo := filepath.Join(tmp, "main")
	subGitDir := filepath.Join(mainRepo, ".git", "modules", "submod")
	if err := os.MkdirAll(subGitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Submodule checkout
	subPath := filepath.Join(mainRepo, "submod")
	if err := os.MkdirAll(subPath, 0o755); err != nil {
		t.Fatal(err)
	}
	gitfileContent := fmt.Sprintf("gitdir: %s\n", subGitDir)
	if err := os.WriteFile(filepath.Join(subPath, ".git"), []byte(gitfileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	gitDir, commonDir, err := resolveGitDir(subPath)
	if err != nil {
		t.Fatalf("resolveGitDir failed: %v", err)
	}
	if gitDir != subGitDir {
		t.Errorf("got gitDir %q, want %q", gitDir, subGitDir)
	}
	if commonDir != subGitDir {
		t.Errorf("got commonDir %q, want %q", commonDir, subGitDir)
	}
}

func TestReadRepoDetails_DetachedHEAD(t *testing.T) {
	tmp := t.TempDir()
	// Git resolves the effective URLs, so initialize refs as well as HEAD/config.
	createFakeCheckout(t, tmp, "main", "", "")
	dotGit := filepath.Join(tmp, ".git")
	if err := os.MkdirAll(dotGit, 0o755); err != nil {
		t.Fatal(err)
	}

	// 40-character commit SHA in HEAD
	if err := os.WriteFile(filepath.Join(dotGit, "HEAD"), []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configContent := `[remote "origin"]
	url = git@github.com:foo/bar.git
`
	if err := os.WriteFile(filepath.Join(dotGit, "config"), []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := parseWorkspaceRepo(tmp, tmp)
	if err != nil {
		t.Fatalf("readRepoDetails failed: %v", err)
	}
	if repo.CurrentBranch != "" {
		t.Errorf("expected empty branch for detached HEAD, got %q", repo.CurrentBranch)
	}
	if repo.OriginOwner != "foo" || repo.OriginRepo != "bar" || repo.Provider != "github" {
		t.Errorf("unexpected origin parsing: %+v", repo)
	}
}

func TestReadRepoDetails_OriginAndUpstream(t *testing.T) {
	tmp := t.TempDir()
	// Git resolves the effective URLs, so initialize refs as well as HEAD/config.
	createFakeCheckout(t, tmp, "main", "", "")
	dotGit := filepath.Join(tmp, ".git")
	if err := os.MkdirAll(dotGit, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dotGit, "HEAD"), []byte("ref: refs/heads/fix-auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configContent := `[remote "origin"]
	url = git@github.com:forkowner/spoon.git
[remote "upstream"]
	url = https://github.com/upstreamowner/spoon.git
`
	if err := os.WriteFile(filepath.Join(dotGit, "config"), []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := parseWorkspaceRepo(tmp, tmp)
	if err != nil {
		t.Fatalf("readRepoDetails failed: %v", err)
	}
	if repo.CurrentBranch != "fix-auth" {
		t.Errorf("expected branch 'fix-auth', got %q", repo.CurrentBranch)
	}
	// Origin identity stays distinct from the upstream target fields.
	if repo.OriginOwner != "forkowner" || repo.OriginRepo != "spoon" {
		t.Errorf("expected origin forkowner/spoon, got %s/%s", repo.OriginOwner, repo.OriginRepo)
	}
	if repo.UpstreamOwner != "upstreamowner" || repo.UpstreamRepo != "spoon" {
		t.Errorf("expected upstream upstreamowner/spoon, got %s/%s", repo.UpstreamOwner, repo.UpstreamRepo)
	}
}

func TestScanWorkspace_MultiRepo(t *testing.T) {
	root := t.TempDir()

	// 1. root itself has no .git
	// 2. sub1 has .git (normal repo)
	sub1 := filepath.Join(root, "frontend")
	createFakeCheckout(t, sub1, "main", "", "")
	_ = os.MkdirAll(filepath.Join(sub1, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(sub1, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	_ = os.WriteFile(filepath.Join(sub1, ".git", "config"), []byte("[remote \"origin\"]\n\turl = https://github.com/acme/frontend.git\n"), 0o644)

	// 3. sub2 has .git (worktree pointing to sub1)
	sub2 := filepath.Join(root, "frontend-feat")
	_ = os.MkdirAll(sub2, 0o755)
	sub1Worktrees := filepath.Join(sub1, ".git", "worktrees", "feat")
	_ = os.MkdirAll(sub1Worktrees, 0o755)
	_ = os.WriteFile(filepath.Join(sub1Worktrees, "commondir"), []byte("../..\n"), 0o644)
	_ = os.WriteFile(filepath.Join(sub1Worktrees, "HEAD"), []byte("ref: refs/heads/feature-x\n"), 0o644)
	_ = os.WriteFile(filepath.Join(sub2, ".git"), []byte("gitdir: "+sub1Worktrees+"\n"), 0o644)

	// 4. sub3 is ignored (node_modules)
	ignoredDir := filepath.Join(root, "node_modules", "package")
	_ = os.MkdirAll(filepath.Join(ignoredDir, ".git"), 0o755)

	repos, err := ScanWorkspace(root, ScanOptions{Depth: 1})
	if err != nil {
		t.Fatalf("ScanWorkspace failed: %v", err)
	}

	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}

	reposByDir := make(map[string]WorkspaceRepo)
	for _, r := range repos {
		reposByDir[r.Dir] = r
	}

	r1, ok := reposByDir[sub1]
	if !ok {
		t.Fatalf("sub1 not found in scan results: %+v", repos)
	}
	if r1.OriginOwner != "acme" || r1.OriginRepo != "frontend" || r1.CurrentBranch != "main" {
		t.Errorf("unexpected r1: %+v", r1)
	}

	r2, ok := reposByDir[sub2]
	if !ok {
		t.Fatalf("sub2 not found in scan results: %+v", repos)
	}
	if r2.OriginOwner != "acme" || r2.OriginRepo != "frontend" || r2.CurrentBranch != "feature-x" {
		t.Errorf("unexpected r2: %+v", r2)
	}
}

func TestDiscoverPRsInWorkspace_ConcurrencyAndOrder(t *testing.T) {
	repos := []WorkspaceRepo{
		{Provider: "github", OriginHost: "github.com", Dir: "/r1", OriginOwner: "org", OriginRepo: "frontend", CurrentBranch: "fix-ui"},
		{Provider: "github", OriginHost: "github.com", Dir: "/r2", OriginOwner: "fork", OriginRepo: "backend", CurrentBranch: "fix-api", UpstreamProvider: "github", UpstreamHost: "github.com", UpstreamOwner: "upstream", UpstreamRepo: "backend"},
		{Provider: "github", OriginHost: "github.com", Dir: "/r3", OriginOwner: "org", OriginRepo: "infra", CurrentBranch: "main"},
		{Provider: "github", OriginHost: "github.com", Dir: "/r4", OriginOwner: "org", OriginRepo: "detached", CurrentBranch: ""},
	}

	var running int32
	var maxRunning int32

	fetcher := &mockPRFetcher{
		fn: func(ctx context.Context, owner, repo, headOwner, headBranch string) (*gh.PullRequest, error) {
			cur := atomic.AddInt32(&running, 1)
			for {
				prev := atomic.LoadInt32(&maxRunning)
				if cur <= prev || atomic.CompareAndSwapInt32(&maxRunning, prev, cur) {
					break
				}
			}
			defer atomic.AddInt32(&running, -1)

			if repo == "frontend" && headBranch == "fix-ui" {
				return &gh.PullRequest{Number: 101, Title: "Fix UI"}, nil
			}
			if owner == "upstream" && repo == "backend" && headOwner == "fork" {
				return &gh.PullRequest{Number: 202, Title: "Fix API upstream"}, nil
			}
			if repo == "infra" {
				return nil, nil // No PR
			}
			return nil, errors.New("unexpected lookup")
		},
	}

	disc := DiscoverPRsForRepos(context.Background(), fetcher, repos, DiscoverOptions{Parallelism: 2, IncludeAll: true})
	if len(disc) != 4 {
		t.Fatalf("expected 4 results, got %d", len(disc))
	}

	// Check result order matches repos slice
	if disc[0].Repo.Dir != "/r1" || disc[0].PR == nil || disc[0].PR.Number != 101 {
		t.Errorf("unexpected disc[0]: %+v", disc[0])
	}
	if disc[1].Repo.Dir != "/r2" || disc[1].PR == nil || disc[1].PR.Number != 202 {
		t.Errorf("unexpected disc[1]: %+v", disc[1])
	}
	if disc[2].Repo.Dir != "/r3" || disc[2].PR != nil || disc[2].Err != nil {
		t.Errorf("unexpected disc[2]: %+v", disc[2])
	}
	if disc[3].Repo.Dir != "/r4" || disc[3].PR != nil || disc[3].Err != nil {
		t.Errorf("unexpected disc[3]: %+v", disc[3])
	}
}

func TestWorkspaceDiscoveryRemoteIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, origin, upstream string
		wantCalls              []string
		wantErr                bool
	}{
		{"github", "git@github.com:fork/repo.git", "https://github.com/up/repo.git", []string{"up/repo<-fork:fix", "fork/repo<-fork:fix"}, false},
		{"quoted URLs", `"https://github.com/fork/repo.git"`, `"git@github.com:up/repo.git"`, []string{"up/repo<-fork:fix", "fork/repo<-fork:fix"}, false},
		{"partial quotes", `https://github.com/"fork/repo.git"`, `git@github.com:"up/repo.git"`, []string{"up/repo<-fork:fix", "fork/repo<-fork:fix"}, false},
		{"inline comments", `"https://github.com/fork/repo.git" # origin`, `https://github.com/up/repo.git ; upstream`, []string{"up/repo<-fork:fix", "fork/repo<-fork:fix"}, false},
		{"continued URL", "https://github.com/fork/\\\nrepo.git", "https://github.com/up/repo.git", []string{"up/repo<-fork:fix", "fork/repo<-fork:fix"}, false},
		{"gitlab origin", "https://gitlab.com/private/repo.git", "https://github.com/up/repo.git", nil, true},
		{"enterprise origin", "ssh://git@github.example.com/private/repo.git", "https://github.com/up/repo.git", nil, true},
		{"gitlab upstream", "https://github.com/fork/repo.git", "https://gitlab.com/private/repo.git", []string{"fork/repo<-fork:fix"}, true},
		{"enterprise upstream", "https://github.com/fork/repo.git", "https://github.example.com/private/repo.git", []string{"fork/repo<-fork:fix"}, true},
		{"identical remotes", "https://github.com/fork/repo.git", "https://github.com/fork/repo.git", []string{"fork/repo<-fork:fix"}, false},
		// Unsupported local origins are incomplete discovery, not a confirmed absence of PRs.
		{"local origin", "/private/repo.git", "https://github.com/up/repo.git", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			createFakeCheckout(t, dir, "fix", "", "")
			gitDir := filepath.Join(dir, ".git")
			if err := os.MkdirAll(gitDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/fix\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			config := fmt.Sprintf("[remote \"origin\"]\nurl = %s\n[remote \"upstream\"]\nurl = %s\n", tc.origin, tc.upstream)
			if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			repo, err := parseWorkspaceRepo(dir, dir)
			if err != nil {
				t.Fatal(err)
			}
			fetcher := &mockPRFetcher{}
			got := DiscoverPRsForRepos(context.Background(), fetcher, []WorkspaceRepo{repo}, DiscoverOptions{IncludeAll: true})
			if !slices.Equal(fetcher.calls, tc.wantCalls) || (got[0].Err != nil) != tc.wantErr {
				t.Fatalf("calls=%v err=%v; want calls=%v error=%v", fetcher.calls, got[0].Err, tc.wantCalls, tc.wantErr)
			}
		})
	}
}

func TestReadRepoDetails_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	// Git resolves the effective URLs, so initialize refs as well as HEAD/config.
	createFakeCheckout(t, dir, "main", "", "")
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[remote \"origin\"]\nurl = \"unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parseWorkspaceRepo(dir, dir); err == nil {
		t.Fatal("malformed Git config must return an error")
	}
}

func TestWorkspaceDiscoveryRetainsLookupErrors(t *testing.T) {
	failure := errors.New("lookup failed")
	for _, tc := range []struct {
		name                   string
		upstreamErr, originErr error
		originMatch            bool
	}{
		{"upstream error with origin match", failure, nil, true},
		{"upstream error with no match", failure, nil, false},
		{"origin error after upstream miss", nil, failure, false},
		{"both fail", failure, failure, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := WorkspaceRepo{Provider: "github", OriginHost: "github.com", OriginOwner: "fork", OriginRepo: "repo", CurrentBranch: "fix", UpstreamProvider: "github", UpstreamHost: "github.com", UpstreamOwner: "up", UpstreamRepo: "repo"}
			fetcher := &mockPRFetcher{fn: func(_ context.Context, owner, _, _, _ string) (*gh.PullRequest, error) {
				if owner == "up" {
					return nil, tc.upstreamErr
				}
				if tc.originMatch {
					return &gh.PullRequest{Number: 42}, nil
				}
				return nil, tc.originErr
			}}
			got := DiscoverPRsForRepos(context.Background(), fetcher, []WorkspaceRepo{repo}, DiscoverOptions{IncludeAll: true})[0]
			if !errors.Is(got.Err, failure) || (got.PR != nil) != tc.originMatch || len(fetcher.calls) != 2 {
				t.Fatalf("result=%+v calls=%v", got, fetcher.calls)
			}
		})
	}
}

func TestScanWorkspaceFromCheckoutAndSubdirectory(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"app", "lib"} {
		createFakeCheckout(t, filepath.Join(root, name), "main", "", "")
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(root, "app", "src", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{root, filepath.Join(root, "app"), nested} {
		t.Run(from, func(t *testing.T) {
			repos, err := ScanWorkspace(from, ScanOptions{Depth: 1})
			if err != nil || len(repos) != 2 || repos[0].Dir != filepath.Join(root, "app") || repos[1].Dir != filepath.Join(root, "lib") {
				t.Fatalf("repos=%+v err=%v", repos, err)
			}
		})
	}
}
