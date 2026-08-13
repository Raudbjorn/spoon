package setupcheck

import (
	"context"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestLocalProviderProbeUsesInjectedEnvironmentSnapshot(t *testing.T) {
	t.Setenv("GH_TOKEN", "ambient-token")
	auth, err := LocalProviderProbe(context.Background(), ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: map[string]string{},
	}, DenyHTTPTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Tier == forge.AuthToken {
		t.Fatal("ambient GH_TOKEN bypassed injected environment snapshot")
	}
}
