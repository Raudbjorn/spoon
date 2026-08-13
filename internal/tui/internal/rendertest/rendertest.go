// Package rendertest provides deterministic ANSI golden helpers for TUI tests.
package rendertest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Force pins Lip Gloss to p until the test completes. Existing Spoon styles
// retain the default renderer created during package initialization, so this
// intentionally mutates that renderer rather than substituting a new one.
// Tests using Force must not call t.Parallel.
func Force(t *testing.T, p termenv.Profile) {
	t.Helper()

	previousRenderer := lipgloss.DefaultRenderer()
	previousProfile := previousRenderer.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() {
		lipgloss.SetDefaultRenderer(previousRenderer)
		lipgloss.SetColorProfile(previousProfile)
	})
}

// Golden compares got to testdata/<name>.golden next to the calling test file.
// UPDATE_GOLDEN=1 permits regeneration outside CI; missing goldens otherwise
// fail so the first run cannot pass vacuously.
func Golden(t *testing.T, name, got string) {
	t.Helper()

	if err := updateGoldenEnvironment(); err != nil {
		t.Fatal(err)
	}

	_, caller, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("locating golden test caller")
	}
	path := filepath.Join(filepath.Dir(caller), "testdata", name+".golden")

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating golden directory for %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("updating golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden %s is missing; rerun with UPDATE_GOLDEN=1 outside CI to create it", path)
		}
		t.Fatalf("reading golden %s: %v", path, err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s (-want +got):\n--- want\n%q\n+++ got\n%q", path, string(want), got)
	}
}

func updateGoldenEnvironment() error {
	if os.Getenv("UPDATE_GOLDEN") != "" && truthy(os.Getenv("CI")) {
		return fmt.Errorf("UPDATE_GOLDEN is forbidden when CI is truthy")
	}
	return nil
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
