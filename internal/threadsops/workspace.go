package threadsops

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/github"
)

// WorkspaceRepo describes a sibling git checkout found in a workspace scan.
type WorkspaceRepo struct {
	Dir           string // absolute path to repository directory
	OriginURL     string // decoded value of remote.origin.url
	Owner         string // parsed from OriginURL via forge.ParseRepoURL
	Repo          string // parsed repository name
	Provider      string // "github" / "gitlab" / "gitea"
	Host          string // origin host; never inferred from the API client
	CurrentBranch string // current branch name, or empty if detached HEAD

	UpstreamURL      string // decoded value of remote.upstream.url, if present
	UpstreamOwner    string // parsed owner of upstream remote, if present
	UpstreamRepo     string // parsed repository name of upstream remote, if present
	UpstreamProvider string
	UpstreamHost     string
}

// ScanOptions configures workspace repository scanning.
type ScanOptions struct {
	Depth      int      // max directory search depth (default 1, max 3)
	IgnoreDirs []string // extra directory names to skip (vendor, node_modules, etc.)
}

// PRFetcher abstracts looking up an open pull request by head ref.
// github.Client implements this via FindOpenPRForHead.
type PRFetcher interface {
	Host() string
	FindOpenPRForHead(ctx context.Context, owner, repo, headOwner, headBranch string) (*github.PullRequest, error)
}

// parseRemoteIdentity rejects local paths instead of treating them as GitHub shorthand.
func parseRemoteIdentity(raw string) (forge.Provider, string, string, string, error) {
	normalized := normalizeRemoteURL(raw)
	u, err := url.Parse(normalized)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return 0, "", "", "", fmt.Errorf("remote has no supported host")
	}
	return forge.ParseRepoURL(normalized, "", 0)
}

// DiscoverOptions configures concurrent pull request discovery in a workspace.
type DiscoverOptions struct {
	Parallelism int // number of concurrent PR queries (default 8)
}

// WorkspaceDiscovery holds the discovery result for one workspace repository.
type WorkspaceDiscovery struct {
	Repo WorkspaceRepo
	PR   *github.PullRequest
	Err  error
}

// defaultIgnoreDirs lists common non-repo or generated directories to skip during scanning.
var defaultIgnoreDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".cache":       true,
	".gemini":      true,
	".superpowers": true,
}

// resolveGitDir handles standard .git directories, git worktrees, and submodules.
// It returns the gitDir (where HEAD and refs reside) and commonDir (where config resides).
func resolveGitDir(repoPath string) (gitDir string, commonDir string, err error) {
	dotGit := filepath.Join(repoPath, ".git")
	fi, err := os.Stat(dotGit)
	if err != nil {
		return "", "", err
	}
	if fi.IsDir() {
		return dotGit, dotGit, nil
	}

	// .git is a regular file (worktree or submodule): "gitdir: <path>\n"
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return "", "", fmt.Errorf("read .git file: %w", err)
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", "", fmt.Errorf("invalid .git pointer file in %s", repoPath)
	}

	gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Clean(filepath.Join(repoPath, gitDir))
	}

	// Check for commondir (present in linked worktrees)
	if commonRaw, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		relCommon := strings.TrimSpace(string(commonRaw))
		if filepath.IsAbs(relCommon) {
			commonDir = filepath.Clean(relCommon)
		} else {
			commonDir = filepath.Clean(filepath.Join(gitDir, relCommon))
		}
	} else {
		commonDir = gitDir
	}
	return gitDir, commonDir, nil
}

// normalizeRemoteURL converts git SSH and SCP-like URLs to https URLs that
// forge.ParseRepoURL understands.
func normalizeRemoteURL(raw string) string {
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

// readRepoDetails reads the current branch and remotes for a repository located at repoPath.
func readRepoDetails(repoPath string) (WorkspaceRepo, error) {
	absDir, err := filepath.Abs(repoPath)
	if err != nil {
		absDir = repoPath
	}

	repo := WorkspaceRepo{
		Dir: absDir,
	}

	gitDir, commonDir, err := resolveGitDir(absDir)
	if err != nil {
		return repo, err
	}

	// 1. Read HEAD for current branch
	headFile := filepath.Join(gitDir, "HEAD")
	if headRaw, err := os.ReadFile(headFile); err == nil {
		headContent := strings.TrimSpace(string(headRaw))
		if strings.HasPrefix(headContent, "ref: refs/heads/") {
			repo.CurrentBranch = strings.TrimPrefix(headContent, "ref: refs/heads/")
		} else {
			// Detached HEAD or 40-character commit SHA -> no branch
			repo.CurrentBranch = ""
		}
	}

	// 2. Read config for remotes
	configFile := filepath.Join(commonDir, "config")
	// Git handles quotes, escapes, comments, and continuations in config values.
	const gitConfigTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), gitConfigTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "config", "--file", configFile, "--null", "--get-regexp", `^remote\.(origin|upstream)\.url$`).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return repo, fmt.Errorf("read remote config: %w", err)
		}
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		key, val, _ := strings.Cut(entry, "\n")
		switch key {
		case "remote.origin.url":
			if repo.OriginURL == "" {
				repo.OriginURL = val
			}
		case "remote.upstream.url":
			if repo.UpstreamURL == "" {
				repo.UpstreamURL = val
			}
		}
	}

	// 3. Parse Origin URL
	if repo.OriginURL != "" {
		prov, host, owner, repoName, err := parseRemoteIdentity(repo.OriginURL)
		if err == nil {
			repo.Provider = prov.String()
			repo.Host = host
			repo.Owner = owner
			repo.Repo = repoName
		}
	}

	// 4. Parse Upstream URL if present
	if repo.UpstreamURL != "" {
		prov, host, upOwner, upRepo, err := parseRemoteIdentity(repo.UpstreamURL)
		if err == nil {
			repo.UpstreamProvider = prov.String()
			repo.UpstreamHost = host
			repo.UpstreamOwner = upOwner
			repo.UpstreamRepo = upRepo
		}
	}

	return repo, nil
}

// ScanWorkspace searches root and its subdirectories up to opts.Depth for git repositories.
// From inside a checkout, it scans the checkout's parent to include siblings.
func ScanWorkspace(root string, opts ScanOptions) ([]WorkspaceRepo, error) {
	depth := opts.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(absRoot); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory: %s", absRoot)
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

	ignored := make(map[string]bool)
	for k, v := range defaultIgnoreDirs {
		ignored[k] = v
	}
	for _, dir := range opts.IgnoreDirs {
		ignored[dir] = true
	}

	var results []WorkspaceRepo
	seenDirs := make(map[string]bool)

	// Check if root itself is a git repo
	if _, _, err := resolveGitDir(absRoot); err == nil {
		if r, err := readRepoDetails(absRoot); err == nil {
			results = append(results, r)
			seenDirs[absRoot] = true
		}
	}

	var scanSubdirs func(dir string, currentDepth int)
	scanSubdirs = func(dir string, currentDepth int) {
		if currentDepth > depth {
			return
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || ignored[name] {
				continue
			}

			subPath := filepath.Join(dir, name)
			if seenDirs[subPath] {
				continue
			}

			// If subPath is a git repository, record it and do not descend into it
			if _, _, err := resolveGitDir(subPath); err == nil {
				if r, err := readRepoDetails(subPath); err == nil {
					results = append(results, r)
					seenDirs[subPath] = true
				}
				continue
			}

			// Recurse to next depth
			scanSubdirs(subPath, currentDepth+1)
		}
	}

	scanSubdirs(absRoot, 1)
	return results, nil
}

// DiscoverPRsInWorkspace concurrently queries fetcher for open PRs for each workspace repo's current branch.
func DiscoverPRsInWorkspace(ctx context.Context, fetcher PRFetcher, repos []WorkspaceRepo, opts DiscoverOptions) []WorkspaceDiscovery {
	parallelism := opts.Parallelism
	if parallelism <= 0 {
		parallelism = 8
	}

	results := make([]WorkspaceDiscovery, len(repos))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup

	for i, r := range repos {
		results[i].Repo = r

		if r.CurrentBranch == "" || r.Owner == "" || r.Repo == "" || fetcher == nil {
			continue
		}
		if r.Provider != "github" || !strings.EqualFold(r.Host, fetcher.Host()) {
			results[i].Err = fmt.Errorf("origin is not served by GitHub host %s", fetcher.Host())
			continue
		}

		wg.Add(1)
		go func(idx int, repo WorkspaceRepo) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[idx].Err = ctx.Err()
				return
			}
			defer func() { <-sem }()

			// Both the head and target must belong to the client's host.
			if repo.UpstreamURL != "" || repo.UpstreamRepo != "" {
				if repo.UpstreamProvider != "github" || !strings.EqualFold(repo.UpstreamHost, fetcher.Host()) || repo.UpstreamOwner == "" || repo.UpstreamRepo == "" {
					results[idx].Err = fmt.Errorf("upstream is not served by GitHub host %s", fetcher.Host())
				} else if !strings.EqualFold(repo.UpstreamOwner+"/"+repo.UpstreamRepo, repo.Owner+"/"+repo.Repo) {
					pr, err := fetcher.FindOpenPRForHead(ctx, repo.UpstreamOwner, repo.UpstreamRepo, repo.Owner, repo.CurrentBranch)
					if err != nil {
						results[idx].Err = fmt.Errorf("upstream lookup: %w", err)
					}
					if pr != nil {
						results[idx].PR = pr
						return
					}
				}
			}
			pr, err := fetcher.FindOpenPRForHead(ctx, repo.Owner, repo.Repo, repo.Owner, repo.CurrentBranch)
			if err != nil {
				results[idx].Err = errors.Join(results[idx].Err, fmt.Errorf("origin lookup: %w", err))
			}
			results[idx].PR = pr
		}(i, r)
	}

	wg.Wait()
	return results
}
