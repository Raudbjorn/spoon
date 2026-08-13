package settings

import (
	"github.com/svnbjrn/spoon/internal/config"
	"strings"
	"testing"
)

func TestLoadedSystemLayerRetainsHostwideIdentity(t *testing.T) {
	m := NewFromLayer(config.LoadedLayer{Config: &config.Config{}, Path: "/fixture/system.json", System: true, State: config.LayerLoaded}, nil)
	if !m.systemLayer || !strings.Contains(m.View(), "affects every user") {
		t.Fatalf("system layer identity lost: %#v\n%s", m.systemLayer, m.View())
	}
}
