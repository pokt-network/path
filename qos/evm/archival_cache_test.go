package evm

import (
	"sync"
	"testing"
	"time"
)

// TestArchivalCache_GetMissing verifies that Get returns (false, false) for missing entries.
func TestArchivalCache_GetMissing(t *testing.T) {
	cache := NewArchivalCache()

	isArchival, ok := cache.Get("nonexistent-key")
	if ok {
		t.Error("Expected ok=false for missing entry, got ok=true")
	}
	if isArchival {
		t.Error("Expected isArchival=false for missing entry, got isArchival=true")
	}
}

// TestArchivalCache_SetAndGet verifies that Set stores entries and Get retrieves them.
func TestArchivalCache_SetAndGet(t *testing.T) {
	cache := NewArchivalCache()

	// Set archival=true
	cache.Set("endpoint-1", true, 1*time.Hour)

	// Get should return (true, true)
	isArchival, ok := cache.Get("endpoint-1")
	if !ok {
		t.Error("Expected ok=true for existing entry, got ok=false")
	}
	if !isArchival {
		t.Error("Expected isArchival=true, got isArchival=false")
	}

	// Set archival=false
	cache.Set("endpoint-2", false, 1*time.Hour)

	// Get should return (false, true) - cached but not archival
	isArchival, ok = cache.Get("endpoint-2")
	if !ok {
		t.Error("Expected ok=true for existing entry, got ok=false")
	}
	if isArchival {
		t.Error("Expected isArchival=false, got isArchival=true")
	}
}

// TestArchivalCache_Expiration verifies that entries expire after TTL.
func TestArchivalCache_Expiration(t *testing.T) {
	cache := NewArchivalCache()

	// Set entry with 100ms TTL
	cache.Set("endpoint-1", true, 100*time.Millisecond)

	// Immediately get - should exist
	isArchival, ok := cache.Get("endpoint-1")
	if !ok {
		t.Error("Expected ok=true immediately after Set, got ok=false")
	}
	if !isArchival {
		t.Error("Expected isArchival=true, got isArchival=false")
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Get should return (false, false) for expired entry
	isArchival, ok = cache.Get("endpoint-1")
	if ok {
		t.Error("Expected ok=false for expired entry, got ok=true")
	}
	if isArchival {
		t.Error("Expected isArchival=false for expired entry, got isArchival=true")
	}
}

// TestArchivalCache_Delete verifies that Delete removes entries from cache.
func TestArchivalCache_Delete(t *testing.T) {
	cache := NewArchivalCache()

	// Set entry
	cache.Set("endpoint-1", true, 1*time.Hour)

	// Verify it exists
	_, ok := cache.Get("endpoint-1")
	if !ok {
		t.Error("Expected ok=true after Set, got ok=false")
	}

	// Delete entry
	cache.Delete("endpoint-1")

	// Verify it's gone
	_, ok = cache.Get("endpoint-1")
	if ok {
		t.Error("Expected ok=false after Delete, got ok=true")
	}
}

// TestArchivalCache_Cleanup verifies that Cleanup removes only expired entries.
func TestArchivalCache_Cleanup(t *testing.T) {
	cache := NewArchivalCache()

	// Set multiple entries with different TTLs
	cache.Set("short-ttl-1", true, 100*time.Millisecond)
	cache.Set("short-ttl-2", true, 100*time.Millisecond)
	cache.Set("long-ttl", true, 1*time.Hour)

	// Wait for short TTL entries to expire
	time.Sleep(150 * time.Millisecond)

	// Run cleanup
	cache.Cleanup()

	// Short TTL entries should be gone
	_, ok := cache.Get("short-ttl-1")
	if ok {
		t.Error("Expected short-ttl-1 to be cleaned up, but it still exists")
	}

	_, ok = cache.Get("short-ttl-2")
	if ok {
		t.Error("Expected short-ttl-2 to be cleaned up, but it still exists")
	}

	// Long TTL entry should still exist
	isArchival, ok := cache.Get("long-ttl")
	if !ok {
		t.Error("Expected long-ttl to still exist after cleanup, got ok=false")
	}
	if !isArchival {
		t.Error("Expected long-ttl isArchival=true, got isArchival=false")
	}
}

// TestArchivalCache_Concurrent verifies that concurrent reads/writes don't race.
func TestArchivalCache_Concurrent(t *testing.T) {
	cache := NewArchivalCache()

	// Number of concurrent goroutines
	const numGoroutines = 100
	const numOperations = 1000

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2) // writers + readers

	// Launch concurrent writers
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				key := "endpoint-1" // All write to same key to maximize contention
				cache.Set(key, j%2 == 0, 1*time.Hour)
			}
		}(i)
	}

	// Launch concurrent readers
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				key := "endpoint-1" // All read from same key
				_, _ = cache.Get(key)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// If we get here without data races, test passes
	// Run with: go test -race ./qos/evm/... -run TestArchivalCache_Concurrent
}
