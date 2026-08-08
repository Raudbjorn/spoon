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

// EnsureDefault returns the effective config, bootstrapping one on first run.
//
// When a config already exists (user path, or the /etc/spoon fallback) it is
// simply loaded. When none exists, a default config — fastembed embedder
// enabled, provider auto-detected at runtime, proxying off — is written to
// DefaultPath() together with a README describing every field and environment
// variable, and a one-line notice plus a short host-detection report goes to
// stderr. Credentials are never written: they stay with gh/glab and the token
// environment variables.
//
// A failed write (e.g. /etc/spoon as non-root) degrades to a warning and the
// in-memory defaults — the config file is a convenience; only the store is
// mandatory. SPOON_NO_CONFIG=1 skips everything and returns nil.
func EnsureDefault(stderr io.Writer) (*Config, error) {
	if os.Getenv("SPOON_NO_CONFIG") == "1" {
		return nil, nil
	}
	if cfg, err := LoadDefault(); cfg != nil || err != nil {
		return cfg, err
	}
	// Distinguish "no config anywhere" from "config present but empty-ish":
	// LoadDefault returns (nil, nil) only when neither path has a file.

	cfg := defaultConfig()
	path, err := DefaultPath()
	if err != nil {
		return cfg, nil
	}
	if err := Save(path, cfg); err != nil {
		fmt.Fprintf(stderr, "warning: first run: could not write default config to %s: %v (continuing with built-in defaults)\n", path, err)
		return cfg, nil
	}
	readmePath := filepath.Join(filepath.Dir(path), "README.md")
	if err := WriteReadme(path); err != nil {
		readmePath = "(README write failed: " + err.Error() + ")"
	}
	fmt.Fprintf(stderr, "first run: wrote default config to %s (instructions in %s)\n", path, readmePath)
	for _, line := range detectHost() {
		fmt.Fprintf(stderr, "  %s\n", line)
	}
	return cfg, nil
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
	_, ghErr := exec.LookPath("gh")
	report(ghErr == nil, "gh CLI found (GitHub auth via gh auth login)",
		"gh CLI not found — install https://cli.github.com or export GH_TOKEN for authenticated GitHub access")
	_, glabErr := exec.LookPath("glab")
	report(glabErr == nil, "glab CLI found (GitLab auth via glab auth login)",
		"glab CLI not found — export GITLAB_TOKEN for authenticated GitLab access")
	report(os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "", "GitHub token present in environment",
		"no GH_TOKEN/GITHUB_TOKEN in environment")
	report(os.Getenv("GITLAB_TOKEN") != "", "GitLab token present in environment",
		"no GITLAB_TOKEN in environment")
	lines = append(lines, "run `spoon setup` anytime to re-check credentials and the embedder")
	return lines
}
