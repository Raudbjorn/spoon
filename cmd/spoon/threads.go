package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	forge "github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
	threadstui "github.com/svnbjrn/spoon/internal/tui/threads"
)

// detectRepoContext returns owner and repo by parsing the URL of the local
// `origin` remote. Returns ("", "") on any error (caller will report a
// clearer error from parsePRRef).
func detectRepoContext() (owner, repo string) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	rawURL := strings.TrimSpace(string(out))
	// Parse using forge.ParseRepoURL with defaultHost=github.com.
	provider, _, o, r, err := forge.ParseRepoURL(rawURL, "github.com", 0)
	if err != nil {
		return "", ""
	}
	if provider != forge.ProviderGitHub {
		return "", ""
	}
	return o, r
}

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

// emitJSON prints all threads as a JSON array.
func emitJSON(w io.Writer, threads []gh.ReviewThread) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(threads)
}

// emitNext prints the single oldest unresolved thread as JSON, or "null".
func emitNext(w io.Writer, threads []gh.ReviewThread) error {
	unresolved := make([]gh.ReviewThread, 0, len(threads))
	for _, t := range threads {
		if !t.IsResolved {
			unresolved = append(unresolved, t)
		}
	}
	if len(unresolved) == 0 {
		_, err := io.WriteString(w, "null\n")
		return err
	}
	sort.Slice(unresolved, func(i, j int) bool {
		ai, aj := firstCommentTime(unresolved[i]), firstCommentTime(unresolved[j])
		return ai < aj
	})
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(unresolved[0])
}

func firstCommentTime(t gh.ReviewThread) string {
	if len(t.Comments) == 0 {
		return ""
	}
	return t.Comments[0].CreatedAt
}

// runThreads is the entry point for the "threads" subcommand. It returns
// an exit code (0/1/2) and writes any error messages to stderr.
func runThreads(args []string) int {
	flags, err := parseThreadsFlags(args)
	if err != nil {
		if err == errThreadsHelp {
			printThreadsHelp()
			return 0
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		printThreadsHelp()
		return 2
	}

	fallbackOwner, fallbackRepo := detectRepoContext()
	owner, repo, number, err := parsePRRef(flags.prRef, fallbackOwner, fallbackRepo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}

	client, status, err := gh.CheckAuth()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: GitHub auth:", err)
		return 1
	}
	if !client.IsAuthenticated() {
		fmt.Fprintln(os.Stderr, "Error: spoon threads requires authentication (run `gh auth login`).")
		return 1
	}
	if !status.HasScope("repo") {
		fmt.Fprintln(os.Stderr, "Error: spoon threads requires the 'repo' OAuth scope.")
		fmt.Fprintln(os.Stderr, "Refresh your token with:  gh auth refresh -s repo")
		return 2
	}

	ctx := context.Background()

	switch flags.mode {
	case modeJSON:
		states := gh.ThreadStateUnresolved
		if flags.includeResolved {
			states = gh.ThreadStateAll
		}
		threads, err := client.ListThreads(ctx, owner, repo, number, states)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		if err := emitJSON(os.Stdout, threads); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		return 0

	case modeNext:
		threads, err := client.ListThreads(ctx, owner, repo, number, gh.ThreadStateUnresolved)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		if err := emitNext(os.Stdout, threads); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		return 0

	case modeReply:
		if _, err := client.ReplyToThread(ctx, flags.targetID, flags.body); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		return 0

	case modeResolve:
		// Apply bot/human policy: fetch the thread to inspect comments.
		all, err := client.ListThreads(ctx, owner, repo, number, gh.ThreadStateAll)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		var target *gh.ReviewThread
		for i := range all {
			if all[i].ID == flags.targetID {
				target = &all[i]
				break
			}
		}
		if target == nil {
			fmt.Fprintf(os.Stderr, "Error: thread %s not found on PR\n", flags.targetID)
			return 1
		}
		if target.IsResolved {
			fmt.Fprintln(os.Stderr, "Warning: thread already resolved; nothing to do")
			return 0
		}
		if target.RequiresBody() && flags.body == "" {
			fmt.Fprintln(os.Stderr, "Error: thread has a non-bot reviewer; --body (or --body-file) is required")
			return 2
		}
		if flags.body != "" {
			if _, err := client.ReplyToThread(ctx, flags.targetID, flags.body); err != nil {
				fmt.Fprintln(os.Stderr, "Error: reply failed:", err)
				return 1
			}
		}
		if err := client.ResolveThread(ctx, flags.targetID); err != nil {
			fmt.Fprintln(os.Stderr, "Error: resolve failed (reply already posted):", err)
			return 1
		}
		return 0

	case modeResolveAll:
		res, err := client.ResolveAllThreads(ctx, owner, repo, number, 4)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		fmt.Printf("resolved %d threads\n", len(res.Succeeded))
		if len(res.Failed) > 0 {
			for _, f := range res.Failed {
				fmt.Fprintf(os.Stderr, "failed %s: %v\n", f.ID, f.Err)
			}
			return 1
		}
		return 0

	case modeUnresolveAll:
		res, err := client.UnresolveAllThreads(ctx, owner, repo, number, 4)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		fmt.Printf("unresolved %d threads\n", len(res.Succeeded))
		if len(res.Failed) > 0 {
			for _, f := range res.Failed {
				fmt.Fprintf(os.Stderr, "failed %s: %v\n", f.ID, f.Err)
			}
			return 1
		}
		return 0

	case modeTUI:
		return runThreadsTUI(ctx, client, owner, repo, number, flags.includeResolved)

	default:
		fmt.Fprintln(os.Stderr, "Error: unknown mode")
		return 2
	}
}

func printThreadsHelp() {
	fmt.Print(`spoon threads — operate on PR review threads

Usage:
  spoon threads <pr-ref> [flags]

PR reference forms:
  owner/repo#42
  https://github.com/owner/repo/pull/42
  #42                  (uses local repo context)

Flags:
  (no mode flag)        Open the TUI for unresolved threads (default)
  --json                Print all unresolved threads as JSON
  --include-resolved    Include resolved threads in --json output
  --next                Print the oldest unresolved thread as JSON, or null
  --reply <id> --body T   Append a reply to a thread
  --resolve <id> [--body T]
                        Resolve one thread; --body required for non-bot threads
  --resolve-all         Mark every unresolved thread as resolved
  --unresolve-all       Mark every resolved thread as unresolved
  --body T              Comment body
  --body-file PATH      Read body from file ('-' = stdin)
  -h, --help            Show this help

Examples:
  spoon threads owner/repo#42
  spoon threads owner/repo#42 --json
  spoon threads owner/repo#42 --resolve PRRT_1 --body "fixed in 1234abc"
  while [ "$(spoon threads owner/repo#42 --next)" != "null" ]; do ... ; done
`)
}

func runThreadsTUI(ctx context.Context, client *gh.Client, owner, repo string, number int, includeResolved bool) int {
	m := threadstui.New(client, owner, repo, number, includeResolved)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	return 0
}
