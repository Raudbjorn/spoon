package genai

// Labeler model comparison: run representative cluster hints through each
// candidate and report labels + latency. Quality gates: non-empty,
// single-line, ≤60 chars, mentions the dominant topic term.
//
// Requires a loadable openvino-genai runtime (see genai.go).
//
//	SPOON_EVAL_LABELERS="/path/model,..." \
//	  go test -run TestEvalLabelers_Manual -v ./internal/genai/

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/cluster"
)

var labelerFixtures = []struct {
	hint  cluster.PolishHint
	topic string // must appear in a good label
}{
	{
		hint: cluster.PolishHint{
			Heuristic:    "compositor/  ·  wayland, protocol, xdg",
			UpstreamRepo: "example/compositor",
			SampleCommits: []string{
				"add wayland protocol support",
				"implement xdg-shell v6",
				"drop x11-only assumptions in the render loop",
			},
			SamplePaths: []string{"compositor/wayland.c", "compositor/xdg_shell.c"},
		},
		topic: "wayland",
	},
	{
		hint: cluster.PolishHint{
			Heuristic:    "auth/  ·  oauth, token, refresh",
			UpstreamRepo: "example/server",
			SampleCommits: []string{
				"add oauth2 token refresh",
				"support PKCE in the login flow",
				"persist refresh tokens encrypted",
			},
			SamplePaths: []string{"auth/oauth.go", "auth/token.go"},
		},
		topic: "oauth",
	},
	{
		hint: cluster.PolishHint{
			Heuristic:    "po/  ·  translations, locale",
			UpstreamRepo: "example/editor",
			SampleCommits: []string{
				"update German translation",
				"add Icelandic locale",
				"regenerate po files",
			},
			SamplePaths: []string{"po/de.po", "po/is.po"},
		},
		topic: "translation",
	},
}

func TestEvalLabelers_Manual(t *testing.T) {
	spec := os.Getenv("SPOON_EVAL_LABELERS")
	if spec == "" {
		t.Skip("SPOON_EVAL_LABELERS not set")
	}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		p, err := NewLabelPolisher(Config{ModelPath: entry})
		if err != nil {
			t.Errorf("load %s: %v", entry, err)
			continue
		}
		var pass int
		var firstLatency, totalLatency time.Duration
		for i, fx := range labelerFixtures {
			start := time.Now()
			label, lerr := p.PolishLabel(context.Background(), fx.hint)
			d := time.Since(start)
			if i == 0 {
				firstLatency = d
			}
			totalLatency += d
			ok := lerr == nil && label != "" && len([]rune(label)) <= 60 &&
				!strings.Contains(label, "\n") &&
				strings.Contains(strings.ToLower(label), fx.topic)
			if ok {
				pass++
			}
			t.Logf("  %-40s [%v, %s] %q", filepath.Base(entry), ok, d.Round(time.Millisecond), label)
		}
		t.Logf("%-40s pass=%d/%d first=%s avg-after-warm=%s",
			filepath.Base(entry), pass, len(labelerFixtures), firstLatency.Round(time.Millisecond),
			((totalLatency - firstLatency) / time.Duration(len(labelerFixtures)-1)).Round(time.Millisecond))
		p.Close()
	}
}
