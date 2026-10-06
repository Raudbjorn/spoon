package secretscan

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

const testSecret = "spoon_test_secret_7f3c9a1e"

func ruleDocument(rule string) string {
	return `{"schemaVersion":1,"version":"rules-v1","rules":[` + rule + `]}`
}

func singleRuleDocument(pattern, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return ruleDocument(fmt.Sprintf(`{"id":"test-rule","name":"Test rule","severity":"warning","pattern":%q%s}`, pattern, extra))
}

func mustScanner(t *testing.T, document string) *Scanner {
	t.Helper()
	scanner, err := LoadRules(strings.NewReader(document))
	if err != nil {
		t.Fatalf("LoadRules() error = %v", err)
	}
	return scanner
}

func requireValidationError(t *testing.T, document, wantCode string) {
	t.Helper()
	_, err := LoadRules(strings.NewReader(document))
	if err == nil {
		t.Fatalf("LoadRules() succeeded, want %s", wantCode)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatal("LoadRules() leaked scanned input in an error")
	}
	if got := err.Error(); !strings.Contains(got, wantCode) || !strings.Contains(got, "rule=") || !strings.Contains(got, "field=") {
		t.Fatalf("LoadRules() did not return stable %s rule/field validation", wantCode)
	}
}

func TestLoadRulesReadsStrictFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/rules-v1.json")
	if err != nil {
		t.Fatal(err)
	}

	scanner, err := LoadRules(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("LoadRules() error = %v", err)
	}
	got := scanner.Rules()
	want := []RuleInfo{{ID: "spoon-test-secret", Name: "Spoon test secret", Version: "test-rules-v1", Severity: "warning"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Rules() = %#v, want %#v", got, want)
	}
	got[0].ID = "mutated"
	if scanner.Rules()[0].ID != "spoon-test-secret" {
		t.Fatal("Rules() returned scanner-owned storage")
	}
}

func TestLoadRulesAllowsFourKiBAllowPatterns(t *testing.T) {
	allow := strings.Repeat("x", 257)
	_, err := LoadRules(strings.NewReader(singleRuleDocument("secret", `"allowPatterns":["`+allow+`"]`)))
	if err != nil {
		t.Fatal("LoadRules() rejected a valid allow pattern below the four KiB cap")
	}
}

func TestLoadRulesRejectsInvalidInputsWithoutEchoingThem(t *testing.T) {
	validRule := `{"id":"rule","name":"Rule","severity":"warning","pattern":"(secret)","secretGroup":1}`
	tooManyRules := make([]string, 0, 5001)
	for i := range 5001 {
		tooManyRules = append(tooManyRules, fmt.Sprintf(`{"id":"r%d","name":"Rule","severity":"note","pattern":"x"}`, i))
	}
	tooManyKeywords := `[` + strings.TrimSuffix(strings.Repeat(`"x",`, 65), ",") + `]`
	cases := []struct {
		name, document, code string
	}{
		{"non-object", `[]`, "invalid_schema"},
		{"unknown-field", ruleDocument(validRule)[:len(ruleDocument(validRule))-1] + `,"unknown":"` + testSecret + `"}`, "invalid_schema"},
		{"trailing-json", ruleDocument(validRule) + `{}`, "invalid_schema"},
		{"schema-version", strings.Replace(ruleDocument(validRule), `"schemaVersion":1`, `"schemaVersion":2`, 1), "invalid_schema"},
		{"empty-version", strings.Replace(ruleDocument(validRule), `"rules-v1"`, `""`, 1), "invalid_schema"},
		{"nul-version", strings.Replace(ruleDocument(validRule), `"rules-v1"`, `"\u0000"`, 1), "invalid_schema"},
		{"identity-too-long", strings.Replace(ruleDocument(validRule), `"rule"`, `"`+strings.Repeat("x", 129)+`"`, 1), "limit_exceeded"},
		{"missing-name", ruleDocument(`{"id":"rule","severity":"warning","pattern":"secret"}`), "invalid_schema"},
		{"duplicate-id", `{"schemaVersion":1,"version":"rules-v1","rules":[` + validRule + `,` + validRule + `]}`, "duplicate_rule_id"},
		{"severity", ruleDocument(`{"id":"rule","name":"Rule","severity":"urgent","pattern":"secret"}`), "invalid_severity"},
		{"invalid-regex", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"[` + testSecret + `"}`), "invalid_regex"},
		{"empty-match-regex", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"x*"}`), "invalid_regex"},
		{"capture-group", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"(secret)","secretGroup":2}`), "invalid_capture_group"},
		{"negative-capture-group", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"secret","secretGroup":-1}`), "invalid_capture_group"},
		{"path-pattern", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"secret","includePaths":["["]}`), "invalid_path_pattern"},
		{"too-many-keywords", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"secret","keywords":` + tooManyKeywords + `}`), "limit_exceeded"},
		{"keyword-too-long", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"secret","keywords":["` + strings.Repeat("x", 257) + `"]}`), "limit_exceeded"},
		{"pattern-too-long", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"` + strings.Repeat("x", 4097) + `"}`), "limit_exceeded"},
		{"allow-pattern-too-long", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"secret","allowPatterns":["` + strings.Repeat("x", 4097) + `"]}`), "limit_exceeded"},
		{"entropy-low", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"secret","minEntropy":-0.01}`), "invalid_schema"},
		{"entropy-high", ruleDocument(`{"id":"rule","name":"Rule","severity":"warning","pattern":"secret","minEntropy":8.01}`), "invalid_schema"},
		{"too-many-rules", `{"schemaVersion":1,"version":"rules-v1","rules":[` + strings.Join(tooManyRules, ",") + `]}`, "limit_exceeded"},
		{"oversize-file", strings.Repeat(" ", 2*1024*1024+1), "limit_exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireValidationError(t, tc.document, tc.code)
		})
	}
}

type secretErrorReader struct{}

func (secretErrorReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("mock API error %s", testSecret)
}

func TestLoadRulesRedactsReaderErrors(t *testing.T) {
	_, err := LoadRules(secretErrorReader{})
	if err == nil {
		t.Fatal("LoadRules() succeeded")
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatal("LoadRules() leaked reader error")
	}
}
