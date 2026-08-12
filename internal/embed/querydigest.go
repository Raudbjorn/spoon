package embed

import (
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
)

// QueryDigestMaxChars bounds the digest handed to a query scorer; cross-encoder
// rows are capped at the model context anyway, and the leading chunk carries
// the signal.
const QueryDigestMaxChars = 2000

// QueryDigest renders a fork's change digest for query scoring: commit
// subjects first (the strongest intent signal), then touched paths.
//
// It lives here, beside BuildFeatures, because more than one caller needs it —
// forksops scores `--query` with it and the TUI's fork filter scores against the
// same text. Two digest builders would let the CLI and the TUI rank the same
// fork differently for the same query.
func QueryDigest(t2 *forge.T2Data) string {
	if t2 == nil {
		return ""
	}
	f := BuildFeatures(*t2, "", 0)
	var b strings.Builder
	b.WriteString(f.Commits)
	if f.Paths != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(f.Paths)
	}
	d := b.String()
	if runes := []rune(d); len(runes) > QueryDigestMaxChars {
		d = string(runes[:QueryDigestMaxChars])
	}
	return d
}
