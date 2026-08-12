package embed

// voyageresolve.go layers Voyage settings from flags, environment and the config
// file. It lives here rather than in each command so `spn forks list`,
// `spn search` and the `spoon` TUI all decide "is Voyage active?" the same way —
// a provider that indexes on one path but is invisible on another is worse than
// one that is simply off.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
)

// Voyage environment variables. VoyageAPIKeyEnv is the documented name;
// VoyageAPIKeyEnvAlt is what Voyage's own SDKs read, accepted so a host that
// already has a Voyage key configured works without duplicating it.
const (
	VoyageAPIKeyEnv      = "VOYAGE_AI_API_KEY"
	VoyageAPIKeyEnvAlt   = "VOYAGE_API_KEY"
	VoyageBaseURLEnv     = "SPOON_VOYAGE_BASE_URL"
	VoyageEmbedModelEnv  = "SPOON_VOYAGE_EMBED_MODEL"
	VoyageRerankModelEnv = "SPOON_VOYAGE_RERANK_MODEL"
	VoyageDimensionEnv   = "SPOON_VOYAGE_DIM"
	VoyageDisableEnv     = "SPOON_NO_VOYAGE"
	// VoyageNoCacheEnv bypasses the paid-response cache. Useful when comparing
	// model revisions served under one name, or when diagnosing the cache itself.
	VoyageNoCacheEnv = "SPOON_VOYAGE_NO_CACHE"
)

// ResolveVoyageConfig layers flag > environment > config file, matching the
// precedence documented for every other spoon setting.
//
// Voyage requires two things, and both are hard preconditions:
//
//  1. an API key, and
//  2. a durable place to write — the cache argument, verified by an actual write.
//
// The second is not an optimization. The same store holds both the response
// cache and the vectors Voyage is paid to produce, so without durable writes
// every request buys a result that is discarded at process exit and bought again
// next run. Paying repeatedly for answers that cannot be kept is worse than not
// using the provider, so Voyage stays off.
//
// active is false when Voyage is simply not configured (no key, disabled in
// config, or suppressed by SPOON_NO_VOYAGE / disable) — that is the normal
// zero-configuration case and never an error. err is returned when Voyage *is*
// configured but cannot be used: an unreadable or world-readable key file, an
// invalid dimension, or no writable store. Callers that named Voyage explicitly
// should fail on that error; callers where Voyage is incidental should warn and
// continue without it.
func ResolveVoyageConfig(ctx context.Context, fileCfg config.VoyageConfig, disable bool, cache ResponseCache) (cfg VoyageConfig, active bool, err error) {
	key, err := voyageKey(fileCfg, disable)
	if err != nil || key == "" {
		return VoyageConfig{}, false, err
	}

	dimension := fileCfg.OutputDimension
	if raw := strings.TrimSpace(os.Getenv(VoyageDimensionEnv)); raw != "" {
		parsed, perr := strconv.Atoi(raw)
		if perr != nil {
			return VoyageConfig{}, false, fmt.Errorf("%s=%q is not an integer", VoyageDimensionEnv, raw)
		}
		dimension = parsed
	}

	// A key with nowhere durable to write is a configured-but-unusable provider,
	// not an absent one: the user asked for Voyage, so say why it is off.
	if cache == nil {
		return VoyageConfig{}, false, errors.New(
			"no durable store is available for the response cache and embeddings; Voyage stays off rather than paying for results that cannot be kept")
	}
	if err := cache.VoyageCacheWritable(ctx); err != nil {
		return VoyageConfig{}, false, fmt.Errorf(
			"the store cannot be written, so Voyage results could not be kept: %w", err)
	}

	cfg = VoyageConfig{
		APIKey:          key,
		BaseURL:         config.Coalesce(os.Getenv(VoyageBaseURLEnv), fileCfg.BaseURL),
		EmbedModel:      config.Coalesce(os.Getenv(VoyageEmbedModelEnv), fileCfg.EmbedModel),
		RerankModel:     config.Coalesce(os.Getenv(VoyageRerankModelEnv), fileCfg.RerankModel),
		OutputDimension: dimension,
		Cache:           cache,
	}
	// Validate now, while we can still report which setting is wrong, rather
	// than at the first API call.
	if cfg, err = cfg.withDefaults(); err != nil {
		return VoyageConfig{}, false, err
	}
	return cfg, true, nil
}

// VoyageKeyConfigured reports whether a Voyage API key resolves at all, without
// needing a store. It exists so a command can reject a flag that names Voyage
// before doing work that a missing key makes pointless — the full resolve
// additionally requires a writable store, which not every code path has reached
// yet at the point the flag must be validated.
func VoyageKeyConfigured(fileCfg config.VoyageConfig, disable bool) (bool, error) {
	key, err := voyageKey(fileCfg, disable)
	return key != "", err
}

// voyageKey resolves the API key from environment then config file, or returns
// "" when Voyage is not configured or is switched off.
func voyageKey(fileCfg config.VoyageConfig, disable bool) (string, error) {
	if disable || fileCfg.Disabled || os.Getenv(VoyageDisableEnv) == "1" {
		return "", nil
	}
	fileKey, err := config.ReadCredentialFile(fileCfg.APIKeyFile, "embedder.voyage.apiKeyFile")
	if err != nil {
		return "", err
	}
	return config.Coalesce(
		strings.TrimSpace(os.Getenv(VoyageAPIKeyEnv)),
		strings.TrimSpace(os.Getenv(VoyageAPIKeyEnvAlt)),
		fileKey,
	), nil
}
