// Package priors models a user's subsystem-interest priors: an opt-in,
// per-run declaration of which forks matter (path globs / directory
// prefixes, keywords, languages, owner allow/deny). It scores each fork
// against that intent using only already-fetched fork data, at zero extra
// API cost. Priors are ordering/presentation intent — never a heat
// mutation and never a filter: a non-matching fork is demoted or laned,
// never hidden. This mirrors the per-run --heat-weights file precedent
// (internal/heat.LoadWeights), not the persisted internal/config.json.
package priors

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/svnbjrn/spoon/internal/pathmatch"
)

// Spec is the on-disk priors interest declaration. A dimension is
// "specified" when its slice is non-empty after trimming; a spec with no
// specified dimension is a load error.
type Spec struct {
	Paths     []string   `json:"paths,omitempty"`     // path globs / directory prefixes
	Keywords  []string   `json:"keywords,omitempty"`  // case-insensitive digest substrings
	Languages []string   `json:"languages,omitempty"` // case-insensitive T1 language match
	Owners    OwnerRules `json:"owners,omitempty"`
}

// OwnerRules is the owner allow/deny hint set. Allow is a positive
// dimension that counts toward the match denominator; Deny is a veto that
// forces the score to 0 and never counts toward matchable.
type OwnerRules struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// Match is the result of scoring one fork against a Spec.
type Match struct {
	Score   float64  // [0,1]
	Reasons []string // stable, sorted; e.g. "path:internal/auth", "keyword:oauth", "language:go", "owner_allow:trusted", "owner_deny:farmer"
}

// Load reads and validates a priors JSON file. A missing file returns the
// os.ReadFile error unchanged, which satisfies errors.Is(err, os.ErrNotExist)
// (callers treat that as "no spec"), mirroring config.Load.
// Languages and owner allow/deny lists are lowercased and de-duplicated;
// blank entries are trimmed from every slice. An empty spec (no paths,
// keywords, languages, or owners after trimming) is a validation error.
func Load(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("read priors %s: %w", path, err)
	}
	var s Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse priors %s: %w", path, err)
	}
	s.normalize()
	if s.empty() {
		return nil, errors.New("priors file has no paths, keywords, languages, or owners")
	}
	return &s, nil
}

// Score evaluates a fork against the spec using only already-fetched data.
// language is forge.T1Data.Language; owner is forge.T1Data.Owner;
// changedPaths are the fork's changed file paths (forge.T2Data.Diffs[].Path,
// nil when T2 is absent); digest is the lowercased commit-subjects+paths
// text. Pure and deterministic: no I/O, no network.
func (s *Spec) Score(language, owner string, changedPaths []string, digest string) Match {
	var reasons []string
	matched := 0

	// Path dimension: directory-prefix / exact for wildcard-free specs,
	// wildcards with * ? [ and ** (delegates to internal/pathmatch).
	// Each matching spec path adds one reason.
	if len(s.Paths) > 0 {
		hit := false
		for _, p := range s.Paths {
			if matchPath(p, changedPaths) {
				reasons = append(reasons, "path:"+p)
				hit = true
			}
		}
		if hit {
			matched++
		}
	}

	// Keyword dimension: case-insensitive substring of the (already
	// lowercased) digest.
	if len(s.Keywords) > 0 {
		hit := false
		for _, kw := range s.Keywords {
			lk := strings.ToLower(kw)
			if lk != "" && strings.Contains(digest, lk) {
				reasons = append(reasons, "keyword:"+lk)
				hit = true
			}
		}
		if hit {
			matched++
		}
	}

	// Language dimension: a fork has one language, so at most one match.
	lang := strings.ToLower(language)
	if len(s.Languages) > 0 && lang != "" {
		for _, l := range s.Languages {
			if l == lang {
				reasons = append(reasons, "language:"+lang)
				matched++
				break
			}
		}
	}

	// Owner allow dimension.
	lowOwner := strings.ToLower(owner)
	if len(s.Owners.Allow) > 0 && lowOwner != "" {
		for _, a := range s.Owners.Allow {
			if a == lowOwner {
				reasons = append(reasons, "owner_allow:"+lowOwner)
				matched++
				break
			}
		}
	}

	// Owner deny veto: forces score 0 but never hides — the record still
	// emits, explaining itself via the owner_deny reason.
	denied := false
	if lowOwner != "" {
		for _, d := range s.Owners.Deny {
			if d == lowOwner {
				reasons = append(reasons, "owner_deny:"+lowOwner)
				denied = true
				break
			}
		}
	}

	matchable := 0
	for _, specified := range []bool{
		len(s.Paths) > 0,
		len(s.Keywords) > 0,
		len(s.Languages) > 0,
		len(s.Owners.Allow) > 0,
	} {
		if specified {
			matchable++
		}
	}

	var score float64
	if !denied && matchable > 0 {
		score = float64(matched) / float64(matchable)
	}

	return Match{Score: score, Reasons: dedupSort(reasons)}
}

// matchPath reports whether spec path p matches any changed path. Delegates
// to internal/pathmatch for full wildcard support including ** (recursive globs).
func matchPath(p string, changedPaths []string) bool {
	for _, c := range changedPaths {
		if ok, _ := pathmatch.Match(p, c); ok {
			return true
		}
	}
	return false
}

func (s *Spec) normalize() {
	s.Paths = trimSlice(s.Paths)
	s.Keywords = trimSlice(s.Keywords)
	s.Languages = lowerDedup(s.Languages)
	s.Owners.Allow = lowerDedup(s.Owners.Allow)
	s.Owners.Deny = lowerDedup(s.Owners.Deny)
}

func (s *Spec) empty() bool {
	return len(s.Paths) == 0 && len(s.Keywords) == 0 && len(s.Languages) == 0 &&
		len(s.Owners.Allow) == 0 && len(s.Owners.Deny) == 0
}

// trimSlice drops blank entries and trims surrounding whitespace, preserving
// order and case (paths and keywords are case-sensitive as written).
func trimSlice(in []string) []string {
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// lowerDedup lowercases, trims, and de-duplicates while preserving
// first-occurrence order (deterministic given the input).
func lowerDedup(in []string) []string {
	var out []string
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// dedupSort returns the sorted, de-duplicated reasons (nil when empty) so a
// fork's explanation is stable regardless of dimension evaluation order.
func dedupSort(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
