package setupcheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
)

// DenyHTTPTransport is the fail-closed transport passed to local preflights.
// Setup checks authenticate from configured local credentials or CLI state; they
// must not turn a settings action into a provider request.
type DenyHTTPTransport struct{}

func (DenyHTTPTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("HTTP is disabled for local setup checks")
}

// ProviderInput names the active forge and any credential stored in the loaded
// configuration. Environment and CLI credentials are reported by ProviderProbe.
type ProviderInput struct {
	Provider        forge.Provider
	Host            string
	ConfiguredToken bool
}

// ProviderProbe observes local provider authentication. The supplied transport
// is always fail-closed and must be used by probes that construct HTTP clients.
type ProviderProbe func(context.Context, ProviderInput, http.RoundTripper) (forge.AuthInfo, error)

type ProviderResult struct {
	Provider forge.Provider
	Auth     forge.AuthInfo
	Ready    bool
}

func CheckProvider(ctx context.Context, input ProviderInput, probe ProviderProbe, transport http.RoundTripper) (ProviderResult, error) {
	if input.Provider != forge.ProviderGitHub && input.Provider != forge.ProviderGitLab {
		return ProviderResult{}, fmt.Errorf("unsupported provider %q", input.Provider.String())
	}
	if probe == nil {
		return ProviderResult{}, fmt.Errorf("provider credential probe is unavailable")
	}
	if transport == nil {
		transport = DenyHTTPTransport{}
	}
	auth, err := probe(ctx, input, transport)
	if err != nil {
		return ProviderResult{Provider: input.Provider}, err
	}
	return ProviderResult{Provider: input.Provider, Auth: auth, Ready: input.ConfiguredToken || auth.Authenticated()}, nil
}

// LocalProviderProbe checks only configured environment/config credentials and
// the gh/glab CLI's local auth state. It never validates a token over HTTP.
func LocalProviderProbe(ctx context.Context, input ProviderInput, _ http.RoundTripper) (forge.AuthInfo, error) {
	host := input.Host
	if host == "" {
		if input.Provider == forge.ProviderGitLab {
			host = "gitlab.com"
		} else {
			host = "github.com"
		}
	}
	auth := forge.AuthInfo{Provider: input.Provider, Host: host}
	command, args := "gh", []string{"auth", "status", "--hostname", host}
	envToken, tokenRate, publicRate, unit := os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "", 5000, 60, "hour"
	if input.Provider == forge.ProviderGitLab {
		command, args = "glab", []string{"auth", "status", "--hostname", host}
		envToken = os.Getenv("GITLAB_TOKEN") != "" || os.Getenv("GITLAB_PAT") != "" || os.Getenv("CI_JOB_TOKEN") != ""
		tokenRate, publicRate, unit = 2000, 500, "minute"
	}
	auth.RateUnit = unit
	if input.ConfiguredToken || envToken {
		auth.Tier, auth.RateLimit = forge.AuthToken, tokenRate
		return auth, nil
	}
	if err := exec.CommandContext(ctx, command, args...).Run(); err == nil {
		auth.Tier, auth.RateLimit = forge.AuthCLI, tokenRate
		return auth, nil
	}
	auth.Tier, auth.RateLimit = forge.AuthNone, publicRate
	return auth, nil
}

// Store is the shared open-and-writability contract for setup and settings.
type Store interface {
	io.Closer
	VoyageCacheWritable(context.Context) error
}

type StoreOpen func() (Store, error)

func CheckStore(ctx context.Context, open StoreOpen) error {
	if open == nil {
		return fmt.Errorf("store opener is unavailable")
	}
	store, err := open()
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.VoyageCacheWritable(ctx); err != nil {
		return fmt.Errorf("store/cache is not writable: %w", err)
	}
	return nil
}

// EffectiveFastEmbed mirrors runtime defaults without opening or downloading a
// model. NewFastEmbedEmbedder treats an empty model/backend and non-positive
// batch size as these same defaults.
func EffectiveFastEmbed(cfg config.EmbedderConfig) config.EmbedderConfig {
	if cfg.Backend == "" {
		cfg.Backend = embed.BackendFastEmbed
	}
	if cfg.Model == "" {
		cfg.Model = DefaultFastEmbedModel
	}
	if cfg.MaxLength == 0 {
		cfg.MaxLength = DefaultFastEmbedMaxLength
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultFastEmbedBatchSize
	}
	return cfg
}

func CheckFastEmbed(cfg config.EmbedderConfig) (config.EmbedderConfig, error) {
	cfg = EffectiveFastEmbed(cfg)
	if err := ValidateFastEmbed(cfg); err != nil {
		return config.EmbedderConfig{}, err
	}
	return cfg, nil
}

// ActiveProvider selects the configured provider without treating a host as an
// implicit GitLab selection. A host belongs to the selected provider.
func ActiveProvider(cfg *config.Config) (forge.Provider, string, bool) {
	if cfg == nil {
		return forge.ProviderGitHub, "", false
	}
	switch strings.ToLower(cfg.Forge.Provider) {
	case "", "github":
		return forge.ProviderGitHub, cfg.Forge.Host, len(cfg.GitHub.Tokens) > 0
	case "gitlab":
		return forge.ProviderGitLab, cfg.Forge.Host, false
	default:
		return forge.ProviderGitHub, cfg.Forge.Host, false
	}
}
