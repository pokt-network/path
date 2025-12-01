// Package reputation provides endpoint reputation tracking and scoring.
//
// The reputation system tracks the reliability and performance of endpoints
// based on their responses to requests. Endpoints accumulate a reputation score
// that influences their selection probability for future requests.
//
// Key concepts:
//   - Score: A numeric value (0-100) representing endpoint reliability
//   - Signal: An event that affects an endpoint's score (success, error, timeout, etc.)
//   - Storage: Backend for persisting scores (memory, Redis, etc.)
package reputation

import (
	"context"
	"time"

	"github.com/pokt-network/path/protocol"
)

// EndpointKey uniquely identifies an endpoint for reputation tracking.
// It combines the service ID and endpoint address to create a unique key.
type EndpointKey struct {
	ServiceID    protocol.ServiceID
	EndpointAddr protocol.EndpointAddr
}

// NewEndpointKey creates a new EndpointKey from service ID and endpoint address.
func NewEndpointKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr) EndpointKey {
	return EndpointKey{
		ServiceID:    serviceID,
		EndpointAddr: endpointAddr,
	}
}

// String returns a string representation of the endpoint key.
func (k EndpointKey) String() string {
	return string(k.ServiceID) + ":" + string(k.EndpointAddr)
}

// Score represents an endpoint's reputation score at a point in time.
type Score struct {
	// Value is the current score (0-100).
	// Higher scores indicate more reliable endpoints.
	Value float64

	// LastUpdated is when the score was last modified.
	LastUpdated time.Time

	// SuccessCount is the total number of successful requests.
	SuccessCount int64

	// ErrorCount is the total number of failed requests.
	ErrorCount int64
}

// IsValid returns true if the score is within valid bounds.
func (s Score) IsValid() bool {
	return s.Value >= MinScore && s.Value <= MaxScore
}

// Score constants define the bounds and defaults for reputation scores.
const (
	// MinScore is the lowest possible reputation score.
	MinScore float64 = 0

	// MaxScore is the highest possible reputation score.
	MaxScore float64 = 100

	// InitialScore is the starting score for new endpoints.
	// Endpoints start with a moderately high score to give them a fair chance.
	InitialScore float64 = 80

	// DefaultMinThreshold is the minimum score required for endpoint selection.
	// Endpoints below this threshold are excluded from selection.
	DefaultMinThreshold float64 = 30
)

// ReputationService provides endpoint reputation tracking and querying.
// Implementations must be safe for concurrent use.
//
// Performance design:
//   - All reads (GetScore, FilterByScore) are served from local in-memory cache (<1μs)
//   - Writes (RecordSignal) update local cache immediately + async sync to backend
//   - Background refresh keeps local cache in sync with shared backend (Redis)
//
// This ensures the hot path (request handling) is never blocked by storage latency.
type ReputationService interface {
	// RecordSignal records a signal event for an endpoint.
	// Updates local cache immediately and queues async write to backend storage.
	// This method is non-blocking - it returns immediately after updating local cache.
	RecordSignal(ctx context.Context, key EndpointKey, signal Signal) error

	// GetScore retrieves the current reputation score for an endpoint.
	// Always reads from local cache for minimal latency (<1μs).
	// Returns the score or an error if the endpoint is not found.
	GetScore(ctx context.Context, key EndpointKey) (Score, error)

	// GetScores retrieves reputation scores for multiple endpoints.
	// Always reads from local cache for minimal latency.
	// Returns a map of endpoint keys to scores. Missing endpoints are omitted.
	GetScores(ctx context.Context, keys []EndpointKey) (map[EndpointKey]Score, error)

	// FilterByScore filters endpoints that meet the minimum score threshold.
	// Always reads from local cache for minimal latency.
	// Returns only endpoints with scores >= minThreshold.
	FilterByScore(ctx context.Context, keys []EndpointKey, minThreshold float64) ([]EndpointKey, error)

	// ResetScore resets an endpoint's score to the initial value.
	// Used for administrative purposes or testing.
	ResetScore(ctx context.Context, key EndpointKey) error

	// Start begins background sync processes (e.g., Redis pub/sub, periodic refresh).
	// Should be called once during initialization.
	Start(ctx context.Context) error

	// Stop gracefully shuts down background processes and flushes pending writes.
	Stop() error
}

// Config holds configuration for the reputation system.
type Config struct {
	// Enabled determines if reputation tracking is active.
	// When false, all ReputationService methods are no-ops.
	Enabled bool `yaml:"enabled"`

	// InitialScore is the starting score for new endpoints.
	// Default: 80
	InitialScore float64 `yaml:"initial_score"`

	// MinThreshold is the minimum score for endpoint selection.
	// Default: 30
	MinThreshold float64 `yaml:"min_threshold"`

	// RecoveryTimeout is the duration after which a low-scoring endpoint
	// with no signals is reset to InitialScore. This allows endpoints
	// to recover from temporary failures (crashes, network issues, etc.).
	// Default: 5m
	RecoveryTimeout time.Duration `yaml:"recovery_timeout"`

	// StorageType specifies the storage backend ("memory" or "redis").
	// Default: "memory"
	StorageType string `yaml:"storage_type"`

	// SyncConfig configures background synchronization behavior.
	SyncConfig SyncConfig `yaml:"sync_config"`
}

// SyncConfig configures background synchronization for the reputation system.
type SyncConfig struct {
	// RefreshInterval is how often to refresh scores from the backend storage.
	// Only applies when using shared storage (Redis).
	// Default: 5s
	RefreshInterval time.Duration `yaml:"refresh_interval"`

	// WriteBufferSize is the max number of pending writes to buffer before blocking.
	// Default: 1000
	WriteBufferSize int `yaml:"write_buffer_size"`

	// FlushInterval is how often to flush buffered writes to backend storage.
	// Default: 100ms
	FlushInterval time.Duration `yaml:"flush_interval"`
}

// Recovery and SyncConfig defaults.
const (
	// DefaultRecoveryTimeout is the duration after which low-scoring endpoints
	// are reset to initial score if they have no signals.
	DefaultRecoveryTimeout = 5 * time.Minute

	// DefaultRefreshInterval is how often to refresh from backend storage.
	DefaultRefreshInterval = 5 * time.Second

	// DefaultWriteBufferSize is the max pending writes before blocking.
	DefaultWriteBufferSize = 1000

	// DefaultFlushInterval is how often to flush buffered writes.
	DefaultFlushInterval = 100 * time.Millisecond
)

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:         false, // Disabled by default for backward compatibility
		InitialScore:    InitialScore,
		MinThreshold:    DefaultMinThreshold,
		RecoveryTimeout: DefaultRecoveryTimeout,
		StorageType:     "memory",
		SyncConfig:      DefaultSyncConfig(),
	}
}

// DefaultSyncConfig returns a SyncConfig with sensible defaults.
func DefaultSyncConfig() SyncConfig {
	return SyncConfig{
		RefreshInterval: DefaultRefreshInterval,
		WriteBufferSize: DefaultWriteBufferSize,
		FlushInterval:   DefaultFlushInterval,
	}
}

// HydrateDefaults fills in zero values with defaults.
func (c *Config) HydrateDefaults() {
	if c.InitialScore == 0 {
		c.InitialScore = InitialScore
	}
	if c.MinThreshold == 0 {
		c.MinThreshold = DefaultMinThreshold
	}
	if c.RecoveryTimeout == 0 {
		c.RecoveryTimeout = DefaultRecoveryTimeout
	}
	if c.StorageType == "" {
		c.StorageType = "memory"
	}
	c.SyncConfig.HydrateDefaults()
}

// HydrateDefaults fills in zero values with defaults.
func (s *SyncConfig) HydrateDefaults() {
	if s.RefreshInterval == 0 {
		s.RefreshInterval = DefaultRefreshInterval
	}
	if s.WriteBufferSize == 0 {
		s.WriteBufferSize = DefaultWriteBufferSize
	}
	if s.FlushInterval == 0 {
		s.FlushInterval = DefaultFlushInterval
	}
}
