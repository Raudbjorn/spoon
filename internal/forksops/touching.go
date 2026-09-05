package forksops

import (
	"sort"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/pathmatch"
	"github.com/svnbjrn/spoon/internal/repo"
)

// Touching answers "did this fork's OWN ahead commits touch path P".
//
// The only admissible source is T2.Diffs: GitHub's compare endpoint computes
// it merge-base-relative (base...head), so upstream commits the fork is
// behind never appear. A tip-vs-tip diff would report all of them as fork
// changes; that exact bug produced two confirmed false positives on
// pbakaus/impeccable (docs/research/2026-09-03-network-wide-distinguishing-
// file-detection.md). Nothing in this file may consult a tree or tip diff.

type TouchStatus string

const (
	TouchMatched     TouchStatus = "matched"
	TouchUnmatched   TouchStatus = "unmatched"
	TouchUnknown     TouchStatus = "unknown"      // no usable compare; see Reason
	TouchNeverPushed TouchStatus = "never_pushed" // no compare and pushed_at <= created_at
)

// TouchedFile is one T2 diff entry that matched a --touching pattern.
// Centrality is the upstream repo.Centrality score of the file's
// directory/module in [0,1]; 0 when no backend was available.
type TouchedFile struct {
	Path, PreviousPath, Status string
	Additions, Deletions       int
	Pattern                    string
	Centrality                 float64
}

// TouchMatch is the per-fork verdict. Partial means the provider capped the
// file list, so an unmatched verdict may be a false negative. Impact is the
// highest upstream centrality among the matched files (0 when no backend);
// CentralityMethod names the backend that produced it.
type TouchMatch struct {
	Status TouchStatus
	// Reason explains a verdict that isn't a plain scan result: on
	// TouchUnknown, one of compare_unavailable | cache_no_files |
	// reserve_skipped; on TouchUnmatched, "last_touch" when the REST
	// compare was skipped by the last-touch proof (lasttouch.go) rather
	// than actually run -- see Result.T2FilesUnfetched. Empty for an
	// ordinary matched/unmatched verdict from a real compare.
	Reason           string
	Files            []TouchedFile
	Partial          bool
	Impact           float64
	CentralityMethod string // "" | "directory" | "mdg"
}

// centralityMethod names the backend for output. The MDG backend lives in
// internal/mdg and only shares the interface, so anything that is not the
// directory proxy is reported as "mdg".
func centralityMethod(c repo.Centrality) string {
	switch c.(type) {
	case nil:
		return ""
	case repo.DirectoryCentrality, *repo.DirectoryCentrality:
		return "directory"
	default:
		return "mdg"
	}
}

// Emit reports whether the CLI should print this fork under --touching:
// matches always; unmatched forks whose file list was truncated (the user
// must be told the answer is incomplete for them); and unmatched forks
// whose REST compare was skipped by the last-touch proof (Reason ==
// "last_touch"). Both of the latter two rest on an undocumented endpoint
// (tree-commit-info for last_touch, GitHub's own 300-file compare cap for
// partial) rather than a plain scan result, so both are surfaced the same
// way rather than silently dropped.
func (t *TouchMatch) Emit() bool {
	if t == nil {
		return false
	}
	if t.Status != TouchUnmatched {
		return t.Status == TouchMatched
	}
	return t.Partial || t.Reason == "last_touch"
}

// TouchSummary is the run-level tally, written to Options.TouchReport.
type TouchSummary struct {
	Matched, Partial, Unmatched, Unknown, NeverPushed int
	CentralityMethod                                  string

	// LastTouchGated, LastTouchLookedUp, LastTouchSkipped, LastTouchMismatch
	// and LastTouchUnavailable tally the last-touch skip stage's per-fork
	// decisions (lasttouch.go). Gated: the selected branch could not
	// contain upstream's last-touch commit for some target, so no lookup
	// was made. LookedUp: the gate actually queried the fork. Skipped: the
	// gate proved every target untouched -- REST compare skipped,
	// Result.T2FilesUnfetched set, verdict TouchUnmatched/"last_touch".
	// Mismatch and Unavailable are lookups that could not prove the
	// negative and fell through to an ordinary compare. All zero when
	// --touching wasn't set, a pattern used a wildcard, or the provider
	// lacks forge.LastTouchProvider.
	LastTouchGated, LastTouchLookedUp, LastTouchSkipped, LastTouchMismatch, LastTouchUnavailable int
}

// NeverPushed reports a fork that has not been pushed since it was created.
// Such forks are zero-ahead in all but pathological cases (1 of 1855 observed
// on impeccable), so callers deprioritise them; they are never skipped.
func NeverPushed(f forge.T1Data) bool {
	return !f.CreatedAt.IsZero() && !f.PushedAt.After(f.CreatedAt)
}

func touchOne(m pathmatch.Matcher, c repo.Centrality, r Result) TouchMatch {
	if r.T2 == nil {
		if r.BudgetSkip != nil {
			return TouchMatch{Status: TouchUnknown, Reason: "reserve_skipped"}
		}
		if NeverPushed(r.Fork) {
			return TouchMatch{Status: TouchNeverPushed}
		}
		return TouchMatch{Status: TouchUnknown, Reason: "compare_unavailable"}
	}
	t2 := r.T2
	if !t2.Performed {
		return TouchMatch{Status: TouchUnknown, Reason: "compare_unavailable"}
	}
	// Zero ahead commits: the three-dot diff is empty by definition. Do not
	// scan Diffs (they may be stale or absent).
	if t2.AheadCount == 0 {
		return TouchMatch{Status: TouchUnmatched}
	}
	// The last-touch skip stage (lasttouch.go) proved every --touching
	// target untouched without ever fetching Diffs; there is nothing to
	// scan here, and the AheadCount/BehindCount on this T2 are real, not a
	// signal that the compare ran empty.
	if r.T2FilesUnfetched {
		return TouchMatch{Status: TouchUnmatched, Reason: "last_touch"}
	}
	// A cached compare rehydrates Diffs from compare_files. Missing rows for a
	// compare that had a non-empty diff are "unknown", not "unmatched".
	if r.T2FromCache && len(t2.Diffs) == 0 && t2.TotalAdditions+t2.TotalDeletions > 0 {
		return TouchMatch{Status: TouchUnknown, Reason: "cache_no_files"}
	}
	partial := t2.IsFilesTruncated()
	var files []TouchedFile
	for _, d := range t2.Diffs {
		pat, ok := m.First(d.Path)
		if !ok && d.PreviousPath != "" {
			pat, ok = m.First(d.PreviousPath)
		}
		if !ok {
			continue
		}
		tf := TouchedFile{
			Path: d.Path, PreviousPath: d.PreviousPath, Status: d.Status,
			Additions: d.Additions, Deletions: d.Deletions, Pattern: pat,
		}
		if c != nil {
			// Per-file, not per-fork: ScoreFork over one path is that
			// path's own directory/module score, which is what "how
			// load-bearing is this file" asks. Heat.ChangeImpact keeps its
			// mean-over-all-paths meaning untouched.
			tf.Centrality = c.ScoreFork([]string{d.Path})
		}
		files = append(files, tf)
	}
	if len(files) == 0 {
		return TouchMatch{Status: TouchUnmatched, Partial: partial}
	}
	out := TouchMatch{Status: TouchMatched, Files: files, Partial: partial, CentralityMethod: centralityMethod(c)}
	for _, f := range files {
		if f.Centrality > out.Impact {
			out.Impact = f.Centrality
		}
	}
	return out
}

// scoreTouching annotates every result in place and returns the tally.
func scoreTouching(m pathmatch.Matcher, c repo.Centrality, results []Result) TouchSummary {
	s := TouchSummary{CentralityMethod: centralityMethod(c)}
	for i := range results {
		t := touchOne(m, c, results[i])
		results[i].Touching = &t
		switch t.Status {
		case TouchMatched:
			s.Matched++
		case TouchUnmatched:
			s.Unmatched++
		case TouchUnknown:
			s.Unknown++
		case TouchNeverPushed:
			s.NeverPushed++
		}
		if t.Partial {
			s.Partial++
		}
	}
	return s
}

// splitTouching partitions results into matched and everything else,
// preserving order within each half.
func splitTouching(rs []Result) (matched, rest []Result) {
	for _, r := range rs {
		if r.Touching != nil && r.Touching.Status == TouchMatched {
			matched = append(matched, r)
		} else {
			rest = append(rest, r)
		}
	}
	return matched, rest
}

func touchLane(r Result) int {
	switch {
	case r.Touching == nil:
		return 2
	case r.Touching.Status == TouchMatched:
		return 0
	case r.Touching.Status == TouchUnmatched && r.Touching.Partial:
		return 1
	default:
		return 2
	}
}

// sortTouchingLanes orders results for --touching output: matches first
// (most load-bearing file first, then whatever order the earlier passes
// produced), then capped-unmatched forks, then the rest. Stable, so
// query/priors/heat order survives inside each lane.
func sortTouchingLanes(rs []Result) {
	sort.SliceStable(rs, func(i, j int) bool {
		li, lj := touchLane(rs[i]), touchLane(rs[j])
		if li != lj {
			return li < lj
		}
		if li == 0 {
			return rs[i].Touching.Impact > rs[j].Touching.Impact
		}
		return false
	})
}
