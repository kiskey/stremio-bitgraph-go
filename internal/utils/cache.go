
package utils

import (
	"sync"
	"time"
)

type TTLCache struct {
	mu    sync.RWMutex
	items map[string]cacheItem
	ttl   time.Duration
}

type cacheItem struct {
	value      interface{}
	expiration time.Time
}

func NewTTLCache(ttl time.Duration) *TTLCache {
	c := &TTLCache{
		items: make(map[string]cacheItem),
		ttl:   ttl,
	}
	go c.startCleanup(5 * time.Minute)
	return c
}

func (c *TTLCache) startCleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for k, v := range c.items {
			if now.After(v.expiration) {
				delete(c.items, k)
			}
		}
		c.mu.Unlock()
	}
}

func (c *TTLCache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(item.expiration) {
		return nil, false
	}
	return item.value, true
}

func (c *TTLCache) Set(key string, value interface{}) {
	c.mu.Lock()
	c.items[key] = cacheItem{value: value, expiration: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}
