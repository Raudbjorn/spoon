package threadsops

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/github"
)

// WorkspaceRepo describes a sibling git checkout found in a workspace scan.
type WorkspaceRepo struct {
	Dir           string // absolute path to repository directory
	OriginURL     string // raw value of remote.origin.url
	Owner         string // parsed from OriginURL via forge.ParseRepoURL
	Repo          string // parsed repository name
	Provider      string // "github" / "gitlab" / "gitea"
	CurrentBranch string // current branch name, or empty if detached HEAD

	UpstreamURL   string // raw value of remote.upstream.url, if present
	UpstreamOwner string // parsed owner of upstream remote, if present
	UpstreamRepo  string // parsed repository name of upstream remote, if present
}

// ScanOptions configures workspace repository scanning.
type ScanOptions struct {
	Depth      int      // max directory search depth (default 1, max 3)
	IgnoreDirs []string // extra directory names to skip (vendor, node_modules, etc.)
}

// PRFetcher abstracts looking up an open pull request by head ref.
// github.Client implements this via FindOpenPRForHead.
type PRFetcher interface {
	FindOpenPRForHead(ctx context.Context, owner, repo, headOwner, headBranch string) (*github.PullRequest, error)
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
	if f, err := os.Open(configFile); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		currentRemote := ""
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				sec := strings.TrimSpace(line[1 : len(line)-1])
				fields := strings.Fields(sec)
				if len(fields) >= 2 && strings.EqualFold(fields[0], "remote") {
					currentRemote = strings.Trim(fields[1], `"'`)
				} else {
					currentRemote = ""
				}
				continue
			}

			if currentRemote == "" {
				continue
			}

			if eqIdx := strings.Index(line, "="); eqIdx > 0 {
				key := strings.TrimSpace(line[:eqIdx])
				val := strings.TrimSpace(line[eqIdx+1:])
				if strings.EqualFold(key, "url") {
					switch strings.ToLower(currentRemote) {
					case "origin":
						if repo.OriginURL == "" {
							repo.OriginURL = val
						}
					case "upstream":
						if repo.UpstreamURL == "" {
							repo.UpstreamURL = val
						}
					}
				}
			}
		}
	}

	// 3. Parse Origin URL
	if repo.OriginURL != "" {
		normalized := normalizeRemoteURL(repo.OriginURL)
		prov, _, owner, repoName, err := forge.ParseRepoURL(normalized, "github.com", 0)
		if err == nil {
			repo.Provider = prov.String()
			repo.Owner = owner
			repo.Repo = repoName
		}
	}

	// 4. Parse Upstream URL if present
	if repo.UpstreamURL != "" {
		normalized := normalizeRemoteURL(repo.UpstreamURL)
		_, _, upOwner, upRepo, err := forge.ParseRepoURL(normalized, "github.com", 0)
		if err == nil {
			repo.UpstreamOwner = upOwner
			repo.UpstreamRepo = upRepo
		}
	}

	return repo, nil
}

// ScanWorkspace searches root and its subdirectories up to opts.Depth for git repositories.
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
		absRoot = root
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

			// If upstream remote is configured, check upstream PRs first
			targetOwner := repo.Owner
			targetRepo := repo.Repo
			if repo.UpstreamOwner != "" && repo.UpstreamRepo != "" {
				targetOwner = repo.UpstreamOwner
				targetRepo = repo.UpstreamRepo
			}

			pr, err := fetcher.FindOpenPRForHead(ctx, targetOwner, targetRepo, repo.Owner, repo.CurrentBranch)
			if err != nil {
				// If upstream query failed and upstream differs from origin, attempt origin fallback
				if repo.UpstreamRepo != "" {
					prOrig, errOrig := fetcher.FindOpenPRForHead(ctx, repo.Owner, repo.Repo, repo.Owner, repo.CurrentBranch)
					if errOrig == nil && prOrig != nil {
						results[idx].PR = prOrig
						return
					}
				}
				results[idx].Err = err
				return
			}

			// If no upstream PR was found, check if an open PR exists against the origin fork itself
			if pr == nil && repo.UpstreamRepo != "" {
				prOrig, errOrig := fetcher.FindOpenPRForHead(ctx, repo.Owner, repo.Repo, repo.Owner, repo.CurrentBranch)
				if errOrig == nil {
					pr = prOrig
				}
			}

			results[idx].PR = pr
		}(i, r)
	}

	wg.Wait()
	return results
}
