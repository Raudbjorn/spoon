package repo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// centralityTTL is the cache lifetime for a DirectoryCentrality entry.
const centralityTTL = 24 * time.Hour

// CachePath returns the canonical cache location for an upstream:
//
//	$XDG_CACHE_HOME/spoon/centrality/<provider>/<owner>__<repo>.json
//
// or, if XDG_CACHE_HOME is empty, $HOME/.cache/spoon/centrality/... If the
// user home dir cannot be resolved, falls back to os.TempDir() so a relative
// path is never written. Each path component is sanitized via filepath.Base
// + filepath.Clean to prevent path traversal from untrusted provider/owner/
// repo strings.
func CachePath(provider, owner, repo string) string {
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			cacheHome = os.TempDir()
		} else {
			cacheHome = filepath.Join(home, ".cache")
		}
	}
	provider = filepath.Base(filepath.Clean(provider))
	owner = filepath.Base(filepath.Clean(owner))
	repo = filepath.Base(filepath.Clean(repo))
	filename := owner + "__" + repo + ".json"
	return filepath.Join(cacheHome, "spoon", "centrality", provider, filename)
}

// LoadCache reads a previously-computed DirectoryCentrality from disk.
// Returns the centrality + true if cache hit and not stale (TTL = 24h from
// ComputedAt). Returns zero value + false on miss, stale entry, or read error.
func LoadCache(provider, owner, repo string) (DirectoryCentrality, bool) {
	path := CachePath(provider, owner, repo)
	data, err := os.ReadFile(path)
	if err != nil {
		return DirectoryCentrality{}, false
	}
	var dc DirectoryCentrality
	if err := json.Unmarshal(data, &dc); err != nil {
		return DirectoryCentrality{}, false
	}
	if dc.ComputedAt.IsZero() {
		return DirectoryCentrality{}, false
	}
	if time.Since(dc.ComputedAt) > centralityTTL {
		return DirectoryCentrality{}, false
	}
	return dc, true
}

// SaveCache atomically writes the centrality to its cache path. Creates
// intermediate directories. Returns nil on success; non-nil error on
// filesystem failure (caller may choose to log and continue).
func SaveCache(dc DirectoryCentrality) error {
	path := CachePath(dc.Provider, dc.Owner, dc.Repo)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(dc)
	if err != nil {
		return err
	}
	// Atomic write: tempfile in same dir, then rename. Clean up tempfile on
	// any error path so we never leak partial state.
	tmp, err := os.CreateTemp(dir, ".centrality-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
