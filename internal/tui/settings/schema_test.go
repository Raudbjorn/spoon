package settings

import (
	"reflect"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestRegistryCoversPersistedConfigLeaves(t *testing.T) {
	want := map[string]bool{
		"version": true, "forge.provider": true, "forge.host": true,
		"github.tokens": true, "github.requestsPerMinute": true,
		"github.proxy.enabled": true, "github.proxy.apiKeyFile": true,
		"github.proxy.staticFile": true, "github.proxy.whitelistPublicIp": true, "github.proxy.cacheTtl": true,
		"embedder.backend": true, "embedder.model": true, "embedder.cacheDir": true, "embedder.maxLength": true, "embedder.batchSize": true,
		"embedder.voyage.disabled": true, "embedder.voyage.apiKeyFile": true, "embedder.voyage.embedModel": true, "embedder.voyage.rerankModel": true, "embedder.voyage.outputDimension": true, "embedder.voyage.baseUrl": true,
		"ui.theme": true, "ui.color": true, "ui.glyphs": true,
	}
	got := map[string]bool{}
	for _, field := range Registry {
		if got[field.Key] {
			t.Fatalf("duplicate registry key %q", field.Key)
		}
		got[field.Key] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registry mismatch\ngot:  %v\nwant: %v", got, want)
	}
	_ = config.Config{} // make the config contract explicit in this package.
}

func TestEveryDocumentedEnvironmentVariableIsExposed(t *testing.T) {
	for _, name := range DocumentedEnvironment {
		if _, ok := Environment[name]; !ok {
			t.Errorf("missing environment entry %s", name)
		}
	}
}
