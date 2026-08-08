package github

import (
	"os"
	"path/filepath"
)

// CacheDir returns the cache directory path (~/.cache/spoon/). It hosts the
// owner-profile and proxy caches; repo/fork data lives in the global store
// (internal/store), not here.
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
