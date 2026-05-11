package cluster

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SchemaVersion is the current on-disk schema version. Bump when the
// ClusterCache shape changes in a way that invalidates older files.
const SchemaVersion = 1

// cacheTTL is the maximum age of a cached cluster result before it is
// considered stale.
const cacheTTL = 24 * time.Hour

// ClusterCache is the serialized output of a clustering pass for a single
// upstream repository. Stored as JSON.
type ClusterCache struct {
	SchemaVersion    int          `json:"schemaVersion"`
	ComputedAt       time.Time    `json:"computedAt"`
	EmbedderModel    string       `json:"embedderModel"`
	EmbedderEndpoint string       `json:"embedderEndpoint"`
	Provider         string       `json:"provider"`
	Owner            string       `json:"owner"`
	Repo             string       `json:"repo"`
	Epsilon          float64      `json:"epsilon"`
	MinClusterSize   int          `json:"minClusterSize"`
	TopM             int          `json:"topM"`
	Clusters         []Cluster    `json:"clusters"`
	Assignments      []Assignment `json:"assignments"`
}

// CachePath returns the canonical cache location for a cluster result:
//
//	$XDG_CACHE_HOME/spoon/clusters/<provider>/<owner>__<repo>.json
//
// or, if XDG_CACHE_HOME is empty, $HOME/.cache/spoon/clusters/...
func CachePath(provider, owner, repo string) string {
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = ""
		}
		cacheHome = filepath.Join(home, ".cache")
	}
	filename := owner + "__" + repo + ".json"
	return filepath.Join(cacheHome, "spoon", "clusters", provider, filename)
}

// LoadCache returns a cached cluster result if all of the following hold:
//   - file exists and parses
//   - ComputedAt < 24h ago
//   - SchemaVersion matches SchemaVersion
//   - EmbedderModel matches the passed model (case-insensitive match on the
//     base name; an empty passed model matches anything)
//   - EmbedderEndpoint matches the passed endpoint (or either is empty)
//
// Otherwise returns (zero, false).
func LoadCache(provider, owner, repo, endpoint, model string) (ClusterCache, bool) {
	path := CachePath(provider, owner, repo)
	data, err := os.ReadFile(path)
	if err != nil {
		return ClusterCache{}, false
	}
	var c ClusterCache
	if err := json.Unmarshal(data, &c); err != nil {
		return ClusterCache{}, false
	}
	if c.SchemaVersion != SchemaVersion {
		return ClusterCache{}, false
	}
	if c.ComputedAt.IsZero() {
		return ClusterCache{}, false
	}
	if time.Since(c.ComputedAt) > cacheTTL {
		return ClusterCache{}, false
	}
	if !modelMatches(c.EmbedderModel, model) {
		return ClusterCache{}, false
	}
	if !endpointMatches(c.EmbedderEndpoint, endpoint) {
		return ClusterCache{}, false
	}
	return c, true
}

// SaveCache atomically writes the cache via tempfile-then-rename. Creates
// intermediate directories. Returns nil on success or a non-nil error for
// filesystem failures (callers may log and continue).
func SaveCache(c ClusterCache) error {
	path := CachePath(c.Provider, c.Owner, c.Repo)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cluster-*.json.tmp")
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

// modelMatches returns true when the cached model and the wanted model
// describe the same embedder. An empty wanted model matches anything (caller
// hasn't pinned a model). Comparison is case-insensitive on the base name
// (the segment before the first ":" in Ollama-style "name:tag" identifiers).
func modelMatches(cached, wanted string) bool {
	if wanted == "" {
		return true
	}
	return strings.EqualFold(modelBaseName(cached), modelBaseName(wanted))
}

// endpointMatches returns true when the cached and wanted endpoints agree.
// An empty value on either side matches anything (caller hasn't pinned an
// endpoint, or the cache was written without one).
func endpointMatches(cached, wanted string) bool {
	if cached == "" || wanted == "" {
		return true
	}
	return cached == wanted
}

// modelBaseName strips an optional ":tag" suffix from an Ollama-style model
// identifier (e.g., "nomic-embed-text:latest" → "nomic-embed-text").
func modelBaseName(m string) string {
	if i := strings.IndexByte(m, ':'); i >= 0 {
		return m[:i]
	}
	return m
}
