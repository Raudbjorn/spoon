package forge

import (
	"context"
	"fmt"
)

// Config carries the inputs from CLI flag parsing needed to build a Forge.
type Config struct {
	RepoURL       string
	ForceProvider Provider
	ForgeHost     string
}

// ParsedRepo is the output of URL parsing.
type ParsedRepo struct {
	Provider Provider
	Host     string
	Owner    string
	Repo     string
}

// Parse runs URL detection and returns a ParsedRepo.
func Parse(cfg Config) (ParsedRepo, error) {
	defaultHost := cfg.ForgeHost
	if defaultHost == "" {
		defaultHost = "github.com"
	}
	provider, host, owner, repo, err := ParseRepoURL(cfg.RepoURL, defaultHost, cfg.ForceProvider)
	if err != nil {
		return ParsedRepo{}, err
	}
	if cfg.ForgeHost != "" {
		host = cfg.ForgeHost
	}
	return ParsedRepo{
		Provider: provider,
		Host:     host,
		Owner:    owner,
		Repo:     repo,
	}, nil
}

// FactoryFn is the signature of a function that builds a forge.Forge.
// Provided for test injection.
type FactoryFn func(ctx context.Context, parsed ParsedRepo) (Forge, AuthInfo, error)

// ErrUnsupportedProvider is returned when no implementation exists for a Provider.
var ErrUnsupportedProvider = fmt.Errorf("unsupported forge provider")
