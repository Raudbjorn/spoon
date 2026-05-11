// cmd/spn/threads.go
package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

// apiFactory builds the threadsops.API used by every threads handler.
// Overridable for tests.
var apiFactory = func() (threadsops.API, *agentio.Error) {
	client, status, err := gh.CheckAuth()
	if err != nil {
		return nil, agentio.NewError(agentio.CodeAuthRequired, "github auth: "+err.Error(), agentio.RemediationAuthRequired())
	}
	if !client.IsAuthenticated() {
		return nil, agentio.NewError(agentio.CodeAuthRequired, "not authenticated", agentio.RemediationAuthRequired())
	}
	if !status.HasScope("repo") {
		return nil, agentio.NewError(agentio.CodeAuthScope, "missing 'repo' scope", agentio.RemediationAuthScope("repo"))
	}
	return client, nil
}

func runThreads(args []string) int { return runThreadsWith(args, os.Stdout, os.Stderr) }

func runThreadsWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (list|next|reply|resolve|resolve-all|unresolve-all|apply-suggestion)", agentio.RemediationBadInput("threads", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		return doThreadsList(rest, stdout, stderr)
	case "next":
		return doThreadsNext(rest, stdout, stderr)
	case "reply":
		return doThreadsReply(rest, stdout, stderr)
	case "resolve":
		return doThreadsResolve(rest, stdout, stderr)
	case "resolve-all":
		return doThreadsResolveAll(rest, stdout, stderr)
	case "unresolve-all":
		return doThreadsUnresolveAll(rest, stdout, stderr)
	case "apply-suggestion":
		return doThreadsApplySuggestion(rest, stdout, stderr)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("threads", "")).Emit(stderr)
	}
}

func doThreadsList(args []string, stdout, stderr io.Writer) int {
	var prRef string
	var filterRaw string
	allFlag := false
	showCode := 0
	verbose := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--all":
			allFlag = true
		case a == "--verbose" || a == "-v":
			verbose = true
		case a == "--filter":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--filter requires a value", remediationFilterBadInput()).Emit(stderr)
			}
			i++
			filterRaw = args[i]
		case strings.HasPrefix(a, "--filter="):
			filterRaw = strings.TrimPrefix(a, "--filter=")
		case a == "--show-code":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--show-code requires a value", agentio.RemediationBadInput("threads", "list")).Emit(stderr)
			}
			i++
			n, err := parseShowCode(args[i])
			if err != nil {
				return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "list")).Emit(stderr)
			}
			showCode = n
		case strings.HasPrefix(a, "--show-code="):
			n, err := parseShowCode(strings.TrimPrefix(a, "--show-code="))
			if err != nil {
				return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "list")).Emit(stderr)
			}
			showCode = n
		case strings.HasPrefix(a, "--"):
			return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+a, agentio.RemediationBadInput("threads", "list")).Emit(stderr)
		default:
			if prRef != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+a, agentio.RemediationBadInput("threads", "list")).Emit(stderr)
			}
			prRef = a
		}
	}
	if prRef == "" {
		return agentio.NewError(agentio.CodeBadInput, "missing PR reference", agentio.RemediationBadInput("threads", "list")).Emit(stderr)
	}
	// --all is shorthand for --filter all. When both are given they must agree
	// (or --filter must be "all"); otherwise prefer the explicit --filter value.
	if allFlag && filterRaw == "" {
		filterRaw = string(threadsops.FilterAll)
	}
	mode, ferr := threadsops.ParseFilterMode(filterRaw)
	if ferr != nil {
		return agentio.NewError(agentio.CodeBadInput, ferr.Error(), remediationFilterBadInput()).Emit(stderr)
	}
	owner, repo, number, ec, ok := resolvePRRef(prRef, "threads", "list", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	listOpts := threadsops.ListOptions{
		IncludeResolved: mode.NeedsResolvedFetch(),
		ShowCodeLines:   showCode,
	}
	if showCode > 0 {
		listOpts.Fetcher = contentFetcherFor(api)
	}
	_, threads, opErr := threadsops.ListWithOptions(context.Background(), api, owner, repo, number, listOpts)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	threads = threadsops.Filter(threads, mode)
	threadsops.SortThreadsForList(threads)
	if !verbose {
		threadsops.StripVerboseFields(threads)
	}
	if err := agentio.WriteJSON(stdout, threads); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

// parseShowCode validates and parses a --show-code argument. Returns the
// integer value (0 means disabled), or an error suitable for bad_input.
func parseShowCode(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errBadShowCode(raw)
	}
	if n < 0 {
		return 0, errBadShowCode(raw)
	}
	return n, nil
}

func errBadShowCode(raw string) error {
	return showCodeError{raw: raw}
}

type showCodeError struct{ raw string }

func (e showCodeError) Error() string {
	return "--show-code must be a non-negative integer (got \"" + e.raw + "\")"
}

// contentFetcherFor returns a ContentFetcher from a threadsops.API value when
// the underlying API also implements ContentFetcher (i.e. *github.Client).
// Returns nil if the API doesn't satisfy the interface, allowing tests with
// pure stub APIs to opt out of code-context fetching cleanly.
func contentFetcherFor(api threadsops.API) threadsops.ContentFetcher {
	if f, ok := api.(threadsops.ContentFetcher); ok {
		return f
	}
	return nil
}

// remediationFilterBadInput returns the remediation text shown when --filter
// receives an unknown mode. It lists the valid modes explicitly so agents can
// retry without re-reading --help.
func remediationFilterBadInput() string {
	return "Valid --filter values: " + threadsops.ValidFilterModesCSV() + ". Default is `unresolved`."
}

func doThreadsNext(args []string, stdout, stderr io.Writer) int {
	var prRef string
	showCode := 0
	verbose := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--verbose" || a == "-v":
			verbose = true
		case a == "--show-code":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--show-code requires a value", agentio.RemediationBadInput("threads", "next")).Emit(stderr)
			}
			i++
			n, err := parseShowCode(args[i])
			if err != nil {
				return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "next")).Emit(stderr)
			}
			showCode = n
		case strings.HasPrefix(a, "--show-code="):
			n, err := parseShowCode(strings.TrimPrefix(a, "--show-code="))
			if err != nil {
				return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "next")).Emit(stderr)
			}
			showCode = n
		case strings.HasPrefix(a, "--"):
			return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+a, agentio.RemediationBadInput("threads", "next")).Emit(stderr)
		default:
			if prRef != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+a, agentio.RemediationBadInput("threads", "next")).Emit(stderr)
			}
			prRef = a
		}
	}
	if prRef == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads next <pr-ref>", agentio.RemediationBadInput("threads", "next")).Emit(stderr)
	}
	owner, repo, number, ec, ok := resolvePRRef(prRef, "threads", "next", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	nextOpts := threadsops.NextOptions{ShowCodeLines: showCode}
	if showCode > 0 {
		nextOpts.Fetcher = contentFetcherFor(api)
	}
	_, t, opErr := threadsops.NextWithOptions(context.Background(), api, owner, repo, number, nextOpts)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if t == nil {
		_ = agentio.WriteNull(stdout)
		return 0
	}
	if !verbose {
		threadsops.StripVerboseFieldsOne(t)
	}
	if err := agentio.WriteJSON(stdout, t); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

// resolvePRRef parses the PR ref via threadsops. On parse failure, emits the
// bad_input envelope to stderr and returns ok=false plus the emit exit code
// (so callers don't have to hardcode 2 — the exit code follows whatever the
// envelope mapping says for the chosen code).
func resolvePRRef(prRef, noun, verb string, stderr io.Writer) (owner, repo string, number int, exitCode int, ok bool) {
	fbO, fbR := threadsops.DetectRepoContext()
	o, r, n, err := threadsops.ParsePRRef(prRef, fbO, fbR)
	if err != nil {
		ec := agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput(noun, verb)).Emit(stderr)
		return "", "", 0, ec, false
	}
	return o, r, n, 0, true
}

func doThreadsReply(args []string, stdout, stderr io.Writer) int {
	var prRef, threadID, body, bodyFile, suggest, suggestFile, intro string
	suggestSet := false
	suggestFileSet := false
	introSet := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--body":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body requires a value", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
			i++
			body = args[i]
		case "--body-file":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body-file requires a path", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
			i++
			bodyFile = args[i]
		case "--suggest":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--suggest requires a value", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
			i++
			suggest = args[i]
			suggestSet = true
		case "--suggest-file":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--suggest-file requires a path", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
			i++
			suggestFile = args[i]
			suggestFileSet = true
		case "--intro":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--intro requires a value", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
			i++
			intro = args[i]
			introSet = true
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
			if prRef == "" {
				prRef = args[i]
			} else if threadID == "" {
				threadID = args[i]
			} else {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
			}
		}
	}
	if prRef == "" || threadID == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads reply <pr-ref> <thread-id> --body T (or --suggest BODY)", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
	}
	// --suggest and --body are mutually exclusive.
	if (suggestSet || suggestFileSet) && (body != "" || bodyFile != "") {
		return agentio.NewError(agentio.CodeBadInput, "--suggest/--suggest-file is mutually exclusive with --body/--body-file", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
	}
	if suggestSet && suggestFileSet {
		return agentio.NewError(agentio.CodeBadInput, "--suggest and --suggest-file are mutually exclusive", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
	}
	if !suggestSet && !suggestFileSet && introSet {
		return agentio.NewError(agentio.CodeBadInput, "--intro requires --suggest or --suggest-file", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
	}
	if suggestFileSet {
		b, err := threadsops.ReadBody(suggestFile)
		if err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
		}
		suggest = b
		suggestSet = true
	}
	if suggestSet {
		body = threadsops.WrapSuggestionBody(intro, suggest)
	}
	if bodyFile != "" && body == "" {
		b, err := threadsops.ReadBody(bodyFile)
		if err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
		}
		body = b
	}
	if body == "" {
		return agentio.NewError(agentio.CodeBadInput, "--body, --body-file, --suggest, or --suggest-file is required", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
	}
	_, _, _, ec, ok := resolvePRRef(prRef, "threads", "reply", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	comment, opErr := threadsops.Reply(context.Background(), api, threadID, body)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if err := agentio.WriteJSON(stdout, comment); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doThreadsResolve(args []string, stdout, stderr io.Writer) int {
	var prRef, threadID, body, bodyFile string
	dryRun := false
	showCode := 0
	verbose := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--body":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body requires a value", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			i++
			body = args[i]
		case a == "--body-file":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body-file requires a path", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			i++
			bodyFile = args[i]
		case a == "--dry-run":
			dryRun = true
		case a == "--verbose" || a == "-v":
			verbose = true
		case a == "--show-code":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--show-code requires a value", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			i++
			n, err := parseShowCode(args[i])
			if err != nil {
				return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			showCode = n
		case strings.HasPrefix(a, "--show-code="):
			n, err := parseShowCode(strings.TrimPrefix(a, "--show-code="))
			if err != nil {
				return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			showCode = n
		default:
			if strings.HasPrefix(a, "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+a, agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			if prRef == "" {
				prRef = a
			} else if threadID == "" {
				threadID = a
			} else {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+a, agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
		}
	}
	if prRef == "" || threadID == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads resolve <pr-ref> <thread-id> [--body T] [--dry-run] [--show-code N]", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
	}
	if bodyFile != "" && body == "" {
		b, err := threadsops.ReadBody(bodyFile)
		if err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
		}
		body = b
	}
	owner, repo, number, ec, ok := resolvePRRef(prRef, "threads", "resolve", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	t, _, opErr := threadsops.ResolveWithOptions(context.Background(), api, owner, repo, number, threadID, body, threadsops.ResolveOptions{DryRun: dryRun})
	if opErr != nil {
		return translateResolveErr(opErr, prRef, threadID, stderr)
	}
	// Attach code context to the printed thread if requested. Best-effort:
	// failures are silently dropped, matching list/next.
	if showCode > 0 && t != nil {
		if fetcher := contentFetcherFor(api); fetcher != nil {
			// We need the PR head SHA — refetch the status. Cheap relative to
			// the resolve mutation that just ran.
			status, _, ferr := api.FetchPR(context.Background(), owner, repo, number, "")
			if ferr == nil {
				if cc, _ := threadsops.FetchCodeContext(context.Background(), fetcher, status.HeadSHA, owner, repo, *t, showCode); cc != nil {
					t.CodeContext = cc
				}
			}
		}
	}
	if !verbose {
		threadsops.StripVerboseFieldsOne(t)
	}
	if err := agentio.WriteJSON(stdout, t); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doThreadsResolveAll(args []string, stdout, stderr io.Writer) int {
	var prRef string
	outdatedOnly := false
	dryRun := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--outdated":
			outdatedOnly = true
		case a == "--dry-run":
			dryRun = true
		case strings.HasPrefix(a, "--"):
			return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+a, agentio.RemediationBadInput("threads", "resolve-all")).Emit(stderr)
		default:
			if prRef != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+a, agentio.RemediationBadInput("threads", "resolve-all")).Emit(stderr)
			}
			prRef = a
		}
	}
	if prRef == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads resolve-all <pr-ref> [--outdated] [--dry-run]", agentio.RemediationBadInput("threads", "resolve-all")).Emit(stderr)
	}
	owner, repo, number, ec, ok := resolvePRRef(prRef, "threads", "resolve-all", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	res, opErr := threadsops.ResolveAllWithOptions(context.Background(), api, owner, repo, number, threadsops.ResolveAllOptions{
		SkipHumanThreads: true,
		OutdatedOnly:     outdatedOnly,
		DryRun:           dryRun,
	})
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if err := agentio.WriteJSON(stdout, res); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doThreadsUnresolveAll(args []string, stdout, stderr io.Writer) int {
	var prRef string
	dryRun := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
		case strings.HasPrefix(a, "--"):
			return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+a, agentio.RemediationBadInput("threads", "unresolve-all")).Emit(stderr)
		default:
			if prRef != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+a, agentio.RemediationBadInput("threads", "unresolve-all")).Emit(stderr)
			}
			prRef = a
		}
	}
	if prRef == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads unresolve-all <pr-ref> [--dry-run]", agentio.RemediationBadInput("threads", "unresolve-all")).Emit(stderr)
	}
	owner, repo, number, ec, ok := resolvePRRef(prRef, "threads", "unresolve-all", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	res, opErr := threadsops.UnresolveAllWithOptions(context.Background(), api, owner, repo, number, threadsops.UnresolveAllOptions{DryRun: dryRun})
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if err := agentio.WriteJSON(stdout, res); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

// doThreadsApplySuggestion implements `spn threads apply-suggestion`.
//
//	spn threads apply-suggestion <pr-ref> <thread-id>
//	    [--suggestion-index N] [--dry-run] [--force] [--repo-root PATH]
//
// Emits one ApplyResult JSON object on stdout on success. On error, emits the
// agentio.Error envelope on stderr.
//
// Error code semantics:
//   - bad_input: missing args, invalid index, thread has no Path/Line.
//   - not_found: thread doesn't exist, no suggestion at the chosen index,
//     or the file doesn't exist on disk.
//   - policy_violation: thread is outdated and --force not given.
//   - internal: read/write failure.
func doThreadsApplySuggestion(args []string, stdout, stderr io.Writer) int {
	var prRef, threadID, repoRoot string
	idx := 0
	dryRun := false
	force := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--suggestion-index":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--suggestion-index requires a value", agentio.RemediationBadInput("threads", "apply-suggestion")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return agentio.NewError(agentio.CodeBadInput, "--suggestion-index must be a non-negative integer", agentio.RemediationBadInput("threads", "apply-suggestion")).Emit(stderr)
			}
			idx = n
		case "--dry-run":
			dryRun = true
		case "--force":
			force = true
		case "--repo-root":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--repo-root requires a path", agentio.RemediationBadInput("threads", "apply-suggestion")).Emit(stderr)
			}
			i++
			repoRoot = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("threads", "apply-suggestion")).Emit(stderr)
			}
			if prRef == "" {
				prRef = args[i]
			} else if threadID == "" {
				threadID = args[i]
			} else {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("threads", "apply-suggestion")).Emit(stderr)
			}
		}
	}
	if prRef == "" || threadID == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads apply-suggestion <pr-ref> <thread-id> [--suggestion-index N] [--dry-run] [--force]", agentio.RemediationBadInput("threads", "apply-suggestion")).Emit(stderr)
	}
	owner, repo, number, ec, ok := resolvePRRef(prRef, "threads", "apply-suggestion", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	status, threads, opErr := threadsops.List(context.Background(), api, owner, repo, number, true)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	var target *threadsops.ReviewThreadWithPolicy
	for i := range threads {
		if threads[i].ID == threadID {
			target = &threads[i]
			break
		}
	}
	if target == nil {
		return agentio.NewError(agentio.CodeNotFound, "thread "+threadID+" not found on PR", agentio.RemediationNotFound()).Emit(stderr)
	}
	sugs := target.Suggestions
	if len(sugs) == 0 {
		return agentio.NewError(agentio.CodeNotFound, "thread has no suggestion blocks", "Review the comment body for a ```suggestion fenced block, or use --suggest on the reply command to propose one.").Emit(stderr)
	}
	if idx >= len(sugs) {
		return agentio.NewError(agentio.CodeBadInput, "suggestion index out of range", agentio.RemediationBadInput("threads", "apply-suggestion")).Emit(stderr)
	}
	res, applyErr := threadsops.ApplySuggestion(context.Background(), *target, sugs[idx], threadsops.ApplyOptions{
		RepoRoot:  repoRoot,
		DryRun:    dryRun,
		Force:     force,
		Fetcher:   contentFetcherFor(api),
		Owner:     owner,
		Repo:      repo,
		PRHeadRef: status.HeadSHA,
	})
	if applyErr != nil {
		// Translate apply-specific errors with helpful remediations.
		code := agentio.Code(applyErr.Code)
		var rem string
		switch applyErr.Code {
		case threadsops.OpCodePolicy:
			rem = "Thread is outdated. Re-run with --force to apply anyway, or skip this thread."
		case threadsops.OpCodeNotFound:
			rem = "Verify you're running from the checkout root, or pass --repo-root PATH."
		case threadsops.OpCodeBadInput:
			rem = agentio.RemediationBadInput("threads", "apply-suggestion")
		default:
			rem = agentio.RemediationInternal()
		}
		e := agentio.NewError(code, applyErr.Message, rem)
		e.Retryable = applyErr.Retryable
		if applyErr.Details != nil {
			e = e.WithDetails(applyErr.Details)
		}
		return e.Emit(stderr)
	}
	if err := agentio.WriteJSON(stdout, res); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

// translateResolveErr produces a resolve-specific remediation.
func translateResolveErr(op *threadsops.OpError, prRef, threadID string, stderr io.Writer) int {
	code := agentio.Code(op.Code)
	var rem string
	switch code {
	case agentio.CodeRateLimited:
		resetAt, _ := op.Details["reset_at"].(string)
		secs, _ := op.Details["retry_after_seconds"].(int)
		rem = agentio.RemediationRateLimited(resetAt, secs)
	case agentio.CodePolicy:
		rem = agentio.RemediationPolicyBodyRequired(prRef, threadID)
	case agentio.CodeNotFound:
		rem = agentio.RemediationNotFound()
	case agentio.CodeUpstream:
		if op.Details != nil && op.Details["comment_posted"] == true {
			rem = agentio.RemediationResolvePartialFailure(prRef, threadID)
		} else {
			rem = agentio.RemediationUpstream()
		}
	default:
		rem = agentio.RemediationInternal()
	}
	e := agentio.NewError(code, op.Message, rem)
	e.Retryable = op.Retryable
	if op.Details != nil {
		e = e.WithDetails(op.Details)
	}
	if code == agentio.CodeRateLimited {
		if secs, ok := op.Details["retry_after_seconds"].(int); ok && secs > 0 {
			e = e.WithRetryAfter(secs)
		}
	}
	return e.Emit(stderr)
}

// translateOpErr converts a threadsops.OpError into an agentio.Error and emits it.
func translateOpErr(op *threadsops.OpError, stderr io.Writer) int {
	code := agentio.Code(op.Code)
	rem := ""
	switch code {
	case agentio.CodeRateLimited:
		resetAt, _ := op.Details["reset_at"].(string)
		secs, _ := op.Details["retry_after_seconds"].(int)
		rem = agentio.RemediationRateLimited(resetAt, secs)
	case agentio.CodeNotFound:
		rem = agentio.RemediationNotFound()
	case agentio.CodeUpstream:
		rem = agentio.RemediationUpstream()
	case agentio.CodePolicy:
		// caller (resolve / resolve-all) overrides with a more specific remediation
		rem = "Provide additional context with --body."
	default:
		rem = "Re-run with --help for usage details."
	}
	e := agentio.NewError(code, op.Message, rem)
	e.Retryable = op.Retryable
	if op.Details != nil {
		e = e.WithDetails(op.Details)
	}
	if code == agentio.CodeRateLimited {
		if secs, ok := op.Details["retry_after_seconds"].(int); ok && secs > 0 {
			e = e.WithRetryAfter(secs)
		}
	}
	return e.Emit(stderr)
}
