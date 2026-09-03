package forksops

import (
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
	Status           TouchStatus
	Reason           string // unknown only: compare_unavailable | cache_no_files | reserve_skipped
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
// matches always, and unmatched forks whose file list was truncated (the
// user must be told the answer is incomplete for them).
func (t *TouchMatch) Emit() bool {
	if t == nil {
		return false
	}
	return t.Status == TouchMatched || (t.Status == TouchUnmatched && t.Partial)
}

// TouchSummary is the run-level tally, written to Options.TouchReport.
type TouchSummary struct {
	Matched, Partial, Unmatched, Unknown, NeverPushed int
	CentralityMethod                                  string
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
	// A cached compare rehydrates Diffs from compare_files. Missing rows for a
	// compare that had a non-empty diff are "unknown", not "unmatched".
	if r.T2FromCache && len(t2.Diffs) == 0 && t2.TotalAdditions+t2.TotalDeletions > 0 {
		return TouchMatch{Status: TouchUnknown, Reason: "cache_no_files"}
	}
	partial := t2.FilesTruncated || len(t2.Diffs) >= forge.CompareFilesCap
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
