package cluster

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/svnbjrn/spoon/internal/embed"
)

// LabelerContext is the input given to an optional LLM polish step.
type LabelerContext struct {
	Heuristic        string               // the deterministic label from HeuristicLabel
	Members          []embed.ForkFeatures // up to 5 representative members (caller-trimmed)
	UpstreamRepo     string               // "owner/repo"
	UpstreamDesc     string               // upstream description (may be "")
	UpstreamReadme   string               // truncated upstream README, ≤ 2 KB (may be "")
	UpstreamCoreDirs []string             // top-K central directories
}

// Labeler is an optional hook that polishes a cluster's heuristic label.
// Implementations typically call out to an LLM. Returning an error means the
// caller should fall back to the heuristic label.
type Labeler interface {
	Polish(ctx context.Context, lc LabelerContext) (string, error)
}

// labelSeparator joins the dir-prefix and tokens portions of a heuristic
// label. The doubled spaces and middle dot are intentional — they make the
// label visually parseable in a terminal.
const labelSeparator = "  ·  "

// stopwords is the hardcoded list applied case-insensitively after
// lowercasing. Kept small on purpose — see package doc.
var stopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "from": {}, "this": {},
	"that": {}, "into": {}, "have": {}, "will": {}, "when": {}, "what": {},
	"your": {}, "their": {}, "there": {}, "should": {}, "would": {},
	"could": {}, "merge": {}, "branch": {}, "master": {}, "main": {},
	"fix": {}, "update": {}, "updated": {}, "updates": {}, "change": {},
	"changes": {}, "remove": {}, "removed": {}, "removes": {}, "add": {},
	"added": {}, "adds": {}, "use": {}, "used": {}, "uses": {},
}

// tokenTrimCutset is the punctuation stripped from token edges before
// stopword/length filtering.
const tokenTrimCutset = ".,!?:;()[]\"'`"

// HeuristicLabel produces a deterministic, human-readable label for a cluster.
// See package comment / task spec for the precise format and rules.
func HeuristicLabel(members []embed.ForkFeatures, corpus []embed.ForkFeatures) string {
	if len(members) == 0 {
		return ""
	}

	dir := dirPrefix(members)
	tokens := topTokens(members, corpus, 3)

	switch {
	case dir != "" && len(tokens) > 0:
		return dir + labelSeparator + strings.Join(tokens, ", ")
	case dir != "":
		return dir
	case len(tokens) > 0:
		return strings.Join(tokens, ", ")
	default:
		return ""
	}
}

// dirPrefix returns the most common directory prefix (path component up to
// and including the first "/") across the union of members' file paths.
// Returns "" if no prefix occurs more than once.
func dirPrefix(members []embed.ForkFeatures) string {
	counts := make(map[string]int)
	for _, m := range members {
		if m.Paths == "" {
			continue
		}
		for _, line := range strings.Split(m.Paths, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			slash := strings.IndexByte(line, '/')
			if slash < 0 {
				continue
			}
			prefix := line[:slash+1]
			counts[prefix]++
		}
	}

	type prefixCount struct {
		prefix string
		count  int
	}
	candidates := make([]prefixCount, 0, len(counts))
	for p, c := range counts {
		if c > 1 {
			candidates = append(candidates, prefixCount{p, c})
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].count != candidates[j].count {
			return candidates[i].count > candidates[j].count
		}
		return candidates[i].prefix < candidates[j].prefix
	})
	return candidates[0].prefix
}

// topTokens computes TF-IDF over a word-bag model of member commit messages
// against the corpus, returning the top-n discriminative tokens.
func topTokens(members []embed.ForkFeatures, corpus []embed.ForkFeatures, n int) []string {
	memberCounts, memberTotal := tokenizeMembers(members)
	if memberTotal == 0 || len(memberCounts) == 0 {
		return nil
	}

	N := len(corpus)
	df := documentFrequencies(corpus, memberCounts)

	type scored struct {
		token string
		score float64
	}
	scoredTokens := make([]scored, 0, len(memberCounts))
	for tok, c := range memberCounts {
		tf := float64(c) / float64(memberTotal)
		idf := math.Log(float64(1+N) / float64(1+df[tok]))
		scoredTokens = append(scoredTokens, scored{tok, tf * idf})
	}

	sort.Slice(scoredTokens, func(i, j int) bool {
		if scoredTokens[i].score != scoredTokens[j].score {
			return scoredTokens[i].score > scoredTokens[j].score
		}
		return scoredTokens[i].token < scoredTokens[j].token
	})

	if n > len(scoredTokens) {
		n = len(scoredTokens)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, scoredTokens[i].token)
	}
	return out
}

// tokenizeMembers returns the per-token frequency in the members corpus and
// the total token count (after filtering). All tokens are already lowercased
// and stripped of edge punctuation.
func tokenizeMembers(members []embed.ForkFeatures) (map[string]int, int) {
	counts := make(map[string]int)
	total := 0
	for _, m := range members {
		for _, tok := range tokenize(m.Commits) {
			counts[tok]++
			total++
		}
	}
	return counts, total
}

// documentFrequencies counts, for each candidate token in `candidates`, how
// many corpus entries contain that token (case-insensitive).
func documentFrequencies(corpus []embed.ForkFeatures, candidates map[string]int) map[string]int {
	df := make(map[string]int, len(candidates))
	for _, doc := range corpus {
		seen := make(map[string]struct{})
		for _, tok := range tokenize(doc.Commits) {
			if _, ok := candidates[tok]; !ok {
				continue
			}
			if _, dup := seen[tok]; dup {
				continue
			}
			seen[tok] = struct{}{}
			df[tok]++
		}
	}
	return df
}

// tokenize splits text on whitespace, lowercases, strips edge punctuation,
// and drops short and stopword tokens.
func tokenize(text string) []string {
	if text == "" {
		return nil
	}
	fields := strings.Fields(text)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		tok := strings.ToLower(strings.Trim(f, tokenTrimCutset))
		if len(tok) < 4 {
			continue
		}
		if _, stop := stopwords[tok]; stop {
			continue
		}
		out = append(out, tok)
	}
	return out
}
