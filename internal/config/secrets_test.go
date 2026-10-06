package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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
