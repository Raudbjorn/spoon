package embed

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
)

const (
	VoyageAPIKeyEnv      = "VOYAGE_AI_API_KEY"
	VoyageAPIKeyEnvAlt   = "VOYAGE_API_KEY"
	VoyageBaseURLEnv     = "SPOON_VOYAGE_BASE_URL"
	VoyageEmbedModelEnv  = "SPOON_VOYAGE_EMBED_MODEL"
	VoyageRerankModelEnv = "SPOON_VOYAGE_RERANK_MODEL"
	VoyageDimensionEnv   = "SPOON_VOYAGE_DIM"
	VoyageDisableEnv     = "SPOON_NO_VOYAGE"
	VoyageNoCacheEnv     = "SPOON_VOYAGE_NO_CACHE"
)

// ResolveVoyageEffective resolves the paid provider from the command's one
// effective configuration result and injected environment snapshot. It never
// consults process state or reloads configuration.
func ResolveVoyageEffective(ctx context.Context, effective config.EffectiveConfig, disable bool, cache ResponseCache, env map[string]string) (cfg VoyageConfig, active bool, err error) {
	key, err := voyageKeyEffective(effective, disable, env)
	if err != nil || key == "" {
		return VoyageConfig{}, false, err
	}
	dimension, err := strconv.Atoi(effective.Voyage.OutputDimension.Value)
	if err != nil {
		return VoyageConfig{}, false, fmt.Errorf("%s=%q is not an integer", VoyageDimensionEnv, effective.Voyage.OutputDimension.Value)
	}
	if cache == nil {
		return VoyageConfig{}, false, errors.New("no durable store is available for the response cache and embeddings; Voyage stays off rather than paying for results that cannot be kept")
	}
	if err := cache.VoyageCacheWritable(ctx); err != nil {
		return VoyageConfig{}, false, fmt.Errorf("the store cannot be written, so Voyage results could not be kept: %w", err)
	}
	cfg = VoyageConfig{
		APIKey: key, BaseURL: effective.Voyage.BaseURL.Value, EmbedModel: effective.Voyage.EmbedModel.Value,
		RerankModel: effective.Voyage.RerankModel.Value, OutputDimension: dimension, Cache: cache,
	}
	if cfg, err = cfg.withDefaults(); err != nil {
		return VoyageConfig{}, false, err
	}
	return cfg, true, nil
}

// VoyageKeyConfiguredEffective checks only key availability using the shared
// runtime result; it deliberately does not contact Voyage or open a store.
func VoyageKeyConfiguredEffective(effective config.EffectiveConfig, disable bool, env map[string]string) (bool, error) {
	key, err := voyageKeyEffective(effective, disable, env)
	return key != "", err
}

func voyageKeyEffective(effective config.EffectiveConfig, disable bool, env map[string]string) (string, error) {
	disabled, err := strconv.ParseBool(effective.Voyage.Disabled.Value)
	if err != nil {
		return "", fmt.Errorf("embedder.voyage.disabled: %w", err)
	}
	if disable || disabled {
		return "", nil
	}
	fileKey, err := config.ReadCredentialFile(effective.Voyage.APIKeyFile.Value, "embedder.voyage.apiKeyFile")
	if err != nil {
		return "", err
	}
	return config.Coalesce(strings.TrimSpace(env[VoyageAPIKeyEnv]), strings.TrimSpace(env[VoyageAPIKeyEnvAlt]), fileKey), nil
}

// ResolveVoyageConfig is retained for existing package callers. New command
// startup must resolve once and call ResolveVoyageEffective.
func ResolveVoyageConfig(ctx context.Context, fileCfg config.VoyageConfig, disable bool, cache ResponseCache) (cfg VoyageConfig, active bool, err error) {
	effective := config.ResolveEffectiveConfig(&config.Config{Embedder: config.EmbedderConfig{Voyage: fileCfg}}, nil, config.EnvironmentSnapshot())
	return ResolveVoyageEffective(ctx, effective, disable, cache, config.EnvironmentSnapshot())
}

// VoyageKeyConfigured is retained for existing package callers. New command
// startup must use VoyageKeyConfiguredEffective.
func VoyageKeyConfigured(fileCfg config.VoyageConfig, disable bool) (bool, error) {
	effective := config.ResolveEffectiveConfig(&config.Config{Embedder: config.EmbedderConfig{Voyage: fileCfg}}, nil, config.EnvironmentSnapshot())
	return VoyageKeyConfiguredEffective(effective, disable, config.EnvironmentSnapshot())
}
