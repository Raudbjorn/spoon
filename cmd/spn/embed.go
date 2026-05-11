// cmd/spn/embed.go
package main

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/embed"
)

// detectFn and pullFn are indirected through variables so tests can stub them
// without starting a real Ollama process.
var detectFn = embed.Detect
var pullFn = embed.Pull

func runEmbed(args []string) int { return runEmbedWith(args, os.Stdout, os.Stderr) }

func runEmbedWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (status|pull|models)", agentio.RemediationBadInput("embed", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "status":
		return doEmbedStatus(rest, stdout, stderr)
	case "pull":
		return doEmbedPull(rest, stdout, stderr)
	case "models":
		return doEmbedModels(rest, stdout, stderr)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("embed", "")).Emit(stderr)
	}
}

func doEmbedStatus(args []string, stdout, stderr io.Writer) int {
	var endpoint string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--endpoint":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--endpoint requires a value", agentio.RemediationBadInput("embed", "status")).Emit(stderr)
			}
			i++
			endpoint = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("embed", "status")).Emit(stderr)
			}
			return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("embed", "status")).Emit(stderr)
		}
	}

	ctx := context.Background()
	running, normalizedEndpoint, installed := detectFn(ctx, endpoint)

	recommended := embed.PreferredEmbeddingModelsCopy()

	out := map[string]any{
		"running":     running,
		"endpoint":    normalizedEndpoint,
		"installed":   installed,
		"recommended": recommended,
	}
	if installed == nil {
		out["installed"] = []string{}
	}
	if err := agentio.WriteJSON(stdout, out); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doEmbedPull(args []string, stdout, stderr io.Writer) int {
	var model, endpoint string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--endpoint":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--endpoint requires a value", agentio.RemediationBadInput("embed", "pull")).Emit(stderr)
			}
			i++
			endpoint = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("embed", "pull")).Emit(stderr)
			}
			if model != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("embed", "pull")).Emit(stderr)
			}
			model = args[i]
		}
	}
	if model == "" {
		return agentio.NewError(agentio.CodeBadInput, "missing model argument", agentio.RemediationBadInput("embed", "pull")).Emit(stderr)
	}

	ctx := context.Background()
	err := pullFn(ctx, endpoint, model, func(phase string, pct float64) {
		_ = agentio.WriteNDJSON(stdout, map[string]any{
			"model": model,
			"phase": phase,
			"pct":   pct,
		})
	})
	if err != nil {
		return agentio.NewError(agentio.CodeUpstream, "embed pull: "+err.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}
	return 0
}

func doEmbedModels(_ []string, stdout, stderr io.Writer) int {
	models := embed.PreferredEmbeddingModelsCopy()
	if err := agentio.WriteJSON(stdout, models); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
