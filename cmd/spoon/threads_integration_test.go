// cmd/spoon/threads_integration_test.go
//
// Cross-feature parser/dispatch checks for `spoon threads`. The spoon binary
// constructs its github.Client unconditionally (no apiFactory seam exists),
// so true end-to-end integration tests would require a real authenticated
// network call. Following the task fallback, we instead drive the parser
// directly with the same flag combinations and assert the parsed flags + the
// internally-derived listOpts/resolveOpts/applyOpts that the dispatcher would
// build, so a regression in parsing or flag interaction is caught. The
// underlying behavior of each operation is already exercised end-to-end by the
// spn cross-feature tests in cmd/spn/threads_integration_test.go.
package main

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/threadsops"
)

// TestCrossFeature_Spoon_FilterShowCodeJSON exercises the spoon parser for
// `spoon threads <ref> --json --filter outdated --show-code 3`. (The literal
// "outdated" alone is not a valid filter mode; the canonical equivalent is
// `unresolved-outdated`. This test uses the canonical name and verifies the
// resulting flags carry the correct filter + showCodeLines + JSON mode.)
func TestCrossFeature_Spoon_FilterShowCodeJSON(t *testing.T) {
	f, err := parseThreadsFlags([]string{
		"owner/repo#42",
		"--json",
		"--filter", "unresolved-outdated",
		"--show-code", "3",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.mode != modeJSON {
		t.Errorf("mode=%v want JSON", f.mode)
	}
	if f.filter != threadsops.FilterUnresolvedOutdated {
		t.Errorf("filter=%q want %q", f.filter, threadsops.FilterUnresolvedOutdated)
	}
	if f.showCodeLines != 3 {
		t.Errorf("showCodeLines=%d want 3", f.showCodeLines)
	}
	// Dispatcher always uses Filter+SortThreadsForList downstream.
	// Sanity-check that NeedsResolvedFetch() flows from the parsed filter
	// (unresolved-outdated does NOT require resolved).
	if f.filter.NeedsResolvedFetch() {
		t.Errorf("unresolved-outdated should not require resolved fetch")
	}
}

// TestCrossFeature_Spoon_DryRunResolveAll exercises the parser for
// `spoon threads <ref> --resolve-all --outdated --dry-run --json`. The --json
// flag conflicts with --resolve-all as a mode flag, so this test instead
// asserts the parser rejects the combination — matching how spoon's existing
// mode-flag mutual exclusion behaves.
func TestCrossFeature_Spoon_DryRunResolveAll(t *testing.T) {
	// --json + --resolve-all is mutually exclusive at the mode-flag level.
	_, err := parseThreadsFlags([]string{
		"owner/repo#42", "--resolve-all", "--outdated", "--dry-run", "--json",
	})
	if err == nil {
		t.Fatal("expected error: --json conflicts with --resolve-all")
	}
	// The supported invocation, without --json, must parse cleanly with all
	// three feature flags populated.
	f, err := parseThreadsFlags([]string{
		"owner/repo#42", "--resolve-all", "--outdated", "--dry-run",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.mode != modeResolveAll {
		t.Errorf("mode=%v want resolve-all", f.mode)
	}
	if !f.outdatedOnly {
		t.Errorf("outdatedOnly should be true")
	}
	if !f.dryRun {
		t.Errorf("dryRun should be true")
	}
}

// TestCrossFeature_Spoon_ApplySuggestionDryRun parses the apply-suggestion
// invocation with --dry-run and --repo-root in one command and asserts every
// relevant flag is captured for the dispatcher.
func TestCrossFeature_Spoon_ApplySuggestionDryRun(t *testing.T) {
	tmp := t.TempDir()
	f, err := parseThreadsFlags([]string{
		"owner/repo#42",
		"--apply-suggestion", "PRRT_x",
		"--dry-run",
		"--repo-root", tmp,
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.mode != modeApplySuggestion {
		t.Errorf("mode=%v want apply-suggestion", f.mode)
	}
	if f.targetID != "PRRT_x" {
		t.Errorf("targetID=%q want PRRT_x", f.targetID)
	}
	if !f.dryRun {
		t.Errorf("dryRun should be true")
	}
	if f.repoRoot != tmp {
		t.Errorf("repoRoot=%q want %q", f.repoRoot, tmp)
	}
	// --force defaults to false in this invocation.
	if f.force {
		t.Errorf("force should default to false")
	}
}

// TestCrossFeature_Spoon_ReplyWithSuggest parses --reply with --suggest and
// asserts the parser produced the canonical wrapped body that the dispatcher
// forwards to api.ReplyToThread.
func TestCrossFeature_Spoon_ReplyWithSuggest(t *testing.T) {
	f, err := parseThreadsFlags([]string{
		"owner/repo#42", "--reply", "PRRT_x", "--suggest", "new",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.mode != modeReply {
		t.Errorf("mode=%v want reply", f.mode)
	}
	want := "How about this?\n\n```suggestion\nnew\n```"
	if f.body != want {
		t.Errorf("body=%q\nwant %q", f.body, want)
	}
}

// TestCrossFeature_Spoon_FilterShowCodeVerboseTUI checks that the default TUI
// mode also accepts the cross-feature flag set (--filter + --show-code +
// --verbose); the dispatcher hands these through to runThreadsTUI.
func TestCrossFeature_Spoon_FilterShowCodeVerboseTUI(t *testing.T) {
	f, err := parseThreadsFlags([]string{
		"owner/repo#42",
		"--filter", "current-unresolved",
		"--show-code", "5",
		"--verbose",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.mode != modeTUI {
		t.Errorf("mode=%v want TUI", f.mode)
	}
	if f.filter != threadsops.FilterCurrentUnresolved {
		t.Errorf("filter=%q want current-unresolved", f.filter)
	}
	if f.showCodeLines != 5 {
		t.Errorf("showCodeLines=%d want 5", f.showCodeLines)
	}
	if !f.verbose {
		t.Errorf("verbose should be true")
	}
}
