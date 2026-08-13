// Package setupcheck contains local, reusable setup preflight helpers.
package setupcheck

import (
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
	cfg.Embedder.Backend = embed.BackendFastEmbed
	cfg.Embedder.Model = DefaultFastEmbedModel
	cfg.Embedder.CacheDir = cacheDir
	if cfg.Embedder.CacheDir == "" {
		var err error
		cfg.Embedder.CacheDir, err = embed.DefaultFastEmbedCacheDir()
		if err != nil {
			return embed.FastEmbedConfig{}, err
		}
	}
	cfg.Embedder.MaxLength = DefaultFastEmbedMaxLength
	cfg.Embedder.BatchSize = DefaultFastEmbedBatchSize
	return embed.FastEmbedConfig{Model: cfg.Embedder.Model, CacheDir: cfg.Embedder.CacheDir, MaxLength: cfg.Embedder.MaxLength, BatchSize: cfg.Embedder.BatchSize}, nil
}
