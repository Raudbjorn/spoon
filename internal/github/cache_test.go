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
	err = SaveCompare("test", "repo", "user1/repo", compare)
	if err != nil {
		t.Fatalf("SaveCompare: %v", err)
	}

	// Reload and check compare
	entry = LoadCache("test", "repo")
	if entry == nil {
		t.Fatal("LoadCache returned nil after SaveCompare")
	}
	if !entry.CompareValid("user1/repo") {
		t.Error("CompareValid(user1/repo) should be true")
	}
	if entry.CompareValid("no-such/fork") {
		t.Error("CompareValid(no-such/fork) should be false")
	}
	if entry.Compares["user1/repo"].AheadBy != 5 {
		t.Errorf("Expected AheadBy=5, got %d", entry.Compares["user1/repo"].AheadBy)
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

// SaveCompare renewing the shared entry-level FetchedAt used to resurrect an
// expired fork list: any compare write inside the 24h compare TTL made
// ForkListValid look fresh again, so under continuous use (a repo checked
// more often than every 12h) the fork list could never age out — new forks,
// renames, and star counts would go stale indefinitely. Each compare now
// carries its own FetchedAt and SaveCompare must not touch the entry's.
func TestSaveCompare_DoesNotResurrectExpiredForkList(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	path, err := cacheFile("test", "repo")
	if err != nil {
		t.Fatalf("cacheFile: %v", err)
	}

	old := time.Now().UTC().Add(-13 * time.Hour).Format(time.RFC3339)
	entry := &CacheEntry{
		FetchedAt: old,
		RepoKey:   "test/repo",
		Parent:    &RepoInfo{FullName: "test/repo", DefaultBranch: "main"},
		Forks:     []ForkInfo{{ID: 1, FullName: "user1/repo"}},
	}
	if err := writeCache(path, entry); err != nil {
		t.Fatalf("writeCache: %v", err)
	}

	loaded := LoadCache("test", "repo")
	if loaded.ForkListValid() {
		t.Fatal("fork list should already be expired at 13h (TTL 12h) before the test begins")
	}

	if err := SaveCompare("test", "repo", "user1/repo", CompareResult{Performed: true, AheadBy: 5}); err != nil {
		t.Fatalf("SaveCompare: %v", err)
	}

	after := LoadCache("test", "repo")
	if after.ForkListValid() {
		t.Error("SaveCompare resurrected an expired fork list by touching the shared FetchedAt")
	}
	if after.FetchedAt != old {
		t.Errorf("entry FetchedAt changed from %q to %q; SaveCompare must not touch it", old, after.FetchedAt)
	}
	if !after.CompareValid("user1/repo") {
		t.Error("the compare just saved should be valid on its own per-compare timestamp")
	}
	// CompareValid alone cannot prove the per-compare stamp exists: an entry
	// FetchedAt of -13h is still within the 24h compare TTL, so the fallback
	// path would also report valid. Assert the stamp directly.
	if after.Compares["user1/repo"].FetchedAt == "" {
		t.Error("SaveCompare did not stamp the compare's own FetchedAt; validity above came from the entry-level fallback")
	}
}

// T1Extras and Compares used to be keyed by a synthetic int64 hash of the
// fork's FullName, so on-disk maps carry numeric-string JSON keys. Those files
// must still load — and their entries must read as absent (re-fetch once)
// rather than error out or be served under the wrong fork.
func TestCache_LegacyNumericKeysSelfHeal(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	legacy := []byte(`{
		"fetched_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
		"repo_key": "test/repo",
		"parent": {"full_name": "test/repo", "default_branch": "main"},
		"forks": [{"id": 4009534899, "full_name": "user1/repo"}],
		"t1_extras": {"4009534899": {"OpenPRCount": 3}},
		"compares": {"4009534899": {"performed": true, "ahead_by": 5}}
	}`)
	path, err := cacheFile("test", "repo")
	if err != nil {
		t.Fatalf("cacheFile: %v", err)
	}
	if err := os.WriteFile(path, legacy, 0644); err != nil {
		t.Fatalf("write legacy cache: %v", err)
	}

	entry := LoadCache("test", "repo")
	if entry == nil {
		t.Fatal("a legacy numeric-keyed cache file failed to load at all")
	}
	if entry.CompareValid("user1/repo") {
		t.Error("a compare keyed under the legacy numeric ID was served for the FullName; it must read as absent and re-fetch")
	}
	if _, ok := entry.T1Extras["user1/repo"]; ok {
		t.Error("a T1Extra keyed under the legacy numeric ID was served for the FullName")
	}
}

// SaveCompare used to require resolving the fork's numeric ID from m.ghCache,
// which is nil after a cold start or an explicit refresh — findGHForkID
// returned 0 and the save was silently skipped for exactly the runs that had
// just paid for fresh compares. Keying by FullName removes the lookup: a
// compare must persist as long as the fork-list entry exists, no numeric ID
// involved.
func TestSaveCompare_PersistsWithoutNumericIDResolution(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	parent := RepoInfo{FullName: "test/repo", DefaultBranch: "main"}
	// The fork list as a cold fetch writes it: numeric IDs all zero, because
	// the forge layer identifies forks by FullName alone.
	forks := []ForkInfo{{FullName: "user1/repo"}}
	if err := SaveForkList("test", "repo", parent, forks, nil); err != nil {
		t.Fatalf("SaveForkList: %v", err)
	}

	if err := SaveCompare("test", "repo", "user1/repo", CompareResult{Performed: true, AheadBy: 2}); err != nil {
		t.Fatalf("SaveCompare: %v", err)
	}

	after := LoadCache("test", "repo")
	if !after.CompareValid("user1/repo") {
		t.Error("a compare saved right after a cold fork-list write was dropped; the cold-start path must persist compares")
	}
}

// A compare saved before per-compare timestamps existed has no FetchedAt of
// its own. It must fall back to the entry-level timestamp rather than being
// treated as permanently invalid (which would silently discard every
// pre-upgrade cache entry) or permanently valid (which would never expire).
func TestCompareValid_FallsBackToEntryTimestampForLegacyEntries(t *testing.T) {
	fresh := time.Now().UTC().Format(time.RFC3339)
	stale := time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339)

	freshEntry := &CacheEntry{
		FetchedAt: fresh,
		Compares:  map[string]CompareResult{"user1/repo": {Performed: true, AheadBy: 5}}, // no FetchedAt
	}
	if !freshEntry.CompareValid("user1/repo") {
		t.Error("a legacy compare in a fresh entry should fall back to the entry timestamp and be valid")
	}

	staleEntry := &CacheEntry{
		FetchedAt: stale,
		Compares:  map[string]CompareResult{"user1/repo": {Performed: true, AheadBy: 5}},
	}
	if staleEntry.CompareValid("user1/repo") {
		t.Error("a legacy compare in a stale entry should fall back to the entry timestamp and be expired")
	}
}
