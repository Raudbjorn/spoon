package genai

import (
	"strings"
	"testing"
)

func TestCleanLabel(t *testing.T) {
	cases := map[string]string{
		"\"Wayland compositor support\"": "Wayland compositor support",
		"`ci pipeline overhaul`":         "ci pipeline overhaul",
		"Security hardening patches.":    "Security hardening patches",
		"Title here\nand an explanation": "Title here",
		"  padded   ":                    "padded",
		"":                               "",
	}
	for in, want := range cases {
		if got := CleanLabel(in); got != want {
			t.Errorf("CleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("word ", 30)
	got := CleanLabel(long)
	if len([]rune(got)) > maxLabelLen {
		t.Errorf("long label not capped: %d runes", len([]rune(got)))
	}
	if strings.HasSuffix(got, " ") || strings.Contains(got, "wor d") {
		t.Errorf("cap should land on a word boundary: %q", got)
	}
}
