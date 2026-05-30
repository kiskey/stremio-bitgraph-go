package debrid

import (
	"sync"
	"time"
)

type MemoryCache struct {
	store map[string]cacheEntry
	mu    sync.RWMutex
	ttl   time.Duration
}

type cacheEntry struct {
	value   map[string]interface{}
	expires time.Time
}

func NewMemoryCache(ttl time.Duration) *MemoryCache {
	return &MemoryCache{
		store: make(map[string]cacheEntry),
		ttl:   ttl,
	}
}

func (m *MemoryCache) Get(_ context.Context, key string) (map[string]interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.store[key]
	if !ok || time.Now().After(entry.expires) {
		return nil, nil
	}
	return entry.value, nil
}

func (m *MemoryCache) Set(_ context.Context, key string, value map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = cacheEntry{value: value, expires: time.Now().Add(m.ttl)}
	return nil
}

func (m *MemoryCache) Update(_ context.Context, key string, updates map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.store[key]
	if !ok {
		entry = cacheEntry{value: make(map[string]interface{}), expires: time.Now().Add(m.ttl)}
	}
	for k, v := range updates {
		entry.value[k] = v
	}
	entry.expires = time.Now().Add(m.ttl)
	m.store[key] = entry
	return nil
}

func (m *MemoryCache) GetByProviderID(_ context.Context, _ string) (map[string]interface{}, error) {
	return nil, nil
}
