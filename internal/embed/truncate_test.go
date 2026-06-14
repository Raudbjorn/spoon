package embed

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTruncateForEmbed(t *testing.T) {
	if got := truncateForEmbed("short"); got != "short" {
		t.Errorf("short string changed: %q", got)
	}
	long := strings.Repeat("a", maxEmbedModalityChars+500)
	got := truncateForEmbed(long)
	if len([]rune(got)) != maxEmbedModalityChars {
		t.Errorf("len=%d want %d", len([]rune(got)), maxEmbedModalityChars)
	}
	// Rune-safe: multi-byte runes must not be split.
	multi := strings.Repeat("é", maxEmbedModalityChars+10) // 2 bytes each
	g := truncateForEmbed(multi)
	if !json.Valid([]byte(`"` + g + `"`)) {
		t.Error("truncation split a multi-byte rune")
	}
	if len([]rune(g)) != maxEmbedModalityChars {
		t.Errorf("rune count=%d want %d", len([]rune(g)), maxEmbedModalityChars)
	}
}
