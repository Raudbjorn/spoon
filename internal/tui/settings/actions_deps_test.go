package settings

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/setupcheck"
)

type failingRoundTripper struct{ t *testing.T }

func (rt failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.t.Fatal("settings action attempted HTTP")
	return nil, errors.New("HTTP is forbidden in settings actions")
}

type actionStore struct{}

func (actionStore) Close() error                              { return nil }
func (actionStore) VoyageCacheWritable(context.Context) error { return nil }

func TestEveryActionUsesInjectedLocalDependencies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("VOYAGE_AI_API_KEY", "")
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("SPOON_FASTEMBED_MODEL", "")
	t.Setenv("SPOON_FASTEMBED_CACHE", "")
	var copied string
	providerCalls, storeCalls := 0, 0
	deps := ActionDeps{
		HTTPTransport: failingRoundTripper{t},
		Provider: func(_ context.Context, input setupcheck.ProviderInput, transport http.RoundTripper) (forge.AuthInfo, error) {
			providerCalls++
			if input.Provider != forge.ProviderGitHub || transport == nil {
				t.Fatalf("provider input = %#v, transport = %T", input, transport)
			}
			return forge.AuthInfo{Provider: forge.ProviderGitHub, Tier: forge.AuthCLI}, nil
		},
		StoreOpen: func() (setupcheck.Store, error) {
			storeCalls++
			return actionStore{}, nil
		},
		Clipboard:     func(value string) error { copied = value; return nil },
		Save:          config.Save,
		RewriteReadme: config.WriteReadme,
	}
	m := New(&config.Config{}, path, writableCache{}).WithActionDeps(deps)
	for _, action := range Actions {
		if _, err := runActionWithDeps(action.ID, &m, m.deps); err != nil {
			t.Fatalf("%s: %v", action.ID, err)
		}
	}
	if providerCalls != 1 || storeCalls != 1 {
		t.Fatalf("provider/store calls = %d/%d, want 1/1", providerCalls, storeCalls)
	}
	if copied != path {
		t.Fatalf("clipboard = %q, want %q", copied, path)
	}
}
