package main

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/threadsops"
)

func TestParseThreadsFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(t *testing.T, f threadsFlags)
	}{
		{
			name: "default mode (TUI)",
			args: []string{"owner/repo#42"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeTUI {
					t.Errorf("mode=%v want TUI", f.mode)
				}
			},
		},
		{
			name: "json",
			args: []string{"owner/repo#42", "--json"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeJSON {
					t.Errorf("mode=%v want JSON", f.mode)
				}
			},
		},
		{
			name: "next",
			args: []string{"owner/repo#42", "--next"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeNext {
					t.Errorf("mode=%v want Next", f.mode)
				}
			},
		},
		{
			name: "reply with body",
			args: []string{"owner/repo#42", "--reply", "PRRT_1", "--body", "hello"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeReply || f.targetID != "PRRT_1" || f.body != "hello" {
					t.Errorf("got %+v", f)
				}
			},
		},
		{
			name: "resolve",
			args: []string{"owner/repo#42", "--resolve", "PRRT_1", "--body", "fixed"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeResolve || f.targetID != "PRRT_1" || f.body != "fixed" {
					t.Errorf("got %+v", f)
				}
			},
		},
		{
			name: "resolve-all",
			args: []string{"owner/repo#42", "--resolve-all"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeResolveAll {
					t.Errorf("mode=%v", f.mode)
				}
			},
		},
		{
			name:    "json + next mutually exclusive",
			args:    []string{"owner/repo#42", "--json", "--next"},
			wantErr: true,
		},
		{
			name:    "reply needs target id",
			args:    []string{"owner/repo#42", "--reply"},
			wantErr: true,
		},
		{
			name:    "reply needs body",
			args:    []string{"owner/repo#42", "--reply", "PRRT_1"},
			wantErr: true,
		},
		{
			name:    "missing pr ref",
			args:    []string{"--json"},
			wantErr: true,
		},
		{
			name: "resolve without body (parser accepts; policy applies in dispatcher)",
			args: []string{"owner/repo#42", "--resolve", "PRRT_1"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeResolve || f.targetID != "PRRT_1" || f.body != "" {
					t.Errorf("got %+v", f)
				}
			},
		},
		{
			name: "no-status sets the flag",
			args: []string{"owner/repo#42", "--no-status"},
			check: func(t *testing.T, f threadsFlags) {
				if !f.noStatus {
					t.Errorf("noStatus should be true")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseThreadsFlags(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			tc.check(t, f)
		})
	}
}


func TestParseThreadsFlags_FilterModes(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantErr    bool
		wantFilter threadsops.FilterMode
	}{
		{
			name:       "no filter → unresolved default",
			args:       []string{"owner/repo#42"},
			wantFilter: threadsops.FilterUnresolved,
		},
		{
			name:       "--filter all",
			args:       []string{"owner/repo#42", "--filter", "all"},
			wantFilter: threadsops.FilterAll,
		},
		{
			name:       "--filter=resolved-active",
			args:       []string{"owner/repo#42", "--filter=resolved-active"},
			wantFilter: threadsops.FilterResolvedActive,
		},
		{
			name:       "--filter unresolved-outdated",
			args:       []string{"owner/repo#42", "--filter", "unresolved-outdated"},
			wantFilter: threadsops.FilterUnresolvedOutdated,
		},
		{
			name:       "--filter current-unresolved",
			args:       []string{"owner/repo#42", "--filter", "current-unresolved"},
			wantFilter: threadsops.FilterCurrentUnresolved,
		},
		{
			name:       "--include-resolved is shorthand for --filter all",
			args:       []string{"owner/repo#42", "--include-resolved"},
			wantFilter: threadsops.FilterAll,
		},
		{
			name:    "unknown filter mode",
			args:    []string{"owner/repo#42", "--filter", "bogus"},
			wantErr: true,
		},
		{
			name:    "--filter without value",
			args:    []string{"owner/repo#42", "--filter"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseThreadsFlags(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if f.filter != tc.wantFilter {
				t.Errorf("filter=%q want %q", f.filter, tc.wantFilter)
			}
		})
	}
}

func TestParsePRRef(t *testing.T) {
	cases := []struct {
		in            string
		fallbackOwner string
		fallbackRepo  string
		wantOwner     string
		wantRepo      string
		wantNumber    int
		wantErr       bool
	}{
		{"owner/repo#42", "", "", "owner", "repo", 42, false},
		{"https://github.com/owner/repo/pull/42", "", "", "owner", "repo", 42, false},
		{"#42", "fallO", "fallR", "fallO", "fallR", 42, false},
		{"#42", "", "", "", "", 0, true}, // no fallback
		{"owner/repo", "", "", "", "", 0, true},
		{"https://gitlab.com/g/r/-/merge_requests/1", "", "", "", "", 0, true}, // gitlab rejected
		{"owner/repo#abc", "", "", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			o, r, n, err := parsePRRef(tc.in, tc.fallbackOwner, tc.fallbackRepo)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err mismatch: got %v, wantErr=%v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if o != tc.wantOwner || r != tc.wantRepo || n != tc.wantNumber {
				t.Errorf("got (%q,%q,%d), want (%q,%q,%d)", o, r, n, tc.wantOwner, tc.wantRepo, tc.wantNumber)
			}
		})
	}
}

func TestParseThreadsFlags_ApplySuggestion(t *testing.T) {
	f, err := parseThreadsFlags([]string{"owner/repo#1", "--apply-suggestion", "PRRT_1", "--suggestion-index", "2", "--dry-run", "--force", "--repo-root", "/tmp/x"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if f.mode != modeApplySuggestion {
		t.Errorf("mode=%v want apply-suggestion", f.mode)
	}
	if f.targetID != "PRRT_1" {
		t.Errorf("targetID=%q", f.targetID)
	}
	if f.suggestionIndex != 2 {
		t.Errorf("index=%d", f.suggestionIndex)
	}
	if !f.dryRun || !f.force {
		t.Errorf("dryRun=%v force=%v", f.dryRun, f.force)
	}
	if f.repoRoot != "/tmp/x" {
		t.Errorf("repoRoot=%q", f.repoRoot)
	}
}

func TestParseThreadsFlags_SuggestOnReply(t *testing.T) {
	f, err := parseThreadsFlags([]string{"owner/repo#1", "--reply", "PRRT_1", "--suggest", "new code"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if f.mode != modeReply {
		t.Errorf("mode=%v want reply", f.mode)
	}
	// Body must be the wrapped suggestion.
	if f.body != "How about this?\n\n```suggestion\nnew code\n```" {
		t.Errorf("body=%q", f.body)
	}
}

func TestParseThreadsFlags_SuggestAndBodyMutuallyExclusive(t *testing.T) {
	_, err := parseThreadsFlags([]string{"owner/repo#1", "--reply", "PRRT_1", "--suggest", "x", "--body", "y"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseThreadsFlags_IntroRequiresSuggest(t *testing.T) {
	_, err := parseThreadsFlags([]string{"owner/repo#1", "--reply", "PRRT_1", "--body", "hi", "--intro", "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseThreadsFlags_CustomIntro(t *testing.T) {
	f, err := parseThreadsFlags([]string{"owner/repo#1", "--reply", "PRRT_1", "--suggest", "x", "--intro", "Try:"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if f.body != "Try:\n\n```suggestion\nx\n```" {
		t.Errorf("body=%q", f.body)
	}
}

// --- G3: --outdated parser tests -------------------------------------------

func TestParseThreadsFlags_OutdatedFlag(t *testing.T) {
	f, err := parseThreadsFlags([]string{"owner/repo#1", "--resolve-all", "--outdated"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if f.mode != modeResolveAll {
		t.Errorf("mode=%v want resolve-all", f.mode)
	}
	if !f.outdatedOnly {
		t.Errorf("outdatedOnly should be true")
	}
}

func TestParseThreadsFlags_OutdatedWithoutResolveAll(t *testing.T) {
	_, err := parseThreadsFlags([]string{"owner/repo#1", "--outdated"})
	if err == nil {
		t.Fatal("expected error: --outdated requires --resolve-all")
	}
}

// silence the unused-import linter if no usage needs threadsops
var _ = threadsops.FilterAll

