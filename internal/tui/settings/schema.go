// Package settings owns the in-TUI representation of persisted Spoon settings.
// The registry is deliberately declarative: rendering, validation, consequence
// confirmation and coverage tests all consume this one table.
package settings

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
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
	Editable         bool
	Consequence      Consequence
	Environment      []string
	Get              func(*config.Config) string
	Set              func(*config.Config, string) error
	Default          string
}

// IsCredential delegates credential classification to config's permission
// registry; Settings does not maintain a second list.
func (f Field) IsCredential() bool { return config.IsCredentialKey(f.Key) }

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
	{Key: "version", Label: "Schema version", Section: HostSection, Help: "Written by Spoon; version 2 introduces UI preferences.", Default: strconv.Itoa(config.CurrentVersion), Get: func(c *config.Config) string { return strconv.Itoa(c.Version) }},
	stringField("forge.provider", "Provider", ForgeSection, "github, gitlab, or empty to auto-detect", "", func(c *config.Config) string { return c.Forge.Provider }, func(c *config.Config, v string) { c.Forge.Provider = strings.ToLower(strings.TrimSpace(v)) }),
	stringField("forge.host", "Host", ForgeSection, "Self-hosted GitHub or GitLab hostname.", "", func(c *config.Config) string { return c.Forge.Host }, func(c *config.Config, v string) { c.Forge.Host = strings.TrimSpace(v) }),
	{Key: "github.tokens", Label: "GitHub tokens", Section: GitHubSection, Help: "One token per line; values are always masked.", Editable: true, Get: func(c *config.Config) string { return strings.Join(c.GitHub.Tokens, "\n") }, Set: func(c *config.Config, v string) error {
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
	{Key: "secrets.store", Label: "Token storage", Section: GitHubSection, Help: "keyring (default) keeps tokens in the OS keyring; file keeps them in this config file.", Editable: true, Default: "keyring", Environment: []string{"SPOON_SECRET_STORE"}, Get: func(c *config.Config) string { return c.Secrets.Store }, Set: func(c *config.Config, v string) error {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" && v != "keyring" && v != "file" {
			return fmt.Errorf("token storage must be keyring or file")
		}
		c.Secrets.Store = v
		return nil
	}},
	boolField("github.proxy.enabled", "Proxy enabled", ProxySection, "Enable ProxyScrape transport routing.", NoConsequence, func(c *config.Config) bool { return c.GitHub.Proxy.Enabled }, func(c *config.Config, v bool) { c.GitHub.Proxy.Enabled = v }),
	{Key: "github.proxy.apiKeyFile", Label: "Proxy API key file", Section: ProxySection, Help: "0600 credential file path.", Editable: true, Get: func(c *config.Config) string { return c.GitHub.Proxy.APIKeyFile }, Set: func(c *config.Config, v string) error { c.GitHub.Proxy.APIKeyFile = strings.TrimSpace(v); return nil }},
	{Key: "github.proxy.staticFile", Label: "Proxy static file", Section: ProxySection, Help: "0600 credential file path.", Editable: true, Get: func(c *config.Config) string { return c.GitHub.Proxy.StaticFile }, Set: func(c *config.Config, v string) error { c.GitHub.Proxy.StaticFile = strings.TrimSpace(v); return nil }},
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
	{Key: "embedder.model", Label: "Model", Section: EmbedderSection, Help: "Bundled FastEmbed model. ready = cached; download = not installed.", Editable: true, Consequence: Reindex, Default: "fast-bge-small-en-v1.5", Environment: []string{"SPOON_FASTEMBED_MODEL"}, Get: func(c *config.Config) string { return c.Embedder.Model }, Set: func(c *config.Config, v string) error {
		name := strings.TrimSpace(v)
		if name != "" {
			profile, ok := embed.LookupFastEmbedProfile(name)
			if !ok {
				return fmt.Errorf("embedder.model %q is not a supported FastEmbed model", name)
			}
			c.Embedder.MaxLength = profile.MaxLength
		}
		c.Embedder.Model = name
		return nil
	}},
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
	// The local embedder defaults to ON and the paid one to OFF. The asymmetry
	// is the point: fastembed costs CPU, Voyage costs money per token, and
	// opening a large fork network must never start spending on its own.
	{Key: "embedder.autoIndex", Label: "Auto-embed fork lists", Section: EmbedderSection, Help: "Embed each fork list with FastEmbed once enrichment settles. Local and free.", Editable: true, Default: "true", Environment: []string{"SPOON_AUTO_INDEX"}, Get: func(c *config.Config) string {
		if c.Embedder.AutoIndex == nil {
			return ""
		}
		return strconv.FormatBool(*c.Embedder.AutoIndex)
	}, Set: func(c *config.Config, v string) error {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("embedder.autoIndex: %w", err)
		}
		c.Embedder.AutoIndex = &b
		return nil
	}},
	boolField("embedder.voyage.autoIndex", "Auto-embed with Voyage", VoyageSection, "Sends every listed fork to a per-token billed service. Off by default.", Billing, func(c *config.Config) bool { return c.Embedder.Voyage.AutoIndex }, func(c *config.Config, v bool) { c.Embedder.Voyage.AutoIndex = v }),
	boolField("embedder.voyage.disabled", "Voyage disabled", VoyageSection, "Clearing this can enable an external per-token billed service.", Billing, func(c *config.Config) bool { return c.Embedder.Voyage.Disabled }, func(c *config.Config, v bool) { c.Embedder.Voyage.Disabled = v }),
	{Key: "embedder.voyage.apiKeyFile", Label: "Voyage API key file", Section: VoyageSection, Help: "0600 credential file path; the key itself is not shown.", Editable: true, Consequence: Billing, Get: func(c *config.Config) string { return c.Embedder.Voyage.APIKeyFile }, Set: func(c *config.Config, v string) error {
		c.Embedder.Voyage.APIKeyFile = strings.TrimSpace(v)
		return nil
	}},
	{Key: "embedder.voyage.embedModel", Label: "Embed model", Section: VoyageSection, Help: "Defaults to voyage-code-3.", Editable: true, Default: "voyage-code-3", Environment: []string{"SPOON_VOYAGE_EMBED_MODEL"}, Get: func(c *config.Config) string { return c.Embedder.Voyage.EmbedModel }, Set: func(c *config.Config, v string) error {
		c.Embedder.Voyage.EmbedModel = strings.TrimSpace(v)
		return nil
	}},
	{Key: "embedder.voyage.rerankModel", Label: "Rerank model", Section: VoyageSection, Help: "Defaults to rerank-2.5.", Editable: true, Default: "rerank-2.5", Environment: []string{"SPOON_VOYAGE_RERANK_MODEL"}, Get: func(c *config.Config) string { return c.Embedder.Voyage.RerankModel }, Set: func(c *config.Config, v string) error {
		c.Embedder.Voyage.RerankModel = strings.TrimSpace(v)
		return nil
	}},
	{Key: "embedder.voyage.outputDimension", Label: "Output dimension", Section: VoyageSection, Help: "Changing this re-partitions the index; pending documents must be re-embedded.", Editable: true, Consequence: Reindex, Default: "1024", Environment: []string{"SPOON_VOYAGE_DIM"}, Get: func(c *config.Config) string { return strconv.Itoa(c.Embedder.Voyage.OutputDimension) }, Set: func(c *config.Config, v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		c.Embedder.Voyage.OutputDimension = n
		return nil
	}},
	{Key: "embedder.voyage.baseUrl", Label: "Base URL", Section: VoyageSection, Help: "Optional Voyage-compatible gateway URL.", Editable: true, Environment: []string{"SPOON_VOYAGE_BASE_URL"}, Get: func(c *config.Config) string { return c.Embedder.Voyage.BaseURL }, Set: func(c *config.Config, v string) error { c.Embedder.Voyage.BaseURL = strings.TrimSpace(v); return nil }},
	{Key: "ui.theme", Label: "Theme", Section: AppearanceSection, Help: "dark, light, or amber.", Editable: true, Default: "dark", Environment: []string{"SPOON_TUI_THEME"}, Get: func(c *config.Config) string { return c.UI.Theme }, Set: func(c *config.Config, v string) error { c.UI.Theme = strings.ToLower(strings.TrimSpace(v)); return nil }},
	{Key: "ui.color", Label: "Color profile", Section: AppearanceSection, Help: "truecolor, ansi256, ansi16, ansi8, mono, or no-color.", Editable: true, Default: "truecolor", Environment: []string{"SPOON_TUI_COLOR", "NO_COLOR"}, Get: func(c *config.Config) string { return c.UI.Color }, Set: func(c *config.Config, v string) error { c.UI.Color = strings.ToLower(strings.TrimSpace(v)); return nil }},
	{Key: "ui.glyphs", Label: "Glyph profile", Section: AppearanceSection, Help: "unicode or ascii.", Editable: true, Default: "unicode", Environment: []string{"SPOON_TUI_GLYPHS"}, Get: func(c *config.Config) string { return c.UI.Glyphs }, Set: func(c *config.Config, v string) error {
		c.UI.Glyphs = strings.ToLower(strings.TrimSpace(v))
		return nil
	}},
}

// Populate display metadata from the same typed descriptors used by runtime
// resolution; Field owns only TUI editing/rendering behavior.
func init() {
	for i := range Registry {
		if setting, ok := config.SettingByKey(Registry[i].Key); ok {
			Registry[i].Default = setting.Default
			Registry[i].Environment = append([]string(nil), setting.Environment...)
		}
	}
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
