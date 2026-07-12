package main

import (
	"context"
	"encoding/json"
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

type searchResult struct {
	ForkID, Repo, Fork, URL, Model, IndexedAt string
	Score                                     float64
}

func runSearch(args []string) int { return runSearchWith(args, os.Stdout, os.Stderr) }

func runSearchWith(args []string, stdout, stderr io.Writer) int {
	query := ""
	repoFilter := ""
	top := 20
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--repo requires owner/repo", "Pass --repo owner/repo.").Emit(stderr)
			}
			i++
			repoFilter = strings.TrimSpace(args[i])
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
		default:
			if strings.HasPrefix(args[i], "-") {
				return agentio.NewError(agentio.CodeBadInput, "unknown search flag: "+args[i], "Usage: spn search \"query\" [--repo owner/repo] [--top N]").Emit(stderr)
			}
			if query != "" {
				return agentio.NewError(agentio.CodeBadInput, "search accepts exactly one query argument", "Quote multi-word queries.").Emit(stderr)
			}
			query = strings.TrimSpace(args[i])
		}
	}
	if query == "" {
		return agentio.NewError(agentio.CodeBadInput, "search query must not be empty", "Usage: spn search \"query\" [--repo owner/repo] [--top N]").Emit(stderr)
	}

	cfg, err := config.LoadDefault()
	if err != nil {
		return agentio.NewError(agentio.CodeBadInput, "embedder_unavailable: "+err.Error(), "Secure and repair the spoon config, then retry.").Emit(stderr)
	}
	if cfg == nil || strings.ToLower(cfg.Embedder.Backend) != embed.BackendFastEmbed {
		return agentio.NewError(agentio.CodeBadInput, "embedder_unavailable: configure the fastembed backend", "Run spoon setup and select FastEmbed.").Emit(stderr)
	}
	model, err := embed.NewFastEmbedEmbedder(embed.FastEmbedConfig{
		Model: cfg.Embedder.Model, CacheDir: cfg.Embedder.CacheDir,
		MaxLength: cfg.Embedder.MaxLength, BatchSize: cfg.Embedder.BatchSize,
	})
	if err != nil {
		return agentio.NewError(agentio.CodeInternal, "embedder_unavailable: "+err.Error(), "Set ONNX_PATH to libonnxruntime.so and verify the FastEmbed cache.").Emit(stderr)
	}
	defer model.Close()

	path, pathErr := store.DefaultPath()
	if pathErr != nil {
		return agentio.NewError(agentio.CodeInternal, pathErr.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		emitSemanticEmpty(stderr)
		return 0
	}
	db, err := store.OpenDefault()
	if err != nil {
		return agentio.NewError(agentio.CodeInternal, "store_unavailable: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	defer db.Close()

	queryVector, err := model.EmbedQuery(context.Background(), query)
	if err != nil {
		return agentio.NewError(agentio.CodeInternal, "embedder_unavailable: "+err.Error(), "Verify ONNX Runtime and the FastEmbed model cache.").Emit(stderr)
	}
	repoKey := ""
	if repoFilter != "" {
		owner, name := splitRepoArg(repoFilter)
		if owner == "" || name == "" {
			return agentio.NewError(agentio.CodeBadInput, "--repo must be owner/repo", "Pass --repo owner/repo.").Emit(stderr)
		}
		provider, host := "github", "github.com"
		if cfg.Forge.Provider != "" {
			provider = strings.ToLower(cfg.Forge.Provider)
		}
		if cfg.Forge.Host != "" {
			host = cfg.Forge.Host
		}
		repoKey = store.RepoKey(provider, host, owner, name)
	}
	rows, err := db.SearchRows(context.Background(), model.ModelID(), repoKey)
	if err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	results := make([]searchResult, 0, len(rows))
	for _, row := range rows {
		vector, err := semantic.DecodeVector(row.Vector, row.Dim)
		if err != nil {
			return agentio.NewError(agentio.CodeInternal, fmt.Sprintf("invalid stored embedding %s: %v", row.DocumentID, err), agentio.RemediationInternal()).Emit(stderr)
		}
		score, err := semantic.Dot(queryVector, vector)
		if err != nil {
			return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
		results = append(results, searchResult{
			ForkID: row.ForkKey, Repo: row.Repo, Fork: row.Fork, URL: row.URL,
			Score: score, Model: row.Model, IndexedAt: row.IndexedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ForkID < results[j].ForkID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > top {
		results = results[:top]
	}
	if len(results) == 0 {
		emitSemanticEmpty(stderr)
		return 0
	}
	for _, result := range results {
		if err := agentio.WriteNDJSON(stdout, map[string]any{
			"forkId": result.ForkID, "repo": result.Repo, "fork": result.Fork,
			"url": result.URL, "score": result.Score, "model": result.Model, "indexedAt": result.IndexedAt,
		}); err != nil {
			return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	return 0
}

func emitSemanticEmpty(stderr io.Writer) {
	_ = json.NewEncoder(stderr).Encode(map[string]any{"warning": map[string]any{
		"code": "semantic_index_empty", "message": "no matching semantic embeddings are indexed",
		"remediation": "Run spn forks list with --embedder-backend fastembed first.",
	}})
}
