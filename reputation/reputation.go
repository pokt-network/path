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
	"fmt"
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

	// LatencyMetrics tracks response latency statistics for this endpoint.
	// Updated from both health checks and client requests.
	LatencyMetrics LatencyMetrics
}

// LatencyMetrics tracks response latency statistics for an endpoint.
// These metrics are used for endpoint selection within the same reputation tier.
type LatencyMetrics struct {
	// LastLatency is the most recent response latency.
	LastLatency time.Duration

	// AvgLatency is the exponential moving average of response latency.
	// Updated using: avg = (1-alpha)*avg + alpha*new_sample, where alpha=0.1
	AvgLatency time.Duration

	// MinLatency is the minimum latency observed (best case).
	MinLatency time.Duration

	// MaxLatency is the maximum latency observed (worst case).
	MaxLatency time.Duration

	// SampleCount is the number of latency samples collected.
	SampleCount int64

	// LastUpdated is when the latency metrics were last updated.
	LastUpdated time.Time
}

// UpdateLatency updates the latency metrics with a new sample.
// Uses exponential moving average with alpha=0.1 for AvgLatency.
func (m *LatencyMetrics) UpdateLatency(latency time.Duration) {
	m.LastLatency = latency
	m.SampleCount++
	m.LastUpdated = time.Now()

	// Update min/max
	if m.MinLatency == 0 || latency < m.MinLatency {
		m.MinLatency = latency
	}
	if latency > m.MaxLatency {
		m.MaxLatency = latency
	}

	// Update exponential moving average (alpha = 0.1)
	const alpha = 0.1
	if m.AvgLatency == 0 {
		m.AvgLatency = latency
	} else {
		// EMA: new_avg = (1-alpha)*old_avg + alpha*new_sample
		m.AvgLatency = time.Duration(float64(m.AvgLatency)*(1-alpha) + float64(latency)*alpha)
	}
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

// Key granularity options determine how endpoints are grouped for scoring.
// Ordered from finest to coarsest granularity.
const (
	// KeyGranularityEndpoint scores each endpoint URL separately (finest granularity).
	// Key format: serviceID:supplierAddr-endpointURL
	// This is the default behavior.
	KeyGranularityEndpoint = "per-endpoint"

	// KeyGranularityDomain scores all endpoints from the same hosting domain together.
	// Key format: serviceID:domain (e.g., eth:nodefleet.net)
	// Use when a hosting provider's overall reliability matters.
	KeyGranularityDomain = "per-domain"

	// KeyGranularitySupplier scores all endpoints from the same supplier together.
	// Key format: serviceID:supplierAddr
	// Use when a supplier's overall reliability matters more than individual endpoints.
	KeyGranularitySupplier = "per-supplier"
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

	// KeyBuilderForService returns the KeyBuilder for the given service.
	// Uses service-specific config if available, otherwise falls back to global default.
	KeyBuilderForService(serviceID protocol.ServiceID) KeyBuilder

	// Start begins background sync processes (e.g., Redis pub/sub, periodic refresh).
	// Should be called once during initialization.
	Start(ctx context.Context) error

	// Stop gracefully shuts down background processes and flushes pending writes.
	Stop() error
}

// Config holds configuration for the reputation system.
type Config struct {
	// Enabled determines if reputation tracking is active.
	// IMPORTANT: Reputation is MANDATORY - setting this to false is ignored.
	// This field is kept for backward compatibility.
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
	// IMPORTANT: This is IGNORED when TieredSelection.Probation or HealthChecks are enabled.
	// Signal-based recovery takes precedence over time-based recovery.
	// Default: 5m
	RecoveryTimeout time.Duration `yaml:"recovery_timeout"`

	// StorageType specifies the storage backend ("memory" or "redis").
	// Default: "memory"
	StorageType string `yaml:"storage_type"`

	// KeyGranularity determines how endpoints are grouped for scoring (global default).
	// Options: "per-endpoint" (default), "per-domain", "per-supplier"
	// Default: "per-endpoint"
	KeyGranularity string `yaml:"key_granularity"`

	// ServiceOverrides allows per-service configuration overrides.
	// Use this to apply different granularity rules to specific services.
	ServiceOverrides map[string]ServiceConfig `yaml:"service_overrides,omitempty"`

	// SyncConfig configures background synchronization behavior.
	SyncConfig SyncConfig `yaml:"sync_config"`

	// TieredSelection configures endpoint selection based on reputation tiers.
	// Endpoints are grouped into tiers based on their scores.
	TieredSelection TieredSelectionConfig `yaml:"tiered_selection,omitempty"`

	// Latency configures how response latency affects reputation scoring.
	// Fast endpoints get bonuses, slow endpoints get penalties.
	Latency LatencyConfig `yaml:"latency,omitempty"`
}

// TieredSelectionConfig configures tier-based endpoint selection.
type TieredSelectionConfig struct {
	// Enabled enables/disables tiered selection.
	Enabled bool `yaml:"enabled,omitempty"`
	// Tier1Threshold is the minimum score for tier 1 (highest quality).
	Tier1Threshold float64 `yaml:"tier1_threshold,omitempty"`
	// Tier2Threshold is the minimum score for tier 2 (medium quality).
	Tier2Threshold float64 `yaml:"tier2_threshold,omitempty"`
	// Probation configures the probation system for recovering low-scoring endpoints.
	Probation ProbationConfig `yaml:"probation,omitempty"`
}

// LatencyConfig configures how latency affects reputation scoring.
// Latency is measured from both health checks (background) and client requests.
//
// IMPORTANT: These are global defaults. Different services have different latency profiles:
//   - EVM: 50-200ms typical
//   - LLM: 2-30s typical
//
// Use ServiceLatencyProfiles to define per-service-type thresholds,
// or configure latency thresholds per-service in health_checks config.
type LatencyConfig struct {
	// Enabled enables latency-aware scoring. Default: true
	Enabled bool `yaml:"enabled,omitempty"`

	// FastThreshold is the maximum latency for "fast" responses.
	// Fast responses get a bonus multiplier on success signals.
	// Default: 100ms (appropriate for blockchain services)
	FastThreshold time.Duration `yaml:"fast_threshold,omitempty"`

	// NormalThreshold is the maximum latency for "normal" responses.
	// Normal responses get standard success impact.
	// Default: 500ms
	NormalThreshold time.Duration `yaml:"normal_threshold,omitempty"`

	// SlowThreshold is the maximum latency for "slow" responses.
	// Slow responses get reduced success impact.
	// Default: 1000ms (1 second)
	SlowThreshold time.Duration `yaml:"slow_threshold,omitempty"`

	// PenaltyThreshold triggers a slow_response penalty signal.
	// Responses slower than this get a reputation penalty even if successful.
	// Default: 2000ms (2 seconds)
	PenaltyThreshold time.Duration `yaml:"penalty_threshold,omitempty"`

	// SevereThreshold triggers a very_slow_response penalty signal.
	// Responses slower than this get a larger reputation penalty.
	// Default: 5000ms (5 seconds)
	SevereThreshold time.Duration `yaml:"severe_threshold,omitempty"`

	// FastBonus is the multiplier for success impact when response is fast.
	// Default: 2.0 (fast success = +2 instead of +1)
	FastBonus float64 `yaml:"fast_bonus,omitempty"`

	// SlowPenalty is the multiplier for success impact when response is slow.
	// Default: 0.5 (slow success = +0.5 instead of +1)
	SlowPenalty float64 `yaml:"slow_penalty,omitempty"`

	// VerySlowPenalty is the multiplier for success when response is very slow.
	// Default: 0.0 (very slow success = +0, no reputation gain)
	VerySlowPenalty float64 `yaml:"very_slow_penalty,omitempty"`

	// ServiceProfiles defines latency thresholds for different service types.
	// Services can reference a profile by name, or define inline thresholds.
	// If a service doesn't match any profile, global defaults are used.
	ServiceProfiles map[string]LatencyProfile `yaml:"service_profiles,omitempty"`
}

// LatencyProfile defines latency thresholds for a category of services.
// This allows different service types (blockchain, LLM, etc.) to have
// appropriate latency expectations.
type LatencyProfile struct {
	// Name is the profile identifier (e.g., "evm", "llm", "cosmos")
	Name string `yaml:"name,omitempty"`

	// Description explains what services this profile is for
	Description string `yaml:"description,omitempty"`

	// FastThreshold - responses faster than this get a bonus
	FastThreshold time.Duration `yaml:"fast_threshold"`

	// NormalThreshold - responses faster than this are considered normal
	NormalThreshold time.Duration `yaml:"normal_threshold"`

	// SlowThreshold - responses faster than this are slow but acceptable
	SlowThreshold time.Duration `yaml:"slow_threshold"`

	// PenaltyThreshold - responses slower than this get a penalty signal
	PenaltyThreshold time.Duration `yaml:"penalty_threshold"`

	// SevereThreshold - responses slower than this get a severe penalty
	SevereThreshold time.Duration `yaml:"severe_threshold"`
}

// GetProfileForService returns the latency profile for a service.
// Lookup order:
//  1. ServiceProfiles[serviceID] - exact service match
//  2. ServiceProfiles[profileName] - profile name match (e.g., "evm")
//  3. Built-in defaults - DefaultLatencyProfiles()
//  4. Global defaults - LatencyConfig thresholds
//
// The profileName parameter is optional - if empty, only serviceID is checked.
func (c *LatencyConfig) GetProfileForService(serviceID, profileName string) LatencyProfile {
	// 1. Check for exact service ID match in config
	if profile, ok := c.ServiceProfiles[serviceID]; ok {
		return profile
	}

	// 2. Check for profile name match in config
	if profileName != "" {
		if profile, ok := c.ServiceProfiles[profileName]; ok {
			return profile
		}
	}

	// 3. Check built-in defaults for profile name
	if profileName != "" {
		defaults := DefaultLatencyProfiles()
		if profile, ok := defaults[profileName]; ok {
			return profile
		}
	}

	// 4. Return global defaults from config
	return LatencyProfile{
		Name:             "default",
		Description:      "Global default thresholds",
		FastThreshold:    c.FastThreshold,
		NormalThreshold:  c.NormalThreshold,
		SlowThreshold:    c.SlowThreshold,
		PenaltyThreshold: c.PenaltyThreshold,
		SevereThreshold:  c.SevereThreshold,
	}
}

// ToLatencyConfig converts a LatencyProfile to a LatencyConfig for use in calculations.
// This allows using the profile thresholds with the CalculateLatencyAwareImpact method.
func (p LatencyProfile) ToLatencyConfig(baseConfig LatencyConfig) LatencyConfig {
	return LatencyConfig{
		Enabled:          baseConfig.Enabled,
		FastThreshold:    p.FastThreshold,
		NormalThreshold:  p.NormalThreshold,
		SlowThreshold:    p.SlowThreshold,
		PenaltyThreshold: p.PenaltyThreshold,
		SevereThreshold:  p.SevereThreshold,
		FastBonus:        baseConfig.FastBonus,
		SlowPenalty:      baseConfig.SlowPenalty,
		VerySlowPenalty:  baseConfig.VerySlowPenalty,
	}
}

// ProbationConfig configures the probation system for endpoint recovery.
// Probation gives low-scoring endpoints a small percentage of traffic
// to allow them to recover via successful requests.
type ProbationConfig struct {
	// Enabled enables/disables the probation system.
	Enabled bool `yaml:"enabled,omitempty"`
	// Threshold is the score below which endpoints enter probation.
	Threshold float64 `yaml:"threshold,omitempty"`
	// TrafficPercent is the percentage of traffic sent to probation endpoints.
	TrafficPercent float64 `yaml:"traffic_percent,omitempty"`
	// RecoveryMultiplier multiplies the score increase for successful requests from probation.
	RecoveryMultiplier float64 `yaml:"recovery_multiplier,omitempty"`
}

// ServiceConfig holds service-specific reputation configuration overrides.
type ServiceConfig struct {
	// KeyGranularity overrides the global key granularity for this service.
	// Options: "per-endpoint", "per-domain", "per-supplier"
	KeyGranularity string `yaml:"key_granularity"`
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

	// PoolSize is the maximum number of socket connections.
	// Default: 10
	PoolSize int `yaml:"pool_size"`

	// DialTimeout is the timeout for establishing new connections.
	// Default: 5s
	DialTimeout time.Duration `yaml:"dial_timeout"`

	// ReadTimeout is the timeout for socket reads.
	// Default: 3s
	ReadTimeout time.Duration `yaml:"read_timeout"`

	// WriteTimeout is the timeout for socket writes.
	// Default: 3s
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

// DefaultRedisConfig returns a RedisConfig with sensible defaults.
func DefaultRedisConfig() RedisConfig {
	return RedisConfig{
		Address:      "localhost:6379",
		Password:     "",
		DB:           0,
		KeyPrefix:    "path:reputation:",
		PoolSize:     10,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	}
}

// HydrateDefaults fills in zero values with defaults.
func (c *RedisConfig) HydrateDefaults() {
	defaults := DefaultRedisConfig()
	if c.Address == "" {
		c.Address = defaults.Address
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = defaults.KeyPrefix
	}
	if c.PoolSize == 0 {
		c.PoolSize = defaults.PoolSize
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = defaults.DialTimeout
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = defaults.ReadTimeout
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = defaults.WriteTimeout
	}
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

// Latency defaults for reputation scoring.
const (
	// DefaultLatencyFastThreshold is the max latency for "fast" responses.
	DefaultLatencyFastThreshold = 100 * time.Millisecond

	// DefaultLatencyNormalThreshold is the max latency for "normal" responses.
	DefaultLatencyNormalThreshold = 500 * time.Millisecond

	// DefaultLatencySlowThreshold is the max latency for "slow" responses.
	DefaultLatencySlowThreshold = 1000 * time.Millisecond

	// DefaultLatencyPenaltyThreshold triggers slow_response penalty.
	DefaultLatencyPenaltyThreshold = 2000 * time.Millisecond

	// DefaultLatencySevereThreshold triggers very_slow_response penalty.
	DefaultLatencySevereThreshold = 5000 * time.Millisecond

	// DefaultFastBonus is the multiplier for success when response is fast.
	DefaultFastBonus = 2.0

	// DefaultSlowPenalty is the multiplier for success when response is slow.
	DefaultSlowPenalty = 0.5

	// DefaultVerySlowPenalty is the multiplier for success when response is very slow.
	DefaultVerySlowPenalty = 0.0
)

// Well-known latency profile names.
const (
	LatencyProfileEVM     = "evm"
	LatencyProfileCosmos  = "cosmos"
	LatencyProfileSolana  = "solana"
	LatencyProfileLLM     = "llm"
	LatencyProfileGeneric = "generic"
)

// DefaultLatencyProfiles returns the built-in latency profiles for common service types.
// These can be overridden or extended in configuration.
func DefaultLatencyProfiles() map[string]LatencyProfile {
	return map[string]LatencyProfile{
		LatencyProfileEVM: {
			Name:             LatencyProfileEVM,
			Description:      "EVM-compatible blockchains (Ethereum, Base, Polygon, etc.)",
			FastThreshold:    50 * time.Millisecond,
			NormalThreshold:  200 * time.Millisecond,
			SlowThreshold:    500 * time.Millisecond,
			PenaltyThreshold: 1000 * time.Millisecond,
			SevereThreshold:  3000 * time.Millisecond,
		},
		LatencyProfileCosmos: {
			Name:             LatencyProfileCosmos,
			Description:      "Cosmos SDK chains (CosmosHub, Osmosis, etc.)",
			FastThreshold:    100 * time.Millisecond,
			NormalThreshold:  500 * time.Millisecond,
			SlowThreshold:    1000 * time.Millisecond,
			PenaltyThreshold: 2000 * time.Millisecond,
			SevereThreshold:  5000 * time.Millisecond,
		},
		LatencyProfileSolana: {
			Name:             LatencyProfileSolana,
			Description:      "Solana blockchain",
			FastThreshold:    100 * time.Millisecond,
			NormalThreshold:  300 * time.Millisecond,
			SlowThreshold:    800 * time.Millisecond,
			PenaltyThreshold: 1500 * time.Millisecond,
			SevereThreshold:  4000 * time.Millisecond,
		},
		LatencyProfileLLM: {
			Name:             LatencyProfileLLM,
			Description:      "LLM inference services (slow by nature)",
			FastThreshold:    2 * time.Second,
			NormalThreshold:  10 * time.Second,
			SlowThreshold:    30 * time.Second,
			PenaltyThreshold: 60 * time.Second,
			SevereThreshold:  120 * time.Second,
		},
		LatencyProfileGeneric: {
			Name:             LatencyProfileGeneric,
			Description:      "Generic/unknown services (conservative defaults)",
			FastThreshold:    500 * time.Millisecond,
			NormalThreshold:  2 * time.Second,
			SlowThreshold:    5 * time.Second,
			PenaltyThreshold: 10 * time.Second,
			SevereThreshold:  30 * time.Second,
		},
	}
}

// DefaultConfig returns a Config with sensible defaults.
// Reputation is MANDATORY and cannot be disabled - it is the unified QoS system.
func DefaultConfig() Config {
	return Config{
		Enabled:         true, // MANDATORY - reputation cannot be disabled (setting false is ignored)
		InitialScore:    InitialScore,
		MinThreshold:    DefaultMinThreshold,
		RecoveryTimeout: DefaultRecoveryTimeout,
		StorageType:     "memory",
		KeyGranularity:  KeyGranularityEndpoint,
		SyncConfig:      DefaultSyncConfig(),
		Latency:         DefaultLatencyConfig(),
	}
}

// DefaultLatencyConfig returns a LatencyConfig with sensible defaults.
func DefaultLatencyConfig() LatencyConfig {
	return LatencyConfig{
		Enabled:          true,
		FastThreshold:    DefaultLatencyFastThreshold,
		NormalThreshold:  DefaultLatencyNormalThreshold,
		SlowThreshold:    DefaultLatencySlowThreshold,
		PenaltyThreshold: DefaultLatencyPenaltyThreshold,
		SevereThreshold:  DefaultLatencySevereThreshold,
		FastBonus:        DefaultFastBonus,
		SlowPenalty:      DefaultSlowPenalty,
		VerySlowPenalty:  DefaultVerySlowPenalty,
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
// This is only called when reputation is enabled.
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
	if c.KeyGranularity == "" {
		c.KeyGranularity = KeyGranularityEndpoint
	}
	c.SyncConfig.HydrateDefaults()
	c.Latency.HydrateDefaults()
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

// HydrateDefaults fills in zero values with defaults for latency configuration.
func (l *LatencyConfig) HydrateDefaults() {
	// Default to enabled if not explicitly set
	// Note: Enabled defaults to true unless explicitly set to false
	if l.FastThreshold == 0 {
		l.FastThreshold = DefaultLatencyFastThreshold
	}
	if l.NormalThreshold == 0 {
		l.NormalThreshold = DefaultLatencyNormalThreshold
	}
	if l.SlowThreshold == 0 {
		l.SlowThreshold = DefaultLatencySlowThreshold
	}
	if l.PenaltyThreshold == 0 {
		l.PenaltyThreshold = DefaultLatencyPenaltyThreshold
	}
	if l.SevereThreshold == 0 {
		l.SevereThreshold = DefaultLatencySevereThreshold
	}
	if l.FastBonus == 0 {
		l.FastBonus = DefaultFastBonus
	}
	if l.SlowPenalty == 0 {
		l.SlowPenalty = DefaultSlowPenalty
	}
	// VerySlowPenalty defaults to 0, which is intentional (no bonus for very slow)
}

// Validate checks that the configuration values are valid.
// Returns an error if any values are out of bounds or inconsistent.
func (c *Config) Validate() error {
	// Check bounds first
	if c.InitialScore < MinScore || c.InitialScore > MaxScore {
		return fmt.Errorf("initial_score (%.1f) must be between %.1f and %.1f", c.InitialScore, MinScore, MaxScore)
	}
	if c.MinThreshold < MinScore || c.MinThreshold > MaxScore {
		return fmt.Errorf("min_threshold (%.1f) must be between %.1f and %.1f", c.MinThreshold, MinScore, MaxScore)
	}
	// Then check relationship between values
	if c.InitialScore < c.MinThreshold {
		return fmt.Errorf("initial_score (%.1f) must be >= min_threshold (%.1f)", c.InitialScore, c.MinThreshold)
	}
	if c.RecoveryTimeout < 0 {
		return fmt.Errorf("recovery_timeout must be non-negative")
	}
	return nil
}

// RecoveryConflictResult contains the result of recovery configuration validation.
type RecoveryConflictResult struct {
	// HasConflict indicates if there's a conflict between recovery mechanisms.
	HasConflict bool
	// HasNoRecovery indicates if no recovery mechanism is configured at all.
	HasNoRecovery bool
	// OriginalRecoveryTimeout is the recovery_timeout value before resolution.
	OriginalRecoveryTimeout time.Duration
	// Message contains a description of the issue found.
	Message string
}

// ValidateRecoveryConfig checks for conflicts between time-based (recovery_timeout)
// and signal-based recovery (probation, health_checks).
//
// Recovery mechanisms:
//   - recovery_timeout: Time-based. Resets score after X time with no signals.
//   - probation: Signal-based. Gives low-scoring endpoints traffic to recover.
//   - health_checks: Signal-based. Probes endpoints to generate recovery signals.
//
// When signal-based recovery is enabled, time-based recovery should be disabled
// because it can prematurely promote endpoints that are still failing.
//
// Returns a RecoveryConflictResult with:
//   - HasConflict: true if both time-based and signal-based recovery are enabled
//   - HasNoRecovery: true if no recovery mechanism is configured
//   - Message: description of the issue
func (c *Config) ValidateRecoveryConfig(healthChecksEnabled bool) RecoveryConflictResult {
	probationEnabled := c.TieredSelection.Probation.Enabled
	hasSignalBasedRecovery := healthChecksEnabled || probationEnabled
	hasTimeBasedRecovery := c.RecoveryTimeout > 0

	result := RecoveryConflictResult{
		OriginalRecoveryTimeout: c.RecoveryTimeout,
	}

	if hasSignalBasedRecovery && hasTimeBasedRecovery {
		result.HasConflict = true
		result.Message = fmt.Sprintf(
			"recovery_timeout (%v) is ignored when probation (%v) or health_checks (%v) are enabled. "+
				"Signal-based recovery takes precedence. Set recovery_timeout: 0 to suppress this warning.",
			c.RecoveryTimeout, probationEnabled, healthChecksEnabled,
		)
		return result
	}

	if !hasSignalBasedRecovery && !hasTimeBasedRecovery {
		result.HasNoRecovery = true
		result.Message = "No recovery mechanism configured. Endpoints below min_threshold will NEVER recover. " +
			"Enable probation, health_checks, or set recovery_timeout > 0."
		return result
	}

	return result
}

// ResolveRecoveryConflict disables recovery_timeout if signal-based recovery is active.
// This should be called after ValidateRecoveryConfig to apply the resolution.
func (c *Config) ResolveRecoveryConflict(healthChecksEnabled bool) {
	probationEnabled := c.TieredSelection.Probation.Enabled
	hasSignalBasedRecovery := healthChecksEnabled || probationEnabled

	if hasSignalBasedRecovery && c.RecoveryTimeout > 0 {
		c.RecoveryTimeout = 0
	}
}
