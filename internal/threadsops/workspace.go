package threadsops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// WorkspaceRepo describes a sibling git checkout found in a workspace scan.
type WorkspaceRepo struct {
	Dir              string // absolute path to repository directory
	RelDir           string // relative path from the scan root (e.g. "./frontend")
	OriginURL        string // effective fetch URL for origin, including Git URL rewrites
	UpstreamURL      string // effective fetch URL for upstream (if present)
	Owner            string // target repo owner (upstream if present, else origin)
	Repo             string // target repo name (upstream if present, else origin)
	OriginOwner      string // owner parsed from OriginURL
	OriginRepo       string // repo parsed from OriginURL
	OriginHost       string // service host parsed from OriginURL
	UpstreamOwner    string // owner parsed from UpstreamURL
	UpstreamRepo     string // repo parsed from UpstreamURL
	UpstreamHost     string // service host parsed from UpstreamURL
	Provider         string // origin provider: "github" / "gitlab" / etc.
	UpstreamProvider string // provider parsed from UpstreamURL
	CurrentBranch    string // current branch from HEAD (empty on detached HEAD)
}

// ScanOptions configures ScanWorkspace.
type ScanOptions struct {
	Depth      int      // max subdirectory depth (default 1, capped at 3)
	IgnoreDirs []string // extra directory names to skip (vendor/, node_modules/, etc.)
}

// PRFetcher is the interface for resolving an open PR for a head branch.
// Satisfied by *github.Client.
type PRFetcher interface {
	Host() string
	FindOpenPRForHead(ctx context.Context, owner, repo, headOwner, headBranch string) (*gh.PullRequest, error)
}

// DiscoverOptions configures DiscoverPRsInWorkspace.
type DiscoverOptions struct {
	ScanOptions                 // embedded ScanOptions
	Parallelism int             // bounded worker count (default 8)
	Logger      io.Writer       // optional logger for per-repo lookup warnings/errors
	IncludeAll  bool            // include confirmed no-PR results too; failures are always returned
	Repos       []WorkspaceRepo // optional pre-scanned repos (if empty, ScanWorkspace is called)
}

// WorkspaceDiscovery holds the discovery result for a single workspace checkout.
type WorkspaceDiscovery struct {
	Repo WorkspaceRepo
	PR   *gh.PullRequest // nil if no open PR found
	Err  error           // non-nil if lookup failed
}

// resolveGitDir handles both standard .git directories and .git pointer files (gitdir: <path>),
// resolving linked worktree commondir (for config) and worktree-local gitdir (for HEAD).
func resolveGitDir(dir string) (gitDir, commonDir string, err error) {
	gitPath := dir
	baseDir := filepath.Dir(dir)
	if filepath.Base(dir) != ".git" {
		gitPath = filepath.Join(dir, ".git")
		baseDir = dir
	}
	fi, err := os.Stat(gitPath)
	if err != nil {
		return "", "", err
	}

	if fi.IsDir() {
		return gitPath, gitPath, nil
	}

	// Pointer file: read "gitdir: <path>"
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return "", "", fmt.Errorf("reading .git file: %w", err)
	}

	trimmed := strings.TrimSpace(string(content))
	if !strings.HasPrefix(trimmed, "gitdir:") {
		return "", "", fmt.Errorf("invalid .git pointer file in %s", dir)
	}
	target := strings.TrimSpace(strings.TrimPrefix(trimmed, "gitdir:"))
	if !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, target)
	}
	gitDir = filepath.Clean(target)
	commonDir = gitDir

	// Linked worktrees contain a commondir file pointing to the main git directory.
	commonPath := filepath.Join(gitDir, "commondir")
	if cBytes, err := os.ReadFile(commonPath); err == nil {
		cTarget := strings.TrimSpace(string(cBytes))
		if !filepath.IsAbs(cTarget) {
			cTarget = filepath.Join(gitDir, cTarget)
		}
		commonDir = filepath.Clean(cTarget)
	}

	return gitDir, commonDir, nil
}

// ResolveGitDir is resolveGitDir exported for external callers and tests.
func ResolveGitDir(dir string) (gitDir, commonDir string, err error) {
	return resolveGitDir(dir)
}

// normalizeGitURL converts scp-like SSH URLs (git@github.com:owner/repo.git)
// and ssh:// URLs into an HTTPS form compatible with forge.ParseRepoURL.
func normalizeGitURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	// git@host:owner/repo.git -> https://host/owner/repo.git
	if strings.HasPrefix(s, "git@") {
		colonIdx := strings.Index(s, ":")
		if colonIdx > 0 {
			host := strings.TrimPrefix(s[:colonIdx], "git@")
			path := strings.TrimPrefix(s[colonIdx+1:], "/")
			return "https://" + host + "/" + path
		}
	}

	// ssh://git@host/owner/repo.git -> https://host/owner/repo.git
	if strings.HasPrefix(s, "ssh://") {
		trimmed := strings.TrimPrefix(s, "ssh://")
		if atIdx := strings.Index(trimmed, "@"); atIdx >= 0 {
			trimmed = trimmed[atIdx+1:]
		}
		return "https://" + trimmed
	}

	// git://host/owner/repo.git -> https://host/owner/repo.git
	if strings.HasPrefix(s, "git://") {
		return "https://" + strings.TrimPrefix(s, "git://")
	}

	return s
}

// parseRemoteIdentity rejects local paths instead of treating them as hosted repositories.
func parseRemoteIdentity(raw string) (forge.Provider, string, string, string, error) {
	normalized := normalizeGitURL(raw)
	u, err := url.Parse(normalized)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return 0, "", "", "", fmt.Errorf("remote has no supported host")
	}
	return forge.ParseRepoURL(normalized, "", 0)
}

// NormalizeGitURL is normalizeGitURL exported for testing.
func NormalizeGitURL(raw string) string {
	return normalizeGitURL(raw)
}

// parseBranch parses the current branch from <gitdir>/HEAD content.
// Returns empty string on detached HEAD (raw commit hash) or invalid ref.
func parseBranch(headContent string) string {
	trimmed := strings.TrimSpace(headContent)
	if strings.HasPrefix(trimmed, "ref: refs/heads/") {
		return strings.TrimPrefix(trimmed, "ref: refs/heads/")
	}
	return ""
}

// readRemotes delegates includes, URL selection and insteadOf rewriting to Git.
func readRemotes(dir string) (originURL, upstreamURL string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var urls [2]string
	for i, remote := range []string{"origin", "upstream"} {
		out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", remote).Output()
		var exitErr *exec.ExitError
		if err != nil {
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
				continue // Git reports an absent remote with exit status 2.
			}
			return "", "", fmt.Errorf("reading remote %s in %q: %w", remote, dir, err)
		}
		urls[i] = strings.TrimSuffix(string(out), "\n")
	}
	return urls[0], urls[1], nil
}

func parseWorkspaceRepo(repoDir, root string) (WorkspaceRepo, error) {
	gitDir, _, err := resolveGitDir(repoDir)
	if err != nil {
		return WorkspaceRepo{}, err
	}

	absDir, err := filepath.Abs(repoDir)
	if err != nil {
		absDir = repoDir
	}

	relDir := "."
	if absRoot, err := filepath.Abs(root); err == nil {
		if rel, err := filepath.Rel(absRoot, absDir); err == nil {
			if rel != "." && !strings.HasPrefix(rel, "./") && !strings.HasPrefix(rel, "../") {
				relDir = "./" + rel
			} else {
				relDir = rel
			}
		}
	}

	repo := WorkspaceRepo{
		Dir:    absDir,
		RelDir: relDir,
	}

	// Read HEAD
	if headBytes, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		repo.CurrentBranch = parseBranch(string(headBytes))
	}

	repo.OriginURL, repo.UpstreamURL, err = readRemotes(absDir)
	if err != nil {
		return WorkspaceRepo{}, err
	}

	if repo.OriginURL != "" {
		provider, host, o, r, err := parseRemoteIdentity(repo.OriginURL)
		if err == nil {
			repo.OriginHost = host
			repo.OriginOwner = o
			repo.OriginRepo = r
			repo.Owner = o
			repo.Repo = r
			repo.Provider = provider.String()
		}
	}

	if repo.UpstreamURL != "" {
		provider, host, o, r, err := parseRemoteIdentity(repo.UpstreamURL)
		if err == nil {
			repo.UpstreamProvider = provider.String()
			repo.UpstreamHost = host
			repo.UpstreamOwner = o
			repo.UpstreamRepo = r
			// Upstream takes precedence for target repository; Provider describes origin.
			repo.Owner = o
			repo.Repo = r
		}
	}

	return repo, nil
}

var defaultIgnoreDirs = map[string]bool{
	".git":           true,
	"node_modules":   true,
	"vendor":         true,
	".do-not-commit": true,
	".cache":         true,
	".gemini":        true,
	".superpowers":   true,
	"build":          true,
	"dist":           true,
}

// ScanWorkspace returns one entry per .git directory or worktree pointer found
// in root or immediate subdirectories up to opts.Depth (default 1, capped at 3).
// From inside a checkout, it scans the checkout's parent to include siblings.
func ScanWorkspace(root string, opts ScanOptions) ([]WorkspaceRepo, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}

	if fi, err := os.Stat(absRoot); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory: %q", absRoot)
	}
	for dir := absRoot; ; dir = filepath.Dir(dir) {
		if _, _, err := resolveGitDir(dir); err == nil {
			absRoot = filepath.Dir(dir)
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}

	depth := opts.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}

	ignore := make(map[string]bool, len(defaultIgnoreDirs)+len(opts.IgnoreDirs))
	for k, v := range defaultIgnoreDirs {
		ignore[k] = v
	}
	for _, d := range opts.IgnoreDirs {
		ignore[d] = true
	}

	var repos []WorkspaceRepo
	seenDirs := make(map[string]bool)

	// Check if root itself is a git repository
	if _, _, err := resolveGitDir(absRoot); err == nil {
		r, err := parseWorkspaceRepo(absRoot, absRoot)
		if err != nil {
			return nil, err
		}
		repos = append(repos, r)
		seenDirs[absRoot] = true
	}

	var walk func(dir string, curDepth int) error
	walk = func(dir string, curDepth int) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("reading workspace directory %q: %w", dir, err)
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if ignore[name] || (strings.HasPrefix(name, ".") && name != ".worktrees") {
				continue
			}

			subPath := filepath.Join(dir, name)
			if seenDirs[subPath] {
				continue
			}

			// Check if subPath is a git repo/worktree
			if _, _, err := resolveGitDir(subPath); err == nil {
				r, err := parseWorkspaceRepo(subPath, absRoot)
				if err != nil {
					return err
				}
				repos = append(repos, r)
				seenDirs[subPath] = true
			}

			if curDepth < depth {
				if err := walk(subPath, curDepth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walk(absRoot, 1); err != nil {
		return nil, err
	}

	sort.Slice(repos, func(i, j int) bool {
		return repos[i].Dir < repos[j].Dir
	})

	return repos, nil
}

// DiscoverPRsForRepos concurrently queries open PRs for each workspace repo
// against upstream (with origin fallback) using bounded workers.
func DiscoverPRsForRepos(ctx context.Context, fetcher PRFetcher, repos []WorkspaceRepo, opts DiscoverOptions) []WorkspaceDiscovery {
	if len(repos) == 0 {
		return nil
	}

	parallelism := opts.Parallelism
	if parallelism <= 0 {
		parallelism = 8
	}
	if parallelism > len(repos) {
		parallelism = len(repos)
	}

	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	rawResults := make([]WorkspaceDiscovery, len(repos))

	for i, r := range repos {
		wg.Add(1)
		go func(idx int, repo WorkspaceRepo) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				rawResults[idx] = WorkspaceDiscovery{Repo: repo, Err: ctx.Err()}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			rawResults[idx] = discoverPRForRepo(ctx, fetcher, repo)
		}(i, r)
	}

	wg.Wait()

	var filtered []WorkspaceDiscovery
	for _, d := range rawResults {
		// Writers need not be concurrency-safe; emit warnings after all workers finish.
		if d.Err != nil && opts.Logger != nil {
			fmt.Fprintf(opts.Logger, "warning: checking PR for %q: %q\n", d.Repo.RelDir, d.Err.Error())
		}
		if opts.IncludeAll || d.PR != nil || d.Err != nil {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

func discoverPRForRepo(ctx context.Context, fetcher PRFetcher, repo WorkspaceRepo) WorkspaceDiscovery {
	d := WorkspaceDiscovery{Repo: repo}
	if repo.CurrentBranch == "" || fetcher == nil {
		return d
	}
	// Both the head and target identities must belong to the client's service.
	host := fetcher.Host()
	if repo.Provider != "github" || !strings.EqualFold(repo.OriginHost, host) {
		d.Err = fmt.Errorf("origin host %q is not served by GitHub host %q", repo.OriginHost, host)
		return d
	}
	if repo.OriginOwner == "" || repo.OriginRepo == "" {
		return d
	}
	lookup := func(owner, name, headOwner string) {
		pr, err := fetcher.FindOpenPRForHead(ctx, owner, name, headOwner, repo.CurrentBranch)
		if err != nil {
			d.Err = errors.Join(d.Err, fmt.Errorf("%s/%s: %w", owner, name, err))
			return
		}
		if pr != nil {
			copyPR := *pr
			copyPR.RepoOwner, copyPR.RepoName = owner, name
			copyPR.RepoDir, copyPR.RepoRoot = repo.RelDir, repo.Dir
			d.PR = &copyPR
		}
	}
	upstreamSupported := repo.UpstreamProvider == "github" && strings.EqualFold(repo.UpstreamHost, host) && repo.UpstreamOwner != "" && repo.UpstreamRepo != ""
	if upstreamSupported {
		lookup(repo.UpstreamOwner, repo.UpstreamRepo, repo.OriginOwner)
	} else if repo.UpstreamURL != "" || repo.UpstreamOwner != "" {
		d.Err = fmt.Errorf("upstream host %q is not served by GitHub host %q", repo.UpstreamHost, host)
	}
	if d.PR == nil && (!upstreamSupported || !strings.EqualFold(repo.OriginOwner+"/"+repo.OriginRepo, repo.UpstreamOwner+"/"+repo.UpstreamRepo)) {
		lookup(repo.OriginOwner, repo.OriginRepo, repo.OriginOwner)
	}
	return d
}

// DiscoverPRsInWorkspace scans sibling checkouts up to opts.Depth (or uses opts.Repos)
// and concurrently queries open PRs against upstream (with origin fallback) using bounded workers.
func DiscoverPRsInWorkspace(ctx context.Context, fetcher PRFetcher, root string, opts DiscoverOptions) ([]WorkspaceDiscovery, error) {
	repos := opts.Repos
	if len(repos) == 0 {
		var err error
		repos, err = ScanWorkspace(root, opts.ScanOptions)
		if err != nil {
			return nil, err
		}
	}
	return DiscoverPRsForRepos(ctx, fetcher, repos, opts), nil
}
