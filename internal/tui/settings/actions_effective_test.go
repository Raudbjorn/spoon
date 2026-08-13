package settings

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestFastEmbedActionValidatesRuntimeEffectiveConfiguration(t *testing.T) {
	cfg := &config.Config{}
	model := Model{
		Config:      cfg,
		Path:        t.TempDir() + "/config.json",
		Environment: map[string]string{"SPOON_FASTEMBED_MODEL": "unsupported"},
	}
	_, err := runActionWithDeps(ActionFastEmbedCheck, &model, ActionDeps{})
	if err == nil || !strings.Contains(err.Error(), "fixed") {
		t.Fatalf("startup snapshot override was not checked: %v", err)
	}
}
