package settings

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestFastEmbedActionValidatesRuntimeEffectiveConfiguration(t *testing.T) {
	t.Setenv("SPOON_FASTEMBED_MODEL", "unsupported")
	_, err := runAction(ActionFastEmbedCheck, &config.Config{}, t.TempDir()+"/config.json", nil)
	if err == nil || !strings.Contains(err.Error(), "fixed") {
		t.Fatalf("runtime override was not checked: %v", err)
	}
}
