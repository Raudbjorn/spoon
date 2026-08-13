// Package setupcheck contains local, reusable setup preflight helpers.
package setupcheck

import (
	"fmt"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
)

const (
	DefaultFastEmbedModel     = "fast-bge-small-en-v1.5"
	DefaultFastEmbedMaxLength = 512
	DefaultFastEmbedBatchSize = 32
)

// PrepareFastEmbed applies Spoon setup's durable FastEmbed defaults and returns
// the exact preflight configuration. It never opens or downloads a model; the
// CLI setup command owns the opt-in runtime probe after this shared step.
func PrepareFastEmbed(cfg *config.Config, cacheDir string) (embed.FastEmbedConfig, error) {
	prepared := cfg.Embedder
	prepared.Backend = embed.BackendFastEmbed
	prepared.Model = DefaultFastEmbedModel
	prepared.CacheDir = cacheDir
	if prepared.CacheDir == "" {
		var err error
		prepared.CacheDir, err = embed.DefaultFastEmbedCacheDir()
		if err != nil {
			return embed.FastEmbedConfig{}, err
		}
	}
	prepared.MaxLength = DefaultFastEmbedMaxLength
	prepared.BatchSize = DefaultFastEmbedBatchSize
	cfg.Embedder = prepared
	return embed.FastEmbedConfig{
		Model: prepared.Model, CacheDir: prepared.CacheDir,
		MaxLength: prepared.MaxLength, BatchSize: prepared.BatchSize,
	}, nil
}

// ValidateFastEmbed checks an existing configuration without replacing user
// values or opening/downloading a model.
func ValidateFastEmbed(cfg config.EmbedderConfig) error {
	if cfg.Backend != "" && cfg.Backend != embed.BackendFastEmbed {
		return fmt.Errorf("unsupported FastEmbed backend %q", cfg.Backend)
	}
	if cfg.Model != "" && cfg.Model != DefaultFastEmbedModel {
		return fmt.Errorf("FastEmbed model is fixed at %q", DefaultFastEmbedModel)
	}
	if cfg.MaxLength != 0 && cfg.MaxLength != DefaultFastEmbedMaxLength {
		return fmt.Errorf("FastEmbed max length is fixed at %d", DefaultFastEmbedMaxLength)
	}
	if cfg.BatchSize < 0 {
		return fmt.Errorf("FastEmbed batch size must not be negative")
	}
	return nil
}
