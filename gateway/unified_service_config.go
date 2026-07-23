// Package gateway provides unified service configuration types.
//
// These types are defined in the gateway package to avoid import cycles,
// since protocol/shannon imports gateway.
package gateway

import (
	"fmt"
	"strings"
	"sync"
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

// EndpointPolicyConfig defines operator-level security policies for endpoint selection.
// Gateway operators can use this to enforce requirements on supplier endpoints.
type EndpointPolicyConfig struct {
	// RequireHTTPS rejects endpoints that don't use HTTPS (or WSS for websockets).
	// When true, only endpoints with https:// or wss:// URLs are eligible for selection.
	RequireHTTPS bool `yaml:"require_https,omitempty"`

	// RequireDomain rejects endpoints that use raw IP addresses instead of domain names.
	// When true, only endpoints with resolvable domain names are eligible for selection.
	RequireDomain bool `yaml:"require_domain,omitempty"`
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

	// ConnectTimeout is the maximum time to establish a TCP connection.
	// If connection fails within this time, retry immediately with a different endpoint.
	// Default: 500ms. This is separate from read timeout to enable fast failure on unreachable endpoints.
	ConnectTimeout *time.Duration `yaml:"connect_timeout,omitempty"`

	// HedgeDelay is the time to wait before starting a hedge (parallel) request.
	// If no response is received within this duration after connecting, a second request
	// is sent to a different endpoint. The first response wins.
	// Default: 500ms. Set to 0 to disable hedging.
	HedgeDelay *time.Duration `yaml:"hedge_delay,omitempty"`
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

// ServiceTimeoutConfig holds per-service timeout configuration.
// Different services may require different timeouts based on their payload sizes and response times.
// For example, LLM services may need 60s+ while fast EVM chains may only need 5s.
type ServiceTimeoutConfig struct {
	// RelayTimeout is the maximum time to wait for a response from a backend endpoint.
	// This timeout applies to each individual relay attempt (before retries).
	// Default: 10s (from DefaultRelayRequestTimeout)
	RelayTimeout *time.Duration `yaml:"relay_timeout,omitempty"`
}

// ServiceHealthCheckOverride holds per-service health check configuration overrides.
type ServiceHealthCheckOverride struct {
	Enabled       *bool                 `yaml:"enabled,omitempty"`
	Interval      time.Duration         `yaml:"interval,omitempty"`
	SyncAllowance *int                  `yaml:"sync_allowance,omitempty"`
	External      *ExternalConfigSource `yaml:"external,omitempty"`
	Local         []HealthCheckConfig   `yaml:"local,omitempty"`
}

// ExternalBlockSource configures an optional external RPC endpoint
// for ground-truth block height validation. When configured, the
// external block height acts as a floor for perceivedBlockNumber,
// ensuring behind-on-block suppliers are correctly filtered even when
// all session endpoints are equally stale.
type ExternalBlockSource struct {
	URL         string        `yaml:"url"`                    // RPC endpoint URL
	Type        string        `yaml:"type,omitempty"`         // "jsonrpc" (default) or "rest". REST uses GET, JSON-RPC uses POST.
	Method      string        `yaml:"method,omitempty"`       // JSON-RPC method. Default: "eth_blockNumber". Use "status" for Cosmos, "getBlockHeight" for Solana.
	Path        string        `yaml:"path,omitempty"`         // Request path appended to URL. Default: "/"
	Interval    time.Duration `yaml:"interval,omitempty"`     // Poll interval. Default: 30s
	Timeout     time.Duration `yaml:"timeout,omitempty"`      // HTTP timeout. Default: 5s
	GracePeriod time.Duration `yaml:"grace_period,omitempty"` // Wait after startup before applying external floor. Default: 60s
}

// ServiceFallbackConfig holds per-service fallback endpoint configuration.
type ServiceFallbackConfig struct {
	Enabled        bool                `yaml:"enabled,omitempty"`
	SendAllTraffic bool                `yaml:"send_all_traffic,omitempty"`
	Endpoints      []map[string]string `yaml:"endpoints,omitempty"`
}

// StaticRoute defines a fixed response served directly by the gateway for a specific
// request path, without relaying to a backend endpoint. It lets the gateway expose small,
// service-scoped metadata endpoints (for example an info/version route) whose body is a
// constant configured value rather than something fetched from a supplier.
//
// A static route short-circuits the relay pipeline: a matching request never reaches
// RPC-type detection or endpoint selection, so it does not interact with a service's
// rpc_types (a REST service is unaffected unless it explicitly configures the same path).
// Matching is exact on Path and, when Methods is set, on the request method.
type StaticRoute struct {
	// Path is the exact request path this route serves, matched after the gateway strips
	// the API-version and portal-app-id prefixes (e.g. "/example"). Must begin with "/"
	// and must not be the relay root ("/") — that would shadow all of a service's traffic.
	Path string `yaml:"path"`

	// Methods restricts the route to the listed HTTP methods (case-insensitive). Empty
	// matches any method.
	Methods []string `yaml:"methods,omitempty"`

	// StatusCode is the HTTP status returned. Defaults to 200 when unset.
	StatusCode int `yaml:"status_code,omitempty"`

	// ContentType sets the Content-Type header. Defaults to "text/plain; charset=utf-8".
	ContentType string `yaml:"content_type,omitempty"`

	// Body is the exact response body returned verbatim.
	Body string `yaml:"body"`

	// Headers are additional response headers set on the reply.
	Headers map[string]string `yaml:"headers,omitempty"`
}

// ServiceDefaults contains default settings inherited by all services.
type ServiceDefaults struct {
	Type                 ServiceType                  `yaml:"type,omitempty"`
	RPCTypes             []string                     `yaml:"rpc_types,omitempty"`
	LatencyProfile       string                       `yaml:"latency_profile,omitempty"`
	ReputationConfig     ServiceReputationConfig      `yaml:"reputation_config,omitempty"`
	Latency              ServiceLatencyConfig         `yaml:"latency,omitempty"`
	TieredSelection      ServiceTieredSelectionConfig `yaml:"tiered_selection,omitempty"`
	Probation            ServiceProbationConfig       `yaml:"probation,omitempty"`
	RetryConfig          ServiceRetryConfig           `yaml:"retry_config,omitempty"`
	ObservationPipeline  ServiceObservationConfig     `yaml:"observation_pipeline,omitempty"`
	ConcurrencyConfig    ServiceConcurrencyConfig     `yaml:"concurrency_config,omitempty"`
	TimeoutConfig        ServiceTimeoutConfig         `yaml:"timeout_config,omitempty"`
	ActiveHealthChecks   ServiceHealthCheckOverride   `yaml:"active_health_checks,omitempty"`
	ExternalBlockSources []ExternalBlockSource        `yaml:"external_block_sources,omitempty"`

	// StaticRoutes are gateway-served fixed responses applied to every service as a global
	// default. A per-service StaticRoutes entry with the same Path overrides the global one.
	StaticRoutes []StaticRoute `yaml:"static_routes,omitempty"`

	// MaxOperatorShare is the default per-operator concentration cap applied to any
	// service that does not set its own. nil falls back to DefaultMaxOperatorShare.
	MaxOperatorShare *float64 `yaml:"max_operator_share,omitempty"`

	// WebsocketRebindOperatorUniform is the default WebSocket rebind selection strategy.
	// nil falls back to DefaultWebsocketRebindOperatorUniform (ON).
	WebsocketRebindOperatorUniform *bool `yaml:"websocket_rebind_operator_uniform,omitempty"`
}

// ServiceConfig defines configuration for a single service.
type ServiceConfig struct {
	ID                   protocol.ServiceID            `yaml:"id"`
	Type                 ServiceType                   `yaml:"type,omitempty"`
	RPCTypes             []string                      `yaml:"rpc_types,omitempty"`
	RPCTypeFallbacks     map[string]string             `yaml:"rpc_type_fallbacks,omitempty"`
	LatencyProfile       string                        `yaml:"latency_profile,omitempty"`
	ReputationConfig     *ServiceReputationConfig      `yaml:"reputation_config,omitempty"`
	Latency              *ServiceLatencyConfig         `yaml:"latency,omitempty"`
	TieredSelection      *ServiceTieredSelectionConfig `yaml:"tiered_selection,omitempty"`
	Probation            *ServiceProbationConfig       `yaml:"probation,omitempty"`
	RetryConfig          *ServiceRetryConfig           `yaml:"retry_config,omitempty"`
	ObservationPipeline  *ServiceObservationConfig     `yaml:"observation_pipeline,omitempty"`
	ConcurrencyConfig    *ServiceConcurrencyConfig     `yaml:"concurrency_config,omitempty"`
	TimeoutConfig        *ServiceTimeoutConfig         `yaml:"timeout_config,omitempty"`
	Fallback             *ServiceFallbackConfig        `yaml:"fallback,omitempty"`
	HealthChecks         *ServiceHealthCheckOverride   `yaml:"health_checks,omitempty"`
	ExternalBlockSources []ExternalBlockSource         `yaml:"external_block_sources,omitempty"`

	// BlockedSuppliers is a list of supplier addresses that should be permanently excluded
	// from endpoint selection for this service. This is used to block known-malicious suppliers
	// whose on-chain staked endpoints serve harmful content (e.g., spam, malware redirects).
	// Unlike the auto-generated supplier blacklist (which is temporary/session-scoped),
	// this is a persistent, config-driven blocklist.
	BlockedSuppliers []string `yaml:"blocked_suppliers,omitempty"`

	// StaticRoutes are gateway-served fixed responses for this service. A route whose Path
	// matches a global default (ServiceDefaults.StaticRoutes) overrides that default for this
	// service; paths present only in the defaults still apply.
	StaticRoutes []StaticRoute `yaml:"static_routes,omitempty"`

	// MaxOperatorShare caps the fraction of this service's endpoint selections that
	// any single operator (registrable domain / eTLD+1) may receive, bounding the blast
	// radius of one operator failing when it holds most of the valid endpoint pool. The
	// excess above the cap is spread across the other valid operators (water-filling).
	// nil = use the global default (DefaultMaxOperatorShare). A value <= 0 or >= 1 disables
	// the cap for this service (flat selection ∝ endpoint count).
	//
	// Scope: the cap governs the primary single-endpoint selection (the bulk of traffic,
	// HTTP and WebSocket, plus the WebSocket rebind target). It does NOT reshape
	// multi-endpoint selection (parallel fan-out / hedge), which draw from the same
	// reputation-filtered pool via separate code paths.
	MaxOperatorShare *float64 `yaml:"max_operator_share,omitempty"`

	// WebsocketRebindOperatorUniform selects the WebSocket rebind strategy for this service.
	// When true (the default), a session-rollover rebind picks the target operator uniformly
	// (every provider equally likely), then an endpoint within it — maximizing per-provider
	// spread. When false, the rebind uses the endpoint-count-weighted concentration cap
	// (MaxOperatorShare) instead. nil = use the global default
	// (ServiceDefaults.WebsocketRebindOperatorUniform, then DefaultWebsocketRebindOperatorUniform).
	//
	// Note: operator-uniform gives a thin operator the same share as a large one regardless
	// of endpoint count, so a single-endpoint operator absorbs a full operator-share of the
	// WebSocket load. Disable per-service if that overloads a small provider.
	WebsocketRebindOperatorUniform *bool `yaml:"websocket_rebind_operator_uniform,omitempty"`
}

// UnifiedServicesConfig is the top-level configuration for the unified service system.
type UnifiedServicesConfig struct {
	LatencyProfiles map[string]LatencyProfileConfig `yaml:"latency_profiles,omitempty"`
	Defaults        ServiceDefaults                 `yaml:"defaults,omitempty"`
	Services        []ServiceConfig                 `yaml:"services,omitempty"`

	// servicesMu guards concurrent access to Services. The external health-check
	// refresh mutates Services at runtime (SetServiceSyncAllowance) while every
	// request reads it (GetServiceConfig etc.); without synchronization the
	// append path races a concurrent slice-header read → torn header → panic.
	// It is a pointer (not a value sync.RWMutex) so this struct — which is
	// returned by value from LoadGatewayConfigFromYAML during load — stays
	// copylock-safe. Initialized in HydrateDefaults (called once at startup
	// before any concurrency); the lock helpers no-op if it is nil so
	// directly-constructed configs in tests remain usable.
	servicesMu *sync.RWMutex
}

// rlock/runlock/lock/unlock guard Services access. They are nil-safe: a config
// built without HydrateDefaults (e.g. in tests) is used single-threaded, so
// skipping the lock there is safe.
func (c *UnifiedServicesConfig) rlock() {
	if c.servicesMu != nil {
		c.servicesMu.RLock()
	}
}
func (c *UnifiedServicesConfig) runlock() {
	if c.servicesMu != nil {
		c.servicesMu.RUnlock()
	}
}
func (c *UnifiedServicesConfig) lock() {
	if c.servicesMu != nil {
		c.servicesMu.Lock()
	}
}
func (c *UnifiedServicesConfig) unlock() {
	if c.servicesMu != nil {
		c.servicesMu.Unlock()
	}
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

		if err := validateStaticRoutes(svc.StaticRoutes); err != nil {
			return fmt.Errorf("services[%d] (%s): %w", i, svc.ID, err)
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

	if err := validateStaticRoutes(c.Defaults.StaticRoutes); err != nil {
		return fmt.Errorf("defaults: %w", err)
	}

	return nil
}

// validateStaticRoutes checks a set of static routes for well-formed paths, unique
// path+method coverage, and valid status codes. Returns the first problem found.
func validateStaticRoutes(routes []StaticRoute) error {
	// Track (path, method) pairs so two routes cannot both claim the same request. A
	// route with no methods claims all methods for its path, so it conflicts with any
	// other route on that path.
	pathAllMethods := make(map[string]struct{})
	pathMethod := make(map[string]struct{})
	for i, rt := range routes {
		if rt.Path == "" || rt.Path[0] != '/' {
			return fmt.Errorf("static_routes[%d]: path must be non-empty and begin with '/'", i)
		}
		if rt.Path == "/" {
			return fmt.Errorf("static_routes[%d]: path '/' would shadow all relay traffic and is not allowed", i)
		}
		if rt.StatusCode != 0 && (rt.StatusCode < 100 || rt.StatusCode > 599) {
			return fmt.Errorf("static_routes[%d] (%s): status_code %d out of range 100-599", i, rt.Path, rt.StatusCode)
		}
		if len(rt.Methods) == 0 {
			if _, dup := pathAllMethods[rt.Path]; dup {
				return fmt.Errorf("static_routes[%d]: duplicate path '%s'", i, rt.Path)
			}
			pathAllMethods[rt.Path] = struct{}{}
			continue
		}
		if _, dup := pathAllMethods[rt.Path]; dup {
			return fmt.Errorf("static_routes[%d]: path '%s' conflicts with an all-methods route on the same path", i, rt.Path)
		}
		for _, m := range rt.Methods {
			key := rt.Path + " " + strings.ToUpper(m)
			if _, dup := pathMethod[key]; dup {
				return fmt.Errorf("static_routes[%d]: duplicate path+method '%s'", i, key)
			}
			pathMethod[key] = struct{}{}
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
	// Initialize the Services mutex once, before the config is shared with the
	// request path and the health-check refresh goroutine.
	if c.servicesMu == nil {
		c.servicesMu = &sync.RWMutex{}
	}
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
		traffic := 10.0 // 10% traffic to probation endpoints for recovery opportunities
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
	if c.Defaults.RetryConfig.ConnectTimeout == nil {
		connectTimeout := 500 * time.Millisecond
		c.Defaults.RetryConfig.ConnectTimeout = &connectTimeout
	}
	if c.Defaults.RetryConfig.HedgeDelay == nil {
		hedgeDelay := 500 * time.Millisecond
		c.Defaults.RetryConfig.HedgeDelay = &hedgeDelay
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

	// Hydrate default timeout config
	if c.Defaults.TimeoutConfig.RelayTimeout == nil {
		defaultTimeout := DefaultRelayRequestTimeout
		c.Defaults.TimeoutConfig.RelayTimeout = &defaultTimeout
	}

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
	c.rlock()
	defer c.runlock()
	for i := range c.Services {
		if c.Services[i].ID == serviceID {
			// Return a copy, not &c.Services[i]: a pointer into the slice would
			// let the caller read the element after the lock is released, racing
			// a concurrent SetServiceSyncAllowance. The writer copy-on-writes the
			// HealthChecks pointer, so this shallow copy stays stable.
			cfg := c.Services[i]
			return &cfg
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
	c.rlock()
	defer c.runlock()
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
		if merged.RetryConfig.ConnectTimeout == nil {
			merged.RetryConfig.ConnectTimeout = c.Defaults.RetryConfig.ConnectTimeout
		}
		if merged.RetryConfig.HedgeDelay == nil {
			merged.RetryConfig.HedgeDelay = c.Defaults.RetryConfig.HedgeDelay
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

	// Merge timeout config
	if merged.TimeoutConfig == nil {
		timeoutCopy := c.Defaults.TimeoutConfig
		merged.TimeoutConfig = &timeoutCopy
	} else {
		if merged.TimeoutConfig.RelayTimeout == nil {
			merged.TimeoutConfig.RelayTimeout = c.Defaults.TimeoutConfig.RelayTimeout
		}
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

	// Merge external block sources: service-level sources override defaults entirely
	if len(merged.ExternalBlockSources) == 0 && len(c.Defaults.ExternalBlockSources) > 0 {
		merged.ExternalBlockSources = c.Defaults.ExternalBlockSources
	}

	return &merged
}

// DefaultMaxOperatorShare is the per-operator concentration cap applied when neither the
// service nor the global defaults specify one. Shipped ON: no single operator (eTLD+1)
// receives more than this fraction of a service's endpoint selections when the valid pool
// spans multiple operators. Tuned from production concentration data: a canary survey found
// the dominant operator held 58–90% of most services' sessions, yet reputation/validation
// filtering trims its effective valid-pool share enough that a 0.75 cap almost never
// engaged. 0.65 makes the cap actually bound the concentrated tail (12 of 19 sampled
// services) at negligible redistribution cost — the excess spreads across a handful of
// other valid operators — while staying above the uniform-over-operators feasibility floor
// for services with as few as two operators. Set a per-service or default value of >= 1
// (or <= 0) to disable.
const DefaultMaxOperatorShare = 0.65

// DefaultWebsocketRebindOperatorUniform is the WebSocket rebind strategy applied when neither
// the service nor the global defaults specify one. Shipped ON: a session-rollover rebind
// spreads uniformly across operators (each provider equally likely) rather than by endpoint
// count, so no single operator accumulates WebSocket connections in proportion to its
// (often dominant) endpoint share.
const DefaultWebsocketRebindOperatorUniform = true

// GetWebsocketRebindOperatorUniformForService returns whether the WebSocket rebind should
// pick the target operator uniformly (true) or via the endpoint-count-weighted concentration
// cap (false). It checks per-service config first, then global defaults, then
// DefaultWebsocketRebindOperatorUniform.
func (c *UnifiedServicesConfig) GetWebsocketRebindOperatorUniformForService(serviceID protocol.ServiceID) bool {
	if svc := c.GetServiceConfig(serviceID); svc != nil && svc.WebsocketRebindOperatorUniform != nil {
		return *svc.WebsocketRebindOperatorUniform
	}
	if c.Defaults.WebsocketRebindOperatorUniform != nil {
		return *c.Defaults.WebsocketRebindOperatorUniform
	}
	return DefaultWebsocketRebindOperatorUniform
}

// GetMaxOperatorShareForService returns the per-operator concentration cap for a service.
// It checks per-service config first, then global defaults, then DefaultMaxOperatorShare.
// A configured value <= 0 or >= 1 disables the cap for that service.
func (c *UnifiedServicesConfig) GetMaxOperatorShareForService(serviceID protocol.ServiceID) float64 {
	svc := c.GetServiceConfig(serviceID)
	if svc != nil && svc.MaxOperatorShare != nil {
		return *svc.MaxOperatorShare
	}
	if c.Defaults.MaxOperatorShare != nil {
		return *c.Defaults.MaxOperatorShare
	}
	return DefaultMaxOperatorShare
}

// ResolveStaticResponse returns the configured static response for a service's request path
// and method, or ok=false if none applies. Per-service routes take precedence over the
// global defaults on a matching path; the defaults cover paths a service does not override.
// It implements the router's static-response resolver so a matching request is served
// directly, bypassing endpoint selection and the relay.
//
// The returned headers map always carries Content-Type (defaulting to text/plain) and is a
// fresh copy safe for the caller to write onto the response.
func (c *UnifiedServicesConfig) ResolveStaticResponse(
	serviceID, path, method string,
) (body []byte, statusCode int, headers map[string]string, ok bool) {
	if svc := c.GetServiceConfig(protocol.ServiceID(serviceID)); svc != nil {
		if rt, found := matchStaticRoute(svc.StaticRoutes, path, method); found {
			return buildStaticResponse(rt)
		}
	}
	// Defaults are set once at load and read-only thereafter, so no lock is needed.
	if rt, found := matchStaticRoute(c.Defaults.StaticRoutes, path, method); found {
		return buildStaticResponse(rt)
	}
	return nil, 0, nil, false
}

// matchStaticRoute returns the first route whose path equals the request path and whose
// method set is empty (any) or contains the request method (case-insensitive).
func matchStaticRoute(routes []StaticRoute, path, method string) (StaticRoute, bool) {
	for _, rt := range routes {
		if rt.Path != path {
			continue
		}
		if len(rt.Methods) == 0 {
			return rt, true
		}
		for _, m := range rt.Methods {
			if strings.EqualFold(m, method) {
				return rt, true
			}
		}
	}
	return StaticRoute{}, false
}

// buildStaticResponse materializes a route into a response, applying defaults for status
// code (200) and Content-Type (text/plain).
func buildStaticResponse(rt StaticRoute) (body []byte, statusCode int, headers map[string]string, ok bool) {
	statusCode = rt.StatusCode
	if statusCode == 0 {
		statusCode = 200
	}
	contentType := rt.ContentType
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	headers = make(map[string]string, len(rt.Headers)+1)
	for k, v := range rt.Headers {
		headers[k] = v
	}
	headers["Content-Type"] = contentType
	return []byte(rt.Body), statusCode, headers, true
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

// SetServiceSyncAllowance sets the sync allowance for a specific service.
// This is called when external health check rules are loaded to propagate
// the sync_allowance to the unified config for use by the QoS layer.
func (c *UnifiedServicesConfig) SetServiceSyncAllowance(serviceID protocol.ServiceID, syncAllowance int) {
	c.lock()
	defer c.unlock()
	for i := range c.Services {
		if c.Services[i].ID == serviceID {
			// Copy-on-write the HealthChecks pointer rather than mutating the
			// existing one in place: a concurrent reader may hold a copy of this
			// ServiceConfig (with the old pointer), and mutating that shared
			// override in place would race its read. Publishing a fresh pointer
			// leaves the old one immutable.
			hc := ServiceHealthCheckOverride{}
			if c.Services[i].HealthChecks != nil {
				hc = *c.Services[i].HealthChecks
			}
			hc.SyncAllowance = &syncAllowance
			c.Services[i].HealthChecks = &hc
			return
		}
	}
	// Service not found in existing config, add a new service entry
	c.Services = append(c.Services, ServiceConfig{
		ID: serviceID,
		HealthChecks: &ServiceHealthCheckOverride{
			SyncAllowance: &syncAllowance,
		},
	})
}

// GetRelayTimeoutForService returns the relay timeout for a service.
// It checks per-service config first, then falls back to global defaults.
// Returns DefaultRelayRequestTimeout (10s) if not configured.
func (c *UnifiedServicesConfig) GetRelayTimeoutForService(serviceID protocol.ServiceID) time.Duration {
	// Check per-service config
	svc := c.GetServiceConfig(serviceID)
	if svc != nil && svc.TimeoutConfig != nil && svc.TimeoutConfig.RelayTimeout != nil {
		return *svc.TimeoutConfig.RelayTimeout
	}

	// Check global defaults
	if c.Defaults.TimeoutConfig.RelayTimeout != nil {
		return *c.Defaults.TimeoutConfig.RelayTimeout
	}

	// Return default
	return DefaultRelayRequestTimeout
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
	// ConnectTimeout from retry_config.connect_timeout
	ConnectTimeout *time.Duration
	// HedgeDelay from retry_config.hedge_delay
	HedgeDelay *time.Duration
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
	// LocalHealthChecks from active_health_checks.local
	// Used to propagate per-service sync_allowance before QoS instances are created
	LocalHealthChecks []ServiceHealthCheckConfig
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
	if parent.ConnectTimeout != nil && *parent.ConnectTimeout > 0 {
		c.Defaults.RetryConfig.ConnectTimeout = parent.ConnectTimeout
	}
	if parent.HedgeDelay != nil && *parent.HedgeDelay > 0 {
		c.Defaults.RetryConfig.HedgeDelay = parent.HedgeDelay
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

	// Propagate per-service sync_allowance from local health check configs
	// This ensures QoS instances are created with the correct sync_allowance
	for _, localConfig := range parent.LocalHealthChecks {
		if localConfig.SyncAllowance != nil && *localConfig.SyncAllowance > 0 {
			c.SetServiceSyncAllowance(localConfig.ServiceID, *localConfig.SyncAllowance)
		}
	}
}
