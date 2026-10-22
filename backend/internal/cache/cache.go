// Package cache is a small concurrency-safe LRU with per-entry TTL. Upstream
// music metadata changes slowly, so caching raw responses keeps us well under
// every provider's rate limit and makes steering (re-ranking the same seed)
// nearly free.
package cache

import (
	"container/list"
	"sync"
	"time"
)

type entry[V any] struct {
	key     string
	value   V
	expires time.Time
}

type LRU[V any] struct {
	mu    sync.Mutex
	cap   int
	ttl   time.Duration
	ll    *list.List
	items map[string]*list.Element
	now   func() time.Time

	hits, misses uint64
}

func New[V any](capacity int, ttl time.Duration) *LRU[V] {
	if capacity <= 0 {
		capacity = 1
	}
	return &LRU[V]{cap: capacity, ttl: ttl, ll: list.New(), items: map[string]*list.Element{}, now: time.Now}
}

func (c *LRU[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	el, ok := c.items[key]
	if !ok {
		c.misses++
		return zero, false
	}
	e := el.Value.(*entry[V])
	if c.now().After(e.expires) {
		c.ll.Remove(el)
		delete(c.items, key)
		c.misses++
		return zero, false
	}
	c.ll.MoveToFront(el)
	c.hits++
	return e.value, true
}

func (c *LRU[V]) Set(key string, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp := c.now().Add(c.ttl)
	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry[V])
		e.value, e.expires = v, exp
		c.ll.MoveToFront(el)
		return
	}
	c.items[key] = c.ll.PushFront(&entry[V]{key: key, value: v, expires: exp})
	for c.ll.Len() > c.cap {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.items, last.Value.(*entry[V]).key)
	}
}

// Stats returns hit/miss counters and current size.
func (c *LRU[V]) Stats() (hits, misses uint64, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.ll.Len()
}
