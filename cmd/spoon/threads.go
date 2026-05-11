package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
	threadstui "github.com/svnbjrn/spoon/internal/tui/threads"
)

// detectRepoContext returns owner and repo by parsing the URL of the local
// `origin` remote. Returns ("", "") on any error (caller will report a
// clearer error from parsePRRef).
func detectRepoContext() (owner, repo string) {
	return threadsops.DetectRepoContext()
}

// parsePRRef parses a PR reference into (owner, repo, number).
// Accepted forms:
//   - "owner/repo#42"
//   - "https://github.com/owner/repo/pull/42"
//   - "#42" (uses fallbackOwner/fallbackRepo)
//
// Returns an error for GitLab URLs or malformed input.
func parsePRRef(s, fallbackOwner, fallbackRepo string) (owner, repo string, number int, err error) {
	return threadsops.ParsePRRef(s, fallbackOwner, fallbackRepo)
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
	filter          threadsops.FilterMode
	noStatus        bool
}

// parseThreadsFlags parses the args after "spoon threads".
// Returns flags or an error suitable for stderr output (exit code 2).
func parseThreadsFlags(args []string) (threadsFlags, error) {
	var f threadsFlags
	var filterRaw string
	filterSeen := false
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
		switch {
		case a == "--json":
			if err := setMode(modeJSON, "json"); err != nil {
				return f, err
			}
		case a == "--next":
			if err := setMode(modeNext, "next"); err != nil {
				return f, err
			}
		case a == "--include-resolved":
			f.includeResolved = true
		case a == "--no-status":
			f.noStatus = true
		case a == "--filter":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--filter requires a value (valid: %s)", threadsops.ValidFilterModesCSV())
			}
			i++
			filterRaw = args[i]
			filterSeen = true
		case strings.HasPrefix(a, "--filter="):
			filterRaw = strings.TrimPrefix(a, "--filter=")
			filterSeen = true
		case a == "--reply":
			if err := setMode(modeReply, "reply"); err != nil {
				return f, err
			}
			if i+1 >= len(args) {
				return f, fmt.Errorf("--reply requires a thread id")
			}
			i++
			f.targetID = args[i]
		case a == "--resolve":
			if err := setMode(modeResolve, "resolve"); err != nil {
				return f, err
			}
			if i+1 >= len(args) {
				return f, fmt.Errorf("--resolve requires a thread id")
			}
			i++
			f.targetID = args[i]
		case a == "--resolve-all":
			if err := setMode(modeResolveAll, "resolve-all"); err != nil {
				return f, err
			}
		case a == "--unresolve-all":
			if err := setMode(modeUnresolveAll, "unresolve-all"); err != nil {
				return f, err
			}
		case a == "--body":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--body requires a value")
			}
			i++
			f.body = args[i]
		case a == "--body-file":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--body-file requires a path")
			}
			i++
			f.bodyFile = args[i]
		case a == "-h" || a == "--help":
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

	// Resolve --filter / --include-resolved interaction. --include-resolved is
	// kept for back-compat as a shorthand for --filter all. When both are
	// passed, --filter wins unless filterSeen is false.
	if !filterSeen && f.includeResolved {
		filterRaw = string(threadsops.FilterAll)
	}
	mode, ferr := threadsops.ParseFilterMode(filterRaw)
	if ferr != nil {
		return f, fmt.Errorf("%w (valid: %s)", ferr, threadsops.ValidFilterModesCSV())
	}
	f.filter = mode
	// Reflect the resolved mode back onto includeResolved so downstream code
	// (TUI, fetch hints) sees a consistent picture.
	if mode.NeedsResolvedFetch() {
		f.includeResolved = true
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
	return threadsops.ReadBody(path)
}

// emitJSON prints all threads as a JSON array.
func emitJSON(w io.Writer, threads []gh.ReviewThread) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(threads)
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
		fmt.Fprintln(os.Stderr, "❌ Error:", err)
		printThreadsHelp()
		return 2
	}

	fallbackOwner, fallbackRepo := detectRepoContext()
	owner, repo, number, err := parsePRRef(flags.prRef, fallbackOwner, fallbackRepo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ Error:", err)
		return 2
	}

	client, status, err := gh.CheckAuth()
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ Error: GitHub auth:", err)
		return 1
	}
	if !client.IsAuthenticated() {
		fmt.Fprintln(os.Stderr, "❌ Error: spoon threads requires authentication (run `gh auth login`).")
		return 1
	}
	if !status.HasScope("repo") {
		fmt.Fprintln(os.Stderr, "❌ Error: spoon threads requires the 'repo' OAuth scope.")
		fmt.Fprintln(os.Stderr, "Refresh your token with:  gh auth refresh -s repo")
		return 2
	}

	ctx := context.Background()

	switch flags.mode {
	case modeJSON:
		status, threads, opErr := threadsops.List(ctx, client, owner, repo, number, flags.filter.NeedsResolvedFetch())
		if opErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
			return 1
		}
		emitStatus(os.Stderr, status, number, flags.noStatus)
		threads = threadsops.Filter(threads, flags.filter)
		threadsops.SortThreadsForList(threads)
		raw := make([]gh.ReviewThread, len(threads))
		for i, t := range threads {
			raw[i] = t.ReviewThread
		}
		if err := emitJSON(os.Stdout, raw); err != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", err)
			return 1
		}
		return 0

	case modeNext:
		status, t, opErr := threadsops.Next(ctx, client, owner, repo, number)
		if opErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
			return 1
		}
		emitStatus(os.Stderr, status, number, flags.noStatus)
		if t == nil {
			if _, err := io.WriteString(os.Stdout, "null\n"); err != nil {
				return 1
			}
			return 0
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(t.ReviewThread); err != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", err)
			return 1
		}
		return 0

	case modeReply:
		// Spoon still wants the status header on stdout — fetch it separately.
		status, _, ferr := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", ferr)
			return 1
		}
		emitStatus(os.Stdout, status, number, flags.noStatus)
		if _, opErr := threadsops.Reply(ctx, client, flags.targetID, flags.body); opErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
			return 1
		}
		return 0

	case modeResolve:
		status, all, ferr := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", ferr)
			return 1
		}
		emitStatus(os.Stdout, status, number, flags.noStatus)
		_, wasAlreadyResolved, opErr := threadsops.ResolveWithThreads(ctx, client, all, flags.targetID, flags.body)
		if opErr != nil {
			switch opErr.Code {
			case threadsops.OpCodeNotFound:
				fmt.Fprintf(os.Stderr, "❌ Error: thread %s not found on PR\n", flags.targetID)
				return 1
			case threadsops.OpCodePolicy:
				fmt.Fprintln(os.Stderr, "❌ Error: thread has a non-bot reviewer; --body (or --body-file) is required")
				return 2
			default:
				fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
				return 1
			}
		}
		if wasAlreadyResolved {
			fmt.Fprintln(os.Stderr, "⚠️  Warning: thread already resolved; nothing to do")
		}
		return 0

	case modeResolveAll:
		status, _, ferr := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", ferr)
			return 1
		}
		emitStatus(os.Stdout, status, number, flags.noStatus)
		res, opErr := threadsops.ResolveAll(ctx, client, owner, repo, number, false) // legacy spoon mode
		if opErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
			return 1
		}
		fmt.Printf("✅ resolved %d threads\n", len(res.Succeeded))
		if len(res.Failed) > 0 {
			for _, f := range res.Failed {
				fmt.Fprintf(os.Stderr, "❌ failed %s: %s\n", f.ID, f.Error)
			}
			return 1
		}
		return 0

	case modeUnresolveAll:
		status, _, ferr := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", ferr)
			return 1
		}
		emitStatus(os.Stdout, status, number, flags.noStatus)
		res, opErr := threadsops.UnresolveAll(ctx, client, owner, repo, number)
		if opErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
			return 1
		}
		fmt.Printf("✅ unresolved %d threads\n", len(res.Succeeded))
		if len(res.Failed) > 0 {
			for _, f := range res.Failed {
				fmt.Fprintf(os.Stderr, "❌ failed %s: %s\n", f.ID, f.Error)
			}
			return 1
		}
		return 0

	case modeTUI:
		return runThreadsTUI(ctx, client, owner, repo, number, flags.filter)

	default:
		fmt.Fprintln(os.Stderr, "❌ Error: unknown mode")
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
  (no mode flag)        Open the TUI for the selected filter (default: unresolved)
  --json                Print threads as JSON (filtered by --filter)
  --filter MODE         Which threads to surface. One of:
                          all                  every thread (resolved + unresolved)
                          unresolved           unresolved only (default)
                          resolved-active      resolved, code still active
                          unresolved-outdated  unresolved, code shifted (safe bulk-resolve)
                          current-unresolved   unresolved AND not outdated (most urgent)
  --include-resolved    Shorthand for --filter all (kept for back-compat).
  --no-status           Suppress the PR status header
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
  spoon threads owner/repo#42 --json --filter current-unresolved
  spoon threads owner/repo#42 --resolve PRRT_1 --body "fixed in 1234abc"
  while [ "$(spoon threads owner/repo#42 --next)" != "null" ]; do ... ; done
`)
}

func runThreadsTUI(ctx context.Context, client *gh.Client, owner, repo string, number int, mode threadsops.FilterMode) int {
	_ = ctx // reserved for future cancellable Init paths
	m := threadstui.NewWithFilter(client, owner, repo, number, mode)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "❌ Error:", err)
		return 1
	}
	return 0
}

// emitStatus writes the PR status header to w (unless suppressed). glyphs
// are used when w is a TTY.
func emitStatus(w *os.File, status gh.PullRequestStatus, number int, noStatus bool) {
	if noStatus {
		return
	}
	useGlyphs := isatty.IsTerminal(w.Fd())
	_ = RenderStatusBlock(w, status, number, useGlyphs)
}
