package cluster

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newFixtureCache() ClusterCache {
	return ClusterCache{
		SchemaVersion:    SchemaVersion,
		ComputedAt:       time.Now().UTC().Truncate(time.Second),
		EmbedderModel:    "mxbai-embed-large",
		EmbedderEndpoint: "http://127.0.0.1:11434",
		Provider:         "github",
		Owner:            "acme",
		Repo:             "widget",
		Epsilon:          0.35,
		MinClusterSize:   3,
		TopM:             50,
		Clusters: []Cluster{
			{
				ID:       "c0",
				Members:  []string{"a/a", "a/b", "a/c"},
				Centroid: []float32{0.5, 0.5},
				Label:    "alpha",
			},
		},
		Assignments: []Assignment{
			{ForkID: "a/a", Cluster: "c0", Novelty: 0.1},
			{ForkID: "a/b", Cluster: "c0", Novelty: 0.2},
			{ForkID: "a/c", Cluster: "c0", Novelty: 0.3},
		},
	}
}

func TestClusterCacheRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	original := newFixtureCache()
	original.ConfigFingerprint = "fp-roundtrip"
	if err := SaveCache(original); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	got, ok := LoadCache("github", "acme", "widget",
		original.EmbedderEndpoint, original.EmbedderModel, original.ConfigFingerprint)
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if got.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion mismatch: got %d, want %d", got.SchemaVersion, original.SchemaVersion)
	}
	if !got.ComputedAt.Equal(original.ComputedAt) {
		t.Errorf("ComputedAt mismatch: got %v, want %v", got.ComputedAt, original.ComputedAt)
	}
	if got.EmbedderModel != original.EmbedderModel ||
		got.EmbedderEndpoint != original.EmbedderEndpoint {
		t.Errorf("embedder metadata mismatch: got %+v", got)
	}
	if got.Provider != original.Provider || got.Owner != original.Owner || got.Repo != original.Repo {
		t.Errorf("identity mismatch: got %+v", got)
	}
	if got.ConfigFingerprint != original.ConfigFingerprint {
		t.Errorf("ConfigFingerprint mismatch: got %q, want %q", got.ConfigFingerprint, original.ConfigFingerprint)
	}
	if got.Epsilon != original.Epsilon || got.MinClusterSize != original.MinClusterSize ||
		got.TopM != original.TopM {
		t.Errorf("options mismatch: got %+v", got)
	}
	if !reflect.DeepEqual(got.Clusters, original.Clusters) {
		t.Errorf("Clusters mismatch:\ngot  %+v\nwant %+v", got.Clusters, original.Clusters)
	}
	if !reflect.DeepEqual(got.Assignments, original.Assignments) {
		t.Errorf("Assignments mismatch:\ngot  %+v\nwant %+v", got.Assignments, original.Assignments)
	}
}

func TestLoadCache_Miss(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	c, ok := LoadCache("github", "nothing", "here", "ep", "model", "")
	if ok {
		t.Errorf("expected miss, got hit: %+v", c)
	}
}

func TestLoadCache_Stale(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	c := newFixtureCache()
	c.ComputedAt = time.Now().Add(-25 * time.Hour)
	if err := SaveCache(c); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	if _, ok := LoadCache("github", "acme", "widget", c.EmbedderEndpoint, c.EmbedderModel, ""); ok {
		t.Errorf("expected stale miss, got hit")
	}
}

func TestLoadCache_ModelMismatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	c := newFixtureCache()
	// EmbedderModel is "mxbai-embed-large" via newFixtureCache.
	if err := SaveCache(c); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	if _, ok := LoadCache("github", "acme", "widget", c.EmbedderEndpoint, "nomic-embed-text", ""); ok {
		t.Errorf("expected model-mismatch miss, got hit")
	}
}

func TestLoadCache_ModelEmptyMatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	c := newFixtureCache()
	c.EmbedderModel = "X"
	if err := SaveCache(c); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	if _, ok := LoadCache("github", "acme", "widget", c.EmbedderEndpoint, "", ""); !ok {
		t.Errorf("expected hit when wanted model is empty")
	}
}

func TestLoadCache_EndpointMismatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	c := newFixtureCache()
	c.EmbedderEndpoint = "A"
	if err := SaveCache(c); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	if _, ok := LoadCache("github", "acme", "widget", "B", c.EmbedderModel, ""); ok {
		t.Errorf("expected endpoint-mismatch miss, got hit")
	}
}

func TestLoadCache_EndpointEmptyMatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	c := newFixtureCache()
	c.EmbedderEndpoint = "A"
	if err := SaveCache(c); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	if _, ok := LoadCache("github", "acme", "widget", "", c.EmbedderModel, ""); !ok {
		t.Errorf("expected hit when wanted endpoint is empty")
	}
}

func TestLoadCache_SchemaMismatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	// Write a cache file directly with an older schema version so we don't
	// depend on SaveCache's behavior for the version field.
	c := newFixtureCache()
	c.SchemaVersion = SchemaVersion - 1
	path := CachePath(c.Provider, c.Owner, c.Repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := LoadCache(c.Provider, c.Owner, c.Repo, c.EmbedderEndpoint, c.EmbedderModel, ""); ok {
		t.Errorf("expected schema-mismatch miss, got hit")
	}
}

func TestLoadCache_ConfigMismatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	c := newFixtureCache()
	c.ConfigFingerprint = "abc123"
	if err := SaveCache(c); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	if _, ok := LoadCache("github", "acme", "widget", c.EmbedderEndpoint, c.EmbedderModel, "xyz789"); ok {
		t.Error("expected config-fingerprint mismatch, got hit")
	}
	if _, ok := LoadCache("github", "acme", "widget", c.EmbedderEndpoint, c.EmbedderModel, "abc123"); !ok {
		t.Error("expected config-fingerprint match, got miss")
	}
}

func TestCachePath_XDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/x")
	got := CachePath("github", "o", "r")
	want := "/tmp/x/spoon/clusters/github/o__r.json"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestCachePath_HomeFallback(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/tmp/h")
	got := CachePath("gitlab", "g", "p")
	prefix := "/tmp/h/.cache/spoon/clusters/gitlab/"
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("expected prefix %q, got %q", prefix, got)
	}
	if !strings.HasSuffix(got, "g__p.json") {
		t.Errorf("expected suffix g__p.json, got %q", got)
	}
}
