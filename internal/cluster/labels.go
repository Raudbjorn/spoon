package cluster

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/svnbjrn/spoon/internal/embed"
)

// LabelPolisher rewrites a cluster's heuristic label using broader context
// (e.g. an in-process LLM). Returning an error or an empty string keeps the
// heuristic label; polish never fails the pipeline.
type LabelPolisher interface {
	PolishLabel(ctx context.Context, hint PolishHint) (string, error)
}

// PolishHint is the context handed to a LabelPolisher for one cluster.
type PolishHint struct {
	Heuristic     string   // the deterministic label from HeuristicLabel
	UpstreamRepo  string   // "owner/repo"
	SampleCommits []string // up to a handful of member commit subjects
	SamplePaths   []string // up to a handful of member file paths
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

// pathStopSegments are directory/file segments too generic to discriminate
// clusters in the path-token fallback.
var pathStopSegments = map[string]struct{}{
	"src": {}, "lib": {}, "pkg": {}, "internal": {}, "test": {}, "tests": {},
	"docs": {}, "doc": {}, "main": {}, "index": {}, "util": {}, "utils": {},
	"common": {}, "core": {},
}

// tokenTrimCutset is the punctuation stripped from token edges before
// stopword/length filtering.
const tokenTrimCutset = ".,!?:;()[]\"'`"

// HeuristicLabel produces a deterministic, human-readable label for a cluster:
// the dominant directory prefix (up to two levels deep) plus the top TF-IDF
// discriminators from member commit messages — unigrams and bigrams — scored
// against the rest of the corpus. When members carry no commit text, path
// segments take over as the token source so clusters never go unlabeled
// merely because compare data lacked commit messages.
func HeuristicLabel(members []embed.ForkFeatures, corpus []embed.ForkFeatures) string {
	if len(members) == 0 {
		return ""
	}

	dir := dirPrefix(members)
	tokens := topTokens(members, corpus, 3)
	if len(tokens) == 0 {
		tokens = topPathTokens(members, corpus, 3, dir)
	}

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

// dirPrefix returns the most common directory prefix across the union of
// members' file paths, preferring a two-level prefix ("internal/auth/") over
// its one-level parent ("internal/") when the deeper prefix retains at least
// deepPrefixShare of the parent's count. Returns "" if no prefix occurs more
// than once.
func dirPrefix(members []embed.ForkFeatures) string {
	const deepPrefixShare = 0.6

	depth1 := make(map[string]int)
	depth2 := make(map[string]int)
	for _, m := range members {
		if m.Paths == "" {
			continue
		}
		for _, line := range strings.Split(m.Paths, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			first := strings.IndexByte(line, '/')
			if first < 0 {
				continue
			}
			depth1[line[:first+1]]++
			if second := strings.IndexByte(line[first+1:], '/'); second >= 0 {
				depth2[line[:first+1+second+1]]++
			}
		}
	}

	best := bestPrefix(depth1)
	if best == "" {
		return ""
	}
	// Prefer the dominant two-level refinement of the winner when it covers
	// enough of the parent's paths to still characterize the cluster.
	bestDeep, deepCount := "", 0
	for p, c := range depth2 {
		if !strings.HasPrefix(p, best) {
			continue
		}
		if c > deepCount || (c == deepCount && p < bestDeep) {
			bestDeep, deepCount = p, c
		}
	}
	if bestDeep != "" && deepCount > 1 &&
		float64(deepCount) >= deepPrefixShare*float64(depth1[best]) {
		return bestDeep
	}
	return best
}

// bestPrefix returns the highest-count prefix occurring more than once,
// breaking count ties lexicographically. "" when none qualifies.
func bestPrefix(counts map[string]int) string {
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
// against the corpus, returning the top-n discriminative tokens. Candidates
// include adjacent-word bigrams; when a bigram and one of its constituent
// unigrams both rank, the unigram is dropped so the label reads as a phrase
// rather than repeating itself.
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

	out := make([]string, 0, n)
	for _, st := range scoredTokens {
		if len(out) >= n {
			break
		}
		if redundantWithPicked(st.token, out) {
			continue
		}
		out = append(out, st.token)
	}
	return out
}

// redundantWithPicked reports whether tok repeats a word already present in
// a picked token (e.g. unigram "limit" after bigram "rate limit", or vice
// versa).
func redundantWithPicked(tok string, picked []string) bool {
	words := strings.Fields(tok)
	for _, p := range picked {
		for _, pw := range strings.Fields(p) {
			for _, w := range words {
				if w == pw {
					return true
				}
			}
		}
	}
	return false
}

// topPathTokens is the fallback token source when members have no commit
// text: TF-IDF over path segments (directories and file stems) against the
// corpus's paths. Segments already covered by the dir prefix are excluded so
// the label doesn't repeat itself.
func topPathTokens(members []embed.ForkFeatures, corpus []embed.ForkFeatures, n int, dir string) []string {
	dirWords := make(map[string]struct{})
	for _, seg := range strings.Split(strings.TrimSuffix(dir, "/"), "/") {
		if seg != "" {
			dirWords[strings.ToLower(seg)] = struct{}{}
		}
	}

	memberCounts := make(map[string]int)
	memberTotal := 0
	for _, m := range members {
		for _, tok := range pathTokens(m.Paths) {
			if _, dup := dirWords[tok]; dup {
				continue
			}
			memberCounts[tok]++
			memberTotal++
		}
	}
	if memberTotal == 0 {
		return nil
	}

	N := len(corpus)
	df := make(map[string]int, len(memberCounts))
	for _, doc := range corpus {
		seen := make(map[string]struct{})
		for _, tok := range pathTokens(doc.Paths) {
			if _, ok := memberCounts[tok]; !ok {
				continue
			}
			if _, dup := seen[tok]; dup {
				continue
			}
			seen[tok] = struct{}{}
			df[tok]++
		}
	}

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

// pathTokens splits newline-separated file paths into lowercase directory
// segments and file stems (extension dropped), filtering short and generic
// segments.
func pathTokens(paths string) []string {
	if paths == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(paths, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		segs := strings.Split(line, "/")
		for i, seg := range segs {
			if i == len(segs)-1 { // file name: drop the extension
				if dot := strings.LastIndexByte(seg, '.'); dot > 0 {
					seg = seg[:dot]
				}
			}
			seg = strings.ToLower(seg)
			if len(seg) < 3 {
				continue
			}
			if _, stop := pathStopSegments[seg]; stop {
				continue
			}
			out = append(out, seg)
		}
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
// and drops short and stopword tokens. Alongside the surviving unigrams it
// emits adjacent-pair bigrams ("rate limit") built from consecutive
// surviving words on the same line, which lets phrase-shaped discriminators
// outrank their parts in topTokens.
func tokenize(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		var kept []string
		for _, f := range strings.Fields(line) {
			tok := strings.ToLower(strings.Trim(f, tokenTrimCutset))
			if len(tok) < 4 {
				continue
			}
			if _, stop := stopwords[tok]; stop {
				continue
			}
			kept = append(kept, tok)
		}
		for i, tok := range kept {
			out = append(out, tok)
			if i > 0 {
				out = append(out, kept[i-1]+" "+tok)
			}
		}
	}
	return out
}
