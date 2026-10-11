package secrets

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/keyring"
)

func arrayStore(t *testing.T, items ...keyring.Item) (*KeyringStore, *keyring.ArrayKeyring) {
	t.Helper()
	arr := keyring.NewArrayKeyring(items)
	prev := openKeyring
	openKeyring = func(keyring.Config) (keyring.Keyring, error) { return arr, nil }
	t.Cleanup(func() { openKeyring = prev })
	st, err := NewKeyringStore(KeyringConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return st, arr
}

func TestKeyringStoreRoundTrip(t *testing.T) {
	st, arr := arrayStore(t, keyring.Item{Key: "other-app.token", Data: []byte("not ours")})

	if _, found, err := st.Get("github.token.a"); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found, nil", found, err)
	}
	if err := st.Set("github.token.a", "ghp_secret"); err != nil {
		t.Fatal(err)
	}
	if v, found, err := st.Get("github.token.a"); err != nil || !found || v != "ghp_secret" {
		t.Fatalf("Get = %q %v %v", v, found, err)
	}
	if item, err := arr.Get("spoon.github.token.a"); err != nil || item.KeychainNotSynchronizable != true {
		t.Errorf("item not namespaced under the service / sync-disabled: %+v %v", item, err)
	}
	names, err := st.Names()
	if err != nil || len(names) != 1 || names[0] != "github.token.a" {
		t.Fatalf("Names = %v %v; must list only spoon's entries", names, err)
	}
	if err := st.Remove("github.token.a"); err != nil {
		t.Fatal(err)
	}
	if err := st.Remove("github.token.a"); err != nil {
		t.Errorf("removing a missing entry = %v, want nil", err)
	}
	if _, found, _ := st.Get("github.token.a"); found {
		t.Error("entry still present after Remove")
	}
	if st.Name() != "keyring:spoon" {
		t.Errorf("Name = %q", st.Name())
	}
}

type blockingKeyring struct{ keyring.Keyring }

func (blockingKeyring) Get(string) (keyring.Item, error) {
	time.Sleep(time.Second)
	return keyring.Item{}, nil
}

func TestKeyringStoreTimesOut(t *testing.T) {
	prev := openKeyring
	openKeyring = func(keyring.Config) (keyring.Keyring, error) { return blockingKeyring{}, nil }
	defer func() { openKeyring = prev }()
	st, err := NewKeyringStore(KeyringConfig{Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Get("x"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

type delayedSetKeyring struct {
	keyring.Keyring
	release chan struct{}
	first   atomic.Bool
	active  atomic.Int32
	overlap atomic.Bool
}

func (k *delayedSetKeyring) enter() func() {
	if k.active.Add(1) != 1 {
		k.overlap.Store(true)
	}
	return func() { k.active.Add(-1) }
}

func (k *delayedSetKeyring) Set(item keyring.Item) error {
	defer k.enter()()
	if k.first.CompareAndSwap(false, true) {
		<-k.release
	}
	return k.Keyring.Set(item)
}

func (k *delayedSetKeyring) Get(name string) (keyring.Item, error) {
	defer k.enter()()
	return k.Keyring.Get(name)
}

func (k *delayedSetKeyring) Remove(name string) error {
	defer k.enter()()
	return k.Keyring.Remove(name)
}

func (k *delayedSetKeyring) Keys() ([]string, error) {
	defer k.enter()()
	return k.Keyring.Keys()
}

func TestKeyringTimeoutSerializesAndUndoesLateWrite(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new entry", true: "existing entry"}[existing], func(t *testing.T) {
			st, arr := arrayStore(t)
			if existing {
				if err := arr.Set(keyring.Item{Key: st.key("token"), Data: []byte("previous")}); err != nil {
					t.Fatal(err)
				}
			}
			backend := &delayedSetKeyring{Keyring: arr, release: make(chan struct{})}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(backend.release) }) })
			st.kr, st.timeout = backend, 25*time.Millisecond
			if err := st.Set("token", "synthetic-new-value"); !errors.Is(err, ErrTimeout) {
				t.Fatalf("blocked Set = %v, want timeout", err)
			}
			// Every successor must time out without entering the still-busy backend.
			_, _, getErr := st.Get("token")
			_, listErr := st.Names()
			for op, err := range map[string]error{"get": getErr, "list": listErr, "remove": st.Remove("token"), "set": st.Set("token", "successor")} {
				if !errors.Is(err, ErrTimeout) {
					t.Errorf("queued %s = %v, want timeout", op, err)
				}
			}
			release.Do(func() { close(backend.release) })
			value, found, err := st.Get("token")
			if err != nil || found != existing || (existing && value != "previous") {
				t.Errorf("late write was not undone: found=%v err=%v", found, err)
			}
			if backend.overlap.Load() {
				t.Error("keyring operations overlapped after timeout")
			}
		})
	}
}

func TestKeyringOpenFailureIsUnavailable(t *testing.T) {
	prev := openKeyring
	openKeyring = func(keyring.Config) (keyring.Keyring, error) { return nil, keyring.ErrNoAvailImpl }
	defer func() { openKeyring = prev }()
	if _, err := NewKeyringStore(KeyringConfig{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]error{
		"The collection is Locked": ErrLocked,
		"please unlock the wallet": ErrLocked,
		"Permission denied":        ErrAccessDenied,
		"dial: i/o timeout":        ErrTimeout,
		"backend not supported":    ErrUnavailable,
	}
	for msg, want := range cases {
		if got := classify("get", errors.New(msg)); !errors.Is(got, want) {
			t.Errorf("classify(%q) = %v, want %v", msg, got, want)
		}
	}
	if got := classify("get", errors.New("boom")); strings.Contains(got.Error(), "locked") || got == nil {
		t.Errorf("classify(other) = %v", got)
	}
	if classify("get", nil) != nil {
		t.Error("classify(nil) != nil")
	}
}

func TestRefsAndSelection(t *testing.T) {
	if name, ok := ParseRef(FormatRef("github.token.ab")); !ok || name != "github.token.ab" {
		t.Errorf("ParseRef(FormatRef) = %q %v", name, ok)
	}
	for _, inline := range []string{"ghp_abc", "", "keyring:"} {
		if _, ok := ParseRef(inline); ok {
			t.Errorf("ParseRef(%q) claims a reference", inline)
		}
	}
	env := func(v string) func(string) string { return func(string) string { return v } }
	for in, want := range map[string]string{"": BackendKeyring, "keyring": BackendKeyring, " FILE ": BackendFile} {
		if got, err := Selected(env(in)); err != nil || got != want {
			t.Errorf("Selected(%q) = %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := Selected(env("plaintext-please")); err == nil {
		t.Error("an unknown backend must be an error, not a silent fallback")
	}
	if st, err := Default(); st != nil || err != nil {
		t.Errorf("Default() under go test = %v %v, want (nil, nil) so tests never touch the real keyring", st, err)
	}
}

func TestUnsupportedPlatformBackendsAreNonNilEmpty(t *testing.T) {
	// keyring.Open treats a nil AllowedBackends as "every backend", which would
	// admit pass or the password-less file backend.
	var got []keyring.BackendType
	prev := openKeyring
	openKeyring = func(c keyring.Config) (keyring.Keyring, error) {
		got = c.AllowedBackends
		return nil, keyring.ErrNoAvailImpl
	}
	defer func() { openKeyring = prev }()
	_, _ = NewKeyringStore(KeyringConfig{AllowedBackends: []keyring.BackendType{}})
	if got == nil {
		t.Fatal("empty AllowedBackends was replaced by nil (= all backends)")
	}
}

func TestKeyringOpenIsBoundedByTimeout(t *testing.T) {
	prev := openKeyring
	release := make(chan struct{})
	openKeyring = func(keyring.Config) (keyring.Keyring, error) { <-release; return nil, keyring.ErrNoAvailImpl }
	defer func() { close(release); openKeyring = prev }()
	if _, err := NewKeyringStore(KeyringConfig{Timeout: 20 * time.Millisecond}); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), ErrTimeout.Error()) {
		t.Fatalf("err = %v, want unavailable caused by timeout", err)
	}
}
