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
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/eval"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/heat"
)

func runEval(args []string) int { return runEvalWith(args, os.Stdout, os.Stderr) }

func runEvalWith(args []string, stdout, stderr io.Writer) int {
	boot := config.Bootstrap(io.Discard)
	env := config.EnvironmentSnapshot()
	return runEvalWithEffective(args, stdout, stderr, config.ResolveEffectiveConfig(boot.Config, nil, env), env)
}

func runEvalWithEffective(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig, env map[string]string) int {
	var repoArg, judgmentsPath, exportPath, rankVariant string
	shortlistN := evalDefaultShortlistN
	priorScale := 0.0
	forgeFlag := strings.ToLower(effective.Forge.Provider.Value)
	forgeHost := effective.Forge.Host.Value
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
		case "--from-export":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--from-export requires a value", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			i++
			exportPath = args[i]
		case "--rank-variant":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--rank-variant requires a value", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			i++
			rankVariant = args[i]
			if !validRankVariant(rankVariant) {
				return agentio.NewError(agentio.CodeBadInput, "--rank-variant must be one of "+strings.Join(rankVariants, ", "), agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
		case "--shortlist":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--shortlist requires a value", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--shortlist must be a positive integer", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			shortlistN = n
		case "--prior-scale":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--prior-scale requires a value", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			i++
			f, err := strconv.ParseFloat(args[i], 64)
			if err != nil || !(f > 0) || math.IsInf(f, 0) {
				return agentio.NewError(agentio.CodeBadInput, "--prior-scale must be a positive number", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
			}
			priorScale = f
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
	if rankVariant != "" && exportPath == "" {
		return agentio.NewError(agentio.CodeBadInput, "--rank-variant requires --from-export", agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
	}
	if exportPath != "" {
		if rankVariant == "" {
			rankVariant = rankVariantHeat
		}
		pool, err := loadExportPool(exportPath)
		if err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("forks", "eval")).Emit(stderr)
		}
		report := evalOffline(repoArg, pool, jtmt, rankVariant, shortlistN, priorScale)
		if err := writeDataJSON(stdout, report); err != nil {
			return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
		return 0
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

	opts.Logger = debugDataLogger(io.Discard)
	if os.Getenv("SPOON_DEBUG") == "1" {
		opts.Logger = debugDataLogger(stderr)
	}
	opts.ReserveDisabled = os.Getenv("SPOON_NO_RESERVE") == "1"

	ctx := context.WithValue(context.Background(), effectiveConfigContextKey{}, effective)
	ctx = context.WithValue(ctx, environmentContextKey{}, env)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	provider, repo, e := providerFactory(ctx, repoArg, forgeFlag, forgeHost)
	if e != nil {
		return emitDataError(stderr, e)
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
	if err := writeDataJSON(stdout, report); err != nil {
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

// Offline evaluation: rank an exported fork set under a chosen ordering key
// and score it against judgments, with no network. This is the gate for
// changing shortlist defaults (--shortlist-rule, --eb): every variant is
// scored on identical inputs.

// Rank variants: which per-fork number orders the list (higher = better).
const (
	rankVariantHeat       = "heat"       // raw heat score (spn forks list default ordering)
	rankVariantERank      = "erank"      // −expected rank (the --shortlist default)
	rankVariantPScore     = "pscore"     // P-score (affine in expected rank; same order as erank)
	rankVariantMembership = "membership" // P(rank ≤ k) (--shortlist-rule membership)
	rankVariantEB         = "eb"         // −expected rank after empirical-Bayes shrinkage (--eb)
)

var rankVariants = []string{rankVariantHeat, rankVariantERank, rankVariantPScore, rankVariantMembership, rankVariantEB}

// evalDefaultShortlistN is k for P(rank ≤ k) when --shortlist is not given.
const evalDefaultShortlistN = 10

func validRankVariant(v string) bool {
	for _, r := range rankVariants {
		if r == v {
			return true
		}
	}
	return false
}

// exportFile is the subset of the spoon export JSON (internal/tui/export.go)
// that offline ranking needs.
type exportFile struct {
	Forks []struct {
		FullName string `json:"full_name"`
		Heat     *struct {
			Score      float64 `json:"score"`
			Tier       int     `json:"tier"`
			Confidence float64 `json:"confidence"`
		} `json:"heat"`
	} `json:"forks"`
}

// loadExportPool reads an export and rebuilds the minimal Results the rank
// model needs (heat score + tier confidence). Forks without a heat block are
// skipped; exports that predate the confidence field get it from the tier.
func loadExportPool(path string) ([]forksops.Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read export file: %w", err)
	}
	var ex exportFile
	if err := json.Unmarshal(data, &ex); err != nil {
		return nil, fmt.Errorf("parse export file: %w", err)
	}
	pool := make([]forksops.Result, 0, len(ex.Forks))
	for _, f := range ex.Forks {
		if f.Heat == nil || f.FullName == "" {
			continue
		}
		conf := f.Heat.Confidence
		if conf == 0 {
			conf = heat.TierConfidence(f.Heat.Tier)
		}
		pool = append(pool, forksops.Result{
			Fork: forge.T1Data{ID: f.FullName},
			Heat: heat.HeatResult{Score: f.Heat.Score, Tier: f.Heat.Tier, Confidence: conf},
		})
	}
	if len(pool) == 0 {
		return nil, errors.New("export file has no forks with a heat block")
	}
	return pool, nil
}

// offlineReport is eval.Report plus the ranking provenance an offline run
// adds: which key ordered the list, the pool-level rank report, and the
// ordered list with each fork's key.
type offlineReport struct {
	eval.Report
	RankVariant string               `json:"rankVariant"`
	ShortlistN  int                  `json:"shortlistN"`
	RankReport  *forksops.RankReport `json:"rankReport,omitempty"`
	Ranked      []rankedFork         `json:"ranked"`
}

type rankedFork struct {
	ID           string  `json:"id"`
	Key          float64 `json:"key"`
	Heat         float64 `json:"heat"`
	Tier         int     `json:"tier"`
	ExpectedRank float64 `json:"expectedRank,omitempty"`
	PScore       float64 `json:"pScore,omitempty"`
	PTopK        float64 `json:"pTopK,omitempty"`
	EBTheta      float64 `json:"ebTheta,omitempty"`
}

// evalOffline orders pool by variant, scores the ordering against judgments
// (ranking metrics only — clustering/novelty are not reconstructible from an
// export, so those fields read as zero), and returns the report.
func evalOffline(upstream string, pool []forksops.Result, jtmt eval.Judgments, variant string, shortlistN int, priorScale float64) offlineReport {
	out := offlineReport{RankVariant: variant, ShortlistN: shortlistN}
	var ranked []forksops.Result
	if variant == rankVariantHeat {
		// Same row set as the rank model (top RankPoolCap by heat) so the
		// metrics are comparable across variants: every unlabelled row is a
		// negative for AUC, and the zero-heat tail would otherwise hand the
		// heat variant thousands of free wins.
		ranked = append([]forksops.Result(nil), pool...)
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Heat.Score > ranked[j].Heat.Score })
		if len(ranked) > forksops.RankPoolCap {
			ranked = ranked[:forksops.RankPoolCap]
		}
	} else {
		opts := forksops.Options{ShortlistN: shortlistN, RankKeepAll: true, EB: variant == rankVariantEB, PriorScale: priorScale}
		if variant == rankVariantMembership {
			opts.ShortlistRule = forksops.ShortlistRuleMembership
		}
		var report forksops.RankReport
		ranked, report = forksops.RankResults(pool, opts)
		out.RankReport = &report
	}
	rows := make([]eval.ScoredFork, 0, len(ranked))
	for pos, r := range ranked {
		key := r.Heat.Score
		rf := rankedFork{ID: r.Fork.ID, Heat: r.Heat.Score, Tier: r.Heat.Tier}
		if r.Rank != nil {
			rf.ExpectedRank, rf.PScore, rf.PTopK = r.Rank.ExpectedRank, r.Rank.PScore, r.Rank.PTopK
			switch variant {
			case rankVariantPScore:
				key = r.Rank.PScore
			case rankVariantMembership:
				// P(rank ≤ k) saturates at 1 for every clear member, so it is
				// a selector, not an ordering key; the rule orders the
				// selected set by expected rank, which RankResults already
				// applied — the returned position is the key.
				key = -float64(pos)
			default: // erank, eb
				key = -r.Rank.ExpectedRank
			}
		}
		if r.EB != nil {
			rf.EBTheta = r.EB.Theta
		}
		rf.Key = key
		out.Ranked = append(out.Ranked, rf)
		rows = append(rows, eval.ScoredFork{ID: r.Fork.ID, Score: key})
	}
	out.Report = eval.Compute(upstream, rows, jtmt)
	return out
}
