// Package settings owns the in-TUI representation of persisted Spoon settings.
// The registry is deliberately declarative: rendering, validation, consequence
// confirmation and coverage tests all consume this one table.
package settings

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
)

type Section string

const (
	ForgeSection       Section = "Forge"
	GitHubSection      Section = "GitHub"
	ProxySection       Section = "Proxy"
	EmbedderSection    Section = "Embedder"
	VoyageSection      Section = "Voyage"
	AppearanceSection  Section = "Appearance"
	EnvironmentSection Section = "Environment"
	HostSection        Section = "Host"
)

type Consequence string

const (
	NoConsequence Consequence = ""
	Billing       Consequence = "billing"
	Reindex       Consequence = "reindex"
	Hostwide      Consequence = "hostwide"
)

type Field struct {
	Key, Label, Help string
	Section          Section
	Secret           bool
	Credential       bool
	Editable         bool
	Consequence      Consequence
	Environment      []string
	Get              func(*config.Config) string
	Set              func(*config.Config, string) error
	Default          string
}

func stringField(key, label string, section Section, help, def string, get func(*config.Config) string, set func(*config.Config, string)) Field {
	return Field{Key: key, Label: label, Section: section, Help: help, Editable: true, Default: def, Get: get, Set: func(c *config.Config, value string) error { set(c, value); return nil }}
}

func boolField(key, label string, section Section, help string, consequence Consequence, get func(*config.Config) bool, set func(*config.Config, bool)) Field {
	return Field{Key: key, Label: label, Section: section, Help: help, Editable: true, Consequence: consequence, Default: "false", Get: func(c *config.Config) string { return strconv.FormatBool(get(c)) }, Set: func(c *config.Config, value string) error {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		set(c, b)
		return nil
	}}
}

// Registry is the sole inventory of persisted config settings exposed by the TUI.
var Registry = []Field{
	{Key: "version", Label: "Schema version", Section: HostSection, Help: "Written by Spoon; version 2 introduces UI preferences.", Get: func(c *config.Config) string { return strconv.Itoa(c.Version) }},
	stringField("forge.provider", "Provider", ForgeSection, "github, gitlab, or empty to auto-detect", "", func(c *config.Config) string { return c.Forge.Provider }, func(c *config.Config, v string) { c.Forge.Provider = strings.ToLower(strings.TrimSpace(v)) }),
	stringField("forge.host", "Host", ForgeSection, "Self-hosted GitHub or GitLab hostname.", "", func(c *config.Config) string { return c.Forge.Host }, func(c *config.Config, v string) { c.Forge.Host = strings.TrimSpace(v) }),
	{Key: "github.tokens", Label: "GitHub tokens", Section: GitHubSection, Help: "One token per line; values are always masked.", Secret: true, Credential: true, Editable: true, Get: func(c *config.Config) string { return strings.Join(c.GitHub.Tokens, "\n") }, Set: func(c *config.Config, v string) error {
		values := strings.Split(v, "\n")
		c.GitHub.Tokens = nonEmpty(values)
		return nil
	}},
	{Key: "github.requestsPerMinute", Label: "Requests per minute", Section: GitHubSection, Help: "Client-side pacing cap (maximum 900).", Editable: true, Default: "300", Environment: []string{"SPOON_GITHUB_RPM"}, Get: func(c *config.Config) string { return strconv.FormatFloat(c.GitHub.RequestsPerMinute, 'f', -1, 64) }, Set: func(c *config.Config, v string) error {
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return fmt.Errorf("github.requestsPerMinute: %w", err)
		}
		c.GitHub.RequestsPerMinute = n
		return nil
	}},
	boolField("github.proxy.enabled", "Proxy enabled", ProxySection, "Enable ProxyScrape transport routing.", NoConsequence, func(c *config.Config) bool { return c.GitHub.Proxy.Enabled }, func(c *config.Config, v bool) { c.GitHub.Proxy.Enabled = v }),
	{Key: "github.proxy.apiKeyFile", Label: "Proxy API key file", Section: ProxySection, Help: "0600 credential file path.", Credential: true, Editable: true, Get: func(c *config.Config) string { return c.GitHub.Proxy.APIKeyFile }, Set: func(c *config.Config, v string) error { c.GitHub.Proxy.APIKeyFile = strings.TrimSpace(v); return nil }},
	stringField("github.proxy.staticFile", "Proxy static file", ProxySection, "0600 credential file path.", "", func(c *config.Config) string { return c.GitHub.Proxy.StaticFile }, func(c *config.Config, v string) { c.GitHub.Proxy.StaticFile = strings.TrimSpace(v) }),
	{Key: "github.proxy.whitelistPublicIp", Label: "Whitelist public IP", Section: ProxySection, Help: "true, false, or empty to leave unset.", Editable: true, Get: func(c *config.Config) string {
		if c.GitHub.Proxy.WhitelistPublicIP == nil {
			return ""
		}
		return strconv.FormatBool(*c.GitHub.Proxy.WhitelistPublicIP)
	}, Set: func(c *config.Config, v string) error {
		if strings.TrimSpace(v) == "" {
			c.GitHub.Proxy.WhitelistPublicIP = nil
			return nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return err
		}
		c.GitHub.Proxy.WhitelistPublicIP = &b
		return nil
	}},
	stringField("github.proxy.cacheTtl", "Proxy cache TTL", ProxySection, "Go duration, such as 5m.", "", func(c *config.Config) string { return c.GitHub.Proxy.CacheTTL }, func(c *config.Config, v string) { c.GitHub.Proxy.CacheTTL = strings.TrimSpace(v) }),
	stringField("embedder.backend", "Backend", EmbedderSection, "fastembed or empty.", "fastembed", func(c *config.Config) string { return c.Embedder.Backend }, func(c *config.Config, v string) { c.Embedder.Backend = strings.ToLower(strings.TrimSpace(v)) }),
	{Key: "embedder.model", Label: "Model", Section: EmbedderSection, Help: "FastEmbed model name.", Editable: true, Environment: []string{"SPOON_FASTEMBED_MODEL"}, Get: func(c *config.Config) string { return c.Embedder.Model }, Set: func(c *config.Config, v string) error { c.Embedder.Model = strings.TrimSpace(v); return nil }},
	{Key: "embedder.cacheDir", Label: "Cache directory", Section: EmbedderSection, Help: "FastEmbed cache directory.", Editable: true, Environment: []string{"SPOON_FASTEMBED_CACHE"}, Get: func(c *config.Config) string { return c.Embedder.CacheDir }, Set: func(c *config.Config, v string) error { c.Embedder.CacheDir = strings.TrimSpace(v); return nil }},
	{Key: "embedder.maxLength", Label: "Maximum length", Section: EmbedderSection, Help: "FastEmbed input limit.", Editable: true, Get: func(c *config.Config) string { return strconv.Itoa(c.Embedder.MaxLength) }, Set: func(c *config.Config, v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		c.Embedder.MaxLength = n
		return nil
	}},
	{Key: "embedder.batchSize", Label: "Batch size", Section: EmbedderSection, Help: "FastEmbed batch size.", Editable: true, Get: func(c *config.Config) string { return strconv.Itoa(c.Embedder.BatchSize) }, Set: func(c *config.Config, v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		c.Embedder.BatchSize = n
		return nil
	}},
	boolField("embedder.voyage.disabled", "Voyage disabled", VoyageSection, "Clearing this can enable an external per-token billed service.", Billing, func(c *config.Config) bool { return c.Embedder.Voyage.Disabled }, func(c *config.Config, v bool) { c.Embedder.Voyage.Disabled = v }),
	{Key: "embedder.voyage.apiKeyFile", Label: "Voyage API key file", Section: VoyageSection, Help: "0600 credential file path; the key itself is not shown.", Credential: true, Editable: true, Consequence: Billing, Get: func(c *config.Config) string { return c.Embedder.Voyage.APIKeyFile }, Set: func(c *config.Config, v string) error {
		c.Embedder.Voyage.APIKeyFile = strings.TrimSpace(v)
		return nil
	}},
	stringField("embedder.voyage.embedModel", "Embed model", VoyageSection, "Defaults to voyage-code-3.", "voyage-code-3", func(c *config.Config) string { return c.Embedder.Voyage.EmbedModel }, func(c *config.Config, v string) { c.Embedder.Voyage.EmbedModel = strings.TrimSpace(v) }),
	stringField("embedder.voyage.rerankModel", "Rerank model", VoyageSection, "Defaults to rerank-2.5.", "rerank-2.5", func(c *config.Config) string { return c.Embedder.Voyage.RerankModel }, func(c *config.Config, v string) { c.Embedder.Voyage.RerankModel = strings.TrimSpace(v) }),
	{Key: "embedder.voyage.outputDimension", Label: "Output dimension", Section: VoyageSection, Help: "Changing this re-partitions the index; pending documents must be re-embedded.", Editable: true, Consequence: Reindex, Default: "1024", Environment: []string{"SPOON_VOYAGE_DIM"}, Get: func(c *config.Config) string { return strconv.Itoa(c.Embedder.Voyage.OutputDimension) }, Set: func(c *config.Config, v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		c.Embedder.Voyage.OutputDimension = n
		return nil
	}},
	stringField("embedder.voyage.baseUrl", "Base URL", VoyageSection, "Optional Voyage-compatible gateway URL.", "", func(c *config.Config) string { return c.Embedder.Voyage.BaseURL }, func(c *config.Config, v string) { c.Embedder.Voyage.BaseURL = strings.TrimSpace(v) }),
	stringField("ui.theme", "Theme", AppearanceSection, "dark, light, or amber.", "dark", func(c *config.Config) string { return c.UI.Theme }, func(c *config.Config, v string) { c.UI.Theme = strings.ToLower(strings.TrimSpace(v)) }),
	stringField("ui.color", "Color profile", AppearanceSection, "truecolor, ansi256, ansi16, ansi8, mono, or no-color.", "", func(c *config.Config) string { return c.UI.Color }, func(c *config.Config, v string) { c.UI.Color = strings.ToLower(strings.TrimSpace(v)) }),
	stringField("ui.glyphs", "Glyph profile", AppearanceSection, "unicode or ascii.", "", func(c *config.Config) string { return c.UI.Glyphs }, func(c *config.Config, v string) { c.UI.Glyphs = strings.ToLower(strings.TrimSpace(v)) }),
}

// DocumentedEnvironment is derived from configReadme, the source emitted next
// to every config layer, so Settings cannot silently omit a documented knob.
var DocumentedEnvironment = config.DocumentedEnvironment()

// Environment makes every documented variable visible while marking secret
// values set/unset instead of exposing their contents.
var Environment = func() map[string]bool {
	secret := map[string]bool{"SPOON_GH_COOKIE": true, "TURSO_AUTH_TOKEN": true, "GH_TOKEN": true, "GITHUB_TOKEN": true, "GITLAB_TOKEN": true, "VOYAGE_AI_API_KEY": true, "VOYAGE_API_KEY": true}
	out := make(map[string]bool, len(DocumentedEnvironment))
	for _, name := range DocumentedEnvironment {
		out[name] = secret[name]
	}
	return out
}()

func FieldByKey(key string) (Field, bool) {
	for _, f := range Registry {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}
