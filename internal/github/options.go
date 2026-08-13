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
	var fileRPM string
	filePresent := false
	if cfg != nil {
		opts.Tokens = append([]string(nil), cfg.GitHub.Tokens...)
		if cfg.GitHub.RequestsPerMinute != 0 {
			fileRPM = strconv.FormatFloat(cfg.GitHub.RequestsPerMinute, 'f', -1, 64)
			filePresent = true
		}
		proxy := cfg.GitHub.Proxy
		opts.Proxy.Enabled = proxy.Enabled
		opts.Proxy.APIKeyFile = proxy.APIKeyFile
		opts.Proxy.StaticFile = proxy.StaticFile
		// Default off: opting into proxy routing is not opting into publishing
		// your public IP to api.ipify.org and registering it against a
		// ProxyScrape account. The field is *bool precisely so unset is
		// distinguishable from an explicit false.
		opts.Proxy.WhitelistPublicIP = false
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
	flagRPM := ""
	if rpmOverride != 0 {
		flagRPM = strconv.FormatFloat(rpmOverride, 'f', -1, 64)
	}
	resolved := config.ResolveString(fileRPM, filePresent, strconv.FormatFloat(defaultRPM, 'f', -1, 64), flagRPM, map[string]string{"SPOON_GITHUB_RPM": os.Getenv("SPOON_GITHUB_RPM")}, "SPOON_GITHUB_RPM")
	opts.RequestsPerMinute, err = strconv.ParseFloat(resolved.Value, 64)
	if err != nil {
		return ClientOptions{}, fmt.Errorf("SPOON_GITHUB_RPM: %w", err)
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
