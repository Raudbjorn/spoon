package secretscan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"

	"github.com/svnbjrn/spoon/internal/pathmatch"
)

const (
	maxRuleFileBytes = 2 * 1024 * 1024
	maxRules         = 5000
	maxRuleVersion   = 128
	maxRuleID        = 128
	maxRuleName      = 256
	maxPatternBytes  = 4 * 1024
	maxRuleValues    = 64
	maxValueBytes    = 256
)

type compiledRule struct {
	info        RuleInfo
	pattern     *regexp.Regexp
	secretGroup int
	keywords    []string
	minEntropy  float64
	include     pathmatch.Matcher
	exclude     pathmatch.Matcher
	allow       []*regexp.Regexp
}

type decodedRuleFile struct {
	SchemaVersion *int        `json:"schemaVersion"`
	Version       *string     `json:"version"`
	Rules         *[]RuleSpec `json:"rules"`
}

func loadRules(r io.Reader) (*Scanner, error) {
	if r == nil {
		return nil, validationError("invalid_schema", 0, "document")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxRuleFileBytes+1))
	if err != nil {
		return nil, validationError("invalid_schema", 0, "document")
	}
	if len(data) > maxRuleFileBytes {
		return nil, validationError("limit_exceeded", 0, "document")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, validationError("invalid_schema", 0, "document")
	}

	var decoded decodedRuleFile
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, validationError("invalid_schema", 0, "document")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, validationError("invalid_schema", 0, "document")
	}
	if decoded.SchemaVersion == nil || decoded.Version == nil || decoded.Rules == nil {
		return nil, validationError("invalid_schema", 0, "document")
	}

	file := RuleFile{SchemaVersion: *decoded.SchemaVersion, Version: *decoded.Version, Rules: *decoded.Rules}
	if file.SchemaVersion != 1 {
		return nil, validationError("invalid_schema", 0, "schemaVersion")
	}
	if err := validateIdentity(file.Version, maxRuleVersion, 0, "version"); err != nil {
		return nil, err
	}
	if len(file.Rules) > maxRules {
		return nil, validationError("limit_exceeded", 0, "rules")
	}

	scanner := &Scanner{
		rules: make([]compiledRule, 0, len(file.Rules)),
		infos: make([]RuleInfo, 0, len(file.Rules)),
	}
	ids := make(map[string]struct{}, len(file.Rules))
	for index, spec := range file.Rules {
		ordinal := index + 1
		rule, err := compileRule(file.Version, spec, ordinal, ids)
		if err != nil {
			return nil, err
		}
		scanner.rules = append(scanner.rules, rule)
		scanner.infos = append(scanner.infos, rule.info)
	}
	return scanner, nil
}

func compileRule(version string, spec RuleSpec, ordinal int, ids map[string]struct{}) (compiledRule, error) {
	if err := validateIdentity(spec.ID, maxRuleID, ordinal, "id"); err != nil {
		return compiledRule{}, err
	}
	if _, exists := ids[spec.ID]; exists {
		return compiledRule{}, validationError("duplicate_rule_id", ordinal, "id")
	}
	ids[spec.ID] = struct{}{}
	if err := validateIdentity(spec.Name, maxRuleName, ordinal, "name"); err != nil {
		return compiledRule{}, err
	}
	if spec.Severity != "note" && spec.Severity != "warning" && spec.Severity != "error" {
		return compiledRule{}, validationError("invalid_severity", ordinal, "severity")
	}
	if len(spec.Pattern) > maxPatternBytes {
		return compiledRule{}, validationError("limit_exceeded", ordinal, "pattern")
	}
	pattern, err := regexp.Compile(spec.Pattern)
	if err != nil || pattern.MatchString("") {
		return compiledRule{}, validationError("invalid_regex", ordinal, "pattern")
	}
	if spec.SecretGroup < 0 || spec.SecretGroup > pattern.NumSubexp() {
		return compiledRule{}, validationError("invalid_capture_group", ordinal, "secretGroup")
	}
	if math.IsNaN(spec.MinEntropy) || math.IsInf(spec.MinEntropy, 0) || spec.MinEntropy < 0 || spec.MinEntropy > 8 {
		return compiledRule{}, validationError("invalid_schema", ordinal, "minEntropy")
	}
	if err := validateValues(spec.Keywords, ordinal, "keywords"); err != nil {
		return compiledRule{}, err
	}
	if len(spec.IncludePaths)+len(spec.ExcludePaths) > maxRuleValues {
		return compiledRule{}, validationError("limit_exceeded", ordinal, "pathPatterns")
	}
	if err := validateValues(spec.IncludePaths, ordinal, "includePaths"); err != nil {
		return compiledRule{}, err
	}
	if err := validateValues(spec.ExcludePaths, ordinal, "excludePaths"); err != nil {
		return compiledRule{}, err
	}
	if err := validateAllowPatterns(spec.AllowPatterns, ordinal, "allowPatterns"); err != nil {
		return compiledRule{}, err
	}

	include, err := pathmatch.Compile(spec.IncludePaths)
	if err != nil {
		return compiledRule{}, validationError("invalid_path_pattern", ordinal, "includePaths")
	}
	exclude, err := pathmatch.Compile(spec.ExcludePaths)
	if err != nil {
		return compiledRule{}, validationError("invalid_path_pattern", ordinal, "excludePaths")
	}
	allow := make([]*regexp.Regexp, 0, len(spec.AllowPatterns))
	for _, raw := range spec.AllowPatterns {
		re, err := regexp.Compile(raw)
		if err != nil || re.MatchString("") {
			return compiledRule{}, validationError("invalid_regex", ordinal, "allowPatterns")
		}
		allow = append(allow, re)
	}

	return compiledRule{
		info:        RuleInfo{ID: spec.ID, Name: spec.Name, Version: version, Severity: spec.Severity},
		pattern:     pattern,
		secretGroup: spec.SecretGroup,
		keywords:    foldedKeywords(spec.Keywords),
		minEntropy:  spec.MinEntropy,
		include:     include,
		exclude:     exclude,
		allow:       allow,
	}, nil
}

func foldedKeywords(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	folded := make([]string, len(values))
	for index, value := range values {
		folded[index] = strings.ToLower(value)
	}
	return folded
}

func validateIdentity(value string, maxBytes, ordinal int, field string) error {
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return validationError("invalid_schema", ordinal, field)
	}
	if len(value) > maxBytes {
		return validationError("limit_exceeded", ordinal, field)
	}
	return nil
}

func validateValues(values []string, ordinal int, field string) error {
	if len(values) > maxRuleValues {
		return validationError("limit_exceeded", ordinal, field)
	}
	for _, value := range values {
		if len(value) > maxValueBytes {
			return validationError("limit_exceeded", ordinal, field)
		}
	}
	return nil
}

func validateAllowPatterns(values []string, ordinal int, field string) error {
	if len(values) > maxRuleValues {
		return validationError("limit_exceeded", ordinal, field)
	}
	for _, value := range values {
		if len(value) > maxPatternBytes {
			return validationError("limit_exceeded", ordinal, field)
		}
	}
	return nil
}

func validationError(code string, ordinal int, field string) error {
	return fmt.Errorf("%s rule=%d field=%s", code, ordinal, field)
}
