package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/forge"
)

// Both top-level failure paths must emit the JSON envelope on stderr and keep
// stdout clean, so an agent's NDJSON parser branches on code instead of choking
// on usage text (#86).
func TestDispatchEnvelopeOnFailure(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no subcommand", nil},
		{"unknown subcommand", []string{"frks", "list", "o/r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := dispatch(tc.args, &stdout, &stderr)
			if exit != 2 {
				t.Fatalf("exit = %d, want 2", exit)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout must be empty, got %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), `"code": "bad_input"`) {
				t.Fatalf("stderr is not a bad_input envelope:\n%s", stderr.String())
			}
		})
	}
}

// -h/-v write to stdout and exit 0.
func TestDispatchHelpAndVersion(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "-v", "--version"} {
		var stdout, stderr bytes.Buffer
		if exit := dispatch([]string{arg}, &stdout, &stderr); exit != 0 {
			t.Fatalf("%s: exit = %d, want 0", arg, exit)
		}
		if stdout.Len() == 0 {
			t.Fatalf("%s: expected stdout output", arg)
		}
	}
}

// Help, version, and invalid top-level nouns must not bootstrap configuration:
// automation commonly invokes them before choosing a real command.
func TestDispatchPreBootstrapPathsAreSideEffectFree(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	for _, tc := range []struct {
		name string
		args []string
		exit int
	}{
		{"help", []string{"--help"}, 0},
		{"version", []string{"--version"}, 0},
		{"unknown", []string{"frks", "list", "o/r"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := dispatch(tc.args, &stdout, &stderr); got != tc.exit {
				t.Fatalf("exit = %d, want %d", got, tc.exit)
			}
			if tc.name == "unknown" {
				decoder := json.NewDecoder(&stderr)
				var envelope map[string]any
				if err := decoder.Decode(&envelope); err != nil {
					t.Fatalf("stderr must be one JSON envelope: %v\n%s", err, stderr.String())
				}
				if envelope["error"] == nil {
					t.Fatalf("stderr lacks error envelope: %s", stderr.String())
				}
				var extra any
				if err := decoder.Decode(&extra); err != io.EOF {
					t.Fatalf("stderr must contain exactly one envelope, extra decode = %v", err)
				}
			} else if stderr.Len() != 0 {
				t.Fatalf("stderr must be empty, got %q", stderr.String())
			}
			entries, err := os.ReadDir(xdg)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("pre-bootstrap dispatch created files: %v", entries)
			}
		})
	}
}

// Parse-level error paths return bad_input (exit 2) before any network or ONNX
// work, with clean stdout (#86, #87).
func TestSearchParseErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"empty query", nil},
		{"two positionals", []string{"a", "b"}},
		{"single-dash unknown flag", []string{"-x", "q"}},
		{"top zero", []string{"--top", "0", "q"}},
		{"top at end", []string{"q", "--top"}},
		{"repo not owner/repo", []string{"--repo", "notownerrepo", "q"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exit := runSearchWith(tc.args, &stdout, &stderr); exit != 2 {
				t.Fatalf("exit = %d, want 2 (stderr: %s)", exit, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout must be empty, got %q", stdout.String())
			}
		})
	}
}

func TestForksListParseErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"single-dash typo", []string{"list", "-tier", "1", "o/r"}},
		{"commit-file-budget without commit-files", []string{"list", "o/r", "--commit-file-budget", "5"}},
		{"rpm zero", []string{"list", "--rpm", "0", "o/r"}},
		{"rpm too high", []string{"list", "--rpm", "901", "o/r"}},
		{"rpm non-numeric", []string{"list", "--rpm", "abc", "o/r"}},
		{"cluster-epsilon negative", []string{"list", "--cluster-epsilon", "-1", "o/r"}},
		{"touching without value", []string{"list", "o/r", "--touching"}},
		{"touching bad pattern", []string{"list", "o/r", "--touching", "["}},
		{"touching with tier 1", []string{"list", "o/r", "--touching", "a.go", "--tier", "1"}},
		{"touching with csv", []string{"list", "o/r", "--touching", "a.go", "--csv"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exit := runForksWith(tc.args, &stdout, &stderr); exit != 2 {
				t.Fatalf("exit = %d, want 2 (stderr: %s)", exit, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout must be empty, got %q", stdout.String())
			}
		})
	}
}

// TestForksListTouchingParseErrorMessages pins the exact --touching error
// text from the pairing/parse rules, since TestForksListParseErrors only
// checks the exit code.
func TestForksListTouchingParseErrorMessages(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantSubstr string
	}{
		{"touching without value", []string{"list", "o/r", "--touching"}, "--touching requires"},
		{"touching bad pattern", []string{"list", "o/r", "--touching", "["}, "--touching: "},
		{"touching with tier 1", []string{"list", "o/r", "--touching", "a.go", "--tier", "1"}, "--touching needs compare data"},
		{"touching with csv", []string{"list", "o/r", "--touching", "a.go", "--csv"}, "--touching emits NDJSON only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exit := runForksWith(tc.args, &stdout, &stderr); exit != 2 {
				t.Fatalf("exit = %d, want 2 (stderr: %s)", exit, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.wantSubstr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tc.wantSubstr)
			}
		})
	}
}

// --no-sibling-sim followed by --sibling-sim clears the latch, so a later
// --sibling-sim-mode no longer reports a phantom conflict (#86).
func TestForksSiblingSimLatchCleared(t *testing.T) {
	// Reaches past parsing into embedder selection; block the cache so it
	// degrades instead of initializing real ONNX (keeps the test hermetic).
	blockEmbedderCache(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	// Stub the provider so parsing reaches dispatch; a distinctive error proves
	// the conflict check was passed rather than tripped.
	providerFactory = func(context.Context, string, string, string) (forge.Forge, string, *agentio.Error) {
		return nil, "", agentio.NewError(agentio.CodeUpstream, "STUB_REACHED", "x")
	}
	var stdout, stderr bytes.Buffer
	runForksWith([]string{"list", "o/r", "--no-sibling-sim", "--sibling-sim", "--sibling-sim-mode", "fork_intent"}, &stdout, &stderr)
	if strings.Contains(stderr.String(), "conflicts with --no-sibling-sim") {
		t.Fatalf("phantom sibling-sim conflict fired despite re-enable:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "STUB_REACHED") {
		t.Fatalf("parsing did not reach dispatch; stderr:\n%s", stderr.String())
	}
}
