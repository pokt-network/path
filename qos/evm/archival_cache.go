package evm

import (
	"sync"
	"time"
)

// ArchivalCache provides thread-safe TTL-based caching for archival endpoint status.
// It uses sync.RWMutex for read-heavy workloads (10-100x faster reads than regular mutex).
// This cache is designed to move Redis operations off the hot path by providing O(1) lookups
// for archival endpoint status with local memory access.
//
// Usage:
//
//	cache := NewArchivalCache()
//	cache.Set("endpoint-key", true, 8*time.Hour)
//	isArchival, ok := cache.Get("endpoint-key")
//
// Thread-safety: All methods are safe for concurrent use.
type ArchivalCache struct {
	mu    sync.RWMutex
	items map[string]*archivalCacheEntry
}

// archivalCacheEntry represents a single cache entry with TTL expiration.
type archivalCacheEntry struct {
	// IsArchival indicates whether the endpoint can serve historical blockchain data.
	IsArchival bool

	// ExpiresAt indicates when this cache entry should be considered stale.
	// After expiry, Get() will return (false, false) as if the entry doesn't exist.
	ExpiresAt time.Time
}

// NewArchivalCache creates a new ArchivalCache instance.
func NewArchivalCache() *ArchivalCache {
	return &ArchivalCache{
		items: make(map[string]*archivalCacheEntry),
	}
}

// Get retrieves archival status from cache.
// Returns (isArchival, ok) where ok indicates whether a valid (non-expired) entry exists.
//
// Return values:
//   - (true, true): Entry exists, is valid, and endpoint is archival-capable
//   - (false, true): Entry exists, is valid, but endpoint is NOT archival-capable
//   - (false, false): Entry missing or expired
//
// This is the hot path method - optimized for read-heavy workloads with RLock.
func (c *ArchivalCache) Get(key string) (isArchival bool, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.items[key]
	if !exists {
		return false, false // not cached
	}

	// Check expiration - treat expired entries as missing
	if time.Now().After(entry.ExpiresAt) {
		return false, false // expired
	}

	return entry.IsArchival, true // valid cache hit
}

// Set adds or updates archival status in cache with TTL.
// The entry will be considered expired after ttl duration from now.
//
// Parameters:
//   - key: Unique identifier for the endpoint (typically EndpointKey.String())
//   - isArchival: Whether the endpoint can serve historical data
//   - ttl: How long this entry remains valid (typically 8 hours matching archival expiry)
//
// This method is called by background workers, not the hot path.
func (c *ArchivalCache) Set(key string, isArchival bool, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = &archivalCacheEntry{
		IsArchival: isArchival,
		ExpiresAt:  time.Now().Add(ttl),
	}
}

// Len returns the number of entries in the cache (including potentially expired ones).
// Expired entries are cleaned up periodically by Cleanup(); between cleanups some
// entries may be stale, but Get() handles expiry correctly on reads.
func (c *ArchivalCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Delete removes a cache entry.
// Used for explicit invalidation when endpoint status is known to have changed.
func (c *ArchivalCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, key)
}

// Cleanup removes all expired entries from the cache.
// Should be called periodically by a background janitor goroutine to prevent
// unbounded memory growth.
//
// Typical usage:
//
//	ticker := time.NewTicker(1 * time.Hour)
//	go func() {
//	    for range ticker.C {
//	        cache.Cleanup()
//	    }
//	}()
func (c *ArchivalCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.items {
		if now.After(entry.ExpiresAt) {
			delete(c.items, key)
		}
	}
}
