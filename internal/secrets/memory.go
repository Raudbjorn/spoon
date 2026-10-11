package secrets

import (
	"sort"
	"sync"
)

// MemoryStore is an in-process Store for tests and for callers that need a
// Store without an OS keyring.
type MemoryStore struct {
	mu    sync.Mutex
	items map[string]string
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{items: map[string]string{}} }

func (m *MemoryStore) Get(name string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[name]
	return v, ok, nil
}

func (m *MemoryStore) Set(name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[name] = value
	return nil
}

func (m *MemoryStore) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, name)
	return nil
}

func (m *MemoryStore) Names() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.items))
	for n := range m.items {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func (m *MemoryStore) Name() string { return "memory" }
