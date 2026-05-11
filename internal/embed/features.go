package embed

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
)

const defaultMaxDiffChars = 4000

// ForkFeatures holds the four per-fork modality blobs.
type ForkFeatures struct {
	Paths     string
	Commits   string
	ReadmeDoc string
	DiffChunk string
}

// BuildFeatures composes the four modality strings from T2Data plus an
// optional README delta string. DiffChunk is constructed largest-hunk-first
// (a hunk here is a single FileDiff entry, ranked by Additions+Deletions)
// until maxDiffChars is reached; maxDiffChars defaults to 4000 when zero.
// NormalizeDiff is applied to the assembled diff before storing.
func BuildFeatures(t2 forge.T2Data, readme string, maxDiffChars int) ForkFeatures {
	if maxDiffChars <= 0 {
		maxDiffChars = defaultMaxDiffChars
	}
	return ForkFeatures{
		Paths:     buildPaths(t2.Diffs),
		Commits:   buildCommits(t2.Commits),
		ReadmeDoc: strings.TrimSpace(readme),
		DiffChunk: NormalizeDiff(buildDiffChunk(t2.Diffs, maxDiffChars)),
	}
}

func buildPaths(diffs []forge.FileDiff) string {
	if len(diffs) == 0 {
		return ""
	}
	paths := make([]string, 0, len(diffs))
	seen := make(map[string]struct{}, len(diffs))
	for _, d := range diffs {
		if d.Path == "" {
			continue
		}
		if _, ok := seen[d.Path]; ok {
			continue
		}
		seen[d.Path] = struct{}{}
		paths = append(paths, d.Path)
	}
	sort.Strings(paths)
	return strings.Join(paths, "\n")
}

var mergeMessage = regexp.MustCompile(`(?im)^\s*Merge\b`)

func buildCommits(commits []forge.AheadCommit) string {
	if len(commits) == 0 {
		return ""
	}
	out := make([]string, 0, len(commits))
	seen := make(map[string]struct{}, len(commits))
	for _, c := range commits {
		msg := strings.TrimSpace(c.Message)
		if msg == "" {
			continue
		}
		if mergeMessage.MatchString(msg) {
			continue
		}
		key := strings.ToLower(msg)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, msg)
	}
	return strings.Join(out, "\n")
}

func buildDiffChunk(diffs []forge.FileDiff, maxChars int) string {
	if len(diffs) == 0 || maxChars <= 0 {
		return ""
	}
	ranked := make([]forge.FileDiff, len(diffs))
	copy(ranked, diffs)
	sort.SliceStable(ranked, func(i, j int) bool {
		ci := ranked[i].Additions + ranked[i].Deletions
		cj := ranked[j].Additions + ranked[j].Deletions
		if ci == cj {
			return ranked[i].Path < ranked[j].Path
		}
		return ci > cj
	})
	var b strings.Builder
	first := true
	for _, d := range ranked {
		line := diffLine(d)
		if line == "" {
			continue
		}
		// Always include the first (largest) entry, even when it exceeds the
		// budget. Subsequent entries are gated on the budget so callers still
		// get *some* signal for the dominant change.
		if !first {
			extra := len(line) + 1 // for the separating newline
			if b.Len()+extra > maxChars {
				break
			}
			b.WriteByte('\n')
		}
		b.WriteString(line)
		first = false
	}
	return b.String()
}

func diffLine(d forge.FileDiff) string {
	if d.Path == "" {
		return ""
	}
	del := d.Deletions
	if del < 0 {
		del = 0
	}
	add := d.Additions
	if add < 0 {
		add = 0
	}
	var b strings.Builder
	b.WriteString("--- a/")
	b.WriteString(d.Path)
	b.WriteString("\n+++ b/")
	b.WriteString(d.Path)
	b.WriteString("\n@@ -0,")
	b.WriteString(strconv.Itoa(del))
	b.WriteString(" +0,")
	b.WriteString(strconv.Itoa(add))
	b.WriteString(" @@")
	return b.String()
}

var (
	hunkHeader = regexp.MustCompile(`@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@`)
	wsRun      = regexp.MustCompile(`[ \t]+`)
	identTok   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// NormalizeDiff applies cheap text-level normalization to reduce lexical
// variance: collapses whitespace runs (preserving newlines), strips
// line-number tails from hunk headers (so "@@ -10,5 +10,5 @@" becomes
// "@@@@"), and lowercases pure-identifier tokens. Numbers, symbols, and
// mixed tokens are untouched.
func NormalizeDiff(diff string) string {
	if diff == "" {
		return ""
	}
	diff = hunkHeader.ReplaceAllString(diff, "@@@@")
	lines := strings.Split(diff, "\n")
	for li, line := range lines {
		line = wsRun.ReplaceAllString(line, " ")
		parts := strings.Split(line, " ")
		for i, tok := range parts {
			if identTok.MatchString(tok) {
				parts[i] = strings.ToLower(tok)
			}
		}
		lines[li] = strings.Join(parts, " ")
	}
	return strings.Join(lines, "\n")
}
