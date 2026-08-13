package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/semantic"
	"github.com/svnbjrn/spoon/internal/store"
)

// defaultRerankOverfetch multiplies --top to size the candidate set handed to
// the reranker: a cross-encoder can only promote what retrieval surfaced, so it
// needs more candidates than the caller asked to see. Reranking the whole index
// instead would be a cost blowout — SearchRows returns every stored vector.
const defaultRerankOverfetch = 5

type searchEmbedderFactory func(bool, config.EmbedderConfig, embed.VoyageConfig) (embed.SearchEmbedder, func(), *agentio.Error)

type searchResult struct {
	DocumentID, ForkID, Repo, Fork, URL, Model, IndexedAt string
	Score                                                 float64

	// RerankScore is the cross-encoder's relevance in [0,1] and RerankModel
	// names the model that produced it. Both zero when no reranking ran; Score
	// always stays the retrieval cosine so both signals remain visible.
	RerankScore float64
	RerankModel string
}

// rerankTristate distinguishes "user said nothing" from an explicit choice, so
// the default (on whenever Voyage is active) can be applied without silently
// overriding --no-rerank.
type rerankTristate int

const (
	rerankUnset rerankTristate = iota
	rerankForceOn
	rerankForceOff
)

func runSearch(args []string) int { return runSearchWith(args, os.Stdout, os.Stderr) }

// runSearchWith is the package test seam. Production dispatch provides its
// Bootstrap-owned effective result to runSearchWithEffective.
func runSearchWith(args []string, stdout, stderr io.Writer) int {
	boot := config.Bootstrap(io.Discard)
	env := config.EnvironmentSnapshot()
	return runSearchWithEffective(args, stdout, stderr, config.ResolveEffectiveConfig(boot.Config, nil, env), env)
}

func runSearchWithEffective(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig, env map[string]string) int {
	return runSearchWithEffectiveDeps(args, stdout, stderr, effective, env, defaultCommandDeps())
}

func runSearchWithEffectiveDeps(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig, env map[string]string, deps commandDeps) int {
	deps = deps.withDefaults()
	ctx := context.Background()
	query := ""
	repoOwner, repoName := "", ""
	top := 20
	useVoyage := false
	rerankChoice := rerankUnset
	overfetch := defaultRerankOverfetch
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--repo requires owner/repo", "Pass --repo owner/repo.").Emit(stderr)
			}
			i++
			repoFilter := strings.TrimSpace(args[i])
			if repoFilter == "" {
				return agentio.NewError(agentio.CodeBadInput, "--repo must not be empty", "Pass --repo owner/repo.").Emit(stderr)
			}
			// Validate the owner/repo shape at parse time (before loading the
			// embedder) so a malformed value fails fast and without ONNX.
			repoOwner, repoName = splitRepoArg(repoFilter)
			if repoOwner == "" || repoName == "" {
				return agentio.NewError(agentio.CodeBadInput, "--repo must be owner/repo", "Pass --repo owner/repo.").Emit(stderr)
			}
		case "--top":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--top requires a value", "Pass --top N with N > 0.").Emit(stderr)
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value <= 0 {
				return agentio.NewError(agentio.CodeBadInput, "--top must be a positive integer", "Pass --top N with N > 0.").Emit(stderr)
			}
			top = value
		case "--voyage":
			useVoyage = true
		case "--rerank":
			rerankChoice = rerankForceOn
		case "--no-rerank":
			rerankChoice = rerankForceOff
		case "--rerank-overfetch":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--rerank-overfetch requires a value", "Pass --rerank-overfetch N with N >= 1.").Emit(stderr)
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--rerank-overfetch must be an integer >= 1", "Pass --rerank-overfetch N with N >= 1.").Emit(stderr)
			}
			overfetch = value
		case "--":
			// POSIX flag/positional separator: everything after is positional.
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "missing query after --", searchUsage).Emit(stderr)
			}
			if i+2 < len(args) {
				return agentio.NewError(agentio.CodeBadInput, "search accepts exactly one query argument", "Quote multi-word queries.").Emit(stderr)
			}
			query = strings.TrimSpace(args[i+1])
			i = len(args)
		default:
			if strings.HasPrefix(args[i], "-") {
				return agentio.NewError(agentio.CodeBadInput, "unknown search flag: "+args[i], searchUsage).Emit(stderr)
			}
			if query != "" {
				return agentio.NewError(agentio.CodeBadInput, "search accepts exactly one query argument", "Quote multi-word queries.").Emit(stderr)
			}
			query = strings.TrimSpace(args[i])
		}
	}
	if query == "" {
		return agentio.NewError(agentio.CodeBadInput, "search query must not be empty", searchUsage).Emit(stderr)
	}

	embCfg, err := effective.EmbedderConfig()
	if err != nil {
		return agentio.NewError(agentio.CodeBadInput, "embedder_unavailable: "+err.Error(), "Correct the effective spoon configuration, then retry.").Emit(stderr)
	}

	// A flag naming Voyage is a promise we cannot keep without a key, and the key
	// check needs nothing else — so reject it before any store or model work.
	// Failing beats silently ranking against a different model's index.
	voyageRequested := useVoyage || rerankChoice == rerankForceOn
	keyed, keyErr := embed.VoyageKeyConfiguredEffective(effective, false, env)
	if keyErr != nil && voyageRequested {
		return agentio.NewError(agentio.CodeBadInput, "voyage_unavailable: "+keyErr.Error(), voyageRemediation(keyErr)).Emit(stderr)
	}
	if !keyed && voyageRequested {
		flag := "--voyage"
		if !useVoyage {
			flag = "--rerank"
		}
		return agentio.NewError(agentio.CodeBadInput,
			"voyage_unavailable: "+flag+" requires a Voyage API key", voyageKeyRemediation).Emit(stderr)
	}

	// An absent store is a successful empty result — checked before loading the
	// embedder so a fresh install without onnxruntime still reports
	// semantic_index_empty rather than embedder_unavailable.
	path, pathErr := store.DefaultPath()
	if pathErr != nil {
		return agentio.NewError(agentio.CodeInternal, pathErr.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		emitSemanticEmpty(stderr, useVoyage)
		return 0
	}

	// The store opens before the Voyage resolve and before the embedder: it is
	// both the paid-response cache and where the vectors live, and Voyage is only
	// enabled when it can actually be written (see embed.ResolveVoyageConfig).
	db, err := store.OpenDefault()
	if err != nil {
		return agentio.NewError(agentio.CodeInternal, "store_unavailable: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	defer db.Close()

	voyageCfg, voyageActive, voyageErr := embed.ResolveVoyageEffective(ctx, effective, false, db, env)
	if voyageErr != nil {
		if voyageRequested {
			return agentio.NewError(agentio.CodeBadInput, "voyage_unavailable: "+voyageErr.Error(), voyageRemediation(voyageErr)).Emit(stderr)
		}
		// Voyage was not asked for by name here, so degrade rather than fail.
		emitVoyageWarning(stderr, "is configured but unusable; ranking without it", voyageErr)
	}
	// Reranking is orthogonal to which index retrieved the candidates: a
	// cross-encoder scores (query, body) pairs and never touches the stored
	// vectors, so the fastembed index reranks just as well.
	rerankEnabled := voyageActive && rerankChoice != rerankForceOff

	model, closeModel, aerr := deps.searchEmbedder(useVoyage, embCfg, voyageCfg)
	if aerr != nil {
		return emitDataError(stderr, aerr)
	}
	defer closeModel()

	queryVector, err := model.EmbedQuery(ctx, query)
	if err != nil {
		if useVoyage {
			return agentio.NewError(agentio.CodeBadInput, "voyage_unavailable: "+err.Error(), voyageRemediation(err)).Emit(stderr)
		}
		return agentio.NewError(agentio.CodeInternal, "embedder_unavailable: "+err.Error(), "Verify ONNX Runtime and the FastEmbed model cache.").Emit(stderr)
	}
	rows, err := db.SearchRows(ctx, model.ModelID(), repoOwner, repoName)
	if err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	results, skipped := rankSearchRows(queryVector, rows)
	if skipped > 0 {
		// One unreadable blob (partial write, corruption) must not fail the whole
		// command and strand every intact row — degrade and count instead.
		emitRowsSkipped(stderr, skipped)
	}
	sortByCosine(results)
	if rerankEnabled && len(results) > 0 {
		results = rerankResults(ctx, db, voyageCfg, query, results, top, overfetch, stderr)
	}
	if len(results) > top {
		results = results[:top]
	}
	if len(results) == 0 {
		emitSemanticEmpty(stderr, useVoyage)
		return 0
	}
	if err := emitSearchResults(stdout, results); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func emitSearchResults(stdout io.Writer, results []searchResult) error {
	for _, result := range results {
		record := map[string]any{
			"forkId": result.ForkID, "repo": result.Repo, "fork": result.Fork,
			"url": result.URL, "score": result.Score, "model": result.Model, "indexedAt": result.IndexedAt,
		}
		if result.RerankModel != "" {
			record["rerankScore"] = result.RerankScore
			record["rerankModel"] = result.RerankModel
		}
		if err := writeDataNDJSON(stdout, record); err != nil {
			return err
		}
	}
	return nil
}

const (
	searchUsage = "Usage: spn search \"query\" [--repo owner/repo] [--top N] [--voyage] [--rerank|--no-rerank] [--rerank-overfetch N]"

	voyageKeyRemediation = "Set " + embed.VoyageAPIKeyEnv + " (or embedder.voyage.apiKeyFile in the spoon config), or drop the flag to use the local fastembed index."
)

// searchEmbedderFor picks the index to search. fastembed is the default; only
// --voyage selects the Voyage index, so the paid provider is never queried
// implicitly. With --voyage, fastembed is not initialized at all — which is what
// lets `spn search --voyage` work on a host with no ONNX Runtime installed.
func searchEmbedderFor(useVoyage bool, embCfg config.EmbedderConfig, voyageCfg embed.VoyageConfig) (embed.SearchEmbedder, func(), *agentio.Error) {
	if useVoyage {
		embedder, err := embed.NewVoyageEmbedder(voyageCfg)
		if err != nil {
			return nil, nil, agentio.NewError(agentio.CodeBadInput, "voyage_unavailable: "+err.Error(), voyageRemediation(err))
		}
		return embedder, func() { _ = embedder.Close() }, nil
	}
	embedder, err := embed.NewFastEmbedEmbedder(embed.FastEmbedConfig{
		Model: embCfg.Model, CacheDir: embCfg.CacheDir,
		MaxLength: embCfg.MaxLength, BatchSize: embCfg.BatchSize,
	})
	if err != nil {
		// User-fixable (the remediation says so: set ONNX_PATH), so bad_input
		// (exit 2), not internal (exit 1) which reads as a tool bug to an agent.
		return nil, nil, agentio.NewError(agentio.CodeBadInput, "embedder_unavailable: "+err.Error(),
			"Set ONNX_PATH to libonnxruntime.so and verify the FastEmbed cache.")
	}
	return embedder, func() { _ = embedder.Close() }, nil
}

// sortByCosine orders results by retrieval score, breaking ties on fork ID so
// output is deterministic across runs.
func sortByCosine(results []searchResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ForkID < results[j].ForkID
		}
		return results[i].Score > results[j].Score
	})
}

// rerankResults re-orders the top candidates with the Voyage cross-encoder. Any
// failure returns the cosine-ordered input unchanged and warns: losing the
// second-stage refinement is a degradation, losing the search is not acceptable.
func rerankResults(ctx context.Context, db *store.Store, cfg embed.VoyageConfig, query string, results []searchResult, top, overfetch int, stderr io.Writer) []searchResult {
	reranker, err := embed.NewVoyageReranker(cfg)
	if err != nil {
		emitRerankUnavailable(stderr, err.Error(), nil)
		return results
	}
	candidates := results[:min(top*overfetch, len(results))]
	ids := make([]string, len(candidates))
	for i, candidate := range candidates {
		ids[i] = candidate.DocumentID
	}
	bodies, err := db.DocumentBodies(ctx, ids)
	if err != nil {
		emitRerankUnavailable(stderr, "reading document bodies failed: "+err.Error(), nil)
		return results
	}
	docs := make([]string, len(candidates))
	missing := 0
	for i, candidate := range candidates {
		body := bodies[candidate.DocumentID]
		if strings.TrimSpace(body) == "" {
			missing++
			continue
		}
		docs[i] = body
	}
	if missing > 0 {
		// Reranking only some candidates would interleave two incomparable
		// score scales, which orders results worse than not reranking at all.
		emitRerankUnavailable(stderr, fmt.Sprintf("%d candidate document(s) had no stored body", missing),
			map[string]any{"missingBodies": missing})
		return results
	}
	scores, err := reranker.Rerank(ctx, query, docs)
	if err != nil {
		emitRerankUnavailable(stderr, err.Error(), nil)
		return results
	}
	if len(scores) != len(docs) {
		emitRerankUnavailable(stderr, fmt.Sprintf("reranker returned %d scores, want %d", len(scores), len(docs)), nil)
		return results
	}
	reranked := make([]searchResult, len(candidates))
	copy(reranked, candidates)
	for i := range reranked {
		reranked[i].RerankScore = scores[i]
		reranked[i].RerankModel = reranker.ModelID()
	}
	sort.Slice(reranked, func(i, j int) bool {
		if reranked[i].RerankScore == reranked[j].RerankScore {
			return reranked[i].ForkID < reranked[j].ForkID
		}
		return reranked[i].RerankScore > reranked[j].RerankScore
	})
	return reranked
}

// rankSearchRows scores every readable row against the query vector, skipping
// (and counting) any whose stored vector cannot be decoded or scored. A single
// corrupt blob must not fail the whole search and strand thousands of intact
// rows, so the caller degrades on skipped>0 rather than erroring.
func rankSearchRows(queryVector []float32, rows []store.SearchRow) (results []searchResult, skipped int) {
	results = make([]searchResult, 0, len(rows))
	for _, row := range rows {
		vector, err := semantic.DecodeVector(row.Vector, row.Dim)
		if err != nil {
			skipped++
			continue
		}
		score, err := semantic.Cosine(queryVector, vector)
		if err != nil {
			skipped++
			continue
		}
		results = append(results, searchResult{
			DocumentID: row.DocumentID,
			ForkID:     row.ForkKey, Repo: row.Repo, Fork: row.Fork, URL: row.URL,
			Score: score, Model: row.Model, IndexedAt: row.IndexedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		})
	}
	return results, skipped
}

func emitRowsSkipped(stderr io.Writer, skipped int) {
	_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
		"code":        "semantic_rows_skipped",
		"message":     fmt.Sprintf("%d stored embedding(s) were unreadable and skipped", skipped),
		"details":     map[string]any{"skipped": skipped},
		"remediation": "Re-run 'spn forks list <repo>' to rebuild the affected embeddings.",
	}})
}

func emitRerankUnavailable(stderr io.Writer, message string, details map[string]any) {
	warning := map[string]any{
		"code":        "rerank_unavailable",
		"message":     "voyage reranking skipped: " + message,
		"remediation": "Results are ordered by vector similarity. Retry, or pass --no-rerank to skip reranking.",
	}
	if details != nil {
		warning["details"] = details
	}
	_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": warning})
}

// emitSemanticEmpty reports an empty result set. The remediation names the index
// that was actually searched — telling a --voyage user to install fastembed
// would send them after the wrong prerequisite.
func emitSemanticEmpty(stderr io.Writer, useVoyage bool) {
	remediation := "Run 'spn forks list <repo>' first (with fastembed available) to build the index."
	if useVoyage {
		remediation = "Run 'spn forks list <repo>' first with " + embed.VoyageAPIKeyEnv + " set to build the Voyage index."
	}
	_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
		"code": "semantic_index_empty", "message": "no matching semantic embeddings are indexed",
		"remediation": remediation,
	}})
}
