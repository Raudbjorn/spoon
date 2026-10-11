package recon

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/secretscan"
)

func TestSARIFWritesSortedRedactedDeterministicDocument(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "findings.sarif")
	gzipPath := filepath.Join(dir, "findings.sarif.gz")
	rules := []secretscan.RuleInfo{
		{ID: "z-rule", Name: "Z rule", Version: "v1", Severity: "error"},
		{ID: "a-rule", Name: "A rule", Version: "v1", Severity: "note"},
	}
	findings := []secretscan.Finding{
		{RuleID: "z-rule", Severity: "error", Repository: "z/repo", Fork: "fork/two", SnapshotID: "2026-10-06T00:00:00Z", NetworkRoot: reconTestSecret, Path: "z.go", StartLine: 4, StartColumn: 2, EndLine: 4, EndColumn: 9, Fingerprint: "z-fingerprint"},
		{RuleID: "a-rule", Severity: "note", Repository: "a/repo", Fork: "fork/one", SnapshotID: "2026-10-05T00:00:00Z", Path: "src/a b.go", StartLine: 3, StartColumn: 1, EndLine: 3, EndColumn: 8, Fingerprint: "a-fingerprint"},
	}
	opts := SARIFOptions{ToolVersion: "test-version", Complete: false, Reasons: map[string]int{"z_reason": 1, "a_reason": 2}}
	if err := WriteSARIFFile(plain, rules, findings, opts); err != nil {
		t.Fatalf("WriteSARIFFile() error = %v", err)
	}
	first, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(first), reconTestSecret) {
		t.Fatal("SARIF leaked non-output scanner metadata")
	}
	if mode, err := os.Stat(plain); err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("output mode = %v / %v, want 0600", mode, err)
	}
	assertSARIFDocument(t, first)

	again := filepath.Join(dir, "again.sarif")
	if err := WriteSARIFFile(again, rules, findings, opts); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(again)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("plain SARIF bytes are not deterministic")
	}
	if err := WriteSARIFFile(gzipPath, rules, findings, opts); err != nil {
		t.Fatal(err)
	}
	compressedOne, err := os.ReadFile(gzipPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSARIFFile(gzipPath, rules, findings, opts); err != nil {
		t.Fatal(err)
	}
	compressedTwo, err := os.ReadFile(gzipPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compressedOne, compressedTwo) {
		t.Fatal("gzip SARIF bytes are not deterministic")
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressedOne))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := ioReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, decompressed) {
		t.Fatal("gzip SARIF does not contain the deterministic plain document")
	}
}

func assertSARIFDocument(t *testing.T, data []byte) {
	t.Helper()
	var document struct {
		Schema  string `json:"$schema"`
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name           string `json:"name"`
					InformationURI string `json:"informationUri"`
					Version        string `json:"version"`
					Rules          []struct {
						ID                   string `json:"id"`
						DefaultConfiguration struct {
							Level string `json:"level"`
						} `json:"defaultConfiguration"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID  string `json:"ruleId"`
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
					} `json:"physicalLocation"`
				} `json:"locations"`
				PartialFingerprints map[string]string `json:"partialFingerprints"`
			} `json:"results"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != "https://json.schemastore.org/sarif-2.1.0.json" || document.Version != "2.1.0" || len(document.Runs) != 1 {
		t.Fatalf("document header = %#v", document)
	}
	run := document.Runs[0]
	if run.Tool.Driver.Name != "spoon" || run.Tool.Driver.InformationURI != "https://github.com/Raudbjorn/spoon" || run.Tool.Driver.Version != "test-version" {
		t.Fatalf("tool driver = %#v", run.Tool.Driver)
	}
	if len(run.Tool.Driver.Rules) != 2 || run.Tool.Driver.Rules[0].ID != "a-rule" || run.Tool.Driver.Rules[0].DefaultConfiguration.Level != "note" || run.Tool.Driver.Rules[1].ID != "z-rule" || run.Tool.Driver.Rules[1].DefaultConfiguration.Level != "error" {
		t.Fatalf("rules = %#v", run.Tool.Driver.Rules)
	}
	if len(run.Results) != 2 || run.Results[0].RuleID != "a-rule" || run.Results[1].RuleID != "z-rule" {
		t.Fatalf("results = %#v", run.Results)
	}
	first := run.Results[0]
	if first.Level != "note" || first.Message.Text != "rule a-rule matched at a/repo:src/a b.go:3" || len(first.Locations) != 1 || first.Locations[0].PhysicalLocation.ArtifactLocation.URI != "spoon://repository/a/repo/src/a%20b.go?fork=fork%2Fone&snapshot=2026-10-05T00%3A00%3A00Z" || first.PartialFingerprints["spoon/recon/v1"] != "a-fingerprint" {
		t.Fatalf("first result = %#v", first)
	}
	if string(run.Properties["spoonReconComplete"]) != "false" || string(run.Properties["spoonReconPartialReasons"]) != `{"a_reason":2,"z_reason":1}` {
		t.Fatalf("properties = %#v", run.Properties)
	}
}

func TestSARIFRejectsLimitsAndPreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.sarif")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	findings := make([]secretscan.Finding, 25_001)
	if err := WriteSARIFFile(path, nil, findings, SARIFOptions{}); err == nil {
		t.Fatal("WriteSARIFFile() accepted too many results")
	} else if strings.Contains(err.Error(), reconTestSecret) {
		t.Fatalf("limit error leaked data: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old" {
		t.Fatalf("failed write changed destination: %q / %v", got, err)
	}

	tooLarge := deterministicText(15 * 1024 * 1024)
	if err := WriteSARIFFile(path, nil, []secretscan.Finding{{RuleID: "rule", Severity: "warning", Repository: "owner/repo", Path: tooLarge, StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2, Fingerprint: "fingerprint"}}, SARIFOptions{}); err == nil {
		t.Fatal("WriteSARIFFile() accepted SARIF with gzip bytes above cap")
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "old" {
		t.Fatalf("gzip-limit failure changed destination: %q / %v", got, err)
	}
}

func TestSARIFRefusesSymlinkAndAtomicallyReplacesRegularFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.sarif")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sarif")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteSARIFFile(link, nil, nil, SARIFOptions{}); err == nil {
		t.Fatal("WriteSARIFFile() replaced a symlink")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "target" {
		t.Fatalf("symlink target changed: %q / %v", got, err)
	}

	path := filepath.Join(dir, "replace.sarif")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSARIFFile(path, nil, nil, SARIFOptions{}); err != nil {
		t.Fatal(err)
	}
	if mode, err := os.Stat(path); err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("replacement mode = %v / %v, want 0600", mode, err)
	}
}

func deterministicText(size int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	buf := make([]byte, size)
	state := uint64(1)
	for index := range buf {
		state = state*6364136223846793005 + 1
		buf[index] = alphabet[state>>58]
	}
	return string(buf)
}

func ioReadAll(reader *gzip.Reader) ([]byte, error) {
	var output bytes.Buffer
	_, err := output.ReadFrom(reader)
	return output.Bytes(), err
}
