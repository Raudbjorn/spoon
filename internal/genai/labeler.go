package genai

import (
	"context"
	"strings"

	"github.com/svnbjrn/spoon/internal/cluster"
)

// LabelPolisher adapts a Generator to cluster.LabelPolisher: it asks the
// model for a short, human-readable title for one fork cluster.
type LabelPolisher struct {
	g *Generator
}

// NewLabelPolisher loads the labeler model. Errors when the binary lacks
// the genai tag or the model cannot load.
func NewLabelPolisher(cfg Config) (*LabelPolisher, error) {
	g, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &LabelPolisher{g: g}, nil
}

// Close releases the underlying generator.
func (l *LabelPolisher) Close() { l.g.Close() }

const labelSystemPrompt = "You title clusters of GitHub repository forks. " +
	"Reply with the title only: 3 to 7 plain words describing what the forks change. " +
	"No quotes, no punctuation at the end, no explanations."

const maxLabelLen = 60

// PolishLabel implements cluster.LabelPolisher.
func (l *LabelPolisher) PolishLabel(ctx context.Context, hint cluster.PolishHint) (string, error) {
	var b strings.Builder
	b.WriteString("These forks of ")
	b.WriteString(hint.UpstreamRepo)
	b.WriteString(" were grouped together by what they change.\n")
	if hint.Heuristic != "" {
		// Present the heuristic as keywords, not as a draft to imitate —
		// models otherwise copy its "dir/  ·  a, b" formatting verbatim.
		b.WriteString("Keyword hints: ")
		b.WriteString(strings.NewReplacer("  ·  ", " ", "/", " ", ",", " ").Replace(hint.Heuristic))
		b.WriteString("\n")
	}
	if len(hint.SampleCommits) > 0 {
		b.WriteString("Sample commit subjects:\n")
		for _, c := range hint.SampleCommits {
			b.WriteString("- ")
			b.WriteString(c)
			b.WriteString("\n")
		}
	}
	if len(hint.SamplePaths) > 0 {
		b.WriteString("Touched paths:\n")
		for _, p := range hint.SamplePaths {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	b.WriteString("Write a natural English title (3-7 words) for this group of forks.")

	out, err := l.g.Generate(ctx, labelSystemPrompt, b.String())
	if err != nil {
		return "", err
	}
	return CleanLabel(out), nil
}

// CleanLabel normalizes a model reply into a label: first line only,
// stripped of wrapping quotes/backticks and trailing punctuation, capped at
// maxLabelLen runes (cut at a word boundary).
func CleanLabel(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.TrimRight(s, ".!:;,")
	s = strings.TrimSpace(s)
	if len([]rune(s)) > maxLabelLen {
		runes := []rune(s)[:maxLabelLen]
		cut := string(runes)
		if i := strings.LastIndexByte(cut, ' '); i > 0 {
			cut = cut[:i]
		}
		s = strings.TrimSpace(cut)
	}
	return s
}
