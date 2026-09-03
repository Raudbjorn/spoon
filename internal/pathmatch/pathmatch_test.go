package pathmatch

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"cli/engine/registry/antipatterns.mjs", "cli/engine/registry/antipatterns.mjs", true},
		{"cli/engine", "cli/engine/registry/antipatterns.mjs", true}, // dir prefix
		{"cli/eng", "cli/engine/x", false},                           // not a segment prefix
		{"*.mjs", "a.mjs", true},
		{"*.mjs", "cli/a.mjs", false}, // single segment
		{"**/*.mjs", "cli/a.mjs", true},
		{"**/*.mjs", "a.mjs", true}, // ** matches zero segments
		{"**/registry/antipatterns.mjs", ".claude/skills/impeccable/scripts/detector/registry/antipatterns.mjs", true},
		{"cli/**", "cli/engine/x.js", true},
		{"cli/**/x.js", "cli/x.js", true},
		{"cli/**/x.js", "lib/x.js", false},
		{"tests/**/*.html", "tests/fixtures/antipatterns/label.html", true},
	}
	for _, tc := range cases {
		got, err := Match(tc.pattern, tc.name)
		if err != nil {
			t.Fatalf("Match(%q,%q): %v", tc.pattern, tc.name, err)
		}
		if got != tc.want {
			t.Errorf("Match(%q,%q)=%v want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestMatchBadPattern(t *testing.T) {
	if _, err := Match("[", "x"); err == nil {
		t.Fatal("expected ErrBadPattern")
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"./src/": "src", "src/a.go": "src/a.go"} {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "/", "/abs", "../x", "a/../b"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q): expected error", bad)
		}
	}
}

func TestCompileFirst(t *testing.T) {
	m, err := Compile([]string{"docs/", "**/*.mjs"})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := m.First("cli/a.mjs"); !ok || p != "**/*.mjs" {
		t.Errorf("First = %q,%v", p, ok)
	}
	if p, ok := m.First("docs/x.md"); !ok || p != "docs" {
		t.Errorf("First = %q,%v", p, ok)
	}
	if _, ok := m.First("README"); ok {
		t.Error("unexpected match")
	}
	if _, err := Compile([]string{"["}); err == nil {
		t.Error("expected compile error")
	}
}
