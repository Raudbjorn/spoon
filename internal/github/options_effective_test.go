package github

import (
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestResolveClientOptionsFromEffectiveUsesResolvedLeaves(t *testing.T) {
	whitelist := true
	file := &config.Config{GitHub: config.GitHubConfig{
		Tokens: []string{"file-token"}, RequestsPerMinute: 17,
		Proxy: config.ProxyConfig{Enabled: true, APIKeyFile: "api", StaticFile: "static", WhitelistPublicIP: &whitelist, CacheTTL: "2m"},
	}}
	for _, key := range []string{"github.tokens", "github.requestsPerMinute", "github.proxy.enabled", "github.proxy.apiKeyFile", "github.proxy.staticFile", "github.proxy.whitelistPublicIp", "github.proxy.cacheTtl"} {
		config.RecordFieldValue(file, key, "present")
	}
	effective := config.ResolveEffectiveConfig(file, nil, map[string]string{"SPOON_GITHUB_RPM": "23"})
	opts, err := ResolveClientOptionsFromEffective(effective)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Tokens) != 1 || opts.Tokens[0] != "file-token" || opts.RequestsPerMinute != 23 || !opts.Proxy.Enabled || !opts.Proxy.WhitelistPublicIP || opts.Proxy.CacheTTL != 2*time.Minute {
		t.Fatalf("options = %#v", opts)
	}
}
