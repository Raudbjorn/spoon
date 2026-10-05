package forge

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ValidUTF8 returns s with every byte that is not part of a valid UTF-8
// sequence replaced by U+FFFD. Raw diff bodies (the .diff endpoints, C-quoted
// git paths) carry arbitrary bytes, and the store's libsql driver refuses to
// bind invalid UTF-8 as TEXT, failing the whole snapshot write. Replacement is
// per byte, matching encoding/json, so a path sanitized here equals the same
// path as decoded from a JSON (REST) response.
func ValidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[:size])
		}
		s = s[size:]
	}
	return b.String()
}

// CommitSpanDays returns the number of calendar days between the earliest and
// latest commit in commits. Returns 0 for empty or single-item slices.
func CommitSpanDays(commits []AheadCommit) int {
	if len(commits) < 2 {
		return 0
	}
	var earliest, latest time.Time
	for i, c := range commits {
		if i == 0 || c.Timestamp.Before(earliest) {
			earliest = c.Timestamp
		}
		if i == 0 || c.Timestamp.After(latest) {
			latest = c.Timestamp
		}
	}
	return int(latest.Sub(earliest).Hours() / 24)
}

// UniqueAuthors extracts unique author identifiers from compare commits.
// Uses AuthorLogin if available, falls back to AuthorEmail.
func UniqueAuthors(commits []AheadCommit) []string {
	seen := make(map[string]bool)
	var authors []string
	for _, c := range commits {
		login := c.AuthorLogin
		if login == "" {
			login = c.AuthorEmail
		}
		if login != "" && !seen[login] {
			seen[login] = true
			authors = append(authors, login)
		}
	}
	return authors
}

// FormatCloneCmd returns a git clone command string, optionally with branch checkout.
func FormatCloneCmd(htmlURL, repoName, branch, defaultBranch string) string {
	if branch == "" || branch == defaultBranch {
		return fmt.Sprintf("git clone %s", htmlURL)
	}
	return fmt.Sprintf("git clone %s && cd %s && git checkout %s", htmlURL, repoName, branch)
}

// DefaultHost returns the canonical public host for a provider. It is the single
// source of truth for the "caller had no host" fallback, which was previously
// open-coded in three places that had already drifted apart — one of them
// omitted Gitea, and forge.CompareURL had no fallback at all and emitted
// "https:///owner/repo/..." instead.
//
// Gitea has no single canonical instance; codeberg.org is the convention used
// elsewhere in the codebase for a Gitea caller that supplied no host.
func DefaultHost(provider Provider) string {
	switch provider {
	case ProviderGitLab:
		return "gitlab.com"
	case ProviderGitea:
		return "codeberg.org"
	default:
		return "github.com"
	}
}

// CompareURL returns the compare view URL for a fork vs upstream.
func CompareURL(provider Provider, host, parentFullPath, parentBranch, forkOwner, forkBranch string) string {
	if host == "" {
		host = DefaultHost(provider)
	}
	switch provider {
	case ProviderGitLab:
		return fmt.Sprintf("https://%s/%s/-/compare/%s...%s:%s",
			host, parentFullPath, parentBranch, forkOwner, forkBranch)
	default:
		return fmt.Sprintf("https://%s/%s/compare/%s...%s:%s",
			host, parentFullPath, parentBranch, forkOwner, forkBranch)
	}
}

// FormatStars formats a star count with K/M suffixes.
func FormatStars(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 10_000 {
		return fmt.Sprintf("%.0fK", float64(n)/1_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return strconv.Itoa(n)
}

// PostForkBranches returns the fork's listed side branches minus those it
// inherited from upstream: branches whose tip commit is dated before the fork
// was created. A fork copies every upstream branch when it is made, and an
// unmerged upstream feature branch then reads as "ahead of master" though
// the fork never touched it. On ggml-org/llama.cpp (400 random forks sampled
// 2026-09-28) 224 of 343 side branches predated their fork, including every
// one whose tip matched an upstream branch tip (165), and 5,626 of 7,946
// divergent forks had a side branch chosen as their work.
//
// A branch or fork with no known date is kept: dropping it would hide work on
// a guess. Known miss: commits made locally before forking and pushed to the
// new fork unchanged keep their older dates and are dropped.
func PostForkBranches(f T1Data) []BranchRef {
	if f.CreatedAt.IsZero() || len(f.Branches) == 0 {
		return f.Branches
	}
	kept := make([]BranchRef, 0, len(f.Branches))
	for _, b := range f.Branches {
		if !b.CommittedDate.IsZero() && b.CommittedDate.Before(f.CreatedAt) {
			continue
		}
		kept = append(kept, b)
	}
	return kept
}

// HasBranchInventory reports whether f's Branches slice is a real listing of
// its side branches. Rows from the GitHub GraphQL listing carry the default
// branch tip SHA alongside their branches; REST-listed rows carry neither, so
// an empty Branches there means "unknown", not "no side branches".
func HasBranchInventory(f T1Data) bool {
	return f.DefaultTipSHA != ""
}
