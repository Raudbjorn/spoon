// Package config defines spoon's on-disk user configuration: the persisted,
// validated settings that `spoon setup` writes and re-checks. It is JSON
// (the house format across spoon's caches) at
// $XDG_CONFIG_HOME/spoon/config.json (falling back to ~/.config/spoon/).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CurrentVersion is the schema version written into new/updated config files.
const CurrentVersion = 1

// Config is the root user configuration. Zero values mean "unset"; omitempty
// keeps the written file minimal.
type Config struct {
	Version  int            `json:"version"`
	Forge    ForgeConfig    `json:"forge,omitempty"`
	Embedder EmbedderConfig `json:"embedder,omitempty"`
}

// ForgeConfig records the default forge provider.
type ForgeConfig struct {
	Provider string `json:"provider,omitempty"` // "github" | "gitlab"
	Host     string `json:"host,omitempty"`     // self-hosted GitLab/GHES hostname
}

// EmbedderConfig records the embedding backend setup discovered/validated by
// `spoon setup`.
type EmbedderConfig struct {
	Backend         string `json:"backend,omitempty"`         // "ollama" | "sidecar" | "openai"
	Endpoint        string `json:"endpoint,omitempty"`        // ollama endpoint, or openai base URL
	Model           string `json:"model,omitempty"`           // embedding model id
	SidecarEndpoint string `json:"sidecarEndpoint,omitempty"` // python sidecar base URL
	LabelerModel    string `json:"labelerModel,omitempty"`    // LLM labeler model
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
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &c, nil
}

// Save writes c to path as indented JSON, creating parent directories. The
// write is atomic (temp file + rename) so a crash can't truncate the config.
func Save(path string, c *Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
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

// LayerEmbedder applies this config as a defaults layer beneath the given
// already-resolved (flag > env) embedder values, backend-aware. The backend is
// adopted from the config only when none was supplied. The backend-specific
// fields — endpoint, model, sidecar endpoint — are inherited from the config
// ONLY when the effective backend matches the config's saved backend, so a
// saved setup for one backend (e.g. openai/OVMS) never leaks its endpoint/model
// into a run that selects a different backend (e.g. ollama). The labeler model
// is backend-agnostic and always layered.
func (c *Config) LayerEmbedder(backend, endpoint, model, sidecarEndpoint, labelerModel string) (rBackend, rEndpoint, rModel, rSidecar, rLabeler string) {
	rBackend = backend
	if rBackend == "" {
		rBackend = strings.ToLower(c.Embedder.Backend)
	}
	rEndpoint, rModel, rSidecar = endpoint, model, sidecarEndpoint
	if c.Embedder.Backend != "" && strings.EqualFold(rBackend, c.Embedder.Backend) {
		rEndpoint = Coalesce(rEndpoint, c.Embedder.Endpoint)
		rModel = Coalesce(rModel, c.Embedder.Model)
		rSidecar = Coalesce(rSidecar, c.Embedder.SidecarEndpoint)
	}
	rLabeler = Coalesce(labelerModel, c.Embedder.LabelerModel)
	return rBackend, rEndpoint, rModel, rSidecar, rLabeler
}

var (
	validBackends  = map[string]bool{"": true, "ollama": true, "sidecar": true, "openai": true}
	validProviders = map[string]bool{"": true, "github": true, "gitlab": true}
)

// Validate checks enum fields. Empty values are allowed (mean "unset").
func (c *Config) Validate() error {
	if !validProviders[strings.ToLower(c.Forge.Provider)] {
		return fmt.Errorf("forge.provider %q must be 'github' or 'gitlab'", c.Forge.Provider)
	}
	if !validBackends[strings.ToLower(c.Embedder.Backend)] {
		return fmt.Errorf("embedder.backend %q must be 'ollama', 'sidecar', or 'openai'", c.Embedder.Backend)
	}
	return nil
}
