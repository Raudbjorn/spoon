package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// parsePRRef parses a PR reference into (owner, repo, number).
// Accepted forms:
//   - "owner/repo#42"
//   - "https://github.com/owner/repo/pull/42"
//   - "#42" (uses fallbackOwner/fallbackRepo)
//
// Returns an error for GitLab URLs or malformed input.
func parsePRRef(s, fallbackOwner, fallbackRepo string) (owner, repo string, number int, err error) {
	if s == "" {
		return "", "", 0, fmt.Errorf("empty PR ref")
	}

	// "#42" form
	if strings.HasPrefix(s, "#") {
		n, perr := strconv.Atoi(s[1:])
		if perr != nil {
			return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
		}
		if fallbackOwner == "" || fallbackRepo == "" {
			return "", "", 0, fmt.Errorf("%q has no repo context (run inside a git checkout or pass owner/repo#N)", s)
		}
		return fallbackOwner, fallbackRepo, n, nil
	}

	// URL form
	if strings.Contains(s, "://") {
		u, perr := url.Parse(s)
		if perr != nil {
			return "", "", 0, fmt.Errorf("parse %q: %w", s, perr)
		}
		host := strings.ToLower(u.Hostname())
		if host != "github.com" {
			return "", "", 0, fmt.Errorf("only github.com PR URLs are supported, got %q", host)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		// Expected: owner/repo/pull/N
		if len(parts) < 4 || parts[2] != "pull" {
			return "", "", 0, fmt.Errorf("URL %q is not a github.com PR URL", s)
		}
		n, perr := strconv.Atoi(parts[3])
		if perr != nil {
			return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
		}
		return parts[0], parts[1], n, nil
	}

	// "owner/repo#N" form
	hash := strings.LastIndex(s, "#")
	if hash < 0 {
		return "", "", 0, fmt.Errorf("PR ref %q missing '#N' suffix", s)
	}
	n, perr := strconv.Atoi(s[hash+1:])
	if perr != nil {
		return "", "", 0, fmt.Errorf("invalid PR number in %q", s)
	}
	repoPart := s[:hash]
	slash := strings.IndexByte(repoPart, '/')
	if slash < 0 {
		return "", "", 0, fmt.Errorf("PR ref %q missing 'owner/repo' prefix", s)
	}
	return repoPart[:slash], repoPart[slash+1:], n, nil
}

type threadsMode int

const (
	modeTUI threadsMode = iota
	modeJSON
	modeNext
	modeReply
	modeResolve
	modeResolveAll
	modeUnresolveAll
)

type threadsFlags struct {
	prRef           string
	mode            threadsMode
	targetID        string
	body            string
	bodyFile        string
	includeResolved bool
}

// parseThreadsFlags parses the args after "spoon threads".
// Returns flags or an error suitable for stderr output (exit code 2).
func parseThreadsFlags(args []string) (threadsFlags, error) {
	var f threadsFlags
	modeFlags := 0
	setMode := func(m threadsMode, name string) error {
		modeFlags++
		if modeFlags > 1 {
			return fmt.Errorf("--%s conflicts with another mode flag", name)
		}
		f.mode = m
		return nil
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--json":
			if err := setMode(modeJSON, "json"); err != nil {
				return f, err
			}
		case "--next":
			if err := setMode(modeNext, "next"); err != nil {
				return f, err
			}
		case "--include-resolved":
			f.includeResolved = true
		case "--reply":
			if err := setMode(modeReply, "reply"); err != nil {
				return f, err
			}
			if i+1 >= len(args) {
				return f, fmt.Errorf("--reply requires a thread id")
			}
			i++
			f.targetID = args[i]
		case "--resolve":
			if err := setMode(modeResolve, "resolve"); err != nil {
				return f, err
			}
			if i+1 >= len(args) {
				return f, fmt.Errorf("--resolve requires a thread id")
			}
			i++
			f.targetID = args[i]
		case "--resolve-all":
			if err := setMode(modeResolveAll, "resolve-all"); err != nil {
				return f, err
			}
		case "--unresolve-all":
			if err := setMode(modeUnresolveAll, "unresolve-all"); err != nil {
				return f, err
			}
		case "--body":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--body requires a value")
			}
			i++
			f.body = args[i]
		case "--body-file":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--body-file requires a path")
			}
			i++
			f.bodyFile = args[i]
		case "-h", "--help":
			return f, errThreadsHelp
		default:
			if strings.HasPrefix(a, "--") {
				return f, fmt.Errorf("unknown flag %q", a)
			}
			if f.prRef != "" {
				return f, fmt.Errorf("unexpected positional argument %q", a)
			}
			f.prRef = a
		}
	}

	if f.prRef == "" {
		return f, fmt.Errorf("missing PR reference (e.g. owner/repo#42)")
	}

	// Resolve --body-file before validating body requirement.
	if f.bodyFile != "" {
		body, err := readBody(f.bodyFile)
		if err != nil {
			return f, err
		}
		if f.body == "" {
			f.body = body
		}
	}

	if f.mode == modeReply && f.body == "" {
		return f, fmt.Errorf("--reply requires --body or --body-file")
	}
	return f, nil
}

// errThreadsHelp signals that the caller asked for --help and the parser
// should print help text. The dispatcher checks for this sentinel.
var errThreadsHelp = fmt.Errorf("threads help requested")

func readBody(path string) (string, error) {
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
