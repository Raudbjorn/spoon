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
	// Resolve the default host used for scheme-less shorthand ("owner/repo").
	// When the provider is forced but no host is given, pick that provider's
	// canonical public host — otherwise `--forge gitea owner/repo` would
	// default to github.com and point the Gitea client at GitHub's API.
	defaultHost := cfg.ForgeHost
	if defaultHost == "" {
		switch cfg.ForceProvider {
		case ProviderGitea:
			defaultHost = "codeberg.org"
		case ProviderGitLab:
			defaultHost = "gitlab.com"
		default:
			defaultHost = "github.com"
		}
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
