package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReconScanStreamsRedactedFindingsAndFinalSummary(t *testing.T) {
	fixture := reconFixture(t, "forks-with-patches.ndjson")
	rules := secretscanRulesFixture(t)
	sarif := filepath.Join(t.TempDir(), "scan.sarif")
	var stdout, stderr bytes.Buffer
	exit := dispatch([]string{"recon", "scan", "--authorized", "--rules", rules, "--input", fixture, "--sarif", sarif}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	const sentinel = "spoon_test_secret_7f3c9a1e"
	if strings.Contains(stdout.String(), sentinel) || strings.Contains(stderr.String(), sentinel) {
		t.Fatal("recon scan leaked scanned content")
	}
	var findings []map[string]any
	for line := range strings.Lines(stdout.String()) {
		if line == "" {
			continue
		}
		var finding map[string]any
		if err := json.Unmarshal([]byte(line), &finding); err != nil {
			t.Fatalf("stdout line is not finding NDJSON: %v", err)
		}
		findings = append(findings, finding)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %#v, want aggregate, plus-prefix, and commit findings", findings)
	}
	for _, finding := range findings {
		if finding["type"] != "recon_finding" {
			t.Fatalf("unexpected stdout record %#v", finding)
		}
	}
	var summary map[string]any
	decoder := json.NewDecoder(&stderr)
	if err := decoder.Decode(&summary); err != nil {
		t.Fatalf("stderr summary = %v: %s", err, stderr.String())
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("stderr has extra output: %v", err)
	}
	if summary["type"] != "recon_summary" || summary["mode"] != "scan" || summary["complete"] != false || summary["findings"] != float64(3) {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := os.Stat(sarif); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(sarif)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), sentinel) {
		t.Fatal("SARIF leaked scanned content")
	}
}

func TestReconRoutingPreBootstrapPaths(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	for _, tc := range []struct {
		name string
		args []string
		exit int
	}{
		{"help", []string{"recon", "--help"}, 0},
		{"missing-verb", []string{"recon"}, 2},
		{"unknown-verb", []string{"recon", "unknown"}, 2},
		{"missing-authorization", []string{"recon", "scan", "--rules", secretscanRulesFixture(t)}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := dispatch(tc.args, &stdout, &stderr); got != tc.exit {
				t.Fatalf("exit = %d, want %d", got, tc.exit)
			}
			entries, err := os.ReadDir(xdg)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("pre-bootstrap path created config state: %v", entries)
			}
		})
	}
}

func TestReconRewritesRoutesThroughConfiguredDispatch(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	var stdout, stderr bytes.Buffer
	if exit := dispatch([]string{"recon", "rewrites"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("exit = %d, want bad input", exit)
	}
	entries, err := os.ReadDir(xdg)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("recon rewrites did not reach configured dispatch")
	}
}

func TestReconScanExitMatrixAndSecretSafeFailures(t *testing.T) {
	rules := secretscanRulesFixture(t)
	unscannable := filepath.Join(t.TempDir(), "unscannable.ndjson")
	if err := os.WriteFile(unscannable, []byte(`{"id":"owner/repo","pushed_at":"2026-10-06T00:00:00Z","t2":{"files_unfetched":true}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(t.TempDir(), "malformed.ndjson")
	if err := os.WriteFile(malformed, []byte(`{"id":"spoon_test_secret_7f3c9a1e"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidRules := filepath.Join(t.TempDir(), "invalid-rules.json")
	if err := os.WriteFile(invalidRules, []byte(`{"schemaVersion":2}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		args     []string
		wantExit int
		wantCode string
	}{
		{"policy", []string{"--rules", rules}, 2, "policy_violation"},
		{"all-unscannable", []string{"--authorized", "--rules", rules, "--input", unscannable}, 2, "bad_input"},
		{"malformed", []string{"--authorized", "--rules", rules, "--input", malformed}, 2, "bad_input"},
		{"rule-validation-before-input-open", []string{"--authorized", "--rules", invalidRules, "--input", filepath.Join(t.TempDir(), "missing.ndjson")}, 2, "bad_input"},
		{"input-open", []string{"--authorized", "--rules", rules, "--input", filepath.Join(t.TempDir(), "missing.ndjson")}, 1, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := runReconScanWith(tc.args, &stdout, &stderr)
			if exit != tc.wantExit {
				t.Fatalf("exit = %d, want %d: %s", exit, tc.wantExit, stderr.String())
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code": "`+tc.wantCode+`"`) || strings.Contains(stderr.String(), "spoon_test_secret_7f3c9a1e") || strings.Contains(stderr.String(), "recon_summary") {
				t.Fatalf("failure output = stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestReconScanTreatsWriterAndSARIFFailuresAsInternal(t *testing.T) {
	fixture := reconFixture(t, "forks-with-patches.ndjson")
	rules := secretscanRulesFixture(t)
	var stderr bytes.Buffer
	if exit := runReconScanWith([]string{"--authorized", "--rules", rules, "--input", fixture}, failingReconWriter{}, &stderr); exit != 1 {
		t.Fatalf("writer exit = %d, stderr = %s", exit, stderr.String())
	}
	if strings.Contains(stderr.String(), "writer-sentinel") || strings.Contains(stderr.String(), "recon_summary") || !strings.Contains(stderr.String(), `"code": "internal"`) {
		t.Fatalf("writer failure output = %q", stderr.String())
	}

	stderr.Reset()
	path := t.TempDir()
	if exit := runReconScanWith([]string{"--authorized", "--rules", rules, "--input", fixture, "--sarif", path}, io.Discard, &stderr); exit != 1 {
		t.Fatalf("SARIF exit = %d, stderr = %s", exit, stderr.String())
	}
	if strings.Contains(stderr.String(), "recon_summary") || !strings.Contains(stderr.String(), `"code": "internal"`) {
		t.Fatalf("SARIF failure output = %q", stderr.String())
	}
}

type failingReconWriter struct{}

func (failingReconWriter) Write([]byte) (int, error) { return 0, errors.New("writer-sentinel") }

func reconFixture(t *testing.T, name string) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(here), "..", "..", "internal", "recon", "testdata", name)
}

func secretscanRulesFixture(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(here), "..", "..", "internal", "secretscan", "testdata", "rules-v1.json")
}
