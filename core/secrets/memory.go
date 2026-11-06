package secrets

import (
	"maps"
	"slices"
	"sync"
)

// Memory is a Store in memory, for tests and for the CLI, which takes
// secrets from its environment and flags. The zero value is empty and
// ready to use.
type Memory struct {
	mu     sync.Mutex
	values map[string][]byte
}

// Get implements Store.
func (m *Memory) Get(key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	if !ok {
		return nil, ErrNotFound
	}
	return slices.Clone(v), nil
}

// Set implements Store.
func (m *Memory) Set(key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string][]byte{}
	}
	m.values[key] = slices.Clone(value)
	return nil
}

// Delete implements Store.
func (m *Memory) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, key)
	return nil
}

// Keys returns the keys that have values, sorted.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(maps.Keys(m.values))
}
