package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Setting describes one persisted configuration leaf's runtime precedence.
// It is the single source for Settings and runtime resolution metadata.
type Setting struct {
	Key         string
	Default     string
	Environment []string
	get         func(*Config) string
}

var settings = []Setting{
	{"version", "2", nil, func(c *Config) string { return strconv.Itoa(c.Version) }},
	{"forge.provider", "", nil, func(c *Config) string { return c.Forge.Provider }},
	{"forge.host", "", nil, func(c *Config) string { return c.Forge.Host }},
	{"github.tokens", "", nil, func(c *Config) string { return strings.Join(c.GitHub.Tokens, "\n") }},
	{"github.requestsPerMinute", "300", []string{"SPOON_GITHUB_RPM"}, func(c *Config) string { return strconv.FormatFloat(c.GitHub.RequestsPerMinute, 'f', -1, 64) }},
	{"github.proxy.enabled", "false", nil, func(c *Config) string { return strconv.FormatBool(c.GitHub.Proxy.Enabled) }},
	{"github.proxy.apiKeyFile", "", nil, func(c *Config) string { return c.GitHub.Proxy.APIKeyFile }},
	{"github.proxy.staticFile", "", nil, func(c *Config) string { return c.GitHub.Proxy.StaticFile }},
	{"github.proxy.whitelistPublicIp", "", nil, func(c *Config) string {
		if c.GitHub.Proxy.WhitelistPublicIP == nil {
			return ""
		}
		return strconv.FormatBool(*c.GitHub.Proxy.WhitelistPublicIP)
	}},
	{"github.proxy.cacheTtl", "", nil, func(c *Config) string { return c.GitHub.Proxy.CacheTTL }},
	{"embedder.backend", "fastembed", nil, func(c *Config) string { return c.Embedder.Backend }},
	{"embedder.model", "fast-bge-small-en-v1.5", []string{"SPOON_FASTEMBED_MODEL"}, func(c *Config) string { return c.Embedder.Model }},
	{"embedder.cacheDir", "", []string{"SPOON_FASTEMBED_CACHE"}, func(c *Config) string { return c.Embedder.CacheDir }},
	{"embedder.maxLength", "512", nil, func(c *Config) string { return strconv.Itoa(c.Embedder.MaxLength) }},
	{"embedder.batchSize", "32", nil, func(c *Config) string { return strconv.Itoa(c.Embedder.BatchSize) }},
	{"embedder.voyage.disabled", "false", []string{"SPOON_NO_VOYAGE"}, func(c *Config) string { return strconv.FormatBool(c.Embedder.Voyage.Disabled) }},
	{"embedder.voyage.apiKeyFile", "", nil, func(c *Config) string { return c.Embedder.Voyage.APIKeyFile }},
	{"embedder.voyage.embedModel", "voyage-code-3", []string{"SPOON_VOYAGE_EMBED_MODEL"}, func(c *Config) string { return c.Embedder.Voyage.EmbedModel }},
	{"embedder.voyage.rerankModel", "rerank-2.5", []string{"SPOON_VOYAGE_RERANK_MODEL"}, func(c *Config) string { return c.Embedder.Voyage.RerankModel }},
	{"embedder.voyage.outputDimension", "1024", []string{"SPOON_VOYAGE_DIM"}, func(c *Config) string { return strconv.Itoa(c.Embedder.Voyage.OutputDimension) }},
	{"embedder.voyage.baseUrl", "https://api.voyageai.com/v1", []string{"SPOON_VOYAGE_BASE_URL"}, func(c *Config) string { return c.Embedder.Voyage.BaseURL }},
	{"ui.theme", "dark", []string{"SPOON_TUI_THEME"}, func(c *Config) string { return c.UI.Theme }},
	{"ui.color", "truecolor", []string{"SPOON_TUI_COLOR", "NO_COLOR"}, func(c *Config) string { return c.UI.Color }},
	{"ui.glyphs", "unicode", []string{"SPOON_TUI_GLYPHS"}, func(c *Config) string { return c.UI.Glyphs }},
}

var settingByKey = func() map[string]Setting {
	out := make(map[string]Setting, len(settings))
	for _, setting := range settings {
		out[setting.Key] = setting
	}
	return out
}()

// Settings returns a copy-safe view of all persisted leaf metadata.
func Settings() []Setting                     { return append([]Setting(nil), settings...) }
func SettingByKey(key string) (Setting, bool) { value, ok := settingByKey[key]; return value, ok }

// EffectiveConfig resolves runtime values once from flags, environment, file,
// then defaults. Values carries the source evidence shown by Settings.
type EffectiveConfig struct{ Values map[string]ResolvedString }

func ResolveEffectiveConfig(file *Config, flags map[string]string, env map[string]string) EffectiveConfig {
	values := make(map[string]ResolvedString, len(settings))
	for _, setting := range settings {
		fileValue, filePresent := "", false
		if file != nil {
			fileValue, filePresent = setting.get(file), FieldPresent(file, setting.Key)
		}
		if !filePresent && file != nil && setting.Key == "github.proxy.whitelistPublicIp" {
			filePresent = file.GitHub.Proxy.WhitelistPublicIP != nil
		}
		if !filePresent && fileValue != "" && fileValue != "false" && fileValue != "0" {
			filePresent = true
		}
		if setting.Key == "embedder.voyage.disabled" && env["SPOON_NO_VOYAGE"] != "" {
			env = copyEnv(env)
			env["SPOON_NO_VOYAGE"] = "true"
		}
		if setting.Key == "ui.color" && env["NO_COLOR"] != "" {
			env = copyEnv(env)
			env["NO_COLOR"] = "no-color"
		}
		defaultValue := setting.Default
		if setting.Key == "embedder.cacheDir" && defaultValue == "" {
			defaultValue = defaultFastEmbedCacheDir(env)
		}
		values[setting.Key] = ResolveString(fileValue, filePresent, defaultValue, flags[setting.Key], env, setting.Environment...)
	}
	return EffectiveConfig{Values: values}
}

func copyEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}
func (e EffectiveConfig) Value(key string) ResolvedString { return e.Values[key] }

func defaultFastEmbedCacheDir(env map[string]string) string {
	root := env["XDG_CACHE_HOME"]
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, ".cache")
	}
	return filepath.Join(root, "spoon", "models", "fastembed")
}
