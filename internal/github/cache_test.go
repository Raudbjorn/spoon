package github

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	// Use a temp directory for cache
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	parent := RepoInfo{
		FullName:      "test/repo",
		Name:          "repo",
		Stars:         42,
		DefaultBranch: "main",
	}
	forks := []ForkInfo{
		{ID: 1, FullName: "user1/repo", Stars: 5},
		{ID: 2, FullName: "user2/repo", Stars: 10},
	}

	// Save
	err := SaveForkList("test", "repo", parent, forks, nil)
	if err != nil {
		t.Fatalf("SaveForkList: %v", err)
	}

	// Load
	entry := LoadCache("test", "repo")
	if entry == nil {
		t.Fatal("LoadCache returned nil")
	}

	if !entry.ForkListValid() {
		t.Error("ForkListValid should be true for fresh cache")
	}
	if entry.Parent.FullName != "test/repo" {
		t.Errorf("Expected parent test/repo, got %s", entry.Parent.FullName)
	}
	if len(entry.Forks) != 2 {
		t.Errorf("Expected 2 forks, got %d", len(entry.Forks))
	}

	// Save compare. Performed marks this as a comparison that actually ran;
	// without it SaveCompare correctly refuses to persist and CompareValid
	// correctly refuses to serve it.
	compare := CompareResult{
		Performed: true,
		AheadBy:   5,
		BehindBy:  2,
		Status:    "ahead",
	}
	err = SaveCompare("test", "repo", 1, compare)
	if err != nil {
		t.Fatalf("SaveCompare: %v", err)
	}

	// Reload and check compare
	entry = LoadCache("test", "repo")
	if entry == nil {
		t.Fatal("LoadCache returned nil after SaveCompare")
	}
	if !entry.CompareValid(1) {
		t.Error("CompareValid(1) should be true")
	}
	if entry.CompareValid(999) {
		t.Error("CompareValid(999) should be false")
	}
	if entry.Compares[1].AheadBy != 5 {
		t.Errorf("Expected AheadBy=5, got %d", entry.Compares[1].AheadBy)
	}
}

func TestCacheMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	entry := LoadCache("nonexistent", "repo")
	if entry != nil {
		t.Error("Expected nil for missing cache")
	}
}

func TestCacheExpiry(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	// Create an expired entry manually
	entry := &CacheEntry{
		FetchedAt: time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339),
		RepoKey:   "test/repo",
		Parent:    &RepoInfo{FullName: "test/repo", Name: "repo"},
		Forks:     []ForkInfo{{ID: 1}},
	}

	dir, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}

	path := dir + "/test-repo-manual.json"
	// Write directly using the entry format to test TTL
	data, _ := json.Marshal(entry)
	os.WriteFile(path, data, 0644)

	// The LoadCache function uses cacheFile which generates a different filename,
	// so we test via the entry methods directly
	if entry.ForkListValid() {
		t.Error("ForkListValid should be false for expired entry")
	}
	if entry.isWithinTTL(24 * time.Hour) {
		t.Error("isWithinTTL should be false for 25h old entry with 24h TTL")
	}
	if !entry.isWithinTTL(48 * time.Hour) {
		t.Error("isWithinTTL should be true for 25h old entry with 48h TTL")
	}
}
