package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSearchCache_SetAndGet(t *testing.T) {
	c := NewSearchCache()

	key := SearchKey("python asyncio", 5)
	c.Set(key, "result 1\nresult 2\nresult 3")

	val, ok := c.Get(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !strings.Contains(val, "result 1") {
		t.Errorf("unexpected value: %s", val)
	}
}

func TestSearchCache_Miss(t *testing.T) {
	c := NewSearchCache()

	_, ok := c.Get("nonexistent")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestSearchCache_TTLExpiry(t *testing.T) {
	c := &WebCache{
		entries: make(map[string]*cacheEntry),
		ttl:     50 * time.Millisecond, // very short TTL for testing
	}

	c.Set("k1", "value1")

	// Should hit immediately
	_, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected cache hit before expiry")
	}

	// Wait for expiry
	time.Sleep(60 * time.Millisecond)

	_, ok = c.Get("k1")
	if ok {
		t.Error("expected cache miss after TTL expiry")
	}
}

func TestFetchCache_SizeEviction(t *testing.T) {
	c := &WebCache{
		entries: make(map[string]*cacheEntry),
		maxSize: 100, // 100 bytes max
		ttl:     5 * time.Minute,
	}

	// Fill with small entries
	c.Set("a", strings.Repeat("x", 40))
	c.Set("b", strings.Repeat("y", 40))

	entries, totalBytes := c.Stats()
	if entries != 2 || totalBytes != 80 {
		t.Errorf("expected 2 entries, 80 bytes; got %d entries, %d bytes", entries, totalBytes)
	}

	// This should evict "a" to make room
	c.Set("c", strings.Repeat("z", 50))

	entries, totalBytes = c.Stats()
	if entries != 2 {
		t.Errorf("expected 2 entries after eviction, got %d", entries)
	}

	// "a" should be evicted (oldest)
	_, ok := c.Get("a")
	if ok {
		t.Error("expected 'a' to be evicted")
	}

	// "b" and "c" should remain
	_, ok = c.Get("b")
	if !ok {
		t.Error("expected 'b' to remain")
	}
	_, ok = c.Get("c")
	if !ok {
		t.Error("expected 'c' to remain")
	}
}

func TestFetchCache_MaxEntries(t *testing.T) {
	c := NewSearchCache()

	// Add 201 entries — should cap at 200
	for i := 0; i < 201; i++ {
		c.Set(fmt.Sprintf("key_%d", i), "value")
	}

	entries, _ := c.Stats()
	if entries > 200 {
		t.Errorf("expected max 200 entries, got %d", entries)
	}
}

func TestSearchKey_Deterministic(t *testing.T) {
	k1 := SearchKey("python asyncio", 5)
	k2 := SearchKey("python asyncio", 5)
	k3 := SearchKey("python asyncio", 10)

	if k1 != k2 {
		t.Error("same query+count should produce same key")
	}
	if k1 == k3 {
		t.Error("different count should produce different key")
	}
}

func TestFetchKey_Deterministic(t *testing.T) {
	k1 := FetchKey("https://example.com")
	k2 := FetchKey("https://example.com")
	k3 := FetchKey("https://example.com/other")

	if k1 != k2 {
		t.Error("same URL should produce same key")
	}
	if k1 == k3 {
		t.Error("different URL should produce different key")
	}
}

func TestCache_Clear(t *testing.T) {
	c := NewSearchCache()
	c.Set("k1", "v1")
	c.Set("k2", "v2")

	c.Clear()

	entries, bytes := c.Stats()
	if entries != 0 || bytes != 0 {
		t.Errorf("expected empty after clear, got %d entries, %d bytes", entries, bytes)
	}
}

func TestCache_Update(t *testing.T) {
	c := NewSearchCache()

	c.Set("k1", "old_value")
	c.Set("k1", "new_value")

	val, ok := c.Get("k1")
	if !ok || val != "new_value" {
		t.Errorf("expected updated value 'new_value', got %q", val)
	}

	entries, _ := c.Stats()
	if entries != 1 {
		t.Errorf("expected 1 entry after update, got %d", entries)
	}
}
