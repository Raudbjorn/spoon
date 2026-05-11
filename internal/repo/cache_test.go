package repo

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp) // belt-and-braces in case XDG isn't honored

	original := DirectoryCentrality{
		DirScore:   map[string]float64{"internal/": 0.9, "cmd/": 0.4},
		CoreDirs:   []string{"internal/", "cmd/"},
		ComputedAt: time.Now().UTC().Truncate(time.Second),
		Provider:   "github",
		Owner:      "acme",
		Repo:       "widget",
	}
	if err := SaveCache(original); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	got, ok := LoadCache("github", "acme", "widget")
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if !reflect.DeepEqual(got.DirScore, original.DirScore) {
		t.Errorf("DirScore mismatch: got %v, want %v", got.DirScore, original.DirScore)
	}
	if !reflect.DeepEqual(got.CoreDirs, original.CoreDirs) {
		t.Errorf("CoreDirs mismatch: got %v, want %v", got.CoreDirs, original.CoreDirs)
	}
	if !got.ComputedAt.Equal(original.ComputedAt) {
		t.Errorf("ComputedAt mismatch: got %v, want %v", got.ComputedAt, original.ComputedAt)
	}
	if got.Provider != original.Provider || got.Owner != original.Owner || got.Repo != original.Repo {
		t.Errorf("metadata mismatch: got %+v, want %+v", got, original)
	}
}

func TestLoadCache_Miss(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	dc, ok := LoadCache("github", "nothing", "here")
	if ok {
		t.Errorf("expected miss, got hit: %+v", dc)
	}
}

func TestLoadCache_Stale(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	stale := DirectoryCentrality{
		DirScore:   map[string]float64{"x/": 1.0},
		CoreDirs:   []string{"x/"},
		ComputedAt: time.Now().Add(-25 * time.Hour),
		Provider:   "github",
		Owner:      "old",
		Repo:       "thing",
	}
	if err := SaveCache(stale); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	dc, ok := LoadCache("github", "old", "thing")
	if ok {
		t.Errorf("expected stale miss, got hit: %+v", dc)
	}
}

func TestCachePath_XDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/x")
	got := CachePath("github", "o", "r")
	want := "/tmp/x/spoon/centrality/github/o__r.json"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestCachePath_HomeFallback(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/tmp/h")
	got := CachePath("gitlab", "g", "p")
	prefix := "/tmp/h/.cache/spoon/centrality/gitlab/"
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("expected prefix %q, got %q", prefix, got)
	}
	if !strings.HasSuffix(got, "g__p.json") {
		t.Errorf("expected suffix g__p.json, got %q", got)
	}
}
