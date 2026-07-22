package embed

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
)

// defaultMaxDiffChars caps the assembled diff. The fastembed window is 512
// tokens (~1.5-2k chars); when the diff shares a single embedding document with
// other sections (the semantic index), a larger budget only produces bytes the
// encoder discards. 2000 keeps the diff within reach of the window while leaving
// room for the higher-signal sections that precede it.
const defaultMaxDiffChars = 2000

// ForkFeatures holds the four per-fork modality blobs.
type ForkFeatures struct {
	Paths     string
	Commits   string
	ReadmeDoc string
	DiffChunk string
	// DiffTruncated is true when the diff exceeded maxDiffChars and was cut. It
	// lets callers surface a truncation signal rather than silently indexing a
	// partial diff.
	DiffTruncated bool
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
	diff, truncated := buildDiffChunk(t2.Diffs, maxDiffChars)
	return ForkFeatures{
		Paths:         buildPaths(t2.Diffs),
		Commits:       buildCommits(t2.Commits),
		ReadmeDoc:     strings.TrimSpace(readme),
		DiffChunk:     NormalizeDiff(diff),
		DiffTruncated: truncated,
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

func buildDiffChunk(diffs []forge.FileDiff, maxChars int) (string, bool) {
	if len(diffs) == 0 || maxChars <= 0 {
		return "", false
	}
	ranked := append([]forge.FileDiff(nil), diffs...)
	sort.SliceStable(ranked, func(i, j int) bool {
		ci := ranked[i].Additions + ranked[i].Deletions
		cj := ranked[j].Additions + ranked[j].Deletions
		if ci == cj {
			return ranked[i].Path < ranked[j].Path
		}
		return ci > cj
	})
	var out []rune
	truncated := false
	for _, d := range ranked {
		chunk := diffLine(d)
		if chunk == "" {
			continue
		}
		// Reserve the separating newline's slot before spending the budget, so
		// an exact-fit first entry can't push the result to maxChars+1.
		sep := 0
		if len(out) > 0 {
			sep = 1
		}
		remaining := maxChars - len(out) - sep
		if remaining <= 0 {
			// Budget spent with diffs still pending — the assembled chunk omits
			// content, so report truncation.
			truncated = true
			break
		}
		if sep == 1 {
			out = append(out, '\n')
		}
		// Decode only up to `remaining` runes directly from the string, avoiding
		// a full []rune(chunk) allocation for a potentially large patch.
		count := 0
		for _, r := range chunk {
			if count >= remaining {
				truncated = true
				break
			}
			out = append(out, r)
			count++
		}
		if truncated {
			break
		}
	}
	return string(out), truncated
}

func diffLine(d forge.FileDiff) string {
	if d.Path == "" {
		return ""
	}
	if d.Patch != "" {
		if hunkHeader.MatchString(d.Patch) {
			return d.Patch
		}
		// A headerless patch — github_web scrapes bare +/- lines with no
		// ---/+++ or @@ header. NormalizeDiff rewrites a real hunk header to
		// "@@@@", so leaving these alone makes an identical change embed
		// differently depending on which source fetched it. Detect the missing
		// header rather than matching PatchSource, so any future headerless
		// source converges on the same canonical form.
		return diffFileHeader(d) + "\n" + d.Patch
	}
	return diffFileHeader(d)
}

// diffFileHeader builds the canonical ---/+++/@@ preamble shared by every
// patch source, so normalization collapses them to the same shape.
func diffFileHeader(d forge.FileDiff) string {
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
	// Anchored to the start of a line (multiline) so a real hunk header is
	// stripped but a source line that merely contains "@@ -10,5 +10,5 @@" is not
	// corrupted.
	hunkHeader = regexp.MustCompile(`(?m)^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@`)
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
