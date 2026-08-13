package settings

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

type writableCache struct{ err error }

func (c writableCache) VoyageCacheGetMany(context.Context, []string) (map[string][]byte, error) {
	return nil, nil
}
func (c writableCache) VoyageCachePutMany(context.Context, string, string, map[string][]byte) error {
	return nil
}
func (c writableCache) VoyageCacheWritable(context.Context) error { return c.err }

func TestVoyageDiagnosticsAreLocalAndClassified(t *testing.T) {
	t.Setenv("VOYAGE_AI_API_KEY", "")
	t.Setenv("VOYAGE_API_KEY", "")
	cases := []struct {
		name  string
		cfg   *config.Config
		cache writableCache
		want  string
	}{
		{"disabled-layer", nil, writableCache{}, "not configured"},
		{"no-key", &config.Config{}, writableCache{}, "not configured"},
		{"disabled", &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: true}}}, writableCache{}, "not configured"},
		{"bad-key-file", &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: t.TempDir() + "/missing"}}}, writableCache{}, "not configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := voyageDiagnostic(tc.cfg, tc.cache)
			if err != nil || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
	t.Setenv("VOYAGE_AI_API_KEY", "key")
	if got, err := voyageDiagnostic(&config.Config{}, nil); err == nil || !strings.Contains(got, "unusable") {
		t.Fatalf("no-store = %q, %v", got, err)
	}
	if got, err := voyageDiagnostic(&config.Config{}, writableCache{err: errors.New("read-only")}); err == nil || !strings.Contains(got, "unusable") {
		t.Fatalf("read-only = %q, %v", got, err)
	}
	if got, err := voyageDiagnostic(&config.Config{}, writableCache{}); err != nil || !strings.Contains(got, "active") {
		t.Fatalf("active = %q, %v", got, err)
	}
}
