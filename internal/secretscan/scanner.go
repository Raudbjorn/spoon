package secretscan

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxSessionFindings = 5000
	maxDedupeEntries   = 10000
	fingerprintPrefix  = "spoon-recon-fingerprint-v1"
)

func (s *Session) scan(ctx context.Context, unit ScanUnit) (result ScanResult) {
	result.Complete = true
	defer func() {
		if !result.Complete {
			s.stats.PartialUnits++
		}
	}()

	if ctx == nil || ctx.Err() != nil {
		s.markPartial(&result, "canceled")
		return result
	}
	if !hashableUnit(unit) {
		s.markPartial(&result, "invalid_identity")
		return result
	}

	parsed := parsePatch(unit)
	if parsed.reason != "" {
		s.markPartial(&result, parsed.reason)
		return result
	}
	if unit.PatchSource == "compare_diff_oversize" {
		s.markPartial(&result, "compare_diff_oversize")
	}
	if !unit.Complete {
		s.markPartial(&result, safeIncompleteReason(unit.IncompleteReason))
	}
	if s.findings >= maxSessionFindings {
		s.markPartial(&result, "finding_cap")
		return result
	}

	for _, line := range parsed.additions {
		foldedText := ""
		foldedReady := false
		for ruleIndex := range s.scanner.rules {
			if ctx.Err() != nil {
				s.markPartial(&result, "canceled")
				return result
			}
			rule := &s.scanner.rules[ruleIndex]
			if !ruleMatchesPath(rule, unit.Path) {
				continue
			}
			if len(rule.keywords) > 0 {
				if !foldedReady {
					foldedText = strings.ToLower(line.text)
					foldedReady = true
				}
				if !ruleMatchesKeywords(rule, foldedText) {
					continue
				}
			}
			if !s.scanLine(ctx, &result, unit, line, rule) {
				return result
			}
		}
	}
	return result
}

func (s *Session) scanLine(ctx context.Context, result *ScanResult, unit ScanUnit, line addedLine, rule *compiledRule) bool {
	for offset := 0; offset <= len(line.text); {
		if ctx.Err() != nil {
			s.markPartial(result, "canceled")
			return false
		}
		indices := rule.pattern.FindStringSubmatchIndex(line.text[offset:])
		if indices == nil {
			return true
		}
		fullStart, fullEnd := offset+indices[0], offset+indices[1]
		if fullEnd <= fullStart {
			return true
		}
		groupStart, groupEnd := indices[2*rule.secretGroup], indices[2*rule.secretGroup+1]
		if groupStart >= 0 && groupEnd > groupStart {
			groupStart += offset
			groupEnd += offset
			candidate := line.text[groupStart:groupEnd]
			if !matchesAny(rule.allow, candidate) {
				entropy := shannonEntropy(candidate)
				if entropy >= rule.minEntropy {
					if s.findings >= maxSessionFindings {
						s.markPartial(result, "finding_cap")
						return false
					}
					startColumn := utf8.RuneCountInString(line.text[:groupStart]) + 1
					endColumn := startColumn + utf8.RuneCountInString(candidate)
					fingerprint, ok := findingFingerprint(rule, unit, line.line, startColumn, line.line, endColumn)
					if !ok {
						s.markPartial(result, "invalid_identity")
						return false
					}
					duplicate, evicted := s.dedupe.touch(fingerprint)
					if evicted {
						s.stats.DedupeEvictions++
					}
					if !duplicate {
						result.Findings = append(result.Findings, Finding{
							Type:        "recon_finding",
							RuleID:      rule.info.ID,
							RuleVersion: rule.info.Version,
							Severity:    rule.info.Severity,
							Repository:  unit.Repository,
							NetworkRoot: unit.NetworkRoot,
							Fork:        unit.Fork,
							Source:      unit.Source,
							SnapshotID:  unit.SnapshotID,
							CommitSHA:   unit.CommitSHA,
							Path:        unit.Path,
							PatchSource: unit.PatchSource,
							StartLine:   line.line,
							StartColumn: startColumn,
							EndLine:     line.line,
							EndColumn:   endColumn,
							Entropy:     entropy,
							MatchLength: len(candidate),
							Fingerprint: fingerprint,
							Complete:    result.Complete,
						})
						s.findings++
						s.stats.Findings++
						if s.findings == maxSessionFindings {
							s.markPartial(result, "finding_cap")
							return false
						}
					}
				}
			}
		}
		offset = fullEnd
	}
	return true
}

func ruleMatchesPath(rule *compiledRule, path string) bool {
	if _, excluded := rule.exclude.First(path); excluded {
		return false
	}
	if rule.include.Empty() {
		return true
	}
	_, included := rule.include.First(path)
	return included
}

func ruleMatchesKeywords(rule *compiledRule, foldedText string) bool {
	for _, keyword := range rule.keywords {
		if strings.Contains(foldedText, keyword) {
			return true
		}
	}
	return false
}

func matchesAny(patterns []*regexp.Regexp, candidate string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(candidate) {
			return true
		}
	}
	return false
}

func shannonEntropy(value string) float64 {
	if len(value) == 0 {
		return 0
	}
	var frequencies [256]int
	for i := range len(value) {
		frequencies[value[i]]++
	}
	length := float64(len(value))
	var entropy float64
	for _, count := range frequencies {
		if count == 0 {
			continue
		}
		probability := float64(count) / length
		entropy -= probability * math.Log2(probability)
	}
	return entropy
}

func findingFingerprint(rule *compiledRule, unit ScanUnit, startLine, startColumn, endLine, endColumn int) (string, bool) {
	parts := [...]string{
		fingerprintPrefix,
		rule.info.Version,
		rule.info.ID,
		string(unit.Source),
		unit.Repository,
		unit.NetworkRoot,
		unit.Fork,
		unit.SnapshotID,
		unit.CommitSHA,
		unit.Path,
		unit.PatchSource,
		strconv.Itoa(startLine),
		strconv.Itoa(startColumn),
		strconv.Itoa(endLine),
		strconv.Itoa(endColumn),
	}
	for _, part := range parts {
		if strings.IndexByte(part, 0) >= 0 {
			return "", false
		}
	}
	hash := sha256.New()
	for index, part := range parts {
		if index != 0 {
			_, _ = hash.Write([]byte{0})
		}
		_, _ = io.WriteString(hash, part)
	}
	return hex.EncodeToString(hash.Sum(nil)), true
}

func hashableUnit(unit ScanUnit) bool {
	for _, value := range [...]string{
		string(unit.Source), unit.Repository, unit.NetworkRoot, unit.Fork, unit.SnapshotID,
		unit.CommitSHA, unit.Path, unit.PatchSource,
	} {
		if strings.IndexByte(value, 0) >= 0 {
			return false
		}
	}
	return true
}

func safeIncompleteReason(reason string) string {
	switch reason {
	case "files_truncated":
		return reason
	default:
		return "incomplete_unit"
	}
}

func (s *Session) markPartial(result *ScanResult, reason string) {
	if !result.Complete {
		return
	}
	result.Complete = false
	result.IncompleteReason = reason
	for index := range result.Findings {
		result.Findings[index].Complete = false
	}
}

type fingerprintLRU struct {
	capacity int
	order    *list.List
	entries  map[string]*list.Element
}

type fingerprintEntry struct {
	key string
}

func newFingerprintLRU(capacity int) *fingerprintLRU {
	return &fingerprintLRU{capacity: capacity, order: list.New(), entries: make(map[string]*list.Element, capacity)}
}

func (l *fingerprintLRU) touch(key string) (duplicate, evicted bool) {
	if element, exists := l.entries[key]; exists {
		l.order.MoveToFront(element)
		return true, false
	}
	if l.capacity == 0 {
		return false, false
	}
	if l.order.Len() == l.capacity {
		tail := l.order.Back()
		delete(l.entries, tail.Value.(fingerprintEntry).key)
		l.order.Remove(tail)
		evicted = true
	}
	l.entries[key] = l.order.PushFront(fingerprintEntry{key: key})
	return false, evicted
}
