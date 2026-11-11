package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/secrets"
)

const (
	tokA = "ghp_aaaaaaaaaaaaaaaaaaaa"
	tokB = "ghp_bbbbbbbbbbbbbbbbbbbb"
)

func tokenConfig(tokens ...string) *Config {
	c := &Config{}
	c.GitHub.Tokens = tokens
	return c
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSaveMovesTokensToKeyringAndLoadResolvesThem(t *testing.T) {
	mem := secrets.NewMemoryStore()
	defer UseSecretStore(mem)()
	path := filepath.Join(t.TempDir(), "config.json")

	c := tokenConfig(tokA, tokB)
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	onDisk := readFile(t, path)
	if strings.Contains(onDisk, tokA) || strings.Contains(onDisk, tokB) {
		t.Fatalf("a token is still in the config file:\n%s", onDisk)
	}
	if !strings.Contains(onDisk, `"keyring:github.token.`) {
		t.Fatalf("file carries no keyring references:\n%s", onDisk)
	}
	if names, _ := mem.Names(); len(names) != 2 {
		t.Fatalf("keyring entries = %v, want 2", names)
	}
	if got := TokenStorage(c); got != "keyring" {
		t.Errorf("TokenStorage after Save = %q", got)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.GitHub.Tokens; len(got) != 2 || got[0] != tokA || got[1] != tokB {
		t.Errorf("Load resolved tokens = %v", got)
	}
	if got := TokenStorage(loaded); got != "keyring" {
		t.Errorf("TokenStorage after Load = %q", got)
	}
}

func TestSaveReusesEntriesAndPrunesRemovedTokens(t *testing.T) {
	mem := secrets.NewMemoryStore()
	defer UseSecretStore(mem)()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, tokenConfig(tokA, tokB)); err != nil {
		t.Fatal(err)
	}
	loaded, _ := Load(path)
	firstFile := readFile(t, path)
	before, _ := mem.Names()

	// Unchanged tokens keep their entries: no churn, no new names.
	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	if after, _ := mem.Names(); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("re-saving unchanged tokens changed entries: %v -> %v", before, after)
	}
	if readFile(t, path) != firstFile {
		t.Error("re-saving unchanged tokens rewrote the references")
	}

	// A Clone (as Settings makes) must carry the entry mapping too.
	clone, err := Clone(loaded)
	if err != nil {
		t.Fatal(err)
	}
	clone.GitHub.Tokens = []string{tokB}
	if err := Save(path, clone); err != nil {
		t.Fatal(err)
	}
	if names, _ := mem.Names(); len(names) != 1 {
		t.Errorf("removed token left its keyring entry behind: %v", names)
	}
	again, err := Load(path)
	if err != nil || len(again.GitHub.Tokens) != 1 || again.GitHub.Tokens[0] != tokB {
		t.Fatalf("after removal Load = %v %v", again, err)
	}
}

type flakyStore struct {
	*secrets.MemoryStore
	failSet, dropReads bool
}

func (f *flakyStore) Set(name, value string) error {
	if f.failSet {
		return errors.New("keyring is locked")
	}
	return f.MemoryStore.Set(name, value)
}

func (f *flakyStore) Get(name string) (string, bool, error) {
	if f.dropReads {
		return "", false, nil
	}
	return f.MemoryStore.Get(name)
}

func TestSaveKeepsTokensInlineWhenKeyringFails(t *testing.T) {
	for name, store := range map[string]*flakyStore{
		"write fails":              {MemoryStore: secrets.NewMemoryStore(), failSet: true},
		"write does not read back": {MemoryStore: secrets.NewMemoryStore(), dropReads: true},
	} {
		t.Run(name, func(t *testing.T) {
			defer UseSecretStore(store)()
			path := filepath.Join(t.TempDir(), "config.json")
			c := tokenConfig(tokA)
			if err := Save(path, c); err != nil {
				t.Fatalf("Save must still succeed: %v", err)
			}
			if !strings.Contains(readFile(t, path), tokA) {
				t.Fatal("token was lost: neither in the keyring nor inline in the file")
			}
			if names, _ := store.Names(); len(names) != 0 {
				t.Errorf("a failed save left orphan keyring entries: %v", names)
			}
			if got := TokenStorage(c); got != "file" {
				t.Errorf("TokenStorage = %q, want file", got)
			}
			if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
				t.Errorf("file mode %v, want 0600", info.Mode().Perm())
			}
		})
	}
}

func TestInlineFallbackPrunesLoadedEntriesOnlyAfterPublication(t *testing.T) {
	for _, failRead := range []bool{false, true} {
		for _, failPublish := range []bool{false, true} {
			t.Run(fmt.Sprintf("read=%t/publish=%t", failRead, failPublish), func(t *testing.T) {
				t.Setenv(secrets.EnvBackend, "")
				store := &flakyStore{MemoryStore: secrets.NewMemoryStore()}
				defer UseSecretStore(store)()
				path := filepath.Join(t.TempDir(), "config.json")
				if err := Save(path, tokenConfig(tokA)); err != nil {
					t.Fatal(err)
				}
				loaded, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				before := readFile(t, path)
				loaded.GitHub.Tokens = append(loaded.GitHub.Tokens, tokB)
				store.failSet, store.dropReads = !failRead, failRead
				if failPublish {
					if err := os.Mkdir(path+".tmp", 0o700); err != nil {
						t.Fatal(err)
					}
				}
				err = Save(path, loaded)
				if (err != nil) != failPublish {
					t.Fatalf("Save error = %v, publication failure = %t", err, failPublish)
				}
				names, _ := store.Names()
				if failPublish {
					if len(names) != 1 || readFile(t, path) != before {
						t.Fatal("failed publication changed existing credentials")
					}
				} else {
					if len(names) != 0 {
						t.Fatal("inline fallback left loaded keyring entries behind")
					}
					again, err := Load(path)
					if err != nil || !reflect.DeepEqual(again.GitHub.Tokens, []string{tokA, tokB}) {
						t.Fatalf("inline credentials did not survive: %v", err)
					}
				}
			})
		}
	}
}

func TestMigrateInlineSecretsRejectsChangedTokens(t *testing.T) {
	for _, replacement := range []string{`["new-token"]`, `[]`, `["keyring:new-entry"]`} {
		t.Run(replacement, func(t *testing.T) {
			t.Setenv(secrets.EnvBackend, "")
			store := secrets.NewMemoryStore()
			defer UseSecretStore(store)()
			path := filepath.Join(t.TempDir(), "config.json")
			initial := `{"github":{"tokens":["old-token"]},"github.tokens":` + replacement + `}`
			if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			// A literal dotted key can win flattened metadata; force that ordering.
			loaded.raw["github.tokens"] = json.RawMessage(replacement)
			newer := `{"github":{"tokens":` + replacement + `},"future":true}`
			if err := os.WriteFile(path, []byte(newer), 0o600); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			if moved := MigrateInlineSecrets(path, loaded, &stderr); moved != 0 {
				t.Fatalf("migrated %d stale tokens", moved)
			}
			if readFile(t, path) != newer {
				t.Fatal("migration overwrote a newer credential document")
			}
			if names, _ := store.Names(); len(names) != 0 {
				t.Fatal("migration wrote keyring entries for a stale snapshot")
			}
			if !strings.Contains(stderr.String(), "changed") || strings.Contains(stderr.String(), "old-token") || strings.Contains(stderr.String(), "new-token") {
				t.Fatalf("expected a credential-free conflict warning, got %q", stderr.String())
			}
		})
	}
}

type cleanupFailStore struct {
	*secrets.MemoryStore
	err error
}

func (s *cleanupFailStore) Remove(name string) error { return s.err }

func TestSaveRecordsCleanupFailureWithoutLogging(t *testing.T) {
	t.Setenv(secrets.EnvBackend, "")
	store := &cleanupFailStore{MemoryStore: secrets.NewMemoryStore(), err: errors.New("remove refused")}
	defer UseSecretStore(store)()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := tokenConfig(tokA)
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	if err := SaveInline(path, cfg); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(cfg.SecretCleanupError(), store.err) {
		t.Fatalf("cleanup failure not available to caller: %v", cfg.SecretCleanupError())
	}
	if logs.Len() != 0 {
		t.Fatalf("Save bypassed the command writer: %q", logs.String())
	}
	if !strings.Contains(readFile(t, path), tokA) {
		t.Fatal("cleanup failure lost the published credential")
	}
	if names, _ := store.Names(); len(names) != 1 {
		t.Fatal("test did not leave an entry requiring manual cleanup")
	}
	if err := Save(path, cfg); err != nil || cfg.SecretCleanupError() != nil {
		t.Fatalf("next save retained an old diagnostic: %v / %v", err, cfg.SecretCleanupError())
	}
}

func TestLoadFailsLoudlyWhenReferencedEntryIsUnreachable(t *testing.T) {
	mem := secrets.NewMemoryStore()
	restore := UseSecretStore(mem)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, tokenConfig(tokA)); err != nil {
		t.Fatal(err)
	}
	restore()

	t.Run("entry missing", func(t *testing.T) {
		defer UseSecretStore(secrets.NewMemoryStore())()
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("err = %v, want a missing-entry error", err)
		}
	})
	t.Run("keyring disabled", func(t *testing.T) {
		defer UseSecretStore(nil)()
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), secrets.EnvBackend) {
			t.Fatalf("err = %v, want guidance naming %s", err, secrets.EnvBackend)
		}
		if strings.Contains(err.Error(), tokA) {
			t.Error("error leaks the token")
		}
	})
}

func TestInlineStaysInlineWithoutAStore(t *testing.T) {
	defer UseSecretStore(nil)()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, tokenConfig(tokA)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, path), tokA) {
		t.Fatal("with secrets disabled the token must be inline")
	}
	if got, err := Load(path); err != nil || got.GitHub.Tokens[0] != tokA {
		t.Fatalf("Load = %v %v", got, err)
	}
}

func TestMigrateInlineSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	func() {
		defer UseSecretStore(nil)()
		if err := Save(path, tokenConfig(tokA)); err != nil {
			t.Fatal(err)
		}
	}()

	mem := secrets.NewMemoryStore()
	defer UseSecretStore(mem)()
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if moved := MigrateInlineSecrets(path, loaded, &stderr); moved != 1 {
		t.Fatalf("moved = %d, want 1 (stderr %q)", moved, stderr.String())
	}
	if strings.Contains(readFile(t, path), tokA) {
		t.Error("token still in the file after migration")
	}
	if strings.Contains(stderr.String(), tokA) {
		t.Error("migration notice leaks the token")
	}
	if moved := MigrateInlineSecrets(path, loaded, &stderr); moved != 0 {
		t.Errorf("second migration moved %d, want 0", moved)
	}
	if got, err := Load(path); err != nil || got.GitHub.Tokens[0] != tokA {
		t.Fatalf("Load after migration = %v %v", got, err)
	}
}

func TestMigrateInlineSecretsPreservesOtherJSON(t *testing.T) {
	t.Setenv(secrets.EnvBackend, "")
	defer UseSecretStore(secrets.NewMemoryStore())()
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"version":99,"github.tokens":["unrelated"],"extension":{"large":9007199254740993,"empty":{},"values":[false,null]},"github":{"tokens":["synthetic-token"],"requestsPerMinute":0,"future.setting":{"enabled":false}},"embedder":{"future":[]}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Migration compares the real nested token snapshot, never dotted metadata.
	cfg.raw["github.tokens"] = json.RawMessage(`["unrelated"]`)
	var stderr bytes.Buffer
	if moved := MigrateInlineSecrets(path, cfg, &stderr); moved != 1 {
		t.Fatalf("moved = %d, stderr %q", moved, stderr.String())
	}
	decode := func(data string) map[string]any {
		t.Helper()
		var value map[string]any
		decoder := json.NewDecoder(strings.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		delete(value["github"].(map[string]any), "tokens")
		return value
	}
	// Startup migration owns only github.tokens, including for newer schemas.
	if !reflect.DeepEqual(decode(original), decode(readFile(t, path))) {
		t.Error("migration changed fields other than github.tokens")
	}
	if strings.Contains(readFile(t, path), "synthetic-token") {
		t.Error("token was not replaced with a keyring reference")
	}
	if loaded, err := Load(path); err != nil || loaded.GitHub.Tokens[0] != "synthetic-token" {
		t.Fatalf("migrated token did not resolve: %v", err)
	}
}

func TestTokenStorageCountsDuplicateTokensOnce(t *testing.T) {
	defer UseSecretStore(secrets.NewMemoryStore())()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, tokenConfig(tokA, tokA)); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := TokenStorage(loaded); got != "keyring" {
		t.Errorf("TokenStorage = %q, want keyring", got)
	}
	var stderr bytes.Buffer
	if moved := MigrateInlineSecrets(path, loaded, &stderr); moved != 0 || stderr.Len() != 0 {
		t.Errorf("migration retried for an already-migrated config: moved=%d stderr=%q", moved, stderr.String())
	}
}

func TestSaveRemovesNewKeyringEntriesWhenPublishFails(t *testing.T) {
	mem := secrets.NewMemoryStore()
	defer UseSecretStore(mem)()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.Mkdir(path+".tmp", 0o700); err != nil { // WriteFile onto a directory fails
		t.Fatal(err)
	}
	if err := Save(path, tokenConfig(tokA)); err == nil {
		t.Fatal("Save succeeded, want publish failure")
	}
	if names, _ := mem.Names(); len(names) != 0 {
		t.Errorf("orphaned keyring entries after failed Save: %v", names)
	}
}

func TestMigrateWarnsWhenKeyringUnusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	func() {
		defer UseSecretStore(nil)()
		if err := Save(path, tokenConfig(tokA)); err != nil {
			t.Fatal(err)
		}
	}()
	restore := UseSecretStore(nil)
	defer restore()
	secretStoreMu.Lock()
	secretStoreErr = errors.New("no secret service")
	secretStoreMu.Unlock()
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if moved := MigrateInlineSecrets(path, loaded, &stderr); moved != 0 || !strings.Contains(stderr.String(), "no secret service") {
		t.Fatalf("moved=%d stderr=%q, want a keyring warning", moved, stderr.String())
	}
	if strings.Contains(stderr.String(), tokA) {
		t.Error("warning leaks the token")
	}
}
