// Package secrets keeps credentials out of plaintext config files by storing
// them in the operating system's keyring (macOS Keychain, Secret Service or
// KWallet on Linux, Windows Credential Manager).
//
// The config layer replaces each stored secret with an opaque reference
// ("keyring:<id>") and resolves references back through a Store on load. When
// no keyring is usable the config keeps the secret inline in its 0600 file, as
// before, and says so; nothing here ever writes a secret anywhere else.
package secrets

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Backend names accepted by SPOON_SECRET_STORE.
const (
	BackendKeyring = "keyring"
	BackendFile    = "file"

	// EnvBackend selects where secrets live: "keyring" (default) or "file"
	// (keep them inline in the 0600 config file).
	EnvBackend = "SPOON_SECRET_STORE"

	// ServiceName namespaces every item spoon writes to the keyring.
	ServiceName = "spoon"

	refPrefix = "keyring:"
)

var (
	// ErrLocked reports a keyring that needs unlocking and was not unlocked.
	ErrLocked = errors.New("keyring is locked")
	// ErrAccessDenied reports a keyring that refused access.
	ErrAccessDenied = errors.New("keyring access denied")
	// ErrTimeout reports a keyring that did not answer in time (often an
	// unanswered unlock prompt).
	ErrTimeout = errors.New("keyring operation timed out")
	// ErrUnavailable reports that no keyring backend works on this system.
	ErrUnavailable = errors.New("no usable keyring backend")
)

// Store is a flat namespace of named secrets.
type Store interface {
	// Get returns the secret and whether it exists; a missing secret is not an error.
	Get(name string) (value string, found bool, err error)
	Set(name, value string) error
	// Remove deletes a secret; removing a missing secret is not an error.
	Remove(name string) error
	// Names lists the secrets spoon has stored.
	Names() ([]string, error)
	// Name identifies the backend for diagnostics.
	Name() string
}

// FormatRef renders the config-file reference for a stored secret.
func FormatRef(name string) string { return refPrefix + name }

// ParseRef extracts the secret name from a config-file reference. ok is false
// for anything that is not a reference, i.e. an inline secret.
func ParseRef(s string) (name string, ok bool) {
	name, ok = strings.CutPrefix(s, refPrefix)
	return name, ok && name != ""
}

// Selected reports which backend the environment asks for. Unknown values are
// an error rather than a silent fallback to plaintext.
func Selected(getenv func(string) string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(getenv(EnvBackend))); v {
	case "", BackendKeyring:
		return BackendKeyring, nil
	case BackendFile:
		return BackendFile, nil
	default:
		return "", errors.New(EnvBackend + " must be \"keyring\" or \"file\", got \"" + v + "\"")
	}
}

// Default returns the process's secret store, or (nil, nil) when secrets stay
// inline: SPOON_SECRET_STORE=file, or a test binary (go test must stay hermetic
// and never touch the developer's real keyring; tests inject a Store instead).
// A non-nil error means the keyring was requested but is unusable.
func Default() (Store, error) {
	backend, err := Selected(os.Getenv)
	if err != nil {
		return nil, err
	}
	if backend == BackendFile || testing.Testing() {
		return nil, nil
	}
	return NewKeyringStore(KeyringConfig{})
}
