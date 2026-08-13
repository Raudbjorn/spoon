package github

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/config"
)

// ResolveClientOptionsFromEffective builds client options from the command's
// startup-owned effective configuration. It never reads configuration or the
// process environment itself.
func ResolveClientOptionsFromEffective(effective config.EffectiveConfig) (ClientOptions, error) {
	opts := ClientOptions{RequestsPerMinute: defaultRPM}
	for _, token := range strings.Fields(effective.GitHub.Tokens.Value) {
		opts.Tokens = append(opts.Tokens, token)
	}
	var err error
	opts.RequestsPerMinute, err = strconv.ParseFloat(effective.GitHub.RequestsPerMinute.Value, 64)
	if err != nil {
		return ClientOptions{}, fmt.Errorf("github requests per minute: %w", err)
	}
	if math.IsNaN(opts.RequestsPerMinute) || math.IsInf(opts.RequestsPerMinute, 0) || opts.RequestsPerMinute <= 0 || opts.RequestsPerMinute > 900 {
		return ClientOptions{}, fmt.Errorf("github requests per minute must be a finite value in (0, 900]")
	}
	opts.Proxy.Enabled, err = strconv.ParseBool(effective.GitHub.Proxy.Enabled.Value)
	if err != nil {
		return ClientOptions{}, fmt.Errorf("github.proxy.enabled: %w", err)
	}
	opts.Proxy.APIKeyFile = effective.GitHub.Proxy.APIKeyFile.Value
	opts.Proxy.StaticFile = effective.GitHub.Proxy.StaticFile.Value
	opts.Proxy.WhitelistPublicIP, err = strconv.ParseBool(effective.GitHub.Proxy.WhitelistPublicIP.Value)
	if err != nil {
		return ClientOptions{}, fmt.Errorf("github.proxy.whitelistPublicIp: %w", err)
	}
	opts.Proxy.CacheTTL, err = time.ParseDuration(effective.GitHub.Proxy.CacheTTL.Value)
	if err != nil {
		return ClientOptions{}, fmt.Errorf("github.proxy.cacheTtl: %w", err)
	}
	return opts, nil
}

// CheckAuthWithEffective is the runtime constructor path. Its input is the
// already-resolved startup result, so it cannot select a different config layer.
func CheckAuthWithEffective(effective config.EffectiveConfig) (*Client, AuthStatus, error) {
	opts, err := ResolveClientOptionsFromEffective(effective)
	if err != nil {
		return nil, AuthStatus{}, err
	}
	return CheckAuthWithOptions(opts)
}

// ResolveClientOptions remains for compatibility with callers that explicitly
// want environment/default-only options. Commands must use
// ResolveClientOptionsFromEffective with their Bootstrap snapshot.
func ResolveClientOptions(rpmOverride float64) (ClientOptions, error) {
	flags := map[string]string{}
	if rpmOverride != 0 {
		flags["github.requestsPerMinute"] = strconv.FormatFloat(rpmOverride, 'f', -1, 64)
	}
	return ResolveClientOptionsFromEffective(config.ResolveEffectiveConfig(nil, flags, config.EnvironmentSnapshot()))
}

// CheckAuthConfigured remains for compatibility. Command startup must use
// CheckAuthWithEffective instead.
func CheckAuthConfigured(rpmOverride float64) (*Client, AuthStatus, error) {
	opts, err := ResolveClientOptions(rpmOverride)
	if err != nil {
		return nil, AuthStatus{}, err
	}
	return CheckAuthWithOptions(opts)
}
