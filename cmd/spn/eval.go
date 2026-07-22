// cmd/spn/eval.go — the `spn forks eval` subcommand.
//
// Loads a hand-labeled judgment file, runs the same fork pipeline the
// user would run for `spn forks list`, joins the resulting HeatResult
// rows to the judgments by T1Data.ID, computes binary novelty / ARI /
// ranking metrics in internal/eval, and emits a single JSON Report
// to stdout.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/eval"
	"github.com/svnbjrn/spoon/internal/forksops"
	gh "github.com/svnbjrn/spoon/internal/github"
)

func runEval(args []string) int { return runEvalWith(args, os.Stdout, os.Stderr) }

func runEvalWith(args []string, stdout, stderr io.Writer) int {
	var repoArg, judgmentsPath, forgeFlag, forgeHost string
	opts := forksops.Options{
		Cluster: forksops.ClusterOptions{
			Enabled:        true,
			TopN:           50,
			Epsilon:        0, // resolved per backend below
			MinClusterSize: 3,
		},
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--judgments":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--judgments requires a value", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			i++
			judgmentsPath = args[i]
		case "--no-cluster":
			opts.Cluster.Enabled = false
		case "--cluster-top":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--cluster-top requires a value", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--cluster-top must be a positive integer", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			opts.Cluster.TopN = n
		case "--forge":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--forge requires a value", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			i++
			forgeFlag = strings.ToLower(args[i])
		case "--forge-host":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--forge-host requires a value", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			i++
			forgeHost = args[i]
		case "--full-mdg":
			opts.Cluster.CentralityBackend = "mdg"
		case "--no-mdg":
			opts.Cluster.CentralityBackend = ""
		default:
			// Single leading dash, not just "--", so a typo like `-tier` is
			// reported as an unknown flag instead of swallowed as the positional.
			if strings.HasPrefix(args[i], "-") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			if repoArg != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			repoArg = args[i]
		}
	}
	if repoArg == "" {
		return agentio.NewError(agentio.CodeBadInput, "missing repository argument (owner/repo)", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
	}
	if judgmentsPath == "" {
		return agentio.NewError(agentio.CodeBadInput, "missing --judgments <file>", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
	}

	// Load the judgment file. Surface parse errors with the bad_input
	// envelope so agents can tell "wrong path" from "upstream error".
	jtmt, err := loadJudgments(judgmentsPath)
	if err != nil {
		return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
	}

	// Clustering here uses the built-in lexical embedder (zero-setup,
	// deterministic) so eval never depends on a native runtime; leaving
	// opts.Cluster.Embedder nil selects it in the pipeline.
	if opts.Cluster.Epsilon == 0 {
		opts.Cluster.Epsilon = 0.55
	}
	if embedderHookForTest != nil {
		opts.Cluster.SetEmbedderForTest(embedderHookForTest)
	}

	opts.Logger = io.Discard
	if os.Getenv("SPOON_DEBUG") == "1" {
		opts.Logger = stderr
	}
	opts.ReserveDisabled = os.Getenv("SPOON_NO_RESERVE") == "1"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	provider, repo, e := providerFactory(ctx, repoArg, forgeFlag, forgeHost)
	if e != nil {
		return e.Emit(stderr)
	}
	owner, name := splitRepoArg(repo)
	if owner == "" || name == "" {
		return agentio.NewError(agentio.CodeBadInput, "invalid repo: "+repo, agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
	}
	ch, streamErr := forksops.Stream(ctx, provider, owner, name, opts)
	if streamErr != nil {
		var rejected *gh.AllBackendsRejectedError
		if errors.As(streamErr, &rejected) {
			// Every token was rejected (401): a non-retryable auth failure, not a
			// transient upstream error (#79).
			return agentio.NewError(agentio.CodeAuthRequired, streamErr.Error(), agentio.RemediationAuthRequired()).Emit(stderr)
		}
		return agentio.NewError(agentio.CodeUpstream, streamErr.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}
	// Drain the stream, join to judgments, compute the report. Per-fork
	// errors are tolerated (we want the report on partial data).
	//
	// ClusterSkip and T3Skip warnings are surfaced on stderr via the
	// same emitClusterWarning / emitStageSkipWarning helpers used by
	// `spn forks list`. Without this, an all-zero novelty report looks like
	// "the eval is broken" when the real cause is "the cluster
	// pipeline was skipped because the embedder failed". The emit
	// helpers intentionally discard write errors (stderr is the
	// standard output of last resort); a stuck consumer would still
	// drain the upstream channel, but the cost is bounded by the
	// per-fork budget reserve — see forksops.Stream's ctx wiring.
	rows := make([]eval.ScoredFork, 0, len(jtmt.Forks))
	for r := range ch {
		if r.ClusterSkip != nil {
			emitClusterWarning(stderr, r.ClusterSkip)
		}
		if r.T3Skip != nil {
			emitStageSkipWarning(stderr, r.T3Skip)
		}
		if r.Err != nil {
			continue
		}
		rows = append(rows, eval.ScoredFork{
			ID:        r.Fork.ID,
			ClusterID: r.Heat.ClusterID,
			Novelty:   r.Heat.NoveltyScore,
			Score:     r.Heat.Score,
		})
	}
	report := eval.Compute(repo, rows, jtmt)
	enc := json.NewEncoder(stdout)
	if err := enc.Encode(report); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

// loadJudgments reads a judgment file from disk and returns the parsed
// structure. The file format is the JSON schema in
// internal/eval/schema.go.
func loadJudgments(path string) (eval.Judgments, error) {
	var j eval.Judgments
	data, err := os.ReadFile(path)
	if err != nil {
		return j, fmt.Errorf("read judgments file: %w", err)
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return j, fmt.Errorf("parse judgments file: %w", err)
	}
	return j, nil
}
