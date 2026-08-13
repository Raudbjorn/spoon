package settings

import (
	"github.com/svnbjrn/spoon/internal/config"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingLayerIsEditableNotNoConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := NewFromLayer(config.LoadedLayer{Path: path, State: config.LayerMissing}, nil)
	if m.noConfig || m.readOnly != "" || !m.canEdit() {
		t.Fatalf("missing layer incorrectly disabled: noConfig=%v readOnly=%q", m.noConfig, m.readOnly)
	}
	if strings.Contains(m.View(), "disabled by SPOON_NO_CONFIG") {
		t.Fatal("missing layer rendered as disabled")
	}
}

func TestInvalidLayerIsReadOnlyWithoutNoConfigClaim(t *testing.T) {
	m := NewFromLayer(config.LoadedLayer{Path: filepath.Join(t.TempDir(), "config.json"), State: config.LayerInvalid}, nil)
	if m.noConfig || m.canEdit() || !strings.Contains(m.View(), "invalid or unreadable") {
		t.Fatalf("invalid layer state not preserved: %#v", m)
	}
}
