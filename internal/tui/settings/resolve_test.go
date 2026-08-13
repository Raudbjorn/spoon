package settings

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestResolveShowsEffectiveSourceAndInactiveFile(t *testing.T) {
	t.Setenv("SPOON_GITHUB_RPM", "500")
	cfg := &config.Config{GitHub: config.GitHubConfig{RequestsPerMinute: 300}}
	row := Resolve(FieldByMust("github.requestsPerMinute"), cfg, nil)
	if row.Value != "500" || row.Source != EnvironmentSource || row.Inactive != "saved to config.json; a higher-priority override is active until removed." {
		t.Fatalf("row=%+v", row)
	}
	if got := Resolve(FieldByMust("github.requestsPerMinute"), cfg, nil); got.Source != EnvironmentSource {
		t.Fatal("environment did not win")
	}
	t.Setenv("SPOON_GITHUB_RPM", "")
	if got := Resolve(FieldByMust("github.requestsPerMinute"), cfg, nil); got.Value != "300" || got.Source != FileSource {
		t.Fatalf("file row=%+v", got)
	}
	cfg.GitHub.RequestsPerMinute = 0
	if got := Resolve(FieldByMust("github.requestsPerMinute"), cfg, nil); got.Value != "300" || got.Source != DefaultSource {
		t.Fatalf("default row=%+v", got)
	}
}

func TestResolveFlagWins(t *testing.T) {
	cfg := &config.Config{Forge: config.ForgeConfig{Provider: "gitlab"}}
	got := Resolve(FieldByMust("forge.provider"), cfg, map[string]string{"forge.provider": "github"})
	if got.Source != FlagSource || got.Value != "github" {
		t.Fatalf("%+v", got)
	}
}
