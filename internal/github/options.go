package github

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/svnbjrn/spoon/internal/config"
)

// ResolveClientOptions applies RPM precedence flag > environment > config > 300.
// Credentials are accepted only from the owner-only config file.
func ResolveClientOptions(rpmOverride float64) (ClientOptions, error) {
	cfg, err := config.LoadDefault()
	if err != nil {
		return ClientOptions{}, err
	}
	opts := ClientOptions{RequestsPerMinute: defaultRPM}
	if cfg != nil {
		opts.Tokens = append([]string(nil), cfg.GitHub.Tokens...)
		if cfg.GitHub.RequestsPerMinute != 0 {
			opts.RequestsPerMinute = cfg.GitHub.RequestsPerMinute
		}
		proxy := cfg.GitHub.Proxy
		opts.Proxy.Enabled = proxy.Enabled
		opts.Proxy.APIKeyFile = proxy.APIKeyFile
		opts.Proxy.StaticFile = proxy.StaticFile
		opts.Proxy.WhitelistPublicIP = true
		if proxy.WhitelistPublicIP != nil {
			opts.Proxy.WhitelistPublicIP = *proxy.WhitelistPublicIP
		}
		opts.Proxy.CacheTTL = time.Hour
		if proxy.CacheTTL != "" {
			opts.Proxy.CacheTTL, err = time.ParseDuration(proxy.CacheTTL)
			if err != nil {
				return ClientOptions{}, err
			}
		}
	}
	if raw := os.Getenv("SPOON_GITHUB_RPM"); raw != "" {
		opts.RequestsPerMinute, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			return ClientOptions{}, fmt.Errorf("SPOON_GITHUB_RPM: %w", err)
		}
	}
	if rpmOverride != 0 {
		opts.RequestsPerMinute = rpmOverride
	}
	if math.IsNaN(opts.RequestsPerMinute) || math.IsInf(opts.RequestsPerMinute, 0) || opts.RequestsPerMinute <= 0 || opts.RequestsPerMinute > 900 {
		return ClientOptions{}, fmt.Errorf("github requests per minute must be a finite value in (0, 900]")
	}
	return opts, nil
}

func CheckAuthConfigured(rpmOverride float64) (*Client, AuthStatus, error) {
	opts, err := ResolveClientOptions(rpmOverride)
	if err != nil {
		return nil, AuthStatus{}, err
	}
	return CheckAuthWithOptions(opts)
}
