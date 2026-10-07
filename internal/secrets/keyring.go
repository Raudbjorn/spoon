package secrets

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/99designs/keyring"
)

// opTimeout bounds one keyring call. Backends can block on an unlock prompt
// nobody answers (a locked wallet over SSH, say); that must surface as an
// error, not a hang.
const opTimeout = 20 * time.Second

// KeyringConfig configures KeyringStore. The zero value is correct for
// production.
type KeyringConfig struct {
	ServiceName     string
	AllowedBackends []keyring.BackendType
	Timeout         time.Duration
}

// KeyringStore stores secrets in the OS keyring.
type KeyringStore struct {
	kr      keyring.Keyring
	service string
	timeout time.Duration
	mu      sync.Mutex // serialises calls; several backends are not goroutine-safe
}

// openKeyring is a seam so tests can substitute an in-memory keyring.
var openKeyring = keyring.Open

// NewKeyringStore opens the platform keyring. It fails with ErrUnavailable when
// no backend works (no D-Bus session, no secret service, ...).
func NewKeyringStore(cfg KeyringConfig) (*KeyringStore, error) {
	if cfg.ServiceName == "" {
		cfg.ServiceName = ServiceName
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = opTimeout
	}
	backends := cfg.AllowedBackends
	if backends == nil {
		backends = platformBackends()
	}
	kr, err := openBounded(keyring.Config{
		ServiceName:     cfg.ServiceName,
		AllowedBackends: backends,
		// KWallet and Secret Service are namespaced by these, not by ServiceName alone.
		KWalletAppID:             cfg.ServiceName,
		KWalletFolder:            cfg.ServiceName,
		LibSecretCollectionName:  "login",
		KeychainTrustApplication: true,
	}, cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return &KeyringStore{kr: kr, service: cfg.ServiceName, timeout: cfg.Timeout}, nil
}

// openBounded runs openKeyring under the operation timeout: backend
// initialisation (KWallet openWallet, the Secret Service D-Bus session) can
// block on an unlock prompt just like a later call.
func openBounded(cfg keyring.Config, timeout time.Duration) (keyring.Keyring, error) {
	type result struct {
		kr  keyring.Keyring
		err error
	}
	done := make(chan result, 1)
	go func() {
		kr, err := openKeyring(cfg)
		done <- result{kr, err}
	}()
	select {
	case r := <-done:
		return r.kr, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("open: %w", ErrTimeout)
	}
}

// platformBackends lists only backends that need no interactive password. The
// encrypted-file backend is deliberately absent: with no keyring the config
// keeps secrets in its own 0600 file, which is the fallback users can see.
func platformBackends() []keyring.BackendType {
	switch runtime.GOOS {
	case "darwin":
		return []keyring.BackendType{keyring.KeychainBackend}
	case "linux":
		return []keyring.BackendType{keyring.SecretServiceBackend, keyring.KWalletBackend}
	case "windows":
		return []keyring.BackendType{keyring.WinCredBackend}
	default:
		// Non-nil and empty: a nil slice means "every available backend" to
		// keyring.Open, which would pull in pass or the password-less file backend.
		return []keyring.BackendType{}
	}
}

func (s *KeyringStore) key(name string) string { return s.service + "." + name }

func (s *KeyringStore) Get(name string) (string, bool, error) {
	var item keyring.Item
	err := s.do("get", func() (err error) { item, err = s.kr.Get(s.key(name)); return })
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(item.Data), true, nil
}

func (s *KeyringStore) Set(name, value string) error {
	return s.do("set", func() error {
		return s.kr.Set(keyring.Item{
			Key:         s.key(name),
			Data:        []byte(value),
			Label:       "spoon - " + name,
			Description: "spoon credential",
			// Trusting this application avoids repeated Keychain prompts;
			// never sync credentials to iCloud.
			KeychainNotTrustApplication: false,
			KeychainNotSynchronizable:   true,
		})
	})
}

func (s *KeyringStore) Remove(name string) error {
	err := s.do("remove", func() error { return s.kr.Remove(s.key(name)) })
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return nil
	}
	return err
}

func (s *KeyringStore) Names() ([]string, error) {
	var keys []string
	if err := s.do("list", func() (err error) { keys, err = s.kr.Keys(); return }); err != nil {
		return nil, err
	}
	prefix := s.service + "."
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		if n, ok := strings.CutPrefix(k, prefix); ok && n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

func (s *KeyringStore) Name() string { return "keyring:" + s.service }

// do runs op with the store lock held and a deadline, and classifies failures.
func (s *KeyringStore) do(op string, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return classify(op, err)
	case <-time.After(s.timeout):
		// The goroutine may still finish and its result is dropped; a late Set
		// can therefore still land after this call reported a timeout.
		return fmt.Errorf("%s: %w", op, ErrTimeout)
	}
}

func classify(op string, err error) error {
	if err == nil || errors.Is(err, keyring.ErrKeyNotFound) {
		return err
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "locked"), strings.Contains(msg, "unlock"):
		return fmt.Errorf("%s: %w: %v", op, ErrLocked, err)
	case strings.Contains(msg, "denied"), strings.Contains(msg, "permission"):
		return fmt.Errorf("%s: %w: %v", op, ErrAccessDenied, err)
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return fmt.Errorf("%s: %w: %v", op, ErrTimeout, err)
	case strings.Contains(msg, "unsupported"), strings.Contains(msg, "not supported"):
		return fmt.Errorf("%s: %w: %v", op, ErrUnavailable, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}
