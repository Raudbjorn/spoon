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
	includeResolved := false
	for _, a := range args {
		switch {
		case a == "--all":
			includeResolved = true
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
	owner, repo, number, ok := resolvePRRef(prRef, stderr)
	if !ok {
		return 2
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	_, threads, opErr := threadsops.List(context.Background(), api, owner, repo, number, includeResolved)
	if opErr != nil {
		return translateOpErr(opErr, stderr)
	}
	if err := agentio.WriteJSON(stdout, threads); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doThreadsNext(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return agentio.NewError(agentio.CodeBadInput, "usage: spn threads next <pr-ref>", agentio.RemediationBadInput("threads", "next")).Emit(stderr)
	}
	owner, repo, number, ok := resolvePRRef(args[0], stderr)
	if !ok {
		return 2
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

// resolvePRRef parses the PR ref via threadsops, with stderr-emitted error envelope on failure.
func resolvePRRef(prRef string, stderr io.Writer) (owner, repo string, number int, ok bool) {
	fbO, fbR := threadsops.DetectRepoContext()
	o, r, n, err := threadsops.ParsePRRef(prRef, fbO, fbR)
	if err != nil {
		agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("threads", "")).Emit(stderr)
		return "", "", 0, false
	}
	return o, r, n, true
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
	_, _, _, ok := resolvePRRef(prRef, stderr)
	if !ok {
		return 2
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
	owner, repo, number, ok := resolvePRRef(prRef, stderr)
	if !ok {
		return 2
	}
	api, authErr := apiFactory()
	if authErr != nil {
		return authErr.Emit(stderr)
	}
	t, opErr := threadsops.Resolve(context.Background(), api, owner, repo, number, threadID, body)
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
	owner, repo, number, ok := resolvePRRef(args[0], stderr)
	if !ok {
		return 2
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
	owner, repo, number, ok := resolvePRRef(args[0], stderr)
	if !ok {
		return 2
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
	return e.Emit(stderr)
}

// translateOpErr converts a threadsops.OpError into an agentio.Error and emits it.
func translateOpErr(op *threadsops.OpError, stderr io.Writer) int {
	code := agentio.Code(op.Code)
	rem := ""
	switch code {
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
	return e.Emit(stderr)
}
