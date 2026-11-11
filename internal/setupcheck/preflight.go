package setupcheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"gopkg.in/yaml.v3"
)

// DenyHTTPTransport is the fail-closed transport passed to local preflights.
// Setup checks inspect configured local credentials; they must not turn a
// settings action into a provider request.
type DenyHTTPTransport struct{}

func (DenyHTTPTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("HTTP is disabled for local setup checks")
}

// ProviderInput names the active forge and locally loaded credential state.
type ProviderInput struct {
	Provider        forge.Provider
	Host            string
	ConfiguredToken bool
	Environment     map[string]string
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
	return ProviderResult{Provider: input.Provider, Auth: auth, Ready: input.ConfiguredToken || auth.Configured || auth.Authenticated()}, nil
}

// LocalProviderProbe checks spoon's loaded credentials and the startup
// environment snapshot, plus local gh/glab account configuration. The probe
// never invokes a CLI or constructs a network client.
func LocalProviderProbe(ctx context.Context, input ProviderInput, transport http.RoundTripper) (forge.AuthInfo, error) {
	host := input.Host
	if host == "" {
		if input.Provider == forge.ProviderGitLab {
			host = "gitlab.com"
		} else {
			host = strings.TrimSpace(environmentValueForOS(input.Environment, "GH_HOST", runtime.GOOS))
			if host == "" {
				host = "github.com"
			}
		}
	}
	auth := forge.AuthInfo{Provider: input.Provider, Host: host}
	if err := ctx.Err(); err != nil {
		return auth, err
	}
	keys, publicRate, unit := []string{"GH_TOKEN", "GITHUB_TOKEN"}, 60, "hour"
	if input.Provider == forge.ProviderGitLab {
		keys = []string{"GITLAB_TOKEN", "GITLAB_PAT", "CI_JOB_TOKEN"}
		publicRate, unit = 500, "minute"
	} else if ghauth.IsEnterprise(host) {
		keys = []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
	}
	auth.RateUnit, auth.RateLimit = unit, publicRate
	if input.ConfiguredToken {
		auth.Configured = true
		auth.TokenSource = "config"
		return auth, nil
	}
	for _, key := range keys {
		if strings.TrimSpace(environmentValueForOS(input.Environment, key, runtime.GOOS)) != "" {
			auth.Configured = true
			auth.TokenSource = key
			return auth, nil
		}
	}
	if transport == nil {
		return auth, fmt.Errorf("provider network guard is unavailable")
	}
	if input.Provider == forge.ProviderGitLab {
		auth.Configured = localGitLabTokenConfigured(host, input.Environment)
	} else if localGitHubLoginConfigured(host, input.Environment) {
		auth.Configured = true
		auth.TokenSource = "gh"
	}
	if err := ctx.Err(); err != nil {
		return auth, err
	}
	return auth, nil
}

const maxProviderConfigBytes = 1 << 20

func localGitLabTokenConfigured(host string, env map[string]string) bool {
	path := gitLabConfigPathForOS(env, runtime.GOOS)
	var cfg struct {
		Hosts map[string]struct {
			Token string `yaml:"token"`
		} `yaml:"hosts"`
	}
	return readProviderConfig(path, &cfg) && strings.TrimSpace(cfg.Hosts[host].Token) != ""
}

func localGitHubLoginConfigured(host string, env map[string]string) bool {
	path := cliConfigPathForOS(env, runtime.GOOS, "GH_CONFIG_DIR", "gh", "hosts.yml")
	var hosts map[string]struct {
		User  string `yaml:"user"`
		Token string `yaml:"oauth_token"`
	}
	if !readProviderConfig(path, &hosts) {
		return false
	}
	account := hosts[ghauth.NormalizeHostname(host)]
	// A named active account may keep its token exclusively in the keyring.
	// This local probe reports configuration, never successful authentication.
	return strings.TrimSpace(account.User) != "" || strings.TrimSpace(account.Token) != ""
}

func readProviderConfig(path string, target any) bool {
	if path == "" {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxProviderConfigBytes+1))
	if err != nil || len(data) > maxProviderConfigBytes {
		return false
	}
	return yaml.Unmarshal(data, target) == nil
}

func gitLabConfigPathForOS(env map[string]string, goos string) string {
	return cliConfigPathForOS(env, goos, "GLAB_CONFIG_DIR", "glab-cli", "config.yml")
}

func cliConfigPathForOS(env map[string]string, goos, override, dir, file string) string {
	if root := environmentValueForOS(env, override, goos); root != "" {
		return filepath.Join(root, file)
	}
	if root := environmentValueForOS(env, "XDG_CONFIG_HOME", goos); root != "" {
		return filepath.Join(root, dir, file)
	}
	if goos == "windows" {
		if root := environmentValueForOS(env, "AppData", goos); root != "" {
			return filepath.Join(root, dir, file)
		}
	}
	if home := environmentValueForOS(env, "HOME", goos); home != "" {
		return filepath.Join(home, ".config", dir, file)
	}
	return ""
}

func environmentValueForOS(env map[string]string, name, goos string) string {
	if goos != "windows" {
		return env[name]
	}
	for key, value := range env {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
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
