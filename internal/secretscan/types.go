// Package secretscan scans bounded unified-diff additions without retaining matched text.
package secretscan

import (
	"context"
	"io"
)

// Source identifies the provenance of a scanned patch.
type Source string

const (
	SourceCurrentCompare   Source = "current_compare"
	SourceCurrentCommit    Source = "current_commit"
	SourceDiscardedCompare Source = "discarded_compare"
	SourceDiscardedCommit  Source = "discarded_commit"
)

// ScanUnit is one file patch and its provenance.
type ScanUnit struct {
	Source           Source
	Repository       string
	NetworkRoot      string
	Fork             string
	SnapshotID       string
	CommitSHA        string
	Path             string
	Patch            string
	PatchSource      string
	Additions        int
	Complete         bool
	IncompleteReason string
}

// RuleInfo is safe operator-owned rule metadata.
type RuleInfo struct {
	ID, Name, Version, Severity string
}

// Finding contains only provenance and span metadata, never scanned text.
type Finding struct {
	Type        string  `json:"type"`
	RuleID      string  `json:"ruleId"`
	RuleVersion string  `json:"ruleVersion"`
	Severity    string  `json:"severity"`
	Repository  string  `json:"repository"`
	NetworkRoot string  `json:"networkRoot,omitempty"`
	Fork        string  `json:"fork,omitempty"`
	Source      Source  `json:"source"`
	SnapshotID  string  `json:"snapshotId"`
	CommitSHA   string  `json:"commitSha,omitempty"`
	Path        string  `json:"path"`
	PatchSource string  `json:"patchSource,omitempty"`
	StartLine   int     `json:"startLine"`
	StartColumn int     `json:"startColumn"`
	EndLine     int     `json:"endLine"`
	EndColumn   int     `json:"endColumn"`
	Entropy     float64 `json:"entropy,omitempty"`
	MatchLength int     `json:"matchLength"`
	Fingerprint string  `json:"fingerprint"`
	Complete    bool    `json:"complete"`
}

// ScanResult reports a scan's safe findings and whether its input was complete.
type ScanResult struct {
	Findings         []Finding
	Complete         bool
	IncompleteReason string
}

// Stats is cumulative for one Session.
type Stats struct {
	Findings, DedupeEvictions, PartialUnits int
}

// RuleFile is the strict JSON v1 rules document.
type RuleFile struct {
	SchemaVersion int        `json:"schemaVersion"`
	Version       string     `json:"version"`
	Rules         []RuleSpec `json:"rules"`
}

// RuleSpec is one operator-owned scanner rule.
type RuleSpec struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Severity      string   `json:"severity"`
	Pattern       string   `json:"pattern"`
	SecretGroup   int      `json:"secretGroup,omitempty"`
	Keywords      []string `json:"keywords,omitempty"`
	MinEntropy    float64  `json:"minEntropy,omitempty"`
	IncludePaths  []string `json:"includePaths,omitempty"`
	ExcludePaths  []string `json:"excludePaths,omitempty"`
	AllowPatterns []string `json:"allowPatterns,omitempty"`
}

// Scanner is an immutable compiled rule set.
type Scanner struct {
	rules []compiledRule
	infos []RuleInfo
}

// Session holds bounded dedupe and cumulative scan statistics.
type Session struct {
	scanner  *Scanner
	dedupe   *fingerprintLRU
	stats    Stats
	findings int
}

// LoadRules loads one strict JSON v1 rule document.
func LoadRules(r io.Reader) (*Scanner, error) {
	return loadRules(r)
}

// Rules returns a copy of safe rule metadata.
func (s *Scanner) Rules() []RuleInfo {
	return append([]RuleInfo(nil), s.infos...)
}

// NewSession creates an independent bounded scan session.
func (s *Scanner) NewSession() *Session {
	return &Session{scanner: s, dedupe: newFingerprintLRU(maxDedupeEntries)}
}

// Scan scans one bounded patch.
func (s *Session) Scan(ctx context.Context, unit ScanUnit) ScanResult {
	return s.scan(ctx, unit)
}

// Stats returns cumulative session counters.
func (s *Session) Stats() Stats {
	return s.stats
}
