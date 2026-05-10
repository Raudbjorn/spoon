package main

import (
	"fmt"
	"io"

	gh "github.com/svnbjrn/spoon/internal/github"
)

// statusSignal categorizes a status line for color/glyph selection.
type statusSignal int

const (
	signalGreen statusSignal = iota
	signalYellow
	signalRed
	signalNeutral // for "—" / not applicable
)

// RenderStatusBlock writes a four-line PR status header to w followed by
// a blank line. useGlyphs selects ✓/⏳/✗ vs [OK]/[..]/[X] output.
func RenderStatusBlock(w io.Writer, s gh.PullRequestStatus, number int, useGlyphs bool) error {
	mergeSig := mergeSignal(s.MergeStateStatus)
	mergeText := mergeText(s.MergeStateStatus)
	reviewSig, reviewText := reviewSignal(s.ReviewDecision)
	checksSig, checksText := checksSignal(s.ChecksState)
	threadsSig := signalGreen
	if s.UnresolvedThreads > 0 {
		threadsSig = signalRed
	}

	if _, err := fmt.Fprintf(w, "PR #%d — %s\n", number, s.Title); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Mergeable: %s %s\n", marker(mergeSig, useGlyphs), mergeText); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Reviews:   %s %s\n", marker(reviewSig, useGlyphs), reviewText); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Checks:    %s %s\n", marker(checksSig, useGlyphs), checksText); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Threads:   %s %d unresolved\n", marker(threadsSig, useGlyphs), s.UnresolvedThreads); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func marker(sig statusSignal, useGlyphs bool) string {
	if useGlyphs {
		switch sig {
		case signalGreen:
			return "✓"
		case signalYellow:
			return "⏳"
		case signalRed:
			return "✗"
		default:
			return "—"
		}
	}
	switch sig {
	case signalGreen:
		return "[OK]"
	case signalYellow:
		return "[..]"
	case signalRed:
		return "[X]"
	default:
		return "—"
	}
}

func mergeSignal(state string) statusSignal {
	switch state {
	case "CLEAN":
		return signalGreen
	case "DIRTY", "BLOCKED", "DRAFT":
		return signalRed
	default: // BEHIND, UNSTABLE, HAS_HOOKS, UNKNOWN
		return signalYellow
	}
}

func mergeText(state string) string {
	if state == "" {
		return "—"
	}
	return state
}

func reviewSignal(decision string) (statusSignal, string) {
	if decision == "" {
		return signalNeutral, "—"
	}
	if decision == "APPROVED" {
		return signalGreen, decision
	}
	return signalRed, decision
}

func checksSignal(state string) (statusSignal, string) {
	if state == "" {
		return signalNeutral, "—"
	}
	switch state {
	case "SUCCESS", "EXPECTED":
		return signalGreen, state
	case "PENDING":
		return signalYellow, state
	default: // FAILURE, ERROR
		return signalRed, state
	}
}
