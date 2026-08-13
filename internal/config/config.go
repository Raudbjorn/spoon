// Package config defines spoon's on-disk configuration: the persisted,
// validated settings written automatically on first run (EnsureDefault) and
// re-checked by `spoon setup`. It is JSON at
// $XDG_CONFIG_HOME/spoon/config.json (~/.config/spoon/ by default), with
// /etc/spoon/config.json serving no-home hosts and as an admin-provided
// defaults layer. Fork/compare data lives in the libsql store
// (internal/store), not here.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CurrentVersion is the schema version written into new/updated config files.
// Version 2 adds the optional ui block; absent UI remains a lossless v1 input.
const CurrentVersion = 2

// Config is the root user configuration. Zero values mean "unset"; omitempty
// keeps the written file minimal.
// Config is the on-disk configuration. Old files may carry keys from removed
// features (reranker/labeler); encoding/json ignores unknown keys, so they
// load fine and the stale keys drop on the next Save.
type Config struct {
	Version  int            `json:"version"`
	Forge    ForgeConfig    `json:"forge,omitempty"`
	GitHub   GitHubConfig   `json:"github,omitempty"`
	Embedder EmbedderConfig `json:"embedder,omitempty"`
	UI       UIConfig       `json:"ui,omitempty"`
}

// UIConfig stores terminal appearance preferences. Environment variables remain
// higher precedence and invalid values are reported by TUI startup resolution.
// Empty values defer to built-in defaults; v2 settings saves stamp this schema.
type UIConfig struct {
	Theme  string `json:"theme,omitempty"`
	Color  string `json:"color,omitempty"`
	Glyphs string `json:"glyphs,omitempty"`
}

// EmbedderConfig configures the in-process fastembed embedder that powers
// persistence and semantic search, plus the optional Voyage AI provider layered
// on top of it. fastembed is the only backend and always runs in-process;
// Voyage is a sibling, not an alternative — see Voyage.
type EmbedderConfig struct {
	// Backend is "fastembed" (the default) or empty, which means fastembed.
	Backend string `json:"backend,omitempty"`
	// FastEmbed settings. The persistent semantic model remains fixed; these
	// fields record its cache and batching configuration.
	Model     string `json:"model,omitempty"`
	CacheDir  string `json:"cacheDir,omitempty"`
	MaxLength int    `json:"maxLength,omitempty"`
	BatchSize int    `json:"batchSize,omitempty"`
	// Voyage configures the optional Voyage AI embeddings + reranking provider.
	// It is deliberately not a Backend value: Voyage runs *in addition to*
	// fastembed (the store keys vectors by (document, model), so both models'
	// vectors coexist), and a Backend enum would model replacement instead.
	Voyage VoyageConfig `json:"voyage,omitempty"`
}

// VoyageConfig configures Voyage AI. Voyage is active only when an API key
// resolves — from $VOYAGE_AI_API_KEY, $VOYAGE_API_KEY, or APIKeyFile — and
// Disabled is false.
//
// The key itself is never stored here, only a path to a file holding it:
// credentials stay in the environment or in permission-checked files (see
// bootstrap.go). APIKeyFile is subject to the same 0600 enforcement as
// github.proxy.apiKeyFile.
type VoyageConfig struct {
	// APIKeyFile is a path to a 0600 file containing only the API key.
	APIKeyFile string `json:"apiKeyFile,omitempty"`
	// EmbedModel defaults to voyage-code-3; RerankModel to rerank-2.5.
	EmbedModel  string `json:"embedModel,omitempty"`
	RerankModel string `json:"rerankModel,omitempty"`
	// OutputDimension is 256, 512, 1024 (the default) or 2048. It is part of a
	// stored vector's model identity, so changing it re-partitions the index:
	// existing Voyage rows stay under the old identity and every document
	// becomes pending under the new one.
	OutputDimension int `json:"outputDimension,omitempty"`
	// BaseURL overrides the API root (gateways, tests). Empty → Voyage's own.
	BaseURL string `json:"baseUrl,omitempty"`
	// Disabled turns Voyage off even when a key is present, without unsetting
	// the environment variable.
	Disabled bool `json:"disabled,omitempty"`
}

// GitHubConfig configures authenticated identities, process pacing, and
// optional ProxyScrape-backed transport routing.
type GitHubConfig struct {
	Tokens            []string    `json:"tokens,omitempty"`
	RequestsPerMinute float64     `json:"requestsPerMinute,omitempty"`
	Proxy             ProxyConfig `json:"proxy,omitempty"`
}

type ProxyConfig struct {
	Enabled           bool   `json:"enabled,omitempty"`
	APIKeyFile        string `json:"apiKeyFile,omitempty"`
	StaticFile        string `json:"staticFile,omitempty"`
	WhitelistPublicIP *bool  `json:"whitelistPublicIp,omitempty"`
	CacheTTL          string `json:"cacheTtl,omitempty"`
}

// ForgeConfig records the default forge provider.
type ForgeConfig struct {
	Provider string `json:"provider,omitempty"` // "github" | "gitlab"
	Host     string `json:"host,omitempty"`     // self-hosted GitLab/GHES hostname
}

// DefaultPath returns $XDG_CONFIG_HOME/spoon/config.json, falling back to
// ~/.config/spoon/config.json. On hosts without a resolvable home (system
// accounts, containers) it falls back to the system path /etc/spoon/config.json
// rather than erroring — spoon must run configured even there.
func DefaultPath() (string, error) {
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return SystemPath(), nil
		}
		cfgHome = filepath.Join(home, ".config")
	}
	return filepath.Join(cfgHome, "spoon", "config.json"), nil
}

// SystemPath is the machine-wide config location. It doubles as an
// admin-provided defaults layer: LoadDefault falls back to it when the user
// has no personal config.
func SystemPath() string {
	return filepath.Join("/etc", "spoon", "config.json")
}

// Load reads and validates the config at path. If the file does not exist it
// returns a wrapped os.ErrNotExist (test with errors.Is); callers treat that as
// "no config yet".
func Load(path string) (*Config, error) {
	info, err := os.Stat(path)
	if err == nil && configContainsCredentials(path) && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("config %s contains credentials and is readable by group/other; run chmod 600 %s", path, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.normalizeLegacy()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	if err := validateCredentialFile(c.GitHub.Proxy.APIKeyFile, "github.proxy.apiKeyFile"); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	if err := validateCredentialFile(c.GitHub.Proxy.StaticFile, "github.proxy.staticFile"); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	if err := validateCredentialFile(c.Embedder.Voyage.APIKeyFile, "embedder.voyage.apiKeyFile"); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &c, nil
}

// Save writes c to path as indented JSON, creating parent directories. The
// write is atomic (temp file + rename) so a crash can't truncate the config.
// removeBeforeRename records whether Save must unlink the destination before
// renaming over it. Only Windows requires this; on Unix it would trade an
// atomic replace for a crash window on a PAT-bearing file. Both are variables
// rather than an inline runtime.GOOS check so a test can assert the Unix path
// never unlinks — atomic replace and unlink-then-rename are indistinguishable
// by end state, so without this seam the fix has no regression guard.
var (
	removeBeforeRename = runtime.GOOS == "windows"
	osRemove           = os.Remove
)

func Save(path string, c *Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	// Apply the same credential-file checks Load performs, so setup can never
	// write a config that the next command refuses to load.
	if err := validateCredentialFile(c.GitHub.Proxy.APIKeyFile, "github.proxy.apiKeyFile"); err != nil {
		return err
	}
	if err := validateCredentialFile(c.GitHub.Proxy.StaticFile, "github.proxy.staticFile"); err != nil {
		return err
	}
	if err := validateCredentialFile(c.Embedder.Voyage.APIKeyFile, "embedder.voyage.apiKeyFile"); err != nil {
		return err
	}
	// Always stamp the current schema. Version is an output marker, never a
	// validation input, so rewriting a v1 file is a safe lossless migration.
	c.Version = CurrentVersion
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("secure %s: %w", tmp, err)
	}
	// On Unix os.Rename atomically replaces the destination, so the config is
	// never absent from disk. Only Windows refuses to overwrite, and only there
	// do we unlink first — doing it unconditionally would open a window where a
	// crash between Remove and Rename destroys the file, and this file holds the
	// user's GitHub PATs.
	if removeBeforeRename {
		_ = osRemove(path)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// ProbeAtomicPublication verifies that the current process can publish a
// replacement in path's directory using the same create-and-rename primitive
// as Save. It never opens, replaces, or changes the target config. File mode
// bits are deliberately not used: replacing a read-only file is valid when
// its directory is writable, while an owner-write bit says nothing about this
// process's ability to create the temporary sibling.
func ProbeAtomicPublication(path string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".spoon-config-probe-*")
	if err != nil {
		return fmt.Errorf("cannot atomically publish %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close publication probe %s: %w", path, err)
	}
	published := tmpPath + ".published"
	defer os.Remove(published)
	if err := os.Rename(tmpPath, published); err != nil {
		return fmt.Errorf("cannot atomically publish %s: %w", path, err)
	}
	return nil
}

// CredentialConfigKeys is the persisted credential-bearing schema inventory.
// It drives the config-file 0600 gate and the settings registry parity test.
var CredentialConfigKeys = []string{
	"github.tokens",
	"github.proxy.apiKeyFile",
	"embedder.voyage.apiKeyFile",
}

func configContainsCredentials(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	// Every credential-bearing field must appear here, or the permission gate in
	// Load silently does not apply to it.
	var raw struct {
		GitHub struct {
			Tokens []string `json:"tokens"`
			Proxy  struct {
				APIKeyFile string `json:"apiKeyFile"`
			} `json:"proxy"`
		} `json:"github"`
		Embedder struct {
			Voyage struct {
				APIKeyFile string `json:"apiKeyFile"`
			} `json:"voyage"`
		} `json:"embedder"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return false
	}
	return len(raw.GitHub.Tokens) > 0 ||
		raw.GitHub.Proxy.APIKeyFile != "" ||
		raw.Embedder.Voyage.APIKeyFile != ""
}

func validateCredentialFile(path, field string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s %q: %w", field, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q must reference a regular file", field, path)
	}
	if info.Mode().Perm()&0o400 == 0 {
		return fmt.Errorf("%s %q must be owner-readable", field, path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s %q contains credentials and is readable by group/other; run chmod 600 %s", field, path, path)
	}
	return nil
}

// ReadCredentialFile returns the trimmed contents of a credential file named by
// a config field, after applying the same permission checks Load enforces. An
// empty path yields an empty secret and no error ("not configured"); a path that
// does not exist is likewise not an error, so a stale config entry degrades to
// "no credential" rather than failing every command.
func ReadCredentialFile(path, field string) (string, error) {
	if path == "" {
		return "", nil
	}
	if err := validateCredentialFile(path, field); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", field, path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// LoadDefault loads the config from DefaultPath as an optional defaults layer,
// falling back to the system config (/etc/spoon/config.json — admin-provided
// defaults) when the user has none. Returns (nil, nil) when no config exists;
// (nil, err) when one exists but is unreadable/invalid (callers should warn
// but continue — a run must not fail on a bad config); (cfg, nil) on success.
// Honors $SPOON_NO_CONFIG=1 (returns nil, nil) so the layer can be disabled.
func LoadDefault() (*Config, error) {
	layer := LoadDefaultWithLayer()
	return layer.Config, layer.LoadError
}

// LoadedLayer records the exact source selected by LoadDefaultWithLayer.
// Disabled is distinct from a missing layer so UIs never infer persistence
// state from a nil Config.
type LoadedLayer struct {
	Config    *Config
	Path      string
	System    bool
	Disabled  bool
	LoadError error
}

// LoadDefaultWithLayer is the central config-layer selector used by runtime
// startup and the settings UI. It preserves LoadDefault's graceful semantics
// while exposing the selected path and reason without reimplementing fallback.
func LoadDefaultWithLayer() LoadedLayer {
	if os.Getenv("SPOON_NO_CONFIG") == "1" {
		return LoadedLayer{Disabled: true}
	}
	path, err := DefaultPath()
	if err != nil {
		return LoadedLayer{Path: SystemPath(), System: true, LoadError: err}
	}
	cfg, err := Load(path)
	if errors.Is(err, os.ErrNotExist) && path != SystemPath() {
		path = SystemPath()
		cfg, err = Load(path)
	}
	if errors.Is(err, os.ErrNotExist) {
		return LoadedLayer{Path: path, System: path == SystemPath()}
	}
	return LoadedLayer{Config: cfg, Path: path, System: path == SystemPath(), LoadError: err}
}

// Coalesce returns the first non-empty string, or "" if all are empty. Used to
// layer precedence: Coalesce(flag, env, config).
func Coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// normalizeLegacy rewrites embedder backends written by older spoon versions
// to the empty backend (which resolves to fastembed), so a stale config file
// degrades cleanly instead of failing every load. Covers since-removed
// external backends (ollama, sidecar, openai), the retired in-process
// backends (builtin, lexical), and the removed OpenVINO model embedder.
// Non-backend embedder fields (fastembed model/cache) are preserved.
func (c *Config) normalizeLegacy() {
	switch strings.ToLower(c.Embedder.Backend) {
	case "ollama", "sidecar", "openai", "builtin", "lexical", "openvino":
		c.Embedder.Backend = ""
	}
}

var (
	validProviders = map[string]bool{"": true, "github": true, "gitlab": true}
	validBackends  = map[string]bool{"": true, "fastembed": true}
	validThemes    = map[string]bool{"": true, "dark": true, "light": true, "amber": true}
	validColors    = map[string]bool{"": true, "truecolor": true, "ansi256": true, "ansi16": true, "ansi8": true, "mono": true, "no-color": true}
	validGlyphs    = map[string]bool{"": true, "unicode": true, "ascii": true}
	// Mirrors the output widths voyage-code-3 accepts. Kept here rather than
	// imported from internal/embed so the config package stays dependency-free.
	validVoyageDimensions = map[int]bool{256: true, 512: true, 1024: true, 2048: true}
)

// Validate checks enum fields. Empty values are allowed (mean "unset").
func (c *Config) Validate() error {
	if !validProviders[strings.ToLower(c.Forge.Provider)] {
		return fmt.Errorf("forge.provider %q must be 'github' or 'gitlab'", c.Forge.Provider)
	}
	if !validBackends[strings.ToLower(c.Embedder.Backend)] {
		return fmt.Errorf("embedder.backend %q must be 'fastembed' (or empty)", c.Embedder.Backend)
	}
	if !validThemes[strings.ToLower(c.UI.Theme)] {
		return fmt.Errorf("ui.theme %q must be dark, light or amber", c.UI.Theme)
	}
	if !validColors[strings.ToLower(c.UI.Color)] {
		return fmt.Errorf("ui.color %q is not a supported terminal profile", c.UI.Color)
	}
	if !validGlyphs[strings.ToLower(c.UI.Glyphs)] {
		return fmt.Errorf("ui.glyphs %q must be unicode or ascii", c.UI.Glyphs)
	}
	if math.IsNaN(c.GitHub.RequestsPerMinute) || math.IsInf(c.GitHub.RequestsPerMinute, 0) || c.GitHub.RequestsPerMinute < 0 || c.GitHub.RequestsPerMinute > 900 {
		return fmt.Errorf("github.requestsPerMinute must be a finite value in (0, 900] when set")
	}
	if c.GitHub.Proxy.CacheTTL != "" {
		if _, err := time.ParseDuration(c.GitHub.Proxy.CacheTTL); err != nil {
			return fmt.Errorf("github.proxy.cacheTtl: %w", err)
		}
	}
	if d := c.Embedder.Voyage.OutputDimension; d != 0 && !validVoyageDimensions[d] {
		return fmt.Errorf("embedder.voyage.outputDimension %d must be 256, 512, 1024 or 2048 (or unset)", d)
	}
	if raw := c.Embedder.Voyage.BaseURL; raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("embedder.voyage.baseUrl %q must be an absolute http(s) URL", raw)
		}
	}
	return nil
}
