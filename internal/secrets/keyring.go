package secrets

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
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
	gate    chan struct{} // held until the backend and any late-write rollback finish
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
	return &KeyringStore{kr: kr, service: cfg.ServiceName, timeout: cfg.Timeout, gate: make(chan struct{}, 1)}, nil
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
	open := openKeyring
	go func() {
		kr, err := open(cfg)
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
	err := s.do("get", func() (err error) { item, err = s.kr.Get(s.key(name)); return }, nil)
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(item.Data), true, nil
}

func (s *KeyringStore) Set(name, value string) error {
	var previous keyring.Item
	var existed, started bool
	return s.do("set", func() error {
		var err error
		previous, err = s.kr.Get(s.key(name))
		if err != nil && !errors.Is(err, keyring.ErrKeyNotFound) {
			return err
		}
		existed, started = err == nil, true
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
	}, func() error {
		if !started {
			return nil
		}
		if existed {
			return s.kr.Set(previous)
		}
		err := s.kr.Remove(s.key(name))
		if errors.Is(err, keyring.ErrKeyNotFound) {
			return nil
		}
		return err
	})
}

func (s *KeyringStore) Remove(name string) error {
	err := s.do("remove", func() error { return s.kr.Remove(s.key(name)) }, nil)
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return nil
	}
	return err
}

func (s *KeyringStore) Names() ([]string, error) {
	var keys []string
	if err := s.do("list", func() (err error) { keys, err = s.kr.Keys(); return }, nil); err != nil {
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

// do bounds queueing and execution while serializing uncancellable backend calls.
func (s *KeyringStore) do(op string, fn, undo func() error) error {
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case s.gate <- struct{}{}:
	case <-timer.C:
		return fmt.Errorf("%s: %w", op, ErrTimeout)
	}
	// The unbuffered handoff ensures an abandoned write always runs its rollback.
	done := make(chan error)
	abandoned := make(chan struct{})
	go func() {
		defer func() { <-s.gate }()
		err := fn()
		select {
		case done <- err:
		case <-abandoned:
			if undo != nil {
				if err := undo(); err != nil {
					slog.Warn("late keyring write rollback failed; credential state may have changed", "store", s.Name())
				}
			}
		}
	}()
	select {
	case err := <-done:
		return classify(op, err)
	case <-timer.C:
		close(abandoned)
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
