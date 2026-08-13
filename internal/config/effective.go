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
	{"github.proxy.whitelistPublicIp", "false", nil, func(c *Config) string {
		if c.GitHub.Proxy.WhitelistPublicIP == nil {
			return "false"
		}
		return strconv.FormatBool(*c.GitHub.Proxy.WhitelistPublicIP)
	}},
	{"github.proxy.cacheTtl", "1h", nil, func(c *Config) string { return c.GitHub.Proxy.CacheTTL }},
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

// EffectiveConfig is the startup-owned configuration result. Every runtime
// constructor receives this value rather than loading configuration or reading
// environment variables itself. Each leaf retains its source evidence for
// Settings while the named groups keep production consumers typed.
type EffectiveConfig struct {
	Forge      EffectiveForge
	GitHub     EffectiveGitHub
	FastEmbed  EffectiveFastEmbed
	Voyage     EffectiveVoyage
	Appearance EffectiveAppearance
	Backend    ResolvedString
	Version    ResolvedString
}

type EffectiveForge struct{ Provider, Host ResolvedString }
type EffectiveGitHub struct {
	Tokens, RequestsPerMinute ResolvedString
	Proxy                     EffectiveProxy
}
type EffectiveProxy struct{ Enabled, APIKeyFile, StaticFile, WhitelistPublicIP, CacheTTL ResolvedString }
type EffectiveFastEmbed struct{ Model, CacheDir, MaxLength, BatchSize ResolvedString }
type EffectiveVoyage struct{ Disabled, APIKeyFile, EmbedModel, RerankModel, OutputDimension, BaseURL ResolvedString }
type EffectiveAppearance struct{ Theme, Color, Glyphs ResolvedString }

// EnvironmentSnapshot captures the process environment once at command startup.
// Tests and embedding callers should inject a map into ResolveEffectiveConfig.
func EnvironmentSnapshot() map[string]string {
	env := make(map[string]string)
	for _, pair := range os.Environ() {
		name, value, ok := strings.Cut(pair, "=")
		if ok {
			env[name] = value
		}
	}
	return env
}

// ResolveEffectiveConfig resolves flags > injected environment > the Bootstrap
// snapshot > defaults exactly once. SPOON_NO_VOYAGE follows runtime semantics:
// only the literal value "1" disables Voyage.
func ResolveEffectiveConfig(file *Config, flags map[string]string, env map[string]string) EffectiveConfig {
	values := make(map[string]ResolvedString, len(settings))
	for _, setting := range settings {
		fileValue, filePresent := "", false
		if file != nil {
			fileValue, filePresent = setting.get(file), FieldPresent(file, setting.Key)
		}
		if !filePresent && fileValue != "" && fileValue != "false" && fileValue != "0" {
			filePresent = true
		}
		defaultValue := setting.Default
		if setting.Key == "embedder.cacheDir" {
			defaultValue = defaultFastEmbedCacheDir(env)
		}
		settingEnv := env
		switch setting.Key {
		case "embedder.voyage.disabled":
			if env["SPOON_NO_VOYAGE"] == "1" {
				settingEnv = copyEnv(env)
				settingEnv["SPOON_NO_VOYAGE"] = "true"
			} else {
				settingEnv = copyEnv(env)
				delete(settingEnv, "SPOON_NO_VOYAGE")
			}
		case "ui.color":
			if env["NO_COLOR"] != "" {
				settingEnv = copyEnv(env)
				settingEnv["NO_COLOR"] = "no-color"
			}
		}
		values[setting.Key] = ResolveString(fileValue, filePresent, defaultValue, flags[setting.Key], settingEnv, setting.Environment...)
	}
	return effectiveFromValues(values)
}

func effectiveFromValues(values map[string]ResolvedString) EffectiveConfig {
	value := func(key string) ResolvedString { return values[key] }
	return EffectiveConfig{
		Version: value("version"), Backend: value("embedder.backend"),
		Forge: EffectiveForge{Provider: value("forge.provider"), Host: value("forge.host")},
		GitHub: EffectiveGitHub{Tokens: value("github.tokens"), RequestsPerMinute: value("github.requestsPerMinute"), Proxy: EffectiveProxy{
			Enabled: value("github.proxy.enabled"), APIKeyFile: value("github.proxy.apiKeyFile"), StaticFile: value("github.proxy.staticFile"), WhitelistPublicIP: value("github.proxy.whitelistPublicIp"), CacheTTL: value("github.proxy.cacheTtl"),
		}},
		FastEmbed:  EffectiveFastEmbed{Model: value("embedder.model"), CacheDir: value("embedder.cacheDir"), MaxLength: value("embedder.maxLength"), BatchSize: value("embedder.batchSize")},
		Voyage:     EffectiveVoyage{Disabled: value("embedder.voyage.disabled"), APIKeyFile: value("embedder.voyage.apiKeyFile"), EmbedModel: value("embedder.voyage.embedModel"), RerankModel: value("embedder.voyage.rerankModel"), OutputDimension: value("embedder.voyage.outputDimension"), BaseURL: value("embedder.voyage.baseUrl")},
		Appearance: EffectiveAppearance{Theme: value("ui.theme"), Color: value("ui.color"), Glyphs: value("ui.glyphs")},
	}
}

// Value exposes a descriptor for generic Settings rendering without making the
// string-keyed map the runtime API.
func (e EffectiveConfig) Value(key string) ResolvedString {
	switch key {
	case "version":
		return e.Version
	case "forge.provider":
		return e.Forge.Provider
	case "forge.host":
		return e.Forge.Host
	case "github.tokens":
		return e.GitHub.Tokens
	case "github.requestsPerMinute":
		return e.GitHub.RequestsPerMinute
	case "github.proxy.enabled":
		return e.GitHub.Proxy.Enabled
	case "github.proxy.apiKeyFile":
		return e.GitHub.Proxy.APIKeyFile
	case "github.proxy.staticFile":
		return e.GitHub.Proxy.StaticFile
	case "github.proxy.whitelistPublicIp":
		return e.GitHub.Proxy.WhitelistPublicIP
	case "github.proxy.cacheTtl":
		return e.GitHub.Proxy.CacheTTL
	case "embedder.backend":
		return e.Backend
	case "embedder.model":
		return e.FastEmbed.Model
	case "embedder.cacheDir":
		return e.FastEmbed.CacheDir
	case "embedder.maxLength":
		return e.FastEmbed.MaxLength
	case "embedder.batchSize":
		return e.FastEmbed.BatchSize
	case "embedder.voyage.disabled":
		return e.Voyage.Disabled
	case "embedder.voyage.apiKeyFile":
		return e.Voyage.APIKeyFile
	case "embedder.voyage.embedModel":
		return e.Voyage.EmbedModel
	case "embedder.voyage.rerankModel":
		return e.Voyage.RerankModel
	case "embedder.voyage.outputDimension":
		return e.Voyage.OutputDimension
	case "embedder.voyage.baseUrl":
		return e.Voyage.BaseURL
	case "ui.theme":
		return e.Appearance.Theme
	case "ui.color":
		return e.Appearance.Color
	case "ui.glyphs":
		return e.Appearance.Glyphs
	default:
		return ResolvedString{}
	}
}

// EmbedderConfig returns the effective typed configuration for constructors.
func (e EffectiveConfig) EmbedderConfig() (EmbedderConfig, error) {
	maxLength, err := strconv.Atoi(e.FastEmbed.MaxLength.Value)
	if err != nil {
		return EmbedderConfig{}, err
	}
	batchSize, err := strconv.Atoi(e.FastEmbed.BatchSize.Value)
	if err != nil {
		return EmbedderConfig{}, err
	}
	dimension, err := strconv.Atoi(e.Voyage.OutputDimension.Value)
	if err != nil {
		return EmbedderConfig{}, err
	}
	disabled, err := strconv.ParseBool(e.Voyage.Disabled.Value)
	if err != nil {
		return EmbedderConfig{}, err
	}
	return EmbedderConfig{
		Backend: e.Backend.Value, Model: e.FastEmbed.Model.Value, CacheDir: e.FastEmbed.CacheDir.Value, MaxLength: maxLength, BatchSize: batchSize,
		Voyage: VoyageConfig{Disabled: disabled, APIKeyFile: e.Voyage.APIKeyFile.Value, EmbedModel: e.Voyage.EmbedModel.Value, RerankModel: e.Voyage.RerankModel.Value, OutputDimension: dimension, BaseURL: e.Voyage.BaseURL.Value},
	}, nil
}

func copyEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

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
