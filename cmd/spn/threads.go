// cmd/spn/threads.go
package main

import (
	"context"
	"io"
	"os"
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
		return agentio.NewError(agentio.CodeBadInput, "missing verb (list|next|reply|resolve|resolve-all|unresolve-all)", agentio.RemediationBadInput("threads", "")).Emit(stderr)
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
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("threads", "")).Emit(stderr)
	}
}

func doThreadsList(args []string, stdout, stderr io.Writer) int {
	var prRef string
	var filterRaw string
	allFlag := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--all":
			allFlag = true
		case a == "--filter":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--filter requires a value", remediationFilterBadInput()).Emit(stderr)
			}
			i++
			filterRaw = args[i]
		case strings.HasPrefix(a, "--filter="):
			filterRaw = strings.TrimPrefix(a, "--filter=")
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
	_, threads, opErr := threadsops.List(context.Background(), api, owner, repo, number, mode.NeedsResolvedFetch())
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	threads = threadsops.Filter(threads, mode)
	threadsops.SortThreadsForList(threads)
	if err := agentio.WriteJSON(stdout, threads); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

// remediationFilterBadInput returns the remediation text shown when --filter
// receives an unknown mode. It lists the valid modes explicitly so agents can
// retry without re-reading --help.
func remediationFilterBadInput() string {
	return "Valid --filter values: " + threadsops.ValidFilterModesCSV() + ". Default is `unresolved`."
}

func doThreadsNext(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads next <pr-ref>", agentio.RemediationBadInput("threads", "next")).Emit(stderr)
	}
	owner, repo, number, ec, ok := resolvePRRef(args[0], "threads", "next", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	_, t, opErr := threadsops.Next(context.Background(), api, owner, repo, number)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if t == nil {
		_ = agentio.WriteNull(stdout)
		return 0
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
	var prRef, threadID, body, bodyFile string
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
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads reply <pr-ref> <thread-id> --body T", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
	}
	if bodyFile != "" && body == "" {
		b, err := threadsops.ReadBody(bodyFile)
		if err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
		}
		body = b
	}
	if body == "" {
		return agentio.NewError(agentio.CodeBadInput, "--body or --body-file is required", agentio.RemediationBadInput("threads", "reply")).Emit(stderr)
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
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--body":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body requires a value", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			i++
			body = args[i]
		case "--body-file":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--body-file requires a path", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			i++
			bodyFile = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
			if prRef == "" {
				prRef = args[i]
			} else if threadID == "" {
				threadID = args[i]
			} else {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
			}
		}
	}
	if prRef == "" || threadID == "" {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads resolve <pr-ref> <thread-id> [--body T]", agentio.RemediationBadInput("threads", "resolve")).Emit(stderr)
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
	t, _, opErr := threadsops.Resolve(context.Background(), api, owner, repo, number, threadID, body)
	if opErr != nil {
		return translateResolveErr(opErr, prRef, threadID, stderr)
	}
	if err := agentio.WriteJSON(stdout, t); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doThreadsResolveAll(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads resolve-all <pr-ref>", agentio.RemediationBadInput("threads", "resolve-all")).Emit(stderr)
	}
	owner, repo, number, ec, ok := resolvePRRef(args[0], "threads", "resolve-all", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	res, opErr := threadsops.ResolveAll(context.Background(), api, owner, repo, number, true)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if err := agentio.WriteJSON(stdout, res); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doThreadsUnresolveAll(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads unresolve-all <pr-ref>", agentio.RemediationBadInput("threads", "unresolve-all")).Emit(stderr)
	}
	owner, repo, number, ec, ok := resolvePRRef(args[0], "threads", "unresolve-all", stderr)
	if !ok {
		return ec
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	res, opErr := threadsops.UnresolveAll(context.Background(), api, owner, repo, number)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
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
