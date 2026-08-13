package settings

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/setupcheck"
)

// Every action receives only injected local dependencies; the fail-closed
// transport makes any accidental request fail this test.
func TestEveryActionCompletesWithoutNetworkTransport(t *testing.T) {
	t.Setenv("VOYAGE_AI_API_KEY", "")
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("SPOON_FASTEMBED_MODEL", "")
	t.Setenv("SPOON_FASTEMBED_CACHE", "")
	path := filepath.Join(t.TempDir(), "config.json")
	m := New(&config.Config{}, path, writableCache{}).WithActionDeps(ActionDeps{
		HTTPTransport: failingRoundTripper{t},
		Provider: func(context.Context, setupcheck.ProviderInput, http.RoundTripper) (forge.AuthInfo, error) {
			return forge.AuthInfo{Provider: forge.ProviderGitHub, Tier: forge.AuthCLI}, nil
		},
		StoreOpen:     func() (setupcheck.Store, error) { return actionStore{}, nil },
		Clipboard:     func(string) error { return nil },
		Save:          config.Save,
		RewriteReadme: config.WriteReadme,
	})
	for _, action := range Actions {
		if _, err := runActionWithDeps(action.ID, &m, m.deps); err != nil {
			t.Fatalf("%s: %v", action.ID, err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("save did not publish isolated config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "README.md")); err != nil {
		t.Fatalf("README action did not run: %v", err)
	}
}
