package config

// First-run bootstrap: spoon is zero-configuration. The first invocation
// detects what the host offers, writes a fully-populated default config to a
// predictable place, and drops a README documenting every knob next to it.
// Everything is enabled by default; the README tells the user how to change
// that. Later runs load the written file like any other config.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// BootstrapResult is the single configuration snapshot used by command
// startup, runtime resolution, and the settings editor. Config and
// Layer.Config always point at the same object when configuration is enabled.
// Warning records a first-publication failure while keeping a usable in-memory
// default; Layer retains the selected path and the failure reason for Settings.
type BootstrapResult struct {
	Config  *Config
	Layer   LoadedLayer
	Warning error
}

var (
	saveForBootstrap        = Save
	writeReadmeForBootstrap = WriteReadme
)

// Bootstrap selects a layer exactly once and publishes defaults only when that
// selected layer is missing. It never reloads after publication, preventing a
// startup/settings race that could assign different config snapshots.
func Bootstrap(stderr io.Writer) BootstrapResult {
	layer := LoadDefaultWithLayer()
	if layer.State != LayerMissing {
		if layer.State == LayerLoaded {
			// Keyring-by-default: tokens written by an older spoon sit inline in
			// the file; move them once. A no-op when none are inline.
			MigrateInlineSecrets(layer.Path, layer.Config, stderr)
		}
		return BootstrapResult{Config: layer.Config, Layer: layer, Warning: layer.Reason}
	}

	cfg := defaultConfig()
	if err := saveForBootstrap(layer.Path, cfg); err != nil {
		fmt.Fprintf(stderr, "warning: first run: could not write default config to %s: %v (continuing with built-in defaults)\n", layer.Path, err)
		layer.Config, layer.Reason, layer.LoadError = cfg, err, err
		return BootstrapResult{Config: cfg, Layer: layer, Warning: err}
	}
	layer.State, layer.Config, layer.Reason, layer.LoadError = LayerLoaded, cfg, nil, nil

	readmePath := filepath.Join(filepath.Dir(layer.Path), "README.md")
	if err := writeReadmeForBootstrap(layer.Path); err != nil {
		readmePath = "(README write failed: " + err.Error() + ")"
	}
	fmt.Fprintf(stderr, "first run: wrote default config to %s (instructions in %s)\n", layer.Path, readmePath)
	for _, line := range detectHost() {
		fmt.Fprintf(stderr, "  %s\n", line)
	}
	return BootstrapResult{Config: cfg, Layer: layer}
}

// EnsureDefault is retained for commands that do not need layer metadata.
func EnsureDefault(stderr io.Writer) (*Config, error) {
	result := Bootstrap(stderr)
	if result.Layer.State == LayerInvalid {
		return nil, result.Warning
	}
	return result.Config, nil
}

// WriteReadme (re)generates the README.md documenting every config field and
// environment variable, beside the given config path. Called by the first-run
// bootstrap and by `spoon setup`.
func WriteReadme(configPath string) error {
	return os.WriteFile(filepath.Join(filepath.Dir(configPath), "README.md"), []byte(configReadme), 0o644)
}

// defaultConfig is the everything-enabled zero-configuration baseline. The
// forge provider is left empty (detected per repo URL / flags at runtime), and
// fastembed — the only embedder — self-provisions its model on first use.
func defaultConfig() *Config {
	return &Config{
		Version:  CurrentVersion,
		Embedder: EmbedderConfig{Backend: "fastembed"},
	}
}

// detectHost reports what the host offers, for the one-time first-run notice.
// Informational only — nothing here is persisted, and credentials in
// particular never land in the config file.
func detectHost() []string {
	var lines []string
	report := func(present bool, yes, no string) {
		if present {
			lines = append(lines, "✓ "+yes)
		} else {
			lines = append(lines, "- "+no)
		}
	}
	_, glabErr := exec.LookPath("glab")
	report(glabErr == nil, "glab CLI found (GitLab auth via glab auth login)",
		"glab CLI not found — export GITLAB_TOKEN for authenticated GitLab access")
	report(os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "", "GitHub token present in environment",
		"no GH_TOKEN/GITHUB_TOKEN in environment — use `spoon auth login` (requires SPOON_OAUTH_CLIENT_ID and SPOON_OAUTH_CLIENT_SECRET) or export GH_TOKEN")
	report(os.Getenv("GITLAB_TOKEN") != "", "GitLab token present in environment",
		"no GITLAB_TOKEN in environment")
	lines = append(lines, "run `spoon setup` anytime to re-check credentials and the embedder")
	return lines
}
