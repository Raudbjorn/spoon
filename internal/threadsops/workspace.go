package threadsops

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// WorkspaceRepo describes a sibling git checkout found in a workspace scan.
type WorkspaceRepo struct {
	Dir           string // absolute path to repository directory
	RelDir        string // relative path from the scan root (e.g. "./frontend")
	OriginURL     string // raw value of remote.origin.url
	UpstreamURL   string // raw value of remote.upstream.url (if present)
	Owner         string // target repo owner (upstream if present, else origin)
	Repo          string // target repo name (upstream if present, else origin)
	OriginOwner   string // owner parsed from OriginURL
	OriginRepo    string // repo parsed from OriginURL
	UpstreamOwner string // owner parsed from UpstreamURL
	UpstreamRepo  string // repo parsed from UpstreamURL
	Provider      string // "github" / "gitlab" / etc.
	CurrentBranch string // current branch from HEAD (empty on detached HEAD)
}

// ScanOptions configures ScanWorkspace.
type ScanOptions struct {
	Depth      int      // max subdirectory depth (default 1, capped at 3)
	IgnoreDirs []string // extra directory names to skip (vendor/, node_modules/, etc.)
}

// PRFetcher is the interface for resolving an open PR for a head branch.
// Satisfied by *github.Client.
type PRFetcher interface {
	FindOpenPRForHead(ctx context.Context, owner, repo, headOwner, headBranch string) (*gh.PullRequest, error)
}

// DiscoverOptions configures DiscoverPRsInWorkspace.
type DiscoverOptions struct {
	ScanOptions             // embedded ScanOptions
	Parallelism int         // bounded worker count (default 8)
	Logger      io.Writer   // optional logger for per-repo lookup warnings/errors
	IncludeAll  bool        // if true, include repos without an open PR
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
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "ssh://") {
		raw = strings.TrimPrefix(raw, "ssh://")
		if strings.HasPrefix(raw, "git@") {
			raw = strings.TrimPrefix(raw, "git@")
		}
		return "https://" + raw
	}
	if strings.HasPrefix(raw, "git@") {
		after := strings.TrimPrefix(raw, "git@")
		if idx := strings.Index(after, ":"); idx != -1 {
			host := after[:idx]
			path := after[idx+1:]
			return "https://" + host + "/" + path
		}
	}
	return raw
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
	if strings.HasPrefix(trimmed, "ref: ") {
		ref := strings.TrimPrefix(trimmed, "ref: ")
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return ""
}

// parseRemotes extracts the URL for origin and upstream from <commondir>/config.
func parseRemotes(configContent string) (originURL, upstreamURL string) {
	scanner := bufio.NewScanner(strings.NewReader(configContent))
	var currentRemote string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(line[1 : len(line)-1])
			currentRemote = ""
			if strings.HasPrefix(section, "remote ") {
				remName := strings.TrimPrefix(section, "remote ")
				remName = strings.Trim(remName, `"' `)
				currentRemote = remName
			}
			continue
		}
		if currentRemote == "" {
			continue
		}
		if idx := strings.Index(line, "="); idx != -1 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			val = strings.Trim(val, `"' `)
			if strings.EqualFold(key, "url") {
				if currentRemote == "origin" && originURL == "" {
					originURL = val
				} else if currentRemote == "upstream" && upstreamURL == "" {
					upstreamURL = val
				}
			}
		}
	}
	return originURL, upstreamURL
}

func parseWorkspaceRepo(repoDir, root string) (WorkspaceRepo, error) {
	gitDir, commonDir, err := resolveGitDir(repoDir)
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

	// Read config
	if cfgBytes, err := os.ReadFile(filepath.Join(commonDir, "config")); err == nil {
		repo.OriginURL, repo.UpstreamURL = parseRemotes(string(cfgBytes))

		if repo.OriginURL != "" {
			norm := normalizeGitURL(repo.OriginURL)
			provider, _, o, r, err := forge.ParseRepoURL(norm, "github.com", 0)
			if err == nil {
				repo.OriginOwner = o
				repo.OriginRepo = r
				repo.Owner = o
				repo.Repo = r
				repo.Provider = provider.String()
			}
		}

		if repo.UpstreamURL != "" {
			norm := normalizeGitURL(repo.UpstreamURL)
			provider, _, o, r, err := forge.ParseRepoURL(norm, "github.com", 0)
			if err == nil {
				repo.UpstreamOwner = o
				repo.UpstreamRepo = r
				// Upstream takes precedence for target repository
				repo.Owner = o
				repo.Repo = r
				repo.Provider = provider.String()
			}
		}
	}

	return repo, nil
}

var defaultIgnoreDirs = map[string]bool{
	".git":           true,
	"node_modules":   true,
	"vendor":         true,
	".do-not-commit": true,
	".worktrees":     true,
	"build":          true,
	"dist":           true,
}

// ScanWorkspace returns one entry per .git directory or worktree pointer found
// in root or immediate subdirectories up to opts.Depth (default 1, capped at 3).
func ScanWorkspace(root string, opts ScanOptions) ([]WorkspaceRepo, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
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
		if r, err := parseWorkspaceRepo(absRoot, absRoot); err == nil {
			repos = append(repos, r)
			seenDirs[absRoot] = true
		}
	}

	var walk func(dir string, curDepth int) error
	walk = func(dir string, curDepth int) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil // Skip unreadable directories
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if ignore[name] || strings.HasPrefix(name, ".") {
				continue
			}

			subPath := filepath.Join(dir, name)
			if seenDirs[subPath] {
				continue
			}

			// Check if subPath is a git repo/worktree
			if _, _, err := resolveGitDir(subPath); err == nil {
				if r, err := parseWorkspaceRepo(subPath, absRoot); err == nil {
					repos = append(repos, r)
					seenDirs[subPath] = true
				}
				// Don't recurse into discovered git repo
				continue
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

			// Cannot query without a branch or owner/repo
			if repo.CurrentBranch == "" || repo.Owner == "" || repo.Repo == "" {
				rawResults[idx] = WorkspaceDiscovery{Repo: repo}
				return
			}

			var pr *gh.PullRequest
			var err error

			// 1. Upstream query (with origin headOwner if cross-repo)
			if repo.UpstreamOwner != "" && repo.UpstreamRepo != "" {
				headOwner := repo.OriginOwner
				pr, err = fetcher.FindOpenPRForHead(ctx, repo.UpstreamOwner, repo.UpstreamRepo, headOwner, repo.CurrentBranch)
				if err == nil && pr != nil {
					pr.RepoOwner = repo.UpstreamOwner
					pr.RepoName = repo.UpstreamRepo
					pr.RepoDir = repo.RelDir
				} else if err != nil && opts.Logger != nil {
					fmt.Fprintf(opts.Logger, "warning: checking PR for %s/%s: %v\n", repo.UpstreamOwner, repo.UpstreamRepo, err)
				}

				// 2. Fallback to origin if no PR found in upstream
				if pr == nil && repo.OriginOwner != "" && repo.OriginRepo != "" &&
					(repo.OriginOwner != repo.UpstreamOwner || repo.OriginRepo != repo.UpstreamRepo) {
					prOrig, errOrig := fetcher.FindOpenPRForHead(ctx, repo.OriginOwner, repo.OriginRepo, "", repo.CurrentBranch)
					if errOrig == nil && prOrig != nil {
						pr = prOrig
						pr.RepoOwner = repo.OriginOwner
						pr.RepoName = repo.OriginRepo
						pr.RepoDir = repo.RelDir
						err = nil
					} else if errOrig != nil && err == nil {
						err = errOrig
					}
				}
			} else {
				// No upstream remote, query origin directly
				pr, err = fetcher.FindOpenPRForHead(ctx, repo.OriginOwner, repo.OriginRepo, "", repo.CurrentBranch)
				if err == nil && pr != nil {
					pr.RepoOwner = repo.OriginOwner
					pr.RepoName = repo.OriginRepo
					pr.RepoDir = repo.RelDir
				} else if err != nil && opts.Logger != nil {
					fmt.Fprintf(opts.Logger, "warning: checking PR for %s/%s: %v\n", repo.OriginOwner, repo.OriginRepo, err)
				}
			}

			rawResults[idx] = WorkspaceDiscovery{
				Repo: repo,
				PR:   pr,
				Err:  err,
			}
		}(i, r)
	}

	wg.Wait()

	if opts.IncludeAll {
		return rawResults
	}

	var filtered []WorkspaceDiscovery
	for _, d := range rawResults {
		if d.PR != nil {
			filtered = append(filtered, d)
		}
	}
	return filtered
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
