// Package config defines spoon's on-disk user configuration: the persisted,
// validated settings that `spoon setup` writes and re-checks. It is JSON
// (the house format across spoon's caches) at
// $XDG_CONFIG_HOME/spoon/config.json (falling back to ~/.config/spoon/).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CurrentVersion is the schema version written into new/updated config files.
const CurrentVersion = 1

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
}

// EmbedderConfig configures the in-process fastembed embedder that powers
// persistence and semantic search. fastembed is the only backend; an empty
// Backend means fastembed. It runs inside the spoon process — no external
// services.
type EmbedderConfig struct {
	// Backend is "fastembed" (the default) or empty, which means fastembed.
	Backend string `json:"backend,omitempty"`
	// FastEmbed settings. The persistent semantic model remains fixed; these
	// fields record its cache and batching configuration.
	Model     string `json:"model,omitempty"`
	CacheDir  string `json:"cacheDir,omitempty"`
	MaxLength int    `json:"maxLength,omitempty"`
	BatchSize int    `json:"batchSize,omitempty"`
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
// ~/.config/spoon/config.json.
func DefaultPath() (string, error) {
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		cfgHome = filepath.Join(home, ".config")
	}
	return filepath.Join(cfgHome, "spoon", "config.json"), nil
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
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
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

func configContainsCredentials(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var raw struct {
		GitHub struct {
			Tokens []string `json:"tokens"`
			Proxy  struct {
				APIKeyFile string `json:"apiKeyFile"`
			} `json:"proxy"`
		} `json:"github"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return false
	}
	return len(raw.GitHub.Tokens) > 0 || raw.GitHub.Proxy.APIKeyFile != ""
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

// LoadDefault loads the config from DefaultPath as an optional defaults layer.
// Returns (nil, nil) when no config exists; (nil, err) when one exists but is
// unreadable/invalid (callers should warn but continue — a run must not fail on
// a bad config); (cfg, nil) on success. Honors $SPOON_NO_CONFIG=1 (returns
// nil, nil) so the layer can be disabled.
func LoadDefault() (*Config, error) {
	if os.Getenv("SPOON_NO_CONFIG") == "1" {
		return nil, nil
	}
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	c, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return c, err
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
// external backends (ollama, sidecar, openai) and the retired in-process
// backends (builtin, lexical). Non-backend embedder fields (fastembed
// model/cache) are preserved.
func (c *Config) normalizeLegacy() {
	switch strings.ToLower(c.Embedder.Backend) {
	case "ollama", "sidecar", "openai", "builtin", "lexical":
		c.Embedder.Backend = ""
	}
}

var (
	validProviders = map[string]bool{"": true, "github": true, "gitlab": true}
	validBackends  = map[string]bool{"": true, "fastembed": true}
)

// Validate checks enum fields. Empty values are allowed (mean "unset").
func (c *Config) Validate() error {
	if !validProviders[strings.ToLower(c.Forge.Provider)] {
		return fmt.Errorf("forge.provider %q must be 'github' or 'gitlab'", c.Forge.Provider)
	}
	if !validBackends[strings.ToLower(c.Embedder.Backend)] {
		return fmt.Errorf("embedder.backend %q must be 'fastembed' (or empty)", c.Embedder.Backend)
	}
	if math.IsNaN(c.GitHub.RequestsPerMinute) || math.IsInf(c.GitHub.RequestsPerMinute, 0) || c.GitHub.RequestsPerMinute < 0 || c.GitHub.RequestsPerMinute > 900 {
		return fmt.Errorf("github.requestsPerMinute must be a finite value in (0, 900] when set")
	}
	if c.GitHub.Proxy.CacheTTL != "" {
		if _, err := time.ParseDuration(c.GitHub.Proxy.CacheTTL); err != nil {
			return fmt.Errorf("github.proxy.cacheTtl: %w", err)
		}
	}
	return nil
}
