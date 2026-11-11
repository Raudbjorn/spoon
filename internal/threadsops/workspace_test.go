package threadsops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

type mockPRFetcher struct {
	mu    sync.Mutex
	calls []string
	fn    func(ctx context.Context, owner, repo, headOwner, headBranch string) (*github.PullRequest, error)
}

func (m *mockPRFetcher) Host() string { return "github.com" }

func (m *mockPRFetcher) FindOpenPRForHead(ctx context.Context, owner, repo, headOwner, headBranch string) (*github.PullRequest, error) {
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

func TestResolveGitDir_Submodule(t *testing.T) {
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
	dotGit := filepath.Join(tmp, ".git")
	if err := os.Mkdir(dotGit, 0o755); err != nil {
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

	repo, err := readRepoDetails(tmp)
	if err != nil {
		t.Fatalf("readRepoDetails failed: %v", err)
	}
	if repo.CurrentBranch != "" {
		t.Errorf("expected empty branch for detached HEAD, got %q", repo.CurrentBranch)
	}
	if repo.Owner != "foo" || repo.Repo != "bar" || repo.Provider != "github" {
		t.Errorf("unexpected origin parsing: %+v", repo)
	}
}

func TestReadRepoDetails_OriginAndUpstream(t *testing.T) {
	tmp := t.TempDir()
	dotGit := filepath.Join(tmp, ".git")
	if err := os.Mkdir(dotGit, 0o755); err != nil {
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

	repo, err := readRepoDetails(tmp)
	if err != nil {
		t.Fatalf("readRepoDetails failed: %v", err)
	}
	if repo.CurrentBranch != "fix-auth" {
		t.Errorf("expected branch 'fix-auth', got %q", repo.CurrentBranch)
	}
	if repo.Owner != "forkowner" || repo.Repo != "spoon" {
		t.Errorf("expected origin forkowner/spoon, got %s/%s", repo.Owner, repo.Repo)
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
	if r1.Owner != "acme" || r1.Repo != "frontend" || r1.CurrentBranch != "main" {
		t.Errorf("unexpected r1: %+v", r1)
	}

	r2, ok := reposByDir[sub2]
	if !ok {
		t.Fatalf("sub2 not found in scan results: %+v", repos)
	}
	if r2.Owner != "acme" || r2.Repo != "frontend" || r2.CurrentBranch != "feature-x" {
		t.Errorf("unexpected r2: %+v", r2)
	}
}

func TestDiscoverPRsInWorkspace_ConcurrencyAndOrder(t *testing.T) {
	repos := []WorkspaceRepo{
		{Provider: "github", Host: "github.com", Dir: "/r1", Owner: "org", Repo: "frontend", CurrentBranch: "fix-ui"},
		{Provider: "github", Host: "github.com", Dir: "/r2", Owner: "fork", Repo: "backend", CurrentBranch: "fix-api", UpstreamProvider: "github", UpstreamHost: "github.com", UpstreamOwner: "upstream", UpstreamRepo: "backend"},
		{Provider: "github", Host: "github.com", Dir: "/r3", Owner: "org", Repo: "infra", CurrentBranch: "main"},
		{Provider: "github", Host: "github.com", Dir: "/r4", Owner: "org", Repo: "detached", CurrentBranch: ""},
	}

	var running int32
	var maxRunning int32

	fetcher := &mockPRFetcher{
		fn: func(ctx context.Context, owner, repo, headOwner, headBranch string) (*github.PullRequest, error) {
			cur := atomic.AddInt32(&running, 1)
			for {
				prev := atomic.LoadInt32(&maxRunning)
				if cur <= prev || atomic.CompareAndSwapInt32(&maxRunning, prev, cur) {
					break
				}
			}
			defer atomic.AddInt32(&running, -1)

			if repo == "frontend" && headBranch == "fix-ui" {
				return &github.PullRequest{Number: 101, Title: "Fix UI"}, nil
			}
			if owner == "upstream" && repo == "backend" && headOwner == "fork" {
				return &github.PullRequest{Number: 202, Title: "Fix API upstream"}, nil
			}
			if repo == "infra" {
				return nil, nil // No PR
			}
			return nil, errors.New("unexpected lookup")
		},
	}

	disc := DiscoverPRsInWorkspace(context.Background(), fetcher, repos, DiscoverOptions{Parallelism: 2})
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
		{"gitlab origin", "https://gitlab.com/private/repo.git", "https://github.com/up/repo.git", nil, true},
		{"enterprise origin", "ssh://git@github.example.com/private/repo.git", "https://github.com/up/repo.git", nil, true},
		{"gitlab upstream", "https://github.com/fork/repo.git", "https://gitlab.com/private/repo.git", []string{"fork/repo<-fork:fix"}, true},
		{"enterprise upstream", "https://github.com/fork/repo.git", "https://github.example.com/private/repo.git", []string{"fork/repo<-fork:fix"}, true},
		{"identical remotes", "https://github.com/fork/repo.git", "https://github.com/fork/repo.git", []string{"fork/repo<-fork:fix"}, false},
		{"local origin", "/private/repo.git", "https://github.com/up/repo.git", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitDir := filepath.Join(dir, ".git")
			if err := os.Mkdir(gitDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/fix\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			config := fmt.Sprintf("[remote \"origin\"]\nurl = %s\n[remote \"upstream\"]\nurl = %s\n", tc.origin, tc.upstream)
			if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			repo, err := readRepoDetails(dir)
			if err != nil {
				t.Fatal(err)
			}
			fetcher := &mockPRFetcher{}
			got := DiscoverPRsInWorkspace(context.Background(), fetcher, []WorkspaceRepo{repo}, DiscoverOptions{})
			if !slices.Equal(fetcher.calls, tc.wantCalls) || (got[0].Err != nil) != tc.wantErr {
				t.Fatalf("calls=%v err=%v; want calls=%v error=%v", fetcher.calls, got[0].Err, tc.wantCalls, tc.wantErr)
			}
		})
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
			repo := WorkspaceRepo{Provider: "github", Host: "github.com", Owner: "fork", Repo: "repo", CurrentBranch: "fix", UpstreamProvider: "github", UpstreamHost: "github.com", UpstreamOwner: "up", UpstreamRepo: "repo"}
			fetcher := &mockPRFetcher{fn: func(_ context.Context, owner, _, _, _ string) (*github.PullRequest, error) {
				if owner == "up" {
					return nil, tc.upstreamErr
				}
				if tc.originMatch {
					return &github.PullRequest{Number: 42}, nil
				}
				return nil, tc.originErr
			}}
			got := DiscoverPRsInWorkspace(context.Background(), fetcher, []WorkspaceRepo{repo}, DiscoverOptions{})[0]
			if !errors.Is(got.Err, failure) || (got.PR != nil) != tc.originMatch || len(fetcher.calls) != 2 {
				t.Fatalf("result=%+v calls=%v", got, fetcher.calls)
			}
		})
	}
}

func TestScanWorkspaceFromCheckoutAndSubdirectory(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"app", "lib"} {
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
