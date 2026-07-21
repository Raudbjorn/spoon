package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeProxyCacheFile(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", dir)
	path := filepath.Join(dir, "spoon", "proxies.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(proxyCache{FetchedAt: time.Now().UTC(), URLs: []string{"http://10.0.0.1:8080"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// The cache is consulted before the API-key path and decides which proxies all
// GitHub traffic routes through, so a locally writable file must not be
// trusted — it would let another local user select the interception point.
func TestLoadProxyCacheRejectsWritableFile(t *testing.T) {
	writeProxyCacheFile(t, t.TempDir(), 0o666)
	if got := loadProxyCache(time.Hour); len(got) != 0 {
		t.Fatalf("world-writable proxy cache was trusted: %v", got)
	}
	writeProxyCacheFile(t, t.TempDir(), 0o620)
	if got := loadProxyCache(time.Hour); len(got) != 0 {
		t.Fatalf("group-writable proxy cache was trusted: %v", got)
	}
}

func TestLoadProxyCacheAcceptsSecureFile(t *testing.T) {
	writeProxyCacheFile(t, t.TempDir(), 0o600)
	if got := loadProxyCache(time.Hour); len(got) != 1 {
		t.Fatalf("secure proxy cache rejected: %v", got)
	}
}
