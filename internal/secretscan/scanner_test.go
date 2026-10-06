package secretscan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func scanUnit(patch string, additions int) ScanUnit {
	return ScanUnit{
		Source:      SourceCurrentCompare,
		Repository:  "owner/repo",
		NetworkRoot: "owner/root",
		Fork:        "owner/repo",
		SnapshotID:  "2026-10-06T00:00:00Z",
		Path:        "src/file.go",
		Patch:       patch,
		PatchSource: "compare",
		Additions:   additions,
		Complete:    true,
	}
}

func TestSessionScansAdditionWhoseSourceStartsWithPlus(t *testing.T) {
	scanner := mustScanner(t, singleRuleDocument(`(\+\+`+testSecret+`)`, `"secretGroup":1`))
	unit := scanUnit("--- a/src/file.go\n+++ b/src/file.go\n@@ -0,0 +1,1 @@\n+++"+testSecret, 1)

	result := scanner.NewSession().Scan(context.Background(), unit)
	if !result.Complete {
		t.Fatalf("Scan() complete = false, reason = %q", result.IncompleteReason)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("Scan() findings = %#v, want one finding", result.Findings)
	}
	finding := result.Findings[0]
	if finding.StartLine != 1 || finding.StartColumn != 1 || finding.EndColumn != utf8.RuneCountInString("++"+testSecret)+1 {
		t.Fatalf("finding positions = %d:%d-%d, want 1:1-%d", finding.StartLine, finding.StartColumn, finding.EndColumn, utf8.RuneCountInString("++"+testSecret)+1)
	}
	if finding.Source != SourceCurrentCompare || finding.PatchSource != "compare" || finding.MatchLength != len("++"+testSecret) {
		t.Fatalf("finding attribution = %#v", finding)
	}
}

func TestSessionTreatsEmptyAndBoundedPatchesAsPartialOnlyWhenNeeded(t *testing.T) {
	scanner := mustScanner(t, singleRuleDocument(`(`+testSecret+`)`, `"secretGroup":1`))
	cases := []struct {
		name       string
		unit       ScanUnit
		complete   bool
		wantReason string
	}{
		{
			name:     "empty-binary",
			unit:     scanUnit("", 0),
			complete: true,
		},
		{
			name:       "unhunked-deletion",
			unit:       scanUnit("-not a diff header", 0),
			complete:   false,
			wantReason: "malformed_patch",
		},
		{
			name:       "neutral-unhunked-text",
			unit:       scanUnit("Binary files differ", 0),
			complete:   false,
			wantReason: "malformed_patch",
		},
		{
			name:       "empty-with-additions",
			unit:       scanUnit("", 1),
			complete:   false,
			wantReason: "empty_patch",
		},
		{
			name: "oversize-source",
			unit: func() ScanUnit {
				u := scanUnit("@@ -0,0 +1,1 @@\n+"+testSecret, 1)
				u.PatchSource = "compare_diff_oversize"
				return u
			}(),
			complete:   false,
			wantReason: "compare_diff_oversize",
		},
		{
			name:       "patch-byte-cap",
			unit:       scanUnit(strings.Repeat("x", 64*1024+1), 0),
			complete:   false,
			wantReason: "patch_too_large",
		},
		{
			name: "caller-partial",
			unit: func() ScanUnit {
				u := scanUnit("@@ -0,0 +1,1 @@\n+"+testSecret, 1)
				u.Complete = false
				u.IncompleteReason = "files_truncated"
				return u
			}(),
			complete:   false,
			wantReason: "files_truncated",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := scanner.NewSession().Scan(context.Background(), tc.unit)
			if result.Complete != tc.complete {
				t.Fatalf("Scan() complete = %v, want %v (%q)", result.Complete, tc.complete, result.IncompleteReason)
			}
			if tc.wantReason != "" && result.IncompleteReason != tc.wantReason {
				t.Fatalf("Scan() incomplete reason = %q, want %q", result.IncompleteReason, tc.wantReason)
			}
			if tc.name == "empty-binary" && len(result.Findings) != 0 {
				t.Fatalf("binary empty patch findings = %#v, want none", result.Findings)
			}
		})
	}
}

func TestSessionUsesUnicodeColumnsAndAllMatchingGates(t *testing.T) {
	patch := "@@ -0,0 +12,1 @@\n+αβ TOKEN " + testSecret
	unit := scanUnit(patch, 1)
	baseRule := `(` + testSecret + `)`

	t.Run("unicode-column-and-keyword", func(t *testing.T) {
		scanner := mustScanner(t, singleRuleDocument(baseRule, `"secretGroup":1,"keywords":["token"],"includePaths":["src/**"]`))
		result := scanner.NewSession().Scan(context.Background(), unit)
		if !result.Complete || len(result.Findings) != 1 {
			t.Fatalf("Scan() = %#v, want one complete finding", result)
		}
		finding := result.Findings[0]
		wantStart := utf8.RuneCountInString("αβ TOKEN ") + 1
		wantEnd := wantStart + utf8.RuneCountInString(testSecret)
		if finding.StartLine != 12 || finding.StartColumn != wantStart || finding.EndLine != 12 || finding.EndColumn != wantEnd {
			t.Fatalf("positions = %d:%d-%d:%d, want 12:%d-12:%d", finding.StartLine, finding.StartColumn, finding.EndLine, finding.EndColumn, wantStart, wantEnd)
		}
		if finding.Entropy <= 0 || finding.MatchLength != len(testSecret) {
			t.Fatalf("finding entropy/length = %v/%d", finding.Entropy, finding.MatchLength)
		}
	})

	t.Run("exclude-wins", func(t *testing.T) {
		scanner := mustScanner(t, singleRuleDocument(baseRule, `"secretGroup":1,"includePaths":["src/**"],"excludePaths":["src/generated/**"]`))
		excluded := unit
		excluded.Path = "src/generated/file.go"
		if got := scanner.NewSession().Scan(context.Background(), excluded); !got.Complete || len(got.Findings) != 0 {
			t.Fatalf("Scan() = %#v, want complete excluded result", got)
		}
	})

	t.Run("keyword-gate", func(t *testing.T) {
		scanner := mustScanner(t, singleRuleDocument(baseRule, `"secretGroup":1,"keywords":["missing"]`))
		if got := scanner.NewSession().Scan(context.Background(), unit); !got.Complete || len(got.Findings) != 0 {
			t.Fatalf("Scan() = %#v, want keyword-filtered result", got)
		}
	})

	t.Run("keyword-case-fold", func(t *testing.T) {
		scanner := mustScanner(t, singleRuleDocument(baseRule, `"secretGroup":1,"keywords":["TOKEN"]`))
		if got := scanner.NewSession().Scan(context.Background(), unit); !got.Complete || len(got.Findings) != 1 {
			t.Fatalf("Scan() = %#v, want case-insensitive keyword match", got)
		}
	})

	t.Run("allow-gate", func(t *testing.T) {
		scanner := mustScanner(t, singleRuleDocument(baseRule, `"secretGroup":1,"allowPatterns":["spoon_test_secret"]`))
		if got := scanner.NewSession().Scan(context.Background(), unit); !got.Complete || len(got.Findings) != 0 {
			t.Fatalf("Scan() = %#v, want allow-filtered result", got)
		}
	})

	t.Run("entropy-gate", func(t *testing.T) {
		scanner := mustScanner(t, singleRuleDocument(`(aaaaaaaa)`, `"secretGroup":1,"minEntropy":3`))
		lowEntropy := scanUnit("@@ -0,0 +1,1 @@\n+aaaaaaaa", 1)
		if got := scanner.NewSession().Scan(context.Background(), lowEntropy); !got.Complete || len(got.Findings) != 0 {
			t.Fatalf("Scan() = %#v, want entropy-filtered result", got)
		}
	})
}

func TestSessionSkipsUnmatchedAndEmptyCaptureGroups(t *testing.T) {
	cases := []struct {
		name, pattern, extra, text string
	}{
		{"unmatched", `(prefix(secret)?|other)`, `"secretGroup":2`, "other"},
		{"empty", `prefix()`, `"secretGroup":1`, "prefix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scanner := mustScanner(t, singleRuleDocument(tc.pattern, tc.extra))
			unit := scanUnit("@@ -0,0 +1,1 @@\n+"+tc.text, 1)
			if got := scanner.NewSession().Scan(context.Background(), unit); !got.Complete || len(got.Findings) != 0 {
				t.Fatalf("Scan() did not skip a non-candidate capture")
			}
		})
	}
}

func TestSessionScansSuccessiveMatchesSeparately(t *testing.T) {
	scanner := mustScanner(t, singleRuleDocument(`(`+testSecret+`)`, `"secretGroup":1`))
	unit := scanUnit("@@ -0,0 +1,1 @@\n+"+testSecret+" "+testSecret, 1)
	result := scanner.NewSession().Scan(context.Background(), unit)
	if !result.Complete || len(result.Findings) != 2 {
		t.Fatal("Scan() did not emit one finding per non-overlapping match")
	}
	if result.Findings[0].Fingerprint == result.Findings[1].Fingerprint {
		t.Fatal("distinct match spans shared a fingerprint")
	}
}

func TestSessionStopsAtFindingCapAndTracksStats(t *testing.T) {
	scanner := mustScanner(t, singleRuleDocument(`(x)`, `"secretGroup":1`))
	var patch strings.Builder
	patch.WriteString("@@ -0,0 +1,5001 @@\n")
	for range 5001 {
		patch.WriteString("+x\n")
	}
	session := scanner.NewSession()
	result := session.Scan(context.Background(), scanUnit(patch.String(), 5001))
	if result.Complete || result.IncompleteReason != "finding_cap" {
		t.Fatalf("Scan() completeness = %v/%q, want partial finding cap", result.Complete, result.IncompleteReason)
	}
	if len(result.Findings) != 5000 {
		t.Fatalf("Scan() findings = %d, want 5000", len(result.Findings))
	}
	if result.Findings[0].Complete || result.Findings[len(result.Findings)-1].Complete {
		t.Fatal("finding cap did not mark every emitted finding partial")
	}
	if stats := session.Stats(); stats.Findings != 5000 || stats.PartialUnits != 1 || stats.DedupeEvictions != 0 {
		t.Fatalf("Stats() = %#v, want one capped partial session", stats)
	}
}

func TestFingerprintDedupeAndDeterminism(t *testing.T) {
	scanner := mustScanner(t, singleRuleDocument(`(`+testSecret+`)`, `"secretGroup":1`))
	unit := scanUnit("@@ -0,0 +1,1 @@\n+"+testSecret, 1)

	one := scanner.NewSession().Scan(context.Background(), unit)
	two := scanner.NewSession().Scan(context.Background(), unit)
	if len(one.Findings) != 1 || len(two.Findings) != 1 || one.Findings[0].Fingerprint != two.Findings[0].Fingerprint {
		t.Fatalf("fingerprints = %#v / %#v, want deterministic equal fingerprints", one.Findings, two.Findings)
	}
	payload := strings.Join([]string{
		fingerprintPrefix, "rules-v1", "test-rule", string(SourceCurrentCompare),
		"owner/repo", "owner/root", "owner/repo", "2026-10-06T00:00:00Z", "",
		"src/file.go", "compare", "1", "1", "1", "27",
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	if one.Findings[0].Fingerprint != hex.EncodeToString(sum[:]) {
		t.Fatal("fingerprint did not use the specified metadata serialization")
	}

	changed := unit
	changed.Path = "other/file.go"
	three := scanner.NewSession().Scan(context.Background(), changed)
	if three.Findings[0].Fingerprint == one.Findings[0].Fingerprint {
		t.Fatal("fingerprint did not include path attribution")
	}

	session := scanner.NewSession()
	first := session.Scan(context.Background(), unit)
	second := session.Scan(context.Background(), unit)
	if len(first.Findings) != 1 || len(second.Findings) != 0 {
		t.Fatalf("dedupe findings = %#v / %#v, want one then none", first.Findings, second.Findings)
	}
	if stats := session.Stats(); stats.Findings != 1 {
		t.Fatalf("Stats() = %#v, want one emitted finding", stats)
	}
}

func TestFingerprintLRUEvictsLeastRecentlyUsedEntry(t *testing.T) {
	cache := newFingerprintLRU(2)
	if duplicate, evicted := cache.touch("a"); duplicate || evicted {
		t.Fatal("first fingerprint insertion was not clean")
	}
	if duplicate, evicted := cache.touch("b"); duplicate || evicted {
		t.Fatal("second fingerprint insertion was not clean")
	}
	if duplicate, evicted := cache.touch("a"); !duplicate || evicted {
		t.Fatal("touching a did not preserve it as most-recently-used")
	}
	if duplicate, evicted := cache.touch("c"); duplicate || !evicted {
		t.Fatal("inserting c at capacity did not evict")
	}
	if duplicate, evicted := cache.touch("a"); !duplicate || evicted {
		t.Fatal("recently-used a was evicted instead of least-recently-used b")
	}
	if duplicate, evicted := cache.touch("b"); duplicate || !evicted {
		t.Fatal("reintroducing evicted b did not evict at capacity")
	}
}

func TestSessionReportsDedupeEvictions(t *testing.T) {
	scanner := mustScanner(t, singleRuleDocument(`(`+testSecret+`)`, `"secretGroup":1`))
	session := &Session{scanner: scanner, dedupe: newFingerprintLRU(2)}
	unit := scanUnit("@@ -0,0 +1,1 @@\n+"+testSecret, 1)
	for _, snapshot := range []string{"snapshot-1", "snapshot-2", "snapshot-3"} {
		unit.SnapshotID = snapshot
		result := session.Scan(context.Background(), unit)
		if !result.Complete || len(result.Findings) != 1 {
			t.Fatal("bounded dedupe session did not scan a unique fingerprint")
		}
	}
	if stats := session.Stats(); stats.Findings != 3 || stats.DedupeEvictions != 1 || stats.PartialUnits != 0 {
		t.Fatalf("Stats() = %#v, want one LRU eviction", stats)
	}
}

func TestSessionCancellationAndNULIdentityStayPartial(t *testing.T) {
	scanner := mustScanner(t, singleRuleDocument(`(`+testSecret+`)`, `"secretGroup":1`))
	unit := scanUnit("@@ -0,0 +1,1 @@\n+"+testSecret, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := scanner.NewSession().Scan(ctx, unit); got.Complete || got.IncompleteReason != "canceled" || len(got.Findings) != 0 {
		t.Fatalf("canceled Scan() = %#v", got)
	}

	nulIdentity := unit
	nulIdentity.Repository += "\x00not-hashable"
	if got := scanner.NewSession().Scan(context.Background(), nulIdentity); got.Complete || got.IncompleteReason != "invalid_identity" || len(got.Findings) != 0 {
		t.Fatalf("NUL identity Scan() = %#v", got)
	}
}

func TestSessionRedactsUntrustedIncompleteReason(t *testing.T) {
	scanner := mustScanner(t, singleRuleDocument(`(`+testSecret+`)`, `"secretGroup":1`))
	unit := scanUnit("@@ -0,0 +1,1 @@\n+"+testSecret, 1)
	unit.Complete = false
	unit.IncompleteReason = testSecret

	result := scanner.NewSession().Scan(context.Background(), unit)
	if result.Complete || result.IncompleteReason != "incomplete_unit" {
		t.Fatal("Scan() did not redact an untrusted incomplete reason")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), testSecret) {
		t.Fatal("incomplete reason leaked scanned text")
	}
}

func TestScannerNeverReturnsScannedText(t *testing.T) {
	scanner := mustScanner(t, singleRuleDocument(`(`+testSecret+`)`, `"secretGroup":1`))

	malformed := scanUnit("@@ -0,0 +1,1 @@\n"+testSecret, 1)
	malformedResult := scanner.NewSession().Scan(context.Background(), malformed)
	if malformedResult.Complete {
		t.Fatal("malformed hunk returned a clean verdict")
	}
	matched := scanUnit("@@ -0,0 +1,1 @@\n+"+testSecret, 1)
	matchedResult := scanner.NewSession().Scan(context.Background(), matched)
	for _, value := range []any{malformedResult, matchedResult, matchedResult.Findings} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), testSecret) {
			t.Fatal("scanner output leaked scanned text")
		}
	}
}
