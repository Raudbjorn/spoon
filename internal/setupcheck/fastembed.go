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
	if prepared.Model == "" {
		prepared.Model = DefaultFastEmbedModel
	} else if !embed.KnownFastEmbedModel(prepared.Model) {
		return embed.FastEmbedConfig{}, fmt.Errorf("FastEmbed model %q is not supported", prepared.Model)
	}
	profile, _ := embed.LookupFastEmbedProfile(prepared.Model)
	prepared.CacheDir = cacheDir
	if prepared.CacheDir == "" {
		var err error
		prepared.CacheDir, err = embed.DefaultFastEmbedCacheDir()
		if err != nil {
			return embed.FastEmbedConfig{}, err
		}
	}
	if prepared.MaxLength == 0 {
		prepared.MaxLength = profile.MaxLength
	}
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
	if cfg.Model != "" && !embed.KnownFastEmbedModel(cfg.Model) {
		return fmt.Errorf("FastEmbed model %q is not supported", cfg.Model)
	}
	profile, _ := embed.LookupFastEmbedProfile(cfg.Model)
	if cfg.MaxLength != 0 && cfg.MaxLength != profile.MaxLength {
		return fmt.Errorf("FastEmbed max length for %s is %d", profile.Name, profile.MaxLength)
	}
	if cfg.BatchSize < 0 {
		return fmt.Errorf("FastEmbed batch size must not be negative")
	}
	return nil
}
