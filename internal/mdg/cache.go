package mdg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	mdgTTL             = 24 * time.Hour
	CacheSchemaVersion = 1
)

// MDGCache is the on-disk MDG entry.
type MDGCache struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Provider      string              `json:"provider"`
	Owner         string              `json:"owner"`
	Repo          string              `json:"repo"`
	HeadSHA       string              `json:"headSHA"` // upstream default-branch SHA at build time
	ComputedAt    time.Time           `json:"computedAt"`
	Nodes         []Module            `json:"nodes"`
	Edges         map[string][]string `json:"edges"`
	Scores        map[string]float64  `json:"scores"` // PageRank, keyed by module path
}

// MDGCachePath returns the canonical cache location for an upstream's MDG.
//
//	$XDG_CACHE_HOME/spoon/mdg/<provider>/<owner>__<repo>.json
func MDGCachePath(provider, owner, repo string) string {
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
	return filepath.Join(cacheHome, "spoon", "mdg", provider, filename)
}

// LoadMDGCache returns (cache, true) when a fresh cache entry exists for the
// upstream and headSHA matches the cached HeadSHA. Otherwise (zero, false).
//
// "Fresh" means: schema-version match, ComputedAt within mdgTTL, HeadSHA
// matches. Any read or parse error is a miss (no error is propagated).
func LoadMDGCache(provider, owner, repo, headSHA string) (MDGCache, bool) {
	path := MDGCachePath(provider, owner, repo)
	data, err := os.ReadFile(path)
	if err != nil {
		return MDGCache{}, false
	}
	var c MDGCache
	if err := json.Unmarshal(data, &c); err != nil {
		return MDGCache{}, false
	}
	if c.SchemaVersion != CacheSchemaVersion {
		return MDGCache{}, false
	}
	if c.ComputedAt.IsZero() || time.Since(c.ComputedAt) > mdgTTL {
		return MDGCache{}, false
	}
	if c.HeadSHA == "" || c.HeadSHA != headSHA {
		return MDGCache{}, false
	}
	return c, true
}

// SaveMDGCache atomically persists a cache entry. Returns nil on success.
func SaveMDGCache(c MDGCache) error {
	path := MDGCachePath(c.Provider, c.Owner, c.Repo)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mdg-*.json.tmp")
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
