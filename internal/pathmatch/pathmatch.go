// Package pathmatch matches repository-relative file paths against user
// patterns. It exists so `spn forks list --touching`, `--priors` and the TUI
// path filter agree on one rule set.
//
// Semantics:
//   - no wildcard: exact path, or directory prefix (pattern or pattern+"/").
//   - wildcard without "**": path.Match, so "*" never crosses "/".
//   - a "**" segment matches zero or more whole segments.
package pathmatch

import (
	"errors"
	"path"
	"strings"
)

var ErrBadPattern = errors.New("pathmatch: bad pattern")

// Normalize strips a leading "./" and a trailing "/", and rejects empty,
// absolute, or dot-dot patterns. Patterns are repo-relative by definition.
func Normalize(pattern string) (string, error) {
	p := strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	p = strings.TrimSuffix(p, "/")
	if p == "" || strings.HasPrefix(p, "/") {
		return "", ErrBadPattern
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" {
			return "", ErrBadPattern
		}
	}
	return p, nil
}

func hasWildcard(s string) bool { return strings.ContainsAny(s, "*?[") }

// IsLiteral reports whether pattern, once normalized, names exactly one
// path (file or directory) with no wildcard segments -- the precondition a
// caller needing a single, unambiguous target checks before treating a
// pattern as one concrete path rather than a set Match could match many
// paths against. A pattern that fails to normalize is not literal.
func IsLiteral(pattern string) bool {
	n, err := Normalize(pattern)
	if err != nil {
		return false
	}
	return !hasWildcard(n)
}

// Match reports whether name matches pattern. The only error is ErrBadPattern.
func Match(pattern, name string) (bool, error) {
	if !hasWildcard(pattern) {
		return name == pattern || strings.HasPrefix(name, pattern+"/"), nil
	}
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, name)
		if err != nil {
			return false, ErrBadPattern
		}
		return ok, nil
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, segs []string) (bool, error) {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 0 && pat[0] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true, nil
			}
			for i := 0; i <= len(segs); i++ {
				ok, err := matchSegments(pat, segs[i:])
				if err != nil || ok {
					return ok, err
				}
			}
			return false, nil
		}
		if len(segs) == 0 {
			return false, nil
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil {
			return false, ErrBadPattern
		}
		if !ok {
			return false, nil
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0, nil
}

// Matcher is a compiled, validated pattern list.
type Matcher struct{ patterns []string }

// Compile normalizes and syntax-checks every pattern. An empty list compiles
// to a Matcher that matches nothing.
func Compile(patterns []string) (Matcher, error) {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		n, err := Normalize(p)
		if err != nil {
			return Matcher{}, err
		}
		for _, seg := range strings.Split(n, "/") {
			if seg == "**" {
				continue
			}
			if _, err := path.Match(seg, ""); err != nil {
				return Matcher{}, ErrBadPattern
			}
		}
		out = append(out, n)
	}
	return Matcher{patterns: out}, nil
}

// First returns the first pattern matching name.
func (m Matcher) First(name string) (string, bool) {
	for _, p := range m.patterns {
		if ok, _ := Match(p, name); ok {
			return p, true
		}
	}
	return "", false
}

func (m Matcher) Patterns() []string { return append([]string(nil), m.patterns...) }
func (m Matcher) Empty() bool        { return len(m.patterns) == 0 }
