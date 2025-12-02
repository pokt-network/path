package storage

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// Compile-time check that MemoryStorage implements reputation.Storage.
var _ reputation.Storage = (*MemoryStorage)(nil)

// MemoryStorage implements Storage using an in-memory map.
// This implementation is suitable for single-instance deployments
// or as a fallback when Redis is unavailable.
//
// Note: Data is lost on process restart and is not shared across instances.
type MemoryStorage struct {
	mu     sync.RWMutex
	scores map[string]scoreEntry
	ttl    time.Duration
	closed bool
}

// scoreEntry holds a score with its expiration time.
type scoreEntry struct {
	score     reputation.Score
	expiresAt time.Time // Zero means no expiration
}

// isExpired returns true if the entry has expired.
func (e scoreEntry) isExpired() bool {
	if e.expiresAt.IsZero() {
		return false
	}
	return time.Now().After(e.expiresAt)
}

// NewMemoryStorage creates a new in-memory storage.
// If ttl is zero, entries never expire.
func NewMemoryStorage(ttl time.Duration) *MemoryStorage {
	return &MemoryStorage{
		scores: make(map[string]scoreEntry),
		ttl:    ttl,
	}
}

// Get retrieves the score for an endpoint.
func (m *MemoryStorage) Get(ctx context.Context, key reputation.EndpointKey) (reputation.Score, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return reputation.Score{}, reputation.ErrStorageClosed
	}

	entry, ok := m.scores[key.String()]
	if !ok {
		return reputation.Score{}, reputation.ErrNotFound
	}

	if entry.isExpired() {
		return reputation.Score{}, reputation.ErrNotFound
	}

	return entry.score, nil
}

// GetMultiple retrieves scores for multiple endpoints.
func (m *MemoryStorage) GetMultiple(ctx context.Context, keys []reputation.EndpointKey) (map[reputation.EndpointKey]reputation.Score, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, reputation.ErrStorageClosed
	}

	result := make(map[reputation.EndpointKey]reputation.Score, len(keys))
	for _, key := range keys {
		entry, ok := m.scores[key.String()]
		if ok && !entry.isExpired() {
			result[key] = entry.score
		}
	}

	return result, nil
}

// Set stores or updates the score for an endpoint.
func (m *MemoryStorage) Set(ctx context.Context, key reputation.EndpointKey, score reputation.Score) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return reputation.ErrStorageClosed
	}

	entry := scoreEntry{score: score}
	if m.ttl > 0 {
		entry.expiresAt = time.Now().Add(m.ttl)
	}

	m.scores[key.String()] = entry
	return nil
}

// SetMultiple stores or updates scores for multiple endpoints.
func (m *MemoryStorage) SetMultiple(ctx context.Context, scores map[reputation.EndpointKey]reputation.Score) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return reputation.ErrStorageClosed
	}

	for key, score := range scores {
		entry := scoreEntry{score: score}
		if m.ttl > 0 {
			entry.expiresAt = time.Now().Add(m.ttl)
		}
		m.scores[key.String()] = entry
	}

	return nil
}

// Delete removes the score for an endpoint.
func (m *MemoryStorage) Delete(ctx context.Context, key reputation.EndpointKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return reputation.ErrStorageClosed
	}

	delete(m.scores, key.String())
	return nil
}

// List returns all stored endpoint keys, optionally filtered by service ID.
func (m *MemoryStorage) List(ctx context.Context, serviceID string) ([]reputation.EndpointKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, reputation.ErrStorageClosed
	}

	var keys []reputation.EndpointKey
	prefix := ""
	if serviceID != "" {
		prefix = serviceID + ":"
	}

	for keyStr, entry := range m.scores {
		if entry.isExpired() {
			continue
		}

		if prefix != "" && !strings.HasPrefix(keyStr, prefix) {
			continue
		}

		// Parse the key string back to EndpointKey
		// Format: "serviceID:endpointAddr"
		parts := strings.SplitN(keyStr, ":", 2)
		if len(parts) == 2 {
			keys = append(keys, reputation.NewEndpointKey(
				protocol.ServiceID(parts[0]),
				protocol.EndpointAddr(parts[1]),
			))
		}
	}

	return keys, nil
}

// Close releases resources held by the storage.
func (m *MemoryStorage) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true
	m.scores = nil
	return nil
}

// Len returns the number of entries in the storage.
// This is primarily for testing purposes.
func (m *MemoryStorage) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return 0
	}

	count := 0
	for _, entry := range m.scores {
		if !entry.isExpired() {
			count++
		}
	}
	return count
}

// Cleanup removes expired entries from the storage.
// This is called periodically to prevent memory bloat.
func (m *MemoryStorage) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return
	}

	for key, entry := range m.scores {
		if entry.isExpired() {
			delete(m.scores, key)
		}
	}
}
