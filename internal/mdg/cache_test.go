package mdg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCachePath_SanitizesComponents(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := MDGCachePath("github", "../../etc", "passwd")
	// filepath.Base(filepath.Clean("../../etc")) == "etc", so the file ends up
	// named "etc__passwd.json". Owner and repo are sanitized independently,
	// so the alternative "passwd__passwd.json" is not reachable from this
	// input. The contract: no traversal escapes the cache root.
	base := filepath.Base(p)
	if base != "etc__passwd.json" {
		t.Fatalf("traversal not sanitized; want etc__passwd.json, got base=%q full=%q", base, p)
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("cache path should be absolute: %q", p)
	}
	// The path must live under the configured cache root (no escape).
	if !strings.HasPrefix(p, os.Getenv("XDG_CACHE_HOME")+string(filepath.Separator)) {
		t.Fatalf("path escaped XDG_CACHE_HOME: %q", p)
	}
}

func TestCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	c := MDGCache{
		SchemaVersion: CacheSchemaVersion,
		Provider:      "github",
		Owner:         "o",
		Repo:          "r",
		HeadSHA:       "abc123",
		ComputedAt:    time.Now().UTC(),
		Scores:        map[string]float64{"example.com/m": 0.42},
		Nodes:         []Module{{Path: "example.com/m", Lang: "go", IsMain: true}},
		Edges:         map[string][]string{"example.com/m": {"example.com/m/util"}},
	}
	if err := SaveMDGCache(c); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok := LoadMDGCache("github", "o", "r", "abc123")
	if !ok {
		t.Fatalf("expected cache hit")
	}
	if got.HeadSHA != "abc123" || got.Scores["example.com/m"] != 0.42 {
		t.Fatalf("unexpected entry: %+v", got)
	}
}

func TestCache_HeadSHAMismatchIsMiss(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := MDGCache{
		SchemaVersion: CacheSchemaVersion,
		Provider:      "github",
		Owner:         "o",
		Repo:          "r",
		HeadSHA:       "old-sha",
		ComputedAt:    time.Now().UTC(),
	}
	if err := SaveMDGCache(c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := LoadMDGCache("github", "o", "r", "new-sha"); ok {
		t.Fatalf("HEAD-SHA mismatch should be a miss")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := MDGCache{
		SchemaVersion: CacheSchemaVersion,
		Provider:      "github",
		Owner:         "o",
		Repo:          "r",
		HeadSHA:       "abc",
		ComputedAt:    time.Now().Add(-25 * time.Hour).UTC(),
	}
	if err := SaveMDGCache(c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := LoadMDGCache("github", "o", "r", "abc"); ok {
		t.Fatalf("entry older than TTL should be miss")
	}
}

func TestCache_SchemaMismatchIsMiss(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := MDGCache{
		SchemaVersion: CacheSchemaVersion + 100,
		Provider:      "github",
		Owner:         "o",
		Repo:          "r",
		HeadSHA:       "abc",
		ComputedAt:    time.Now().UTC(),
	}
	if err := SaveMDGCache(c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := LoadMDGCache("github", "o", "r", "abc"); ok {
		t.Fatalf("schema mismatch should be miss")
	}
}
