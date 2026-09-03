package tui

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/pathmatch"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestRenderPatchFiltersAndColours(t *testing.T) {
	t2 := forge.T2Data{Diffs: []forge.FileDiff{
		{Path: "a/registry/antipatterns.mjs", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
		{Path: "README.md", Status: "modified", Patch: "@@ -1 +1 @@\n-x\n+y\n"},
	}}
	m, _ := pathmatch.Compile([]string{"**/antipatterns.mjs"})
	out := renderPatch(theme.Context{}, t2, &m, 200_000)
	if !strings.Contains(out, "a/registry/antipatterns.mjs") || strings.Contains(out, "README.md") {
		t.Fatalf("filter not applied:\n%s", out)
	}
	if !strings.Contains(out, "+new") {
		t.Fatal("patch body missing")
	}
	if out2 := renderPatch(theme.Context{}, t2, nil, 10); !strings.Contains(out2, "truncated") {
		t.Fatal("cap must announce truncation")
	}
}

func TestPatchKeymap(t *testing.T) {
	if keymap.Dispatch(keymap.MainDetail, "p") != keymap.ViewPatch {
		t.Fatal("p must open the patch view from detail")
	}
	if keymap.Dispatch(keymap.MainPatch, "esc") != keymap.Back {
		t.Fatal("esc must close the patch view")
	}
}
