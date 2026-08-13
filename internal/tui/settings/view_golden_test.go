package settings

import (
	"fmt"
	"strings"
	"testing"

	"github.com/muesli/termenv"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestGoldenSettingsSectionsAt80x24(t *testing.T) {
	profiles := []struct {
		name, color, glyph string
		termenv            termenv.Profile
	}{
		{"truecolor-unicode", "truecolor", "unicode", termenv.TrueColor},
		{"ansi16-unicode", "ansi16", "unicode", termenv.ANSI},
		{"mono-ascii", "mono", "ascii", termenv.Ascii},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			rendertest.Force(t, profile.termenv)
			ctx, err := theme.ResolveContext("dark", "", profile.color, "", profile.glyph)
			if err != nil {
				t.Fatal(err)
			}
			for section := range sections {
				cfg := &config.Config{Forge: config.ForgeConfig{Provider: "gitlab", Host: "forge.example"}, GitHub: config.GitHubConfig{RequestsPerMinute: 300}}
				for _, descriptor := range config.CredentialDescriptors() {
					descriptor.Set(cfg, "sentinel-secret")
				}
				m := Model{Config: cfg, Path: "/settings-golden/config.json", Host: HostFacts{SystemConfig: "absent", StorePath: "/settings-golden/store.db", CachePath: "/settings-golden/cache", Home: "/settings-golden/home"}, Theme: ctx}
				m.width, m.height, m.section = 80, 24, section
				got := m.View()
				if strings.Contains(got, "sentinel-secret") {
					t.Fatalf("secret leaked in %s", sections[section])
				}
				rendertest.Golden(t, fmt.Sprintf("settings_%s_%s", profile.name, strings.ToLower(string(sections[section]))), got)
			}
		})
	}
}
