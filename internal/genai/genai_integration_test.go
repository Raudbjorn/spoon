//go:build genai

package genai

import (
	"context"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/config"
)

// TestLabelPolisher_RealModel loads the configured labeler model and polishes
// a synthetic cluster label. Gated on a labeler being configured in spoon's
// config file (run `spoon setup` first); skipped otherwise.
func TestLabelPolisher_RealModel(t *testing.T) {
	cfg, err := config.LoadDefault()
	if err != nil || cfg == nil || cfg.Labeler.ModelPath == "" {
		t.Skip("no labeler configured; run 'spoon setup' first")
	}
	p, err := NewLabelPolisher(Config{ModelPath: cfg.Labeler.ModelPath, Device: cfg.Labeler.Device})
	if err != nil {
		t.Fatalf("NewLabelPolisher: %v", err)
	}
	defer p.Close()

	label, err := p.PolishLabel(context.Background(), cluster.PolishHint{
		Heuristic:    "compositor/  ·  wayland, protocol, xdg",
		UpstreamRepo: "example/compositor",
		SampleCommits: []string{
			"add wayland protocol support",
			"implement xdg-shell v6",
			"drop x11-only assumptions in the render loop",
		},
		SamplePaths: []string{"compositor/wayland.c", "compositor/xdg_shell.c"},
	})
	if err != nil {
		t.Fatalf("PolishLabel: %v", err)
	}
	t.Logf("polished label: %q", label)
	if label == "" {
		t.Fatal("expected a non-empty label")
	}
	if len([]rune(label)) > 60 {
		t.Errorf("label too long: %d runes", len([]rune(label)))
	}
	if strings.Contains(label, "\n") {
		t.Errorf("label must be single-line: %q", label)
	}
	if !strings.Contains(strings.ToLower(label), "wayland") {
		t.Errorf("label should mention the dominant topic (wayland): %q", label)
	}
}
