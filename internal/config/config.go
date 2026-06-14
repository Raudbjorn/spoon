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
	Reranker ModelConfig    `json:"reranker,omitempty"`
	Labeler  ModelConfig    `json:"labeler,omitempty"`
}

// ModelConfig points one OpenVINO-backed feature (reranker, labeler) at a
// model directory and device.
type ModelConfig struct {
	// ModelPath is the OVMS-style model directory.
	ModelPath string `json:"modelPath,omitempty"`
	// Device is the OpenVINO device ("GPU" default).
	Device string `json:"device,omitempty"`
}

// EmbedderConfig selects and configures the in-process embedding backend
// used for fork clustering. Both backends run inside the spoon process;
// there are no external services.
type EmbedderConfig struct {
	// Backend is "builtin" (zero-setup lexical embedder, the default) or
	// "openvino" (transformer encoder via the OpenVINO runtime; requires a
	// binary built with -tags openvino).
	Backend string `json:"backend,omitempty"`
	// ModelPath is the OVMS-style model directory for the openvino backend
	// (openvino_model.xml + openvino_tokenizer.xml).
	ModelPath string `json:"modelPath,omitempty"`
	// Device is the OpenVINO device for the encoder, e.g. "GPU" (default)
	// or "CPU".
	Device string `json:"device,omitempty"`
	// Pooling overrides the hidden-state pooling: "cls", "mean", or
	// "last". Empty → the model dir's graph.pbtxt, else CLS.
	Pooling string `json:"pooling,omitempty"`
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

// normalizeLegacy clears embedder sections written by older spoon versions
// for since-removed external backends (ollama, sidecar, openai), so a stale
// config file degrades to the builtin backend instead of failing every load.
func (c *Config) normalizeLegacy() {
	switch strings.ToLower(c.Embedder.Backend) {
	case "ollama", "sidecar", "openai":
		c.Embedder = EmbedderConfig{}
	}
}

var (
	validProviders = map[string]bool{"": true, "github": true, "gitlab": true}
	validBackends  = map[string]bool{"": true, "builtin": true, "lexical": true, "openvino": true}
	validPoolings  = map[string]bool{"": true, "cls": true, "mean": true, "last": true}
)

// Validate checks enum fields. Empty values are allowed (mean "unset").
func (c *Config) Validate() error {
	if !validProviders[strings.ToLower(c.Forge.Provider)] {
		return fmt.Errorf("forge.provider %q must be 'github' or 'gitlab'", c.Forge.Provider)
	}
	if !validBackends[strings.ToLower(c.Embedder.Backend)] {
		return fmt.Errorf("embedder.backend %q must be 'builtin', 'lexical', or 'openvino'", c.Embedder.Backend)
	}
	if !validPoolings[strings.ToLower(c.Embedder.Pooling)] {
		return fmt.Errorf("embedder.pooling %q must be 'cls', 'mean', or 'last'", c.Embedder.Pooling)
	}
	return nil
}
