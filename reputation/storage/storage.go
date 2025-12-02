// Package storage provides storage backend implementations for reputation scores.
//
// The storage package provides implementations of the reputation.Storage interface:
//   - memory: In-memory storage (single instance, lost on restart)
//   - redis: Redis-based storage (shared across instances, persistent)
package storage

import (
	"time"

	"github.com/pokt-network/path/reputation"
)

// RedisConfig is an alias to reputation.RedisConfig for use in the storage package.
type RedisConfig = reputation.RedisConfig

// StorageConfig holds configuration for storage backends.
type StorageConfig struct {
	// Type specifies the storage backend ("memory" or "redis").
	Type string `yaml:"type"`

	// TTL is the time-to-live for stored scores.
	// After this duration, scores are considered stale and may be removed.
	// Zero means no expiration.
	TTL time.Duration `yaml:"ttl"`

	// Redis-specific configuration (only used when Type is "redis").
	Redis *RedisConfig `yaml:"redis,omitempty"`
}

// DefaultStorageConfig returns a StorageConfig with sensible defaults.
func DefaultStorageConfig() StorageConfig {
	return StorageConfig{
		Type: "memory",
		TTL:  0, // No expiration by default
	}
}
