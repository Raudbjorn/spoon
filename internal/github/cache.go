package github

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

	// Tier 1: fork list + parent
	Parent   *RepoInfo          `json:"parent,omitempty"`
	Forks    []ForkInfo         `json:"forks,omitempty"`
	T1Extras map[int64]T1Extra  `json:"t1_extras,omitempty"`

	// Tier 2: compare results keyed by fork ID
	Compares map[int64]CompareResult `json:"compares,omitempty"`
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

// CompareValid returns true if a specific fork's compare data is within TTL.
func (e *CacheEntry) CompareValid(forkID int64) bool {
	if e == nil || e.Compares == nil {
		return false
	}
	_, ok := e.Compares[forkID]
	if !ok {
		return false
	}
	return e.isWithinTTL(compareTTL)
}

func (e *CacheEntry) isWithinTTL(ttl time.Duration) bool {
	t, err := time.Parse(time.RFC3339, e.FetchedAt)
	if err != nil {
		return false
	}
	return time.Since(t) < ttl
}

// SaveForkList saves parent + forks + optional T1 extras to the cache.
func SaveForkList(owner, repo string, parent RepoInfo, forks []ForkInfo, extras map[int64]T1Extra) error {
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

// SaveCompare saves a compare result for a specific fork.
func SaveCompare(owner, repo string, forkID int64, compare CompareResult) error {
	path, err := cacheFile(owner, repo)
	if err != nil {
		return err
	}

	existing := LoadCache(owner, repo)
	if existing == nil {
		// No cache entry yet — can't save compare without parent/forks context
		return nil
	}

	if existing.Compares == nil {
		existing.Compares = make(map[int64]CompareResult)
	}
	existing.Compares[forkID] = compare
	existing.FetchedAt = time.Now().UTC().Format(time.RFC3339)

	return writeCache(path, existing)
}

func writeCache(path string, entry *CacheEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
