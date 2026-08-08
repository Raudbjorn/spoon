package github

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CacheEntry stores cached data for a repository.
type CacheEntry struct {
	// Metadata
	FetchedAt string `json:"fetched_at"`
	RepoKey   string `json:"repo_key"`

	// Tier 1: fork list + parent.
	//
	// T1Extras and Compares are keyed by the fork's FullName ("owner/repo") —
	// the identity everything else in the pipeline already uses. They were
	// previously keyed by a synthetic int64 derived from hashing the FullName,
	// which invited collisions: any two forks sharing a key silently merge
	// their extras and compare data, one fork's divergence rendered under the
	// other's row, and because the derivation was deterministic the merge
	// recurred on every cache rebuild. Keying by the name itself removes the
	// class — two distinct forks cannot share a FullName.
	//
	// Files written under the old keying unmarshal cleanly (JSON map keys are
	// strings either way) but their numeric-string keys never match a
	// FullName, so old entries read as absent and are re-fetched once.
	Parent   *RepoInfo          `json:"parent,omitempty"`
	Forks    []ForkInfo         `json:"forks,omitempty"`
	T1Extras map[string]T1Extra `json:"t1_extras,omitempty"`

	// Tier 2: compare results keyed by fork FullName.
	Compares map[string]CompareResult `json:"compares,omitempty"`
}

const (
	forkListTTL = 12 * time.Hour
	compareTTL  = 24 * time.Hour
)

// CacheDir returns the cache directory path (~/.cache/spoon/).
func CacheDir() (string, error) {
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cacheHome = filepath.Join(home, ".cache")
	}
	dir := filepath.Join(cacheHome, "spoon")
	return dir, os.MkdirAll(dir, 0755)
}

// cacheFile returns the cache file path for a given owner/repo.
func cacheFile(owner, repo string) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	key := strings.ToLower(owner + "/" + repo)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:12]
	filename := fmt.Sprintf("%s-%s-%s.json", strings.ToLower(owner), strings.ToLower(repo), hash)
	return filepath.Join(dir, filename), nil
}

// LoadCache reads the cache entry for a repo. Returns nil if missing or expired.
func LoadCache(owner, repo string) *CacheEntry {
	path, err := cacheFile(owner, repo)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil
	}
	return &entry
}

// ForkListValid returns true if the cached fork list is within TTL.
func (e *CacheEntry) ForkListValid() bool {
	if e == nil || e.Parent == nil || len(e.Forks) == 0 {
		return false
	}
	return e.isWithinTTL(forkListTTL)
}

// CompareValid returns true if a specific fork's compare data is within TTL
// and records a comparison that actually ran.
//
// The Performed gate is the single chokepoint for every cached-compare reader,
// so a new call site cannot forget the check. It also self-heals caches written
// before the field existed: those entries unmarshal to Performed=false and are
// re-fetched rather than served as a fork with no divergence.
//
// Freshness is measured from the compare's own FetchedAt, not the entry's:
// each compare ages out independently, so one fork's fresh write cannot make
// every other fork's stale compare look valid, and (see SaveCompare) writing
// one no longer has to touch the fork list's own timestamp to record its own.
// A compare saved before this field existed has no FetchedAt of its own; it
// falls back to the entry-level timestamp, which was accurate for it at the
// time it was written, until it ages out on its own.
func (e *CacheEntry) CompareValid(forkName string) bool {
	if e == nil || e.Compares == nil {
		return false
	}
	c, ok := e.Compares[forkName]
	if !ok || !c.Performed {
		return false
	}
	ts := c.FetchedAt
	if ts == "" {
		ts = e.FetchedAt
	}
	return withinTTL(ts, compareTTL)
}

func (e *CacheEntry) isWithinTTL(ttl time.Duration) bool {
	return withinTTL(e.FetchedAt, ttl)
}

func withinTTL(ts string, ttl time.Duration) bool {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	return time.Since(t) < ttl
}

// SaveForkList saves parent + forks + optional T1 extras to the cache.
func SaveForkList(owner, repo string, parent RepoInfo, forks []ForkInfo, extras map[string]T1Extra) error {
	path, err := cacheFile(owner, repo)
	if err != nil {
		return err
	}

	// Load existing to preserve compare data
	existing := LoadCache(owner, repo)
	entry := &CacheEntry{
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		RepoKey:   strings.ToLower(owner + "/" + repo),
		Parent:    &parent,
		Forks:     forks,
		T1Extras:  extras,
	}
	if existing != nil && existing.Compares != nil {
		entry.Compares = existing.Compares
	}

	return writeCache(path, entry)
}

// SaveCompare saves a compare result for a specific fork. A compare that was
// never performed is not persisted: writing it would serve a fabricated
// "identical" for the next 24h.
//
// This stamps the compare's own FetchedAt and deliberately leaves the entry's
// FetchedAt untouched. That field governs ForkListValid, and a compare write
// must not refresh it: doing so let a single compare save resurrect an
// already-expired fork list, silently preventing it from ever aging out under
// continuous use (a new compare typically lands well within every 24h window,
// which kept the list looking fresh forever) — the same shape of bug this
// field's Performed gate exists to close, one layer up.
func SaveCompare(owner, repo, forkName string, compare CompareResult) error {
	if !compare.Performed {
		return nil
	}

	path, err := cacheFile(owner, repo)
	if err != nil {
		return err
	}

	existing := LoadCache(owner, repo)
	if existing == nil {
		// No cache entry yet — can't save compare without parent/forks context.
		// A caller checking only the error can't tell this apart from "saved" —
		// this is the observable trace of that silent decline.
		slog.Debug("github: skipping compare save, no fork-list cache entry yet",
			"owner", owner, "repo", repo, "fork", forkName)
		return nil
	}

	if existing.Compares == nil {
		existing.Compares = make(map[string]CompareResult)
	}
	compare.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	existing.Compares[forkName] = compare

	return writeCache(path, existing)
}

func writeCache(path string, entry *CacheEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
