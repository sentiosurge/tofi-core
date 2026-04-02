package agent

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

// WebCache provides a TTL-based LRU cache for web search results and fetched URLs.
// Avoids redundant API calls when the same query/URL is requested within a short window.
//
// Two separate caches:
//   - Search cache: keyed by (query + count), TTL 5 minutes
//   - Fetch cache:  keyed by URL, TTL 15 minutes, max 50MB total size
type WebCache struct {
	mu      sync.Mutex
	entries map[string]*cacheEntry
	order   []string // LRU order (oldest first)

	maxSize   int64 // max total bytes (0 = unlimited)
	totalSize int64
	ttl       time.Duration
}

type cacheEntry struct {
	key       string
	value     string
	size      int64
	createdAt time.Time
}

// NewSearchCache creates a cache for search results (5 min TTL, 50 entries max).
func NewSearchCache() *WebCache {
	return &WebCache{
		entries: make(map[string]*cacheEntry),
		maxSize: 0, // no size limit for search (entries are small)
		ttl:     5 * time.Minute,
	}
}

// NewFetchCache creates a cache for fetched URLs (15 min TTL, 50MB max).
func NewFetchCache() *WebCache {
	return &WebCache{
		entries: make(map[string]*cacheEntry),
		maxSize: 50 * 1024 * 1024, // 50MB
		ttl:     15 * time.Minute,
	}
}

// Get retrieves a cached value by key. Returns ("", false) if not found or expired.
func (c *WebCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}

	// Check TTL
	if time.Since(entry.createdAt) > c.ttl {
		c.removeEntryLocked(key)
		return "", false
	}

	// Move to end of LRU order (most recently used)
	c.touchLocked(key)

	return entry.value, true
}

// Set stores a value in the cache. Evicts oldest entries if size limit exceeded.
func (c *WebCache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	size := int64(len(value))

	// Remove existing entry if updating
	if _, exists := c.entries[key]; exists {
		c.removeEntryLocked(key)
	}

	// Evict expired entries first
	c.evictExpiredLocked()

	// Evict oldest entries until size fits (if max size is set)
	if c.maxSize > 0 {
		for c.totalSize+size > c.maxSize && len(c.order) > 0 {
			oldest := c.order[0]
			c.removeEntryLocked(oldest)
		}
	}

	// Cap at 200 entries regardless of size
	for len(c.order) >= 200 {
		oldest := c.order[0]
		c.removeEntryLocked(oldest)
	}

	c.entries[key] = &cacheEntry{
		key:       key,
		value:     value,
		size:      size,
		createdAt: time.Now(),
	}
	c.order = append(c.order, key)
	c.totalSize += size
}

// SearchKey generates a cache key for a search query.
func SearchKey(query string, count int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("search:%s:%d", query, count)))
	return fmt.Sprintf("s_%x", h[:8])
}

// FetchKey generates a cache key for a URL fetch.
func FetchKey(url string) string {
	h := sha256.Sum256([]byte("fetch:" + url))
	return fmt.Sprintf("f_%x", h[:8])
}

// Stats returns cache statistics.
func (c *WebCache) Stats() (entries int, totalBytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.totalSize
}

// Clear removes all entries.
func (c *WebCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cacheEntry)
	c.order = nil
	c.totalSize = 0
}

// internal helpers

func (c *WebCache) removeEntryLocked(key string) {
	entry, ok := c.entries[key]
	if !ok {
		return
	}
	c.totalSize -= entry.size
	delete(c.entries, key)

	// Remove from order slice
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *WebCache) touchLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, key)
			break
		}
	}
}

func (c *WebCache) evictExpiredLocked() {
	now := time.Now()
	var toRemove []string
	for key, entry := range c.entries {
		if now.Sub(entry.createdAt) > c.ttl {
			toRemove = append(toRemove, key)
		}
	}
	for _, key := range toRemove {
		c.removeEntryLocked(key)
	}
}
