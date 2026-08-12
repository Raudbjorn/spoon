package ui

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestViewportBoundaries(t *testing.T) {
	cases := []struct {
		width, height int
		tooSmall      bool
	}{
		{79, 24, true},
		{80, 23, true},
		{80, 24, false},
		{120, 30, false},
		{160, 50, false},
		{40, 12, true},
		{0, 0, true},
		{-1, 24, true},
		{80, -1, true},
	}
	for _, tt := range cases {
		t.Run("boundary", func(t *testing.T) {
			if got := TooSmall(tt.width, tt.height); got != tt.tooSmall {
				t.Fatalf("TooSmall(%d, %d) = %t, want %t", tt.width, tt.height, got, tt.tooSmall)
			}
		})
	}
}

func TestFallbackMessageUsesGlyphProfile(t *testing.T) {
	if got, want := FallbackMessage(40, 12), "Terminal too small — requires 80x24, current 40x12"; got != want {
		t.Fatalf("Unicode fallback = %q, want %q", got, want)
	}
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := FallbackMessageFor(ctx, 40, 12), "Terminal too small - requires 80x24, current 40x12"; got != want {
		t.Fatalf("ASCII fallback = %q, want %q", got, want)
	}
}

func TestContentWidth(t *testing.T) {
	for _, tt := range []struct{ input, want int }{{-1, -1}, {0, 0}, {80, 80}, {100, 100}, {120, 120}, {160, 120}} {
		if got := ContentWidth(tt.input); got != tt.want {
			t.Errorf("ContentWidth(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
