// Package gateway provides unified service configuration types.
//
// These types are defined in the gateway package to avoid import cycles,
// since protocol/shannon imports gateway.
package gateway

import (
	"fmt"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// ServiceType defines the QoS type for a service.
// This determines which QoS implementation handles the service.
type ServiceType string

const (
	// ServiceTypeEVM is for EVM-compatible blockchains (Ethereum, Base, Polygon, etc.)
	ServiceTypeEVM ServiceType = "evm"
	// ServiceTypeSolana is for Solana blockchain
	ServiceTypeSolana ServiceType = "solana"
	// ServiceTypeCosmos is for Cosmos SDK chains
	ServiceTypeCosmos ServiceType = "cosmos"
	// ServiceTypeGeneric is for generic JSON-RPC services
	ServiceTypeGeneric ServiceType = "generic"
	// ServiceTypePassthrough is for services with no QoS processing (pass-through)
	ServiceTypePassthrough ServiceType = "passthrough"
)

// ConcurrencyConfig defines global concurrency limits for request processing.
// These limits protect against resource exhaustion from batch requests and parallel relays.
type ConcurrencyConfig struct {
	// MaxParallelEndpoints is the maximum number of endpoints to query in parallel per request.
	// Higher values reduce latency but increase load. Range: 1-10. Default: 1.
	MaxParallelEndpoints int `yaml:"max_parallel_endpoints,omitempty"`

	// MaxConcurrentRelays is the global limit on concurrent relay goroutines across all requests.
	// Prevents resource exhaustion from too many simultaneous relays. Range: 100-10000. Default: 5500.
	MaxConcurrentRelays int `yaml:"max_concurrent_relays,omitempty"`

	// MaxBatchPayloads is the maximum number of payloads allowed in a batch request.
	// Must be less than or equal to MaxConcurrentRelays. Range: 1-10000. Default: 5500.
	MaxBatchPayloads int `yaml:"max_batch_payloads,omitempty"`
}

// Validate validates the ConcurrencyConfig.
func (cc *ConcurrencyConfig) Validate(logger polylog.Logger) error {
	if cc.MaxParallelEndpoints < 1 || cc.MaxParallelEndpoints > 10 {
		return fmt.Errorf("concurrency_config.max_parallel_endpoints must be between 1 and 10 (got %d)", cc.MaxParallelEndpoints)
	}

	// 🚨 BIG WARNING: Parallel endpoints are experimental and increase token burn
	if cc.MaxParallelEndpoints > 1 && logger != nil {
		logger.Warn().
			Int("max_parallel_endpoints", cc.MaxParallelEndpoints).
			Msg("🚨 WARNING: max_parallel_endpoints > 1 is EXPERIMENTAL and will multiply token burn by the number of parallel endpoints! " +
				"Each request will be sent to multiple endpoints simultaneously. " +
				"Monitor your token usage and endpoint metrics closely. " +
				"Recommended: Start with max_parallel_endpoints=1 and test thoroughly before increasing.")
	}

	if cc.MaxConcurrentRelays < 100 || cc.MaxConcurrentRelays > 10000 {
		return fmt.Errorf("concurrency_config.max_concurrent_relays must be between 100 and 10000 (got %d)", cc.MaxConcurrentRelays)
	}

	if cc.MaxBatchPayloads < 1 || cc.MaxBatchPayloads > 10000 {
		return fmt.Errorf("concurrency_config.max_batch_payloads must be between 1 and 10000 (got %d)", cc.MaxBatchPayloads)
	}

	if cc.MaxBatchPayloads > cc.MaxConcurrentRelays {
		return fmt.Errorf("concurrency_config.max_batch_payloads (%d) cannot exceed max_concurrent_relays (%d)", cc.MaxBatchPayloads, cc.MaxConcurrentRelays)
	}

	return nil
}

// LatencyProfileConfig defines latency thresholds for a category of services.
// These profiles are defined once in latency_profiles and referenced by name in services.
type LatencyProfileConfig struct {
	// FastThreshold is the maximum latency for "fast" responses.
	FastThreshold time.Duration `yaml:"fast_threshold"`
	// NormalThreshold is the maximum latency for "normal" responses.
	NormalThreshold time.Duration `yaml:"normal_threshold"`
	// SlowThreshold is the maximum latency for "slow" responses.
	SlowThreshold time.Duration `yaml:"slow_threshold"`
	// PenaltyThreshold triggers a slow_response penalty signal.
	PenaltyThreshold time.Duration `yaml:"penalty_threshold"`
	// SevereThreshold triggers a very_slow_response penalty signal.
	SevereThreshold time.Duration `yaml:"severe_threshold"`
	// FastBonus is the multiplier for success impact when response is fast.
	FastBonus float64 `yaml:"fast_bonus,omitempty"`
	// SlowPenalty is the multiplier for success impact when response is slow.
	SlowPenalty float64 `yaml:"slow_penalty,omitempty"`
	// VerySlowPenalty is the multiplier for success when response is very slow.
	VerySlowPenalty float64 `yaml:"very_slow_penalty,omitempty"`
}

// ServiceReputationConfig holds per-service reputation configuration.
type ServiceReputationConfig struct {
	Enabled         *bool          `yaml:"enabled,omitempty"`
	InitialScore    *float64       `yaml:"initial_score,omitempty"`
	MinThreshold    *float64       `yaml:"min_threshold,omitempty"`
	KeyGranularity  string         `yaml:"key_granularity,omitempty"`
	RecoveryTimeout *time.Duration `yaml:"recovery_timeout,omitempty"`
}

// ServiceLatencyConfig holds per-service latency configuration.
type ServiceLatencyConfig struct {
	Enabled       *bool   `yaml:"enabled,omitempty"`
	TargetMs      int     `yaml:"target_ms,omitempty"`
	PenaltyWeight float64 `yaml:"penalty_weight,omitempty"`
}

// ServiceTieredSelectionConfig holds per-service tiered selection configuration.
type ServiceTieredSelectionConfig struct {
	Enabled        *bool    `yaml:"enabled,omitempty"`
	Tier1Threshold *float64 `yaml:"tier1_threshold,omitempty"`
	Tier2Threshold *float64 `yaml:"tier2_threshold,omitempty"`
}

// ServiceProbationConfig holds per-service probation configuration.
type ServiceProbationConfig struct {
	Enabled            *bool    `yaml:"enabled,omitempty"`
	Threshold          *float64 `yaml:"threshold,omitempty"`
	TrafficPercent     *float64 `yaml:"traffic_percent,omitempty"`
	RecoveryMultiplier *float64 `yaml:"recovery_multiplier,omitempty"`
}

// ServiceRetryConfig holds per-service retry configuration.
type ServiceRetryConfig struct {
	Enabled           *bool          `yaml:"enabled,omitempty"`
	MaxRetries        *int           `yaml:"max_retries,omitempty"`
	RetryOn5xx        *bool          `yaml:"retry_on_5xx,omitempty"`
	RetryOnTimeout    *bool          `yaml:"retry_on_timeout,omitempty"`
	RetryOnConnection *bool          `yaml:"retry_on_connection,omitempty"`
	MaxRetryLatency   *time.Duration `yaml:"max_retry_latency,omitempty"` // Only retry if failed request took less than this duration
}

// ServiceObservationConfig holds per-service observation pipeline configuration.
// Note: worker_count and queue_size are GLOBAL only (set in gateway_config.observation_pipeline).
// Per-service config only supports sample_rate override.
type ServiceObservationConfig struct {
	Enabled    *bool    `yaml:"enabled,omitempty"`
	SampleRate *float64 `yaml:"sample_rate,omitempty"`
}

// ServiceConcurrencyConfig holds per-service concurrency configuration.
// Note: max_concurrent_relays is GLOBAL only (set in gateway_config.concurrency_config).
// Per-service config supports max_parallel_endpoints and max_batch_payloads overrides.
type ServiceConcurrencyConfig struct {
	MaxParallelEndpoints *int `yaml:"max_parallel_endpoints,omitempty"`
	MaxBatchPayloads     *int `yaml:"max_batch_payloads,omitempty"`
}

// ServiceHealthCheckOverride holds per-service health check configuration overrides.
type ServiceHealthCheckOverride struct {
	Enabled       *bool                 `yaml:"enabled,omitempty"`
	Interval      time.Duration         `yaml:"interval,omitempty"`
	SyncAllowance *int                  `yaml:"sync_allowance,omitempty"`
	External      *ExternalConfigSource `yaml:"external,omitempty"`
	Local         []HealthCheckConfig   `yaml:"local,omitempty"`
}

// ServiceFallbackConfig holds per-service fallback endpoint configuration.
type ServiceFallbackConfig struct {
	Enabled        bool                `yaml:"enabled,omitempty"`
	SendAllTraffic bool                `yaml:"send_all_traffic,omitempty"`
	Endpoints      []map[string]string `yaml:"endpoints,omitempty"`
}

// ServiceDefaults contains default settings inherited by all services.
type ServiceDefaults struct {
	Type                ServiceType                  `yaml:"type,omitempty"`
	RPCTypes            []string                     `yaml:"rpc_types,omitempty"`
	LatencyProfile      string                       `yaml:"latency_profile,omitempty"`
	ReputationConfig    ServiceReputationConfig      `yaml:"reputation_config,omitempty"`
	Latency             ServiceLatencyConfig         `yaml:"latency,omitempty"`
	TieredSelection     ServiceTieredSelectionConfig `yaml:"tiered_selection,omitempty"`
	Probation           ServiceProbationConfig       `yaml:"probation,omitempty"`
	RetryConfig         ServiceRetryConfig           `yaml:"retry_config,omitempty"`
	ObservationPipeline ServiceObservationConfig     `yaml:"observation_pipeline,omitempty"`
	ConcurrencyConfig   ServiceConcurrencyConfig     `yaml:"concurrency_config,omitempty"`
	ActiveHealthChecks  ServiceHealthCheckOverride   `yaml:"active_health_checks,omitempty"`
}

// ServiceConfig defines configuration for a single service.
type ServiceConfig struct {
	ID                  protocol.ServiceID            `yaml:"id"`
	Type                ServiceType                   `yaml:"type,omitempty"`
	RPCTypes            []string                      `yaml:"rpc_types,omitempty"`
	RPCTypeFallbacks    map[string]string             `yaml:"rpc_type_fallbacks,omitempty"`
	LatencyProfile      string                        `yaml:"latency_profile,omitempty"`
	ReputationConfig    *ServiceReputationConfig      `yaml:"reputation_config,omitempty"`
	Latency             *ServiceLatencyConfig         `yaml:"latency,omitempty"`
	TieredSelection     *ServiceTieredSelectionConfig `yaml:"tiered_selection,omitempty"`
	Probation           *ServiceProbationConfig       `yaml:"probation,omitempty"`
	RetryConfig         *ServiceRetryConfig           `yaml:"retry_config,omitempty"`
	ObservationPipeline *ServiceObservationConfig     `yaml:"observation_pipeline,omitempty"`
	ConcurrencyConfig   *ServiceConcurrencyConfig     `yaml:"concurrency_config,omitempty"`
	Fallback            *ServiceFallbackConfig        `yaml:"fallback,omitempty"`
	HealthChecks        *ServiceHealthCheckOverride   `yaml:"health_checks,omitempty"`
}

// UnifiedServicesConfig is the top-level configuration for the unified service system.
type UnifiedServicesConfig struct {
	LatencyProfiles map[string]LatencyProfileConfig `yaml:"latency_profiles,omitempty"`
	Defaults        ServiceDefaults                 `yaml:"defaults,omitempty"`
	Services        []ServiceConfig                 `yaml:"services,omitempty"`
}

// Validate validates the UnifiedServicesConfig.
func (c *UnifiedServicesConfig) Validate() error {
	seenIDs := make(map[protocol.ServiceID]struct{})
	for i, svc := range c.Services {
		if svc.ID == "" {
			return fmt.Errorf("services[%d]: id is required", i)
		}
		if _, exists := seenIDs[svc.ID]; exists {
			return fmt.Errorf("services[%d]: duplicate service id '%s'", i, svc.ID)
		}
		seenIDs[svc.ID] = struct{}{}

		if svc.Type != "" {
			if err := ValidateServiceType(svc.Type); err != nil {
				return fmt.Errorf("services[%d] (%s): %w", i, svc.ID, err)
			}
		}

		if svc.LatencyProfile != "" {
			if _, ok := c.LatencyProfiles[svc.LatencyProfile]; !ok {
				if !IsBuiltInLatencyProfile(svc.LatencyProfile) {
					return fmt.Errorf("services[%d] (%s): unknown latency_profile '%s'", i, svc.ID, svc.LatencyProfile)
				}
			}
		}
	}

	if c.Defaults.Type != "" {
		if err := ValidateServiceType(c.Defaults.Type); err != nil {
			return fmt.Errorf("defaults.type: %w", err)
		}
	}

	if c.Defaults.LatencyProfile != "" {
		if _, ok := c.LatencyProfiles[c.Defaults.LatencyProfile]; !ok {
			if !IsBuiltInLatencyProfile(c.Defaults.LatencyProfile) {
				return fmt.Errorf("defaults.latency_profile: unknown profile '%s'", c.Defaults.LatencyProfile)
			}
		}
	}

	return nil
}

// ValidateServiceType validates that a service type is one of the supported types.
func ValidateServiceType(t ServiceType) error {
	switch t {
	case ServiceTypeEVM, ServiceTypeSolana, ServiceTypeCosmos, ServiceTypeGeneric, ServiceTypePassthrough:
		return nil
	default:
		return fmt.Errorf("invalid service type '%s' (must be evm, solana, cosmos, generic, or passthrough)", t)
	}
}

// IsBuiltInLatencyProfile returns true if the profile name is a built-in profile.
func IsBuiltInLatencyProfile(name string) bool {
	switch name {
	case reputation.LatencyProfileEVM,
		reputation.LatencyProfileSolana,
		reputation.LatencyProfileCosmos,
		reputation.LatencyProfileLLM,
		reputation.LatencyProfileGeneric:
		return true
	default:
		return false
	}
}

// HydrateDefaults applies default values to UnifiedServicesConfig.
func (c *UnifiedServicesConfig) HydrateDefaults() {
	if c.Defaults.Type == "" {
		c.Defaults.Type = ServiceTypePassthrough
	}
	if len(c.Defaults.RPCTypes) == 0 {
		c.Defaults.RPCTypes = []string{"json_rpc"}
	}
	if c.Defaults.LatencyProfile == "" {
		c.Defaults.LatencyProfile = "standard"
	}

	// Hydrate default reputation config
	if c.Defaults.ReputationConfig.Enabled == nil {
		enabled := true
		c.Defaults.ReputationConfig.Enabled = &enabled
	}
	if c.Defaults.ReputationConfig.InitialScore == nil {
		initialScore := reputation.InitialScore
		c.Defaults.ReputationConfig.InitialScore = &initialScore
	}
	if c.Defaults.ReputationConfig.MinThreshold == nil {
		minThreshold := reputation.DefaultMinThreshold
		c.Defaults.ReputationConfig.MinThreshold = &minThreshold
	}
	if c.Defaults.ReputationConfig.KeyGranularity == "" {
		c.Defaults.ReputationConfig.KeyGranularity = reputation.KeyGranularityEndpoint
	}

	// Hydrate default latency config
	if c.Defaults.Latency.Enabled == nil {
		enabled := true
		c.Defaults.Latency.Enabled = &enabled
	}

	// Hydrate default tiered selection
	if c.Defaults.TieredSelection.Enabled == nil {
		enabled := true
		c.Defaults.TieredSelection.Enabled = &enabled
	}
	if c.Defaults.TieredSelection.Tier1Threshold == nil {
		tier1 := 70.0
		c.Defaults.TieredSelection.Tier1Threshold = &tier1
	}
	if c.Defaults.TieredSelection.Tier2Threshold == nil {
		tier2 := 50.0
		c.Defaults.TieredSelection.Tier2Threshold = &tier2
	}

	// Hydrate default probation
	if c.Defaults.Probation.Enabled == nil {
		enabled := true
		c.Defaults.Probation.Enabled = &enabled
	}
	if c.Defaults.Probation.Threshold == nil {
		threshold := 10.0
		c.Defaults.Probation.Threshold = &threshold
	}
	if c.Defaults.Probation.TrafficPercent == nil {
		traffic := 10.0
		c.Defaults.Probation.TrafficPercent = &traffic
	}
	if c.Defaults.Probation.RecoveryMultiplier == nil {
		multiplier := 2.0
		c.Defaults.Probation.RecoveryMultiplier = &multiplier
	}

	// Hydrate default retry config
	if c.Defaults.RetryConfig.Enabled == nil {
		enabled := true
		c.Defaults.RetryConfig.Enabled = &enabled
	}
	if c.Defaults.RetryConfig.MaxRetries == nil {
		maxRetries := 1
		c.Defaults.RetryConfig.MaxRetries = &maxRetries
	}
	if c.Defaults.RetryConfig.RetryOn5xx == nil {
		retry5xx := true
		c.Defaults.RetryConfig.RetryOn5xx = &retry5xx
	}
	if c.Defaults.RetryConfig.RetryOnTimeout == nil {
		retryTimeout := true
		c.Defaults.RetryConfig.RetryOnTimeout = &retryTimeout
	}
	if c.Defaults.RetryConfig.RetryOnConnection == nil {
		retryConnection := true
		c.Defaults.RetryConfig.RetryOnConnection = &retryConnection
	}
	if c.Defaults.RetryConfig.MaxRetryLatency == nil {
		maxRetryLatency := 500 * time.Millisecond
		c.Defaults.RetryConfig.MaxRetryLatency = &maxRetryLatency
	}

	// Hydrate default observation pipeline
	if c.Defaults.ObservationPipeline.Enabled == nil {
		enabled := true
		c.Defaults.ObservationPipeline.Enabled = &enabled
	}
	if c.Defaults.ObservationPipeline.SampleRate == nil {
		sampleRate := DefaultObservationPipelineSampleRate
		c.Defaults.ObservationPipeline.SampleRate = &sampleRate
	}
	// Note: worker_count and queue_size are GLOBAL only (gateway_config.observation_pipeline)
	// Per-service config only supports sample_rate override

	// Hydrate default health check config
	if c.Defaults.ActiveHealthChecks.Enabled == nil {
		enabled := true
		c.Defaults.ActiveHealthChecks.Enabled = &enabled
	}
	if c.Defaults.ActiveHealthChecks.Interval == 0 {
		c.Defaults.ActiveHealthChecks.Interval = DefaultHealthCheckInterval
	}

	// Add "standard" latency profile if not defined
	if c.LatencyProfiles == nil {
		c.LatencyProfiles = make(map[string]LatencyProfileConfig)
	}
	if _, exists := c.LatencyProfiles["standard"]; !exists {
		c.LatencyProfiles["standard"] = LatencyProfileConfig{
			FastThreshold:    500 * time.Millisecond,
			NormalThreshold:  2 * time.Second,
			SlowThreshold:    5 * time.Second,
			PenaltyThreshold: 10 * time.Second,
			SevereThreshold:  30 * time.Second,
			FastBonus:        reputation.DefaultFastBonus,
			SlowPenalty:      reputation.DefaultSlowPenalty,
			VerySlowPenalty:  reputation.DefaultVerySlowPenalty,
		}
	}

	// Hydrate user-defined latency profiles with default bonus values if not specified.
	// This ensures profiles defined in YAML with only thresholds get sensible bonus values.
	for name, profile := range c.LatencyProfiles {
		updated := false
		if profile.FastBonus == 0 {
			profile.FastBonus = reputation.DefaultFastBonus
			updated = true
		}
		if profile.SlowPenalty == 0 {
			profile.SlowPenalty = reputation.DefaultSlowPenalty
			updated = true
		}
		// VerySlowPenalty can legitimately be 0.0 (no bonus for very slow),
		// so we don't hydrate it - users must be explicit about penalties.
		if updated {
			c.LatencyProfiles[name] = profile
		}
	}
}

// GetServiceConfig returns the configuration for a specific service.
func (c *UnifiedServicesConfig) GetServiceConfig(serviceID protocol.ServiceID) *ServiceConfig {
	for i := range c.Services {
		if c.Services[i].ID == serviceID {
			return &c.Services[i]
		}
	}
	return nil
}

// GetServiceType returns the QoS type for a service.
func (c *UnifiedServicesConfig) GetServiceType(serviceID protocol.ServiceID) ServiceType {
	if svc := c.GetServiceConfig(serviceID); svc != nil {
		if svc.Type != "" {
			return svc.Type
		}
	}
	return c.Defaults.Type
}

// GetServiceRPCTypes returns the supported RPC types for a service.
func (c *UnifiedServicesConfig) GetServiceRPCTypes(serviceID protocol.ServiceID) []string {
	if svc := c.GetServiceConfig(serviceID); svc != nil {
		if len(svc.RPCTypes) > 0 {
			return svc.RPCTypes
		}
	}
	return c.Defaults.RPCTypes
}

// GetConfiguredServiceIDs returns a list of all configured service IDs.
func (c *UnifiedServicesConfig) GetConfiguredServiceIDs() []protocol.ServiceID {
	ids := make([]protocol.ServiceID, len(c.Services))
	for i, svc := range c.Services {
		ids[i] = svc.ID
	}
	return ids
}

// HasServices returns true if any services are configured.
func (c *UnifiedServicesConfig) HasServices() bool {
	return len(c.Services) > 0
}

// HasService checks if a specific service is configured
func (c *UnifiedServicesConfig) HasService(serviceID protocol.ServiceID) bool {
	return c.GetServiceConfig(serviceID) != nil
}

// GetLatencyProfile returns the latency profile configuration for a profile name.
func (c *UnifiedServicesConfig) GetLatencyProfile(name string) *LatencyProfileConfig {
	if profile, ok := c.LatencyProfiles[name]; ok {
		return &profile
	}

	// Fall back to built-in profiles
	builtIn := reputation.DefaultLatencyProfiles()
	if profile, ok := builtIn[name]; ok {
		return &LatencyProfileConfig{
			FastThreshold:    profile.FastThreshold,
			NormalThreshold:  profile.NormalThreshold,
			SlowThreshold:    profile.SlowThreshold,
			PenaltyThreshold: profile.PenaltyThreshold,
			SevereThreshold:  profile.SevereThreshold,
			FastBonus:        reputation.DefaultFastBonus,
			SlowPenalty:      reputation.DefaultSlowPenalty,
			VerySlowPenalty:  reputation.DefaultVerySlowPenalty,
		}
	}

	return nil
}

// GetMergedServiceConfig returns a service config with defaults merged in.
// Returns nil if the service is not configured.
func (c *UnifiedServicesConfig) GetMergedServiceConfig(serviceID protocol.ServiceID) *ServiceConfig {
	svc := c.GetServiceConfig(serviceID)
	if svc == nil {
		return nil
	}

	// Start with a copy of the service config
	merged := *svc

	// Apply defaults for missing fields
	if merged.Type == "" {
		merged.Type = c.Defaults.Type
	}
	if len(merged.RPCTypes) == 0 {
		merged.RPCTypes = c.Defaults.RPCTypes
	}
	if merged.LatencyProfile == "" {
		merged.LatencyProfile = c.Defaults.LatencyProfile
	}

	// Merge reputation config
	if merged.ReputationConfig == nil {
		// Copy defaults
		repCopy := c.Defaults.ReputationConfig
		merged.ReputationConfig = &repCopy
	} else {
		// Merge with defaults
		if merged.ReputationConfig.Enabled == nil {
			merged.ReputationConfig.Enabled = c.Defaults.ReputationConfig.Enabled
		}
		if merged.ReputationConfig.InitialScore == nil {
			merged.ReputationConfig.InitialScore = c.Defaults.ReputationConfig.InitialScore
		}
		if merged.ReputationConfig.MinThreshold == nil {
			merged.ReputationConfig.MinThreshold = c.Defaults.ReputationConfig.MinThreshold
		}
		if merged.ReputationConfig.KeyGranularity == "" {
			merged.ReputationConfig.KeyGranularity = c.Defaults.ReputationConfig.KeyGranularity
		}
		if merged.ReputationConfig.RecoveryTimeout == nil {
			merged.ReputationConfig.RecoveryTimeout = c.Defaults.ReputationConfig.RecoveryTimeout
		}
	}

	// Merge tiered selection config
	if merged.TieredSelection == nil {
		tierCopy := c.Defaults.TieredSelection
		merged.TieredSelection = &tierCopy
	} else {
		if merged.TieredSelection.Enabled == nil {
			merged.TieredSelection.Enabled = c.Defaults.TieredSelection.Enabled
		}
		if merged.TieredSelection.Tier1Threshold == nil {
			merged.TieredSelection.Tier1Threshold = c.Defaults.TieredSelection.Tier1Threshold
		}
		if merged.TieredSelection.Tier2Threshold == nil {
			merged.TieredSelection.Tier2Threshold = c.Defaults.TieredSelection.Tier2Threshold
		}
	}

	// Merge probation config (nested within tiered selection)
	if merged.Probation == nil {
		probationCopy := c.Defaults.Probation
		merged.Probation = &probationCopy
	} else {
		if merged.Probation.Enabled == nil {
			merged.Probation.Enabled = c.Defaults.Probation.Enabled
		}
		if merged.Probation.Threshold == nil {
			merged.Probation.Threshold = c.Defaults.Probation.Threshold
		}
		if merged.Probation.TrafficPercent == nil {
			merged.Probation.TrafficPercent = c.Defaults.Probation.TrafficPercent
		}
		if merged.Probation.RecoveryMultiplier == nil {
			merged.Probation.RecoveryMultiplier = c.Defaults.Probation.RecoveryMultiplier
		}
	}

	// Merge latency config
	if merged.Latency == nil {
		latencyCopy := c.Defaults.Latency
		merged.Latency = &latencyCopy
	} else {
		// Merge individual latency fields with defaults
		if merged.Latency.Enabled == nil {
			merged.Latency.Enabled = c.Defaults.Latency.Enabled
		}
		if merged.Latency.TargetMs == 0 && c.Defaults.Latency.TargetMs > 0 {
			merged.Latency.TargetMs = c.Defaults.Latency.TargetMs
		}
		if merged.Latency.PenaltyWeight == 0 && c.Defaults.Latency.PenaltyWeight > 0 {
			merged.Latency.PenaltyWeight = c.Defaults.Latency.PenaltyWeight
		}
	}

	// Merge retry config
	if merged.RetryConfig == nil {
		retryCopy := c.Defaults.RetryConfig
		merged.RetryConfig = &retryCopy
	} else {
		if merged.RetryConfig.Enabled == nil {
			merged.RetryConfig.Enabled = c.Defaults.RetryConfig.Enabled
		}
		if merged.RetryConfig.MaxRetries == nil {
			merged.RetryConfig.MaxRetries = c.Defaults.RetryConfig.MaxRetries
		}
		if merged.RetryConfig.RetryOn5xx == nil {
			merged.RetryConfig.RetryOn5xx = c.Defaults.RetryConfig.RetryOn5xx
		}
		if merged.RetryConfig.RetryOnTimeout == nil {
			merged.RetryConfig.RetryOnTimeout = c.Defaults.RetryConfig.RetryOnTimeout
		}
		if merged.RetryConfig.RetryOnConnection == nil {
			merged.RetryConfig.RetryOnConnection = c.Defaults.RetryConfig.RetryOnConnection
		}
		if merged.RetryConfig.MaxRetryLatency == nil {
			merged.RetryConfig.MaxRetryLatency = c.Defaults.RetryConfig.MaxRetryLatency
		}
	}

	// Merge observation pipeline config
	if merged.ObservationPipeline == nil {
		obsCopy := c.Defaults.ObservationPipeline
		merged.ObservationPipeline = &obsCopy
	} else {
		if merged.ObservationPipeline.Enabled == nil {
			merged.ObservationPipeline.Enabled = c.Defaults.ObservationPipeline.Enabled
		}
		if merged.ObservationPipeline.SampleRate == nil {
			merged.ObservationPipeline.SampleRate = c.Defaults.ObservationPipeline.SampleRate
		}
		// Note: worker_count and queue_size are GLOBAL only, not per-service
	}

	// Merge concurrency config
	if merged.ConcurrencyConfig == nil {
		concurrencyCopy := c.Defaults.ConcurrencyConfig
		merged.ConcurrencyConfig = &concurrencyCopy
	} else {
		if merged.ConcurrencyConfig.MaxParallelEndpoints == nil {
			merged.ConcurrencyConfig.MaxParallelEndpoints = c.Defaults.ConcurrencyConfig.MaxParallelEndpoints
		}
		if merged.ConcurrencyConfig.MaxBatchPayloads == nil {
			merged.ConcurrencyConfig.MaxBatchPayloads = c.Defaults.ConcurrencyConfig.MaxBatchPayloads
		}
		// Note: max_concurrent_relays is GLOBAL only, not per-service
	}

	// Merge health checks config (per-service HealthChecks inherits from defaults.ActiveHealthChecks)
	if merged.HealthChecks == nil {
		// Copy defaults from ActiveHealthChecks if no per-service override
		hcCopy := c.Defaults.ActiveHealthChecks
		merged.HealthChecks = &hcCopy
	} else {
		// Merge individual fields with defaults
		if merged.HealthChecks.Enabled == nil {
			merged.HealthChecks.Enabled = c.Defaults.ActiveHealthChecks.Enabled
		}
		if merged.HealthChecks.Interval == 0 && c.Defaults.ActiveHealthChecks.Interval > 0 {
			merged.HealthChecks.Interval = c.Defaults.ActiveHealthChecks.Interval
		}
		if merged.HealthChecks.SyncAllowance == nil {
			merged.HealthChecks.SyncAllowance = c.Defaults.ActiveHealthChecks.SyncAllowance
		}
		// External: per-service external URL inherits from defaults if not set
		if merged.HealthChecks.External == nil && c.Defaults.ActiveHealthChecks.External != nil {
			extCopy := *c.Defaults.ActiveHealthChecks.External
			merged.HealthChecks.External = &extCopy
		}
		// Note: Local checks are NOT merged - per-service local checks completely override defaults
		// This is intentional: if a service specifies its own local checks, they replace the defaults
	}

	// Note: Fallback is service-specific only, no merge with defaults needed
	// (ServiceDefaults doesn't have a Fallback field)

	return &merged
}

// GetSyncAllowanceForService returns the sync allowance for a service.
// It checks per-service config first, then falls back to global defaults.
// Returns DefaultSyncAllowance (5) if not configured.
func (c *UnifiedServicesConfig) GetSyncAllowanceForService(serviceID protocol.ServiceID) uint64 {
	// Check per-service config
	svc := c.GetServiceConfig(serviceID)
	if svc != nil && svc.HealthChecks != nil && svc.HealthChecks.SyncAllowance != nil {
		return uint64(*svc.HealthChecks.SyncAllowance)
	}

	// Check global defaults
	if c.Defaults.ActiveHealthChecks.SyncAllowance != nil && *c.Defaults.ActiveHealthChecks.SyncAllowance > 0 {
		return uint64(*c.Defaults.ActiveHealthChecks.SyncAllowance)
	}

	// Return default
	return DefaultSyncAllowance
}

// ParentConfigDefaults contains values from the parent config that serve as defaults.
// This struct is used to pass values from gateway_config to UnifiedServicesConfig,
// eliminating the need for a separate "defaults" section in YAML.
type ParentConfigDefaults struct {
	// TieredSelectionEnabled from reputation_config.tiered_selection.enabled
	TieredSelectionEnabled bool
	// Tier1Threshold from reputation_config.tiered_selection.tier1_threshold
	Tier1Threshold float64
	// Tier2Threshold from reputation_config.tiered_selection.tier2_threshold
	Tier2Threshold float64
	// ProbationEnabled from reputation_config.tiered_selection.probation.enabled
	ProbationEnabled bool
	// ProbationThreshold from reputation_config.tiered_selection.probation.threshold
	ProbationThreshold float64
	// ProbationTrafficPercent from reputation_config.tiered_selection.probation.traffic_percent
	ProbationTrafficPercent float64
	// ProbationRecoveryMultiplier from reputation_config.tiered_selection.probation.recovery_multiplier
	ProbationRecoveryMultiplier float64
	// RetryEnabled from retry_config.enabled
	RetryEnabled bool
	// MaxRetries from retry_config.max_retries
	MaxRetries int
	// RetryOn5xx from retry_config.retry_on_5xx
	RetryOn5xx bool
	// RetryOnTimeout from retry_config.retry_on_timeout
	RetryOnTimeout bool
	// RetryOnConnection from retry_config.retry_on_connection
	RetryOnConnection bool
	// MaxRetryLatency from retry_config.max_retry_latency
	MaxRetryLatency time.Duration
	// ObservationPipelineEnabled from observation_pipeline.enabled
	ObservationPipelineEnabled bool
	// SampleRate from observation_pipeline.sample_rate
	SampleRate float64
	// HealthChecksEnabled from active_health_checks.enabled
	HealthChecksEnabled bool
	// HealthCheckInterval from active_health_checks interval
	HealthCheckInterval time.Duration
	// SyncAllowance from active_health_checks.sync_allowance
	SyncAllowance int
}

// SetDefaultsFromParent populates the internal Defaults field from parent config values.
// This allows gateway_config top-level settings to serve as defaults for all services,
// eliminating the need for a separate "defaults" section in YAML.
//
// Call this method after loading the config to wire up defaults from:
//   - reputation_config.tiered_selection -> Defaults.TieredSelection
//   - reputation_config.tiered_selection.probation -> Defaults.Probation
//   - retry_config -> Defaults.RetryConfig
//   - observation_pipeline -> Defaults.ObservationPipeline
//   - active_health_checks -> Defaults.ActiveHealthChecks
func (c *UnifiedServicesConfig) SetDefaultsFromParent(parent ParentConfigDefaults) {
	// Set tiered selection defaults
	c.Defaults.TieredSelection.Enabled = &parent.TieredSelectionEnabled
	if parent.Tier1Threshold > 0 {
		c.Defaults.TieredSelection.Tier1Threshold = &parent.Tier1Threshold
	}
	if parent.Tier2Threshold > 0 {
		c.Defaults.TieredSelection.Tier2Threshold = &parent.Tier2Threshold
	}

	// Set probation defaults
	c.Defaults.Probation.Enabled = &parent.ProbationEnabled
	if parent.ProbationThreshold > 0 {
		c.Defaults.Probation.Threshold = &parent.ProbationThreshold
	}
	if parent.ProbationTrafficPercent > 0 {
		c.Defaults.Probation.TrafficPercent = &parent.ProbationTrafficPercent
	}
	if parent.ProbationRecoveryMultiplier > 0 {
		c.Defaults.Probation.RecoveryMultiplier = &parent.ProbationRecoveryMultiplier
	}

	// Set retry defaults
	c.Defaults.RetryConfig.Enabled = &parent.RetryEnabled
	if parent.MaxRetries > 0 {
		c.Defaults.RetryConfig.MaxRetries = &parent.MaxRetries
	}
	c.Defaults.RetryConfig.RetryOn5xx = &parent.RetryOn5xx
	c.Defaults.RetryConfig.RetryOnTimeout = &parent.RetryOnTimeout
	c.Defaults.RetryConfig.RetryOnConnection = &parent.RetryOnConnection
	if parent.MaxRetryLatency > 0 {
		c.Defaults.RetryConfig.MaxRetryLatency = &parent.MaxRetryLatency
	}

	// Set observation pipeline defaults
	c.Defaults.ObservationPipeline.Enabled = &parent.ObservationPipelineEnabled
	if parent.SampleRate > 0 {
		c.Defaults.ObservationPipeline.SampleRate = &parent.SampleRate
	}

	// Set health checks defaults
	c.Defaults.ActiveHealthChecks.Enabled = &parent.HealthChecksEnabled
	if parent.HealthCheckInterval > 0 {
		c.Defaults.ActiveHealthChecks.Interval = parent.HealthCheckInterval
	}
	if parent.SyncAllowance > 0 {
		c.Defaults.ActiveHealthChecks.SyncAllowance = &parent.SyncAllowance
	}
}
