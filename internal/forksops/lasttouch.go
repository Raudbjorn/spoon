package forksops

// Last-touch skip for the --touching path.
//
// touching.go's touchOne answers "did this fork's own ahead commits touch
// path P" strictly from T2.Diffs -- the merge-base-relative compare. That
// compare is a REST/GraphQL call per fork. This file adds one cheap
// negative proof in front of it: when upstream's most recent commit that
// touched a literal --touching target is also the fork's own most recent
// commit that touched it (same OID), and the fork's selected branch is
// close enough behind to be certain it could even contain that upstream
// commit, no commit reachable from the fork's tip after that point could
// have touched the target -- the REST compare is skipped entirely. A
// mismatch or a lookup that returns no information proves nothing and
// must fall through to the ordinary compare; see decide's guard comment.
//
// This stage never inspects a tree or tip diff itself and never treats a
// differing OID as evidence of a touch -- only equality is a proof. See
// touching.go's package doc for why that distinction matters (the
// pbakaus/impeccable false positives).

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"sync/atomic"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/pathmatch"
)

// lastTouchTarget is one literal --touching pattern resolved against
// upstream: dir/name split the way ForkLastTouch's directory-listing call
// addresses it (entry `name` inside the directory listing for `dir`; ""
// dir is the repository root). resolved is false when upstream has no
// history at all for path -- PathLastTouch's absent-key case -- in which
// case c is the zero value and this target can never license a skip: it
// forces decide() to fall through immediately (see decide).
type lastTouchTarget struct {
	path     string
	dir      string
	name     string
	c        forge.PathLastTouch
	resolved bool
}

// lastTouchOutcome classifies one fork's last-touch decision.
type lastTouchOutcome int

const (
	lastTouchGatedOutcome lastTouchOutcome = iota
	lastTouchSkippedOutcome
	lastTouchMismatchOutcome
	lastTouchUnavailableOutcome
)

// lastTouchGate holds the once-per-run upstream baseline for the
// last-touch skip stage, plus the run-wide tallies for the "[touching]"
// summary line. A nil *lastTouchGate means the stage is inactive; callers
// must guard every call site with a nil check before calling decide (decide
// itself is not nil-safe -- it dereferences g.targets unconditionally).
type lastTouchGate struct {
	provider forge.LastTouchProvider
	targets  []lastTouchTarget

	gated       atomic.Int64
	lookedUp    atomic.Int64
	skipped     atomic.Int64
	mismatch    atomic.Int64
	unavailable atomic.Int64
}

// newLastTouchGate builds the last-touch skip stage for one Stream run. It
// returns nil -- stage inactive, every fork falls through to its ordinary
// compare -- when any precondition fails: no --touching patterns, a
// pattern that is not pathmatch.IsLiteral (a wildcard can match many paths,
// so there is no single upstream commit to prove against), the provider
// doesn't implement forge.LastTouchProvider, or the once-per-run upstream
// lookup itself errors (logged and treated as "no baseline available").
//
// Called once per run, after the pre-dispatch batch: the batch is what
// produces the forge.BranchSelection values decide() tests, so building
// this gate when the batch didn't run would spend the upstream lookup for
// a stage no fork's worker iteration could ever reach.
func newLastTouchGate(ctx context.Context, provider forge.Forge, touching []string, logger io.Writer) *lastTouchGate {
	if len(touching) == 0 {
		return nil
	}
	ltp, ok := provider.(forge.LastTouchProvider)
	if !ok {
		return nil
	}
	norm := make([]string, 0, len(touching))
	for _, p := range touching {
		if !pathmatch.IsLiteral(p) {
			return nil
		}
		n, err := pathmatch.Normalize(p)
		if err != nil {
			// Unreachable in practice: Stream already validated every
			// pattern via pathmatch.Compile before dispatch. Defensive
			// only -- treat it the same as any other precondition miss.
			return nil
		}
		norm = append(norm, n)
	}

	upstream, err := ltp.PathLastTouch(ctx, norm)
	if err != nil {
		fmt.Fprintf(logger, "[touching] last-touch disabled: %v\n", err)
		return nil
	}

	g := &lastTouchGate{provider: ltp}
	var unresolved []string
	for _, p := range norm {
		t := lastTouchTarget{path: p, name: path.Base(p)}
		if dir := path.Dir(p); dir != "." {
			t.dir = dir
		}
		if c, ok := upstream[p]; ok {
			t.c = c
			t.resolved = true
		} else {
			unresolved = append(unresolved, p)
		}
		g.targets = append(g.targets, t)
	}
	if len(unresolved) > 0 {
		// One-time, not per-fork: every decide() call involving one of
		// these paths reports lastTouchUnavailableOutcome for the run's
		// whole duration (upstream has no history for it at all, so no
		// per-fork lookup could ever help) -- without this line that shows
		// up only as an unavailable tally with zero lookups, which reads
		// identically to "the run made zero last-touch calls" and hides
		// why.
		fmt.Fprintf(logger, "[touching] no upstream history for %v; last-touch skip can never prove these untouched\n", unresolved)
	}
	return g
}

// decide reports whether fork's REST compare can be skipped for sel, the
// batch-chosen BranchSelection the worker would otherwise pass to
// compareFork.
//
// LAST-TOUCH SKIP GUARD. Skip the REST compare only when (1) the selected
// branch can contain upstream's last-touch commit C for every literal
// target: sel.Behind <= upstream.CommitsSince (contrapositive of "contains
// C ⇒ misses at most the commits after C"), and (2) the fork's last-touch
// OID for the target equals C. Equal OIDs mean no commit reachable from the
// tip after C touched the path, so the merge-base...tip diff cannot include
// it. A different or missing OID proves nothing and must fall through to
// the compare. Never treat a differing OID as a signal (mp3wizard case).
func (g *lastTouchGate) decide(ctx context.Context, fork forge.T1Data, sel forge.BranchSelection) (bool, lastTouchOutcome) {
	for _, t := range g.targets {
		if !t.resolved {
			g.unavailable.Add(1)
			return false, lastTouchUnavailableOutcome
		}
	}
	for _, t := range g.targets {
		if sel.Behind > t.c.CommitsSince {
			g.gated.Add(1)
			return false, lastTouchGatedOutcome
		}
	}

	// Group targets by directory so one ForkLastTouch call serves every
	// target that shares it. Sorted for deterministic call order (and so a
	// short-circuit below always drops the same, lexicographically-first
	// failing directory rather than whichever map iteration happened to
	// hit first).
	byDir := make(map[string][]lastTouchTarget, len(g.targets))
	dirs := make([]string, 0, len(g.targets))
	for _, t := range g.targets {
		if _, ok := byDir[t.dir]; !ok {
			dirs = append(dirs, t.dir)
		}
		byDir[t.dir] = append(byDir[t.dir], t)
	}
	sort.Strings(dirs)

	g.lookedUp.Add(1)
	for _, dir := range dirs {
		entries, outcome := g.provider.ForkLastTouch(ctx, fork, sel.Branch, dir)
		if outcome != forge.LastTouchOK {
			g.unavailable.Add(1)
			return false, lastTouchUnavailableOutcome
		}
		for _, t := range byDir[dir] {
			oid, ok := entries[t.name]
			if !ok {
				g.unavailable.Add(1)
				return false, lastTouchUnavailableOutcome
			}
			if oid != t.c.SHA {
				// A mismatch already forecloses the all-targets-equal
				// proof; stop spending lookups on the remaining
				// directories rather than confirming what we already know.
				g.mismatch.Add(1)
				return false, lastTouchMismatchOutcome
			}
		}
	}
	g.skipped.Add(1)
	return true, lastTouchSkippedOutcome
}

// counts reports the gate's run-wide tallies for the "[touching]" summary
// line and Options.CompareReport. A nil gate (the stage never engaged)
// reports all zeros.
func (g *lastTouchGate) counts() (gated, lookedUp, skipped, mismatch, unavailable int) {
	if g == nil {
		return 0, 0, 0, 0, 0
	}
	return int(g.gated.Load()), int(g.lookedUp.Load()), int(g.skipped.Load()), int(g.mismatch.Load()), int(g.unavailable.Load())
}
