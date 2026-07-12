package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
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
	modeApplySuggestion
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
	outdatedOnly    bool
	// suggestion-related flags
	suggest         string
	suggestFile     string
	suggestSet      bool
	suggestFileSet  bool
	intro           string
	introSet        bool
	suggestionIndex int
	dryRun          bool
	force           bool
	repoRoot        string
	// showCodeLines is the value of --show-code N. 0 (default) disables the
	// code-context fetch entirely. Negative values are rejected at parse time.
	showCodeLines int
	// verbose controls whether per-comment timestamp/author-URL fields are
	// emitted in JSON output and rendered in the TUI detail pane. Default off
	// preserves the historical compact output.
	verbose bool
	// interactive enables the open-PR picker when no PR ref is given. With a
	// PR ref present, the flag is ignored (with a warning) so existing
	// scripts keep working.
	interactive bool
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
		case a == "--outdated":
			f.outdatedOnly = true
		case a == "--unresolve-all":
			if err := setMode(modeUnresolveAll, "unresolve-all"); err != nil {
				return f, err
			}
		case a == "--apply-suggestion":
			if err := setMode(modeApplySuggestion, "apply-suggestion"); err != nil {
				return f, err
			}
			if i+1 >= len(args) {
				return f, fmt.Errorf("--apply-suggestion requires a thread id")
			}
			i++
			f.targetID = args[i]
		case a == "--suggestion-index":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--suggestion-index requires a value")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return f, fmt.Errorf("--suggestion-index must be a non-negative integer")
			}
			f.suggestionIndex = n
		case a == "--dry-run":
			f.dryRun = true
		case a == "--force":
			f.force = true
		case a == "--repo-root":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--repo-root requires a path")
			}
			i++
			f.repoRoot = args[i]
		case a == "--show-code", strings.HasPrefix(a, "--show-code="):
			var val string
			if a == "--show-code" {
				if i+1 >= len(args) {
					return f, fmt.Errorf("--show-code requires a value")
				}
				i++
				val = args[i]
			} else {
				val = strings.TrimPrefix(a, "--show-code=")
			}
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return f, fmt.Errorf("--show-code must be a non-negative integer")
			}
			f.showCodeLines = n
		case a == "--suggest":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--suggest requires a value")
			}
			i++
			f.suggest = args[i]
			f.suggestSet = true
		case a == "--suggest-file":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--suggest-file requires a path")
			}
			i++
			f.suggestFile = args[i]
			f.suggestFileSet = true
		case a == "--intro":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--intro requires a value")
			}
			i++
			f.intro = args[i]
			f.introSet = true
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
		case a == "--verbose" || a == "-v":
			f.verbose = true
		case a == "--interactive" || a == "-i":
			f.interactive = true
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

	if f.prRef == "" && !f.interactive {
		return f, fmt.Errorf("missing PR reference (e.g. owner/repo#42) — pass --interactive/-i to pick one")
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

	// --suggest mutual exclusion + intro guard.
	if (f.suggestSet || f.suggestFileSet) && (f.body != "" || f.bodyFile != "") {
		return f, fmt.Errorf("--suggest/--suggest-file is mutually exclusive with --body/--body-file")
	}
	if f.suggestSet && f.suggestFileSet {
		return f, fmt.Errorf("--suggest and --suggest-file are mutually exclusive")
	}
	if !f.suggestSet && !f.suggestFileSet && f.introSet {
		return f, fmt.Errorf("--intro requires --suggest or --suggest-file")
	}
	// --suggest/--suggest-file only make sense when replying: they wrap the
	// reply body in a suggestion block. Outside reply mode they would be
	// silently ignored, so reject the combination explicitly.
	if (f.suggestSet || f.suggestFileSet) && f.mode != modeReply {
		return f, fmt.Errorf("--suggest/--suggest-file requires --reply")
	}
	if f.suggestFileSet {
		b, err := readBody(f.suggestFile)
		if err != nil {
			return f, err
		}
		f.suggest = b
		f.suggestSet = true
	}
	if f.suggestSet && f.mode == modeReply {
		f.body = threadsops.WrapSuggestionBody(f.intro, f.suggest)
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
		return f, fmt.Errorf("--reply requires --body, --body-file, --suggest, or --suggest-file")
	}
	if f.outdatedOnly && f.mode != modeResolveAll {
		return f, fmt.Errorf("--outdated requires --resolve-all")
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

// emitJSONAnnotated prints the policy-annotated threads (which include the
// codeContext field) as a JSON array. Used when --show-code is in effect.
func emitJSONAnnotated(w io.Writer, threads []threadsops.ReviewThreadWithPolicy) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(threads)
}

// renderCodeContextBlock formats a CodeContext into a plain-text framed block
// for terminal display. Used by spoon (TUI and direct resolve printing). The
// arrow marker points to the thread's anchor line(s).
func renderCodeContextBlock(t threadsops.ReviewThreadWithPolicy, cc threadsops.CodeContext) string {
	var b strings.Builder
	refShort := cc.Ref
	if len(refShort) > 7 {
		refShort = refShort[:7]
	}
	outdatedTag := ""
	if cc.Outdated {
		outdatedTag = " (outdated)"
	}
	fmt.Fprintf(&b, "┌── code at %s:%d-%d [ref %s]%s ──\n", cc.Path, cc.StartLine, cc.EndLine, refShort, outdatedTag)
	// Determine the highlight range. For multi-line threads, every line from
	// startLine..line is marked; for single-line threads, only `line`.
	hlStart, hlEnd := t.Line, t.Line
	if t.StartLine != nil && *t.StartLine > 0 {
		hlStart = *t.StartLine
	}
	if hlEnd < hlStart {
		hlEnd = hlStart
	}
	for i, ln := range cc.Lines {
		n := cc.StartLine + i
		marker := " "
		if n >= hlStart && n <= hlEnd {
			marker = "←"
		}
		fmt.Fprintf(&b, "│ %4d │ %s %s\n", n, marker, ln)
	}
	b.WriteString("└─────")
	if cc.Outdated {
		b.WriteString("\n  (heads up: this thread is marked outdated — the snippet above is the current code at these line numbers, which may differ from what the comment referenced)")
	}
	return b.String()
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

	// --interactive with a PR ref already supplied is a user error in
	// spirit, but rather than refuse, warn and fall through to the
	// existing PR-ref code path.
	if flags.interactive && flags.prRef != "" {
		fmt.Fprintln(os.Stderr, "⚠️  Warning: --interactive ignored: PR ref already supplied")
		flags.interactive = false
	}

	client, status, err := gh.CheckAuthConfigured(0)
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

	// Interactive picker: no PR ref given, --interactive was set, repo
	// context detected. Fetch open PRs, run the picker, and treat the
	// chosen PR as if the user had typed it on the command line.
	if flags.interactive && flags.prRef == "" {
		if fallbackOwner == "" || fallbackRepo == "" {
			fmt.Fprintln(os.Stderr, "❌ Error: --interactive needs a git checkout (couldn't detect origin owner/repo)")
			return 2
		}
		prs, perr := client.ListOpenPRs(ctx, fallbackOwner, fallbackRepo, 30)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error: listing open PRs:", perr)
			return 1
		}
		if len(prs) == 0 {
			fmt.Println("No open PRs in this repo")
			return 0
		}
		picker := threadstui.NewPicker(prs)
		final, runErr := tea.NewProgram(picker, tea.WithAltScreen()).Run()
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", runErr)
			return 1
		}
		pm := final.(threadstui.PickerModel)
		if pm.Cancelled() || pm.Selected() == nil {
			return 0
		}
		// Synthesize the PR ref so the existing parse/dispatch path takes
		// over unchanged.
		flags.prRef = fmt.Sprintf("%s/%s#%d", fallbackOwner, fallbackRepo, pm.Selected().Number)
	}

	owner, repo, number, err := parsePRRef(flags.prRef, fallbackOwner, fallbackRepo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ Error:", err)
		return 2
	}

	switch flags.mode {
	case modeJSON:
		listOpts := threadsops.ListOptions{
			IncludeResolved: flags.filter.NeedsResolvedFetch(),
			ShowCodeLines:   flags.showCodeLines,
		}
		if flags.showCodeLines > 0 {
			listOpts.Fetcher = client
		}
		status, threads, opErr := threadsops.ListWithOptions(ctx, client, owner, repo, number, listOpts)
		if opErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
			return 1
		}
		emitStatus(os.Stderr, status, number, flags.noStatus)
		threads = threadsops.Filter(threads, flags.filter)
		threadsops.SortThreadsForList(threads)
		if !flags.verbose {
			threadsops.StripVerboseFields(threads)
		}
		// When --show-code is in play, emit the policy-annotated threads
		// (which carry codeContext); otherwise preserve the historical raw
		// ReviewThread JSON shape so existing pipelines aren't disturbed.
		if flags.showCodeLines > 0 {
			if err := emitJSONAnnotated(os.Stdout, threads); err != nil {
				fmt.Fprintln(os.Stderr, "❌ Error:", err)
				return 1
			}
			return 0
		}
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
		nextOpts := threadsops.NextOptions{ShowCodeLines: flags.showCodeLines}
		if flags.showCodeLines > 0 {
			nextOpts.Fetcher = client
		}
		status, t, opErr := threadsops.NextWithOptions(ctx, client, owner, repo, number, nextOpts)
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
		if !flags.verbose {
			threadsops.StripVerboseFieldsOne(t)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		// When --show-code is set, emit the annotated form so the codeContext
		// surfaces; otherwise use the raw ReviewThread shape for back-compat.
		if flags.showCodeLines > 0 {
			if err := enc.Encode(t); err != nil {
				fmt.Fprintln(os.Stderr, "❌ Error:", err)
				return 1
			}
			return 0
		}
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
		resolved, wasAlreadyResolved, opErr := threadsops.ResolveWithThreadsAndOptions(ctx, client, all, flags.targetID, flags.body, threadsops.ResolveOptions{DryRun: flags.dryRun})
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
		} else if flags.dryRun {
			fmt.Fprintln(os.Stderr, "ℹ️  Dry run: would resolve thread", flags.targetID)
		}
		// Render code context for the resolved thread, if requested.
		if flags.showCodeLines > 0 && resolved != nil && status.HeadSHA != "" {
			if cc, ccErr := threadsops.FetchCodeContext(ctx, client, status.HeadSHA, owner, repo, *resolved, flags.showCodeLines); ccErr != nil {
				fmt.Fprintln(os.Stderr, "⚠️  Warning: code context fetch failed:", ccErr)
			} else if cc != nil {
				fmt.Fprintln(os.Stdout, renderCodeContextBlock(*resolved, *cc))
			}
		}
		return 0

	case modeResolveAll:
		status, _, ferr := client.FetchPR(ctx, owner, repo, number, gh.ThreadStateAll)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", ferr)
			return 1
		}
		emitStatus(os.Stderr, status, number, flags.noStatus)
		res, opErr := threadsops.ResolveAllWithOptions(ctx, client, owner, repo, number, threadsops.ResolveAllOptions{
			SkipHumanThreads: false, // legacy spoon mode
			OutdatedOnly:     flags.outdatedOnly,
			DryRun:           flags.dryRun,
		})
		if opErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
			return 1
		}
		verb := "resolved"
		if flags.dryRun {
			verb = "would resolve"
		}
		fmt.Printf("✅ %s %d threads\n", verb, len(res.Succeeded))
		if flags.outdatedOnly {
			notOutdated := 0
			for _, s := range res.Skipped {
				if s.Reason == "not_outdated" {
					notOutdated++
				}
			}
			if notOutdated > 0 {
				fmt.Printf("ℹ️  skipped %d non-outdated thread(s)\n", notOutdated)
			}
		}
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
		emitStatus(os.Stderr, status, number, flags.noStatus)
		res, opErr := threadsops.UnresolveAllWithOptions(ctx, client, owner, repo, number, threadsops.UnresolveAllOptions{DryRun: flags.dryRun})
		if opErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
			return 1
		}
		verb := "unresolved"
		if flags.dryRun {
			verb = "would unresolve"
		}
		fmt.Printf("✅ %s %d threads\n", verb, len(res.Succeeded))
		if len(res.Failed) > 0 {
			for _, f := range res.Failed {
				fmt.Fprintf(os.Stderr, "❌ failed %s: %s\n", f.ID, f.Error)
			}
			return 1
		}
		return 0

	case modeApplySuggestion:
		// One round-trip: ListWithOptions returns both the PR status header
		// and the annotated thread list. The previous code did an explicit
		// FetchPR followed by threadsops.List, which internally calls FetchPR
		// again — two GraphQL hits for the same data.
		status, threads, opErr := threadsops.ListWithOptions(ctx, client, owner, repo, number, threadsops.ListOptions{IncludeResolved: true})
		if opErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", opErr.Message)
			return 1
		}
		emitStatus(os.Stdout, status, number, flags.noStatus)
		var target *threadsops.ReviewThreadWithPolicy
		for i := range threads {
			if threads[i].ID == flags.targetID {
				target = &threads[i]
				break
			}
		}
		if target == nil {
			fmt.Fprintf(os.Stderr, "❌ Error: thread %s not found on PR\n", flags.targetID)
			return 1
		}
		sugs := target.Suggestions
		if len(sugs) == 0 {
			fmt.Fprintln(os.Stderr, "❌ Error: thread has no suggestion blocks")
			return 1
		}
		if flags.suggestionIndex >= len(sugs) {
			fmt.Fprintf(os.Stderr, "❌ Error: suggestion index %d out of range (have %d)\n", flags.suggestionIndex, len(sugs))
			return 2
		}
		res, applyErr := threadsops.ApplySuggestion(ctx, *target, sugs[flags.suggestionIndex], threadsops.ApplyOptions{
			RepoRoot:  flags.repoRoot,
			DryRun:    flags.dryRun,
			Force:     flags.force,
			Fetcher:   client,
			Owner:     owner,
			Repo:      repo,
			PRHeadRef: status.HeadSHA,
		})
		if applyErr != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", applyErr.Message)
			if applyErr.Code == threadsops.OpCodePolicy {
				return 2
			}
			return 1
		}
		// Friendly summary
		verb := "applied"
		if flags.dryRun {
			verb = "would apply"
		}
		fmt.Printf("✅ %s suggestion at %s (lines %d): replaced %d line(s)\n",
			verb, target.Path, target.Line, len(res.OldLines))
		if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
			fmt.Fprintln(os.Stderr, "❌ Error:", err)
			return 1
		}
		return 0

	case modeTUI:
		return runThreadsTUI(ctx, client, owner, repo, number, flags.filter, flags.showCodeLines, flags.verbose)

	default:
		fmt.Fprintln(os.Stderr, "❌ Error: unknown mode")
		return 2
	}
}

func printThreadsHelp() {
	fmt.Print(`spoon threads — operate on PR review threads

Usage:
  spoon threads <pr-ref> [flags]
  spoon threads --interactive [flags]      (pick a PR from a list)

PR reference forms:
  owner/repo#42
  https://github.com/owner/repo/pull/42
  #42                  (uses local repo context)

Flags:
  (no mode flag)        Open the TUI for the selected filter (default: unresolved)
  -i, --interactive     If no PR ref is given, present a picker over the open
                        PRs in the current repo (origin). With a PR ref the
                        flag is ignored (with a warning).
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
  --reply <id> --suggest BODY [--intro TEXT]
                        Counter-propose a code change by wrapping BODY in a
                        suggestion fenced block in the reply.
  --resolve <id> [--body T]
                        Resolve one thread; --body required for non-bot threads
  --resolve-all         Mark every unresolved thread as resolved
  --outdated            (with --resolve-all) only resolve threads whose
                        anchored code is outdated; non-outdated threads
                        are skipped with reason "not_outdated"
  --unresolve-all       Mark every resolved thread as unresolved
  --dry-run             Preview what --resolve / --resolve-all / --unresolve-all
                        would do: the fetch + policy gates run, but no
                        GraphQL mutation is issued. (Also used by
                        --apply-suggestion to skip the local file write.)
  --show-code N         Show N lines of code context before/after each
                        thread's comment range. Applied to JSON output,
                        --next, --resolve, and the TUI detail pane.
  -v, --verbose         Include createdAt, updatedAt, and authorUrl on each
                        comment in JSON output. In the TUI, adds a
                        "Created: TS" line under each comment.
  --apply-suggestion <id> [--suggestion-index N] [--dry-run] [--force] [--repo-root PATH]
                        Rewrite the local file at the thread's line range
                        with the parsed suggestion block. --dry-run skips
                        the write but reports what would change. --force
                        bypasses the outdated-thread safety check.
  --body T              Comment body
  --body-file PATH      Read body from file ('-' = stdin)
  --suggest BODY        Wrap BODY in a suggestion fenced block on reply.
  --suggest-file PATH   Read suggestion content from a file ('-' = stdin).
  --intro TEXT          Preface text for --suggest (default: "How about this?")
  -h, --help            Show this help

Examples:
  spoon threads owner/repo#42
  spoon threads owner/repo#42 --json
  spoon threads owner/repo#42 --json --filter current-unresolved
  spoon threads owner/repo#42 --resolve PRRT_1 --body "fixed in 1234abc"
  while [ "$(spoon threads owner/repo#42 --next)" != "null" ]; do ... ; done
`)
}

func runThreadsTUI(ctx context.Context, client *gh.Client, owner, repo string, number int, mode threadsops.FilterMode, showCodeLines int, verbose bool) int {
	_ = ctx // reserved for future cancellable Init paths
	m := threadstui.NewWithFilter(client, owner, repo, number, mode)
	m.ShowCodeLines = showCodeLines
	m.Verbose = verbose
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
