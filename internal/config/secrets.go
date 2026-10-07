package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/svnbjrn/spoon/internal/secrets"
)

// GitHub tokens are the only secret persisted inline in the config; every
// other credential is a path to a 0600 file. By default they live in the OS
// keyring and the file holds opaque "keyring:<name>" references instead.
//
//   - Load resolves references, so callers always see real token values.
//   - Save writes new tokens to the keyring, reads them back, and only then
//     publishes a file that references them. If the keyring cannot be used,
//     the tokens stay inline in the 0600 file (with a warning) rather than
//     being dropped.
//   - SPOON_SECRET_STORE=file keeps everything inline.

const (
	tokenNamePrefix = "github.token."
	tokenNameBytes  = 8
)

var (
	secretStoreMu  sync.Mutex
	secretStoreVal secrets.Store
	secretStoreErr error
	secretStoreSet bool
)

// UseSecretStore replaces the process-wide secret store (nil keeps secrets
// inline) and returns a function that restores the previous selection. It
// exists for tests and for callers that already hold a Store.
func UseSecretStore(st secrets.Store) (restore func()) {
	secretStoreMu.Lock()
	defer secretStoreMu.Unlock()
	prevVal, prevErr, prevSet := secretStoreVal, secretStoreErr, secretStoreSet
	secretStoreVal, secretStoreErr, secretStoreSet = st, nil, true
	return func() {
		secretStoreMu.Lock()
		defer secretStoreMu.Unlock()
		secretStoreVal, secretStoreErr, secretStoreSet = prevVal, prevErr, prevSet
	}
}

// SecretStore returns the active store: (nil, nil) when secrets stay inline,
// an error when the keyring was requested but is unusable. The default is
// resolved once per process.
func SecretStore() (secrets.Store, error) {
	secretStoreMu.Lock()
	defer secretStoreMu.Unlock()
	if !secretStoreSet {
		secretStoreVal, secretStoreErr = secrets.Default()
		secretStoreSet = true
	}
	return secretStoreVal, secretStoreErr
}

// resolveSecrets replaces keyring references in c with the stored values and
// remembers which value came from which entry, so Save can reuse the entry and
// delete it if the token is later removed.
func (c *Config) resolveSecrets() error {
	c.secretRefs = nil
	var refs map[string]string
	resolved := make([]string, 0, len(c.GitHub.Tokens))
	for i, entry := range c.GitHub.Tokens {
		name, isRef := secrets.ParseRef(entry)
		if !isRef {
			resolved = append(resolved, entry)
			continue
		}
		st, err := SecretStore()
		if st == nil || err != nil {
			return fmt.Errorf("github.tokens[%d] is stored in the OS keyring but the keyring is unavailable (%v); unlock it, or set %s=file and re-add the token", i, unavailableReason(err), secrets.EnvBackend)
		}
		value, found, err := st.Get(name)
		if err != nil {
			return fmt.Errorf("github.tokens[%d]: reading %s: %w", i, st.Name(), err)
		}
		if !found {
			return fmt.Errorf("github.tokens[%d] references keyring entry %q, which does not exist in %s; re-run the login that created it", i, name, st.Name())
		}
		if refs == nil {
			refs = map[string]string{}
		}
		refs[value] = name
		resolved = append(resolved, value)
	}
	c.GitHub.Tokens, c.secretRefs = resolved, refs
	return nil
}

func unavailableReason(err error) string {
	if err == nil {
		return "disabled by " + secrets.EnvBackend
	}
	return err.Error()
}

// externalizeSecrets stores c's tokens in the keyring and returns the token
// list to persist (references on success, the inline values when the keyring
// cannot be used), the reference map to remember, and the names of loaded
// entries that are no longer used. It writes only to the keyring.
func (c *Config) externalizeSecrets(forceInline bool) (persist []string, refs map[string]string, stale, created []string) {
	inline := slices.Clone(c.GitHub.Tokens)
	if len(inline) == 0 || forceInline || c.wantsInline() {
		// Nothing to store, or the caller wants plaintext: every loaded entry is
		// now unreferenced and may be pruned once the file is published.
		return inline, nil, c.staleRefs(nil), nil
	}
	st, err := SecretStore()
	if st == nil {
		if err != nil {
			c.inlineFallback = err
		}
		return inline, nil, nil, nil
	}
	refs = make(map[string]string, len(inline))
	persist = make([]string, 0, len(inline))
	for _, token := range inline {
		name, known := refs[token]
		if !known {
			name, known = c.secretRefs[token]
		}
		if !known {
			var nerr error
			if name, nerr = newTokenName(); nerr != nil {
				return c.rollback(st, created, inline, nerr)
			}
			if err := st.Set(name, token); err != nil {
				return c.rollback(st, created, inline, err)
			}
			created = append(created, name)
		}
		// Read back before the file stops carrying the value: a keyring that
		// accepts a write but cannot return it must never cost a token.
		if got, found, err := st.Get(name); err != nil || !found || got != token {
			if err == nil {
				err = fmt.Errorf("entry %q did not read back", name)
			}
			return c.rollback(st, created, inline, err)
		}
		refs[token] = name
		persist = append(persist, secrets.FormatRef(name))
	}
	return persist, refs, c.staleRefs(refs), created
}

// rollback undoes this save's new keyring entries and keeps the tokens inline.
func (c *Config) rollback(st secrets.Store, created, inline []string, cause error) ([]string, map[string]string, []string, []string) {
	removeEntries(st, created)
	c.inlineFallback = cause
	return inline, nil, nil, nil
}

// removeEntries best-effort deletes keyring entries this save created.
func removeEntries(st secrets.Store, names []string) {
	for _, name := range names {
		_ = st.Remove(name)
	}
}

// discardCreated undoes externalizeSecrets' keyring writes when publishing
// the file failed, so a failed save leaves no unreferenced credential behind.
func discardCreated(created []string) {
	if len(created) == 0 {
		return
	}
	if st, _ := SecretStore(); st != nil {
		removeEntries(st, created)
	}
}

// InlineFallback reports why the most recent Save kept GitHub tokens inline
// although the keyring was wanted (nil when it did not). Callers surface it
// through their own output channel; Save never writes to stderr itself.
func (c *Config) InlineFallback() error {
	if c == nil {
		return nil
	}
	return c.inlineFallback
}

// staleRefs lists loaded keyring entries that the saved config no longer uses.
func (c *Config) staleRefs(keep map[string]string) []string {
	var stale []string
	for token, name := range c.secretRefs {
		if _, kept := keep[token]; !kept {
			stale = append(stale, name)
		}
	}
	return stale
}

func newTokenName() (string, error) {
	b := make([]byte, tokenNameBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("generate keyring entry name: %w", err)
	}
	return tokenNamePrefix + hex.EncodeToString(b), nil
}

// pruneSecrets deletes keyring entries the saved config stopped referencing.
// Best effort and last: a failure leaves an unreferenced entry, never a lost token.
func pruneSecrets(names []string) {
	if len(names) == 0 {
		return
	}
	st, _ := SecretStore()
	if st == nil {
		return
	}
	for _, name := range names {
		if err := st.Remove(name); err != nil {
			slog.Warn("could not remove unused keyring entry", "entry", name, "error", err)
		}
	}
}

// TokenStorage reports where c's GitHub tokens are persisted: "keyring" when
// every token is a keyring entry, "file" when any is inline, "none" without tokens.
func TokenStorage(c *Config) string {
	switch {
	case c == nil || len(c.GitHub.Tokens) == 0:
		return "none"
	case c.allInKeyring():
		return "keyring"
	default:
		return "file"
	}
}

// allInKeyring reports whether every token is backed by a keyring entry.
// Duplicate tokens share one entry, so this checks membership, not counts.
func (c *Config) allInKeyring() bool {
	for _, token := range c.GitHub.Tokens {
		if _, ok := c.secretRefs[token]; !ok {
			return false
		}
	}
	return true
}

// MigrateInlineSecrets moves inline GitHub tokens of the loaded config at path
// into the keyring. It is a no-op without inline tokens or without a usable
// keyring, and never leaves a token unrecorded: Save stores and verifies the
// keyring entries before the file is rewritten. Returns the number moved.
func MigrateInlineSecrets(path string, c *Config, stderr io.Writer) int {
	if c == nil || len(c.GitHub.Tokens) == 0 || c.allInKeyring() || c.wantsInline() {
		return 0
	}
	if st, err := SecretStore(); st == nil || err != nil {
		if err != nil {
			fmt.Fprintf(stderr, "warning: GitHub tokens stay in the 0600 config file: the OS keyring could not be used (%v); set %s=file to silence this\n", err, secrets.EnvBackend)
		}
		return 0
	}
	before := len(c.secretRefs)
	if err := Save(path, c); err != nil {
		fmt.Fprintf(stderr, "warning: could not move GitHub tokens into the OS keyring: %v\n", err)
		return 0
	}
	if cause := c.InlineFallback(); cause != nil {
		fmt.Fprintf(stderr, "warning: GitHub tokens stay in the 0600 config file: the OS keyring could not be used (%v); set %s=file to silence this\n", cause, secrets.EnvBackend)
	}
	moved := len(c.secretRefs) - before
	if moved > 0 {
		fmt.Fprintf(stderr, "moved %d GitHub token(s) from %s into the OS keyring\n", moved, path)
	}
	return moved
}

// SaveInline writes c with every GitHub token inline in the 0600 file and then
// removes the keyring entries it no longer references: the inverse of the
// default, for users leaving the keyring.
func SaveInline(path string, c *Config) error {
	c.Secrets.Store = secrets.BackendFile
	return save(path, c, true)
}

// UseKeyringStorage clears the sticky "file" choice so the next Save (or
// startup migration) moves tokens into the keyring again.
func UseKeyringStorage(c *Config) { c.Secrets.Store = "" }

// wantsInline reports whether this config opted out of the keyring.
// SPOON_SECRET_STORE, when set, overrides the file's choice for this process.
func (c *Config) wantsInline() bool {
	if env := strings.TrimSpace(os.Getenv(secrets.EnvBackend)); env != "" {
		return strings.EqualFold(env, secrets.BackendFile)
	}
	return strings.EqualFold(c.Secrets.Store, secrets.BackendFile)
}
