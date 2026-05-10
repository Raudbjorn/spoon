package threadsops

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
)

// DetectRepoContext returns owner and repo by parsing the local origin remote.
// Returns ("", "") on any error.
func DetectRepoContext() (owner, repo string) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", ""
	}
	provider, _, o, r, perr := forge.ParseRepoURL(strings.TrimSpace(string(out)), "github.com", 0)
	if perr != nil || provider != forge.ProviderGitHub {
		return "", ""
	}
	return o, r
}

// ParsePRRef parses a PR reference. Accepts owner/repo#N, full github.com URL,
// or #N (resolved via fallback).
func ParsePRRef(s, fallbackOwner, fallbackRepo string) (owner, repo string, number int, err error) {
	if s == "" {
		return "", "", 0, fmt.Errorf("empty PR ref")
	}
	if strings.HasPrefix(s, "#") {
		n, e := strconv.Atoi(s[1:])
		if e != nil {
			return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
		}
		if fallbackOwner == "" || fallbackRepo == "" {
			return "", "", 0, fmt.Errorf("%q has no repo context (run inside a git checkout or pass owner/repo#N)", s)
		}
		return fallbackOwner, fallbackRepo, n, nil
	}
	if strings.Contains(s, "://") {
		u, e := url.Parse(s)
		if e != nil {
			return "", "", 0, fmt.Errorf("parse %q: %w", s, e)
		}
		if strings.ToLower(u.Hostname()) != "github.com" {
			return "", "", 0, fmt.Errorf("only github.com PR URLs are supported, got %q", u.Hostname())
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 4 || parts[2] != "pull" {
			return "", "", 0, fmt.Errorf("URL %q is not a github.com PR URL", s)
		}
		n, e := strconv.Atoi(parts[3])
		if e != nil {
			return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
		}
		return parts[0], parts[1], n, nil
	}
	hash := strings.LastIndex(s, "#")
	if hash < 0 {
		return "", "", 0, fmt.Errorf("PR ref %q missing '#N' suffix", s)
	}
	n, e := strconv.Atoi(s[hash+1:])
	if e != nil {
		return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
	}
	repoPart := s[:hash]
	slash := strings.IndexByte(repoPart, '/')
	if slash < 0 {
		return "", "", 0, fmt.Errorf("PR ref %q missing 'owner/repo' prefix", s)
	}
	return repoPart[:slash], repoPart[slash+1:], n, nil
}

// ReadBody returns the body content from path; path "-" reads stdin.
func ReadBody(path string) (string, error) {
	if path == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read body file: %w", err)
	}
	return string(b), nil
}
