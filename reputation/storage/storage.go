// Package storage provides storage backend implementations for reputation scores.
//
// The storage package provides implementations of the reputation.Storage interface:
//   - memory: In-memory storage (single instance, lost on restart)
//   - redis: Redis-based storage (shared across instances, persistent) - future PR
package storage

import (
	"time"
)

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

// RedisConfig holds Redis-specific configuration.
type RedisConfig struct {
	// Address is the Redis server address (host:port).
	Address string `yaml:"address"`

	// Password for Redis authentication (optional).
	Password string `yaml:"password"`

	// DB is the Redis database number.
	DB int `yaml:"db"`

	// KeyPrefix is prepended to all keys stored in Redis.
	// Default: "path:reputation:"
	KeyPrefix string `yaml:"key_prefix"`
}

// DefaultStorageConfig returns a StorageConfig with sensible defaults.
func DefaultStorageConfig() StorageConfig {
	return StorageConfig{
		Type: "memory",
		TTL:  0, // No expiration by default
	}
}

// DefaultRedisConfig returns a RedisConfig with sensible defaults.
func DefaultRedisConfig() RedisConfig {
	return RedisConfig{
		Address:   "localhost:6379",
		Password:  "",
		DB:        0,
		KeyPrefix: "path:reputation:",
	}
}
