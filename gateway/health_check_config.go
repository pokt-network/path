// Package gateway provides configuration types for pluggable health checks.
//
// These types are defined in the gateway package to avoid import cycles,
// since protocol/shannon imports gateway.
package gateway

import (
	"fmt"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
)

// Health check configuration defaults
const (
	DefaultHealthCheckTimeout    = 5 * time.Second
	DefaultHealthCheckInterval   = 10 * time.Second
	DefaultLeaderLeaseDuration   = 30 * time.Second
	DefaultLeaderRenewInterval   = 10 * time.Second
	DefaultLeaderKey             = "path:health_check_leader"
	DefaultExternalConfigTimeout = 30 * time.Second
	DefaultExpectedStatusCode    = 200
	DefaultReputationSignal      = "minor_error"
	// DefaultSyncAllowance is the default number of blocks behind the latest block
	// that an endpoint can be before it's considered out of sync.
	// 0 means disabled (no sync allowance check).
	DefaultSyncAllowance = 0
)

// Observation pipeline configuration defaults
const (
	// DefaultObservationPipelineSampleRate is the default fraction of requests to deep-parse.
	// 10% provides good coverage while minimizing latency impact.
	DefaultObservationPipelineSampleRate = 0.1

	// DefaultObservationPipelineWorkerCount is the default number of parsing workers.
	DefaultObservationPipelineWorkerCount = 4

	// DefaultObservationPipelineQueueSize is the default observation queue size.
	DefaultObservationPipelineQueueSize = 1000
)

// HealthCheckType defines the RPC protocol type for a health check.
// These values are aligned with service rpc_types configuration to ensure consistency.
//
// BREAKING CHANGE: The enum values have been updated to match service rpc_types:
//   - "jsonrpc" → "json_rpc" (aligned with service config)
//   - "comet_bft" is newly added for Cosmos CometBFT health checks
//
// Delivery mechanisms by type:
//   - json_rpc, rest, comet_bft: HTTP delivery
//   - websocket: WebSocket delivery
//   - grpc: gRPC delivery (future)
type HealthCheckType string

const (
	// HealthCheckTypeJSONRPC is for JSON-RPC endpoints (HTTP POST with JSON body).
	// Aligned with service rpc_types: "json_rpc"
	HealthCheckTypeJSONRPC HealthCheckType = "json_rpc"

	// HealthCheckTypeREST is for REST API endpoints (HTTP GET/POST).
	// Aligned with service rpc_types: "rest"
	HealthCheckTypeREST HealthCheckType = "rest"

	// HealthCheckTypeCometBFT is for CometBFT RPC endpoints (Cosmos chains).
	// Aligned with service rpc_types: "comet_bft"
	HealthCheckTypeCometBFT HealthCheckType = "comet_bft"

	// HealthCheckTypeWebSocket is for WebSocket endpoints (connect, optionally send/receive).
	// Aligned with service rpc_types: "websocket"
	HealthCheckTypeWebSocket HealthCheckType = "websocket"

	// HealthCheckTypeGRPC is for gRPC endpoints (future implementation).
	// Uses the standard grpc.health.v1.Health service.
	// Aligned with service rpc_types: "grpc"
	HealthCheckTypeGRPC HealthCheckType = "grpc"
)

type (
	// ErrorDetection configures error pattern matching in health check responses.
	// This allows health checks to detect and penalize known error patterns like
	// rate limits, bad gateways, quota errors, etc.
	ErrorDetection struct {
		// StatusCodes defines HTTP status codes to detect and penalize.
		StatusCodes []ErrorStatusCode `yaml:"status_codes,omitempty"`

		// ResponsePatterns defines error patterns to search for in response body.
		// Matches are case-insensitive substring searches.
		ResponsePatterns []ErrorPattern `yaml:"response_patterns,omitempty"`

		// JSONRPCErrorCodes defines JSON-RPC error codes to detect and penalize.
		JSONRPCErrorCodes []JSONRPCErrorCode `yaml:"jsonrpc_error_codes,omitempty"`
	}

	// ErrorStatusCode defines an HTTP status code to detect and its reputation signal.
	ErrorStatusCode struct {
		// Code is the HTTP status code to match (e.g., 429, 502, 503).
		Code int `yaml:"code"`

		// ReputationSignal is the signal to send when this status code is detected.
		// Values: "minor_error", "major_error", "critical_error", "fatal_error"
		ReputationSignal string `yaml:"reputation_signal"`
	}

	// ErrorPattern defines a response body pattern to detect and its reputation signal.
	ErrorPattern struct {
		// Pattern is the substring to search for in the response body (case-insensitive).
		// Examples: "rate limit", "exceeded your limit", "bad gateway", "quota"
		Pattern string `yaml:"pattern"`

		// ReputationSignal is the signal to send when this pattern is detected.
		// Values: "minor_error", "major_error", "critical_error", "fatal_error"
		ReputationSignal string `yaml:"reputation_signal"`
	}

	// JSONRPCErrorCode defines a JSON-RPC error code to detect and its reputation signal.
	JSONRPCErrorCode struct {
		// Code is the JSON-RPC error code to match (e.g., -31002, -32001).
		Code int `yaml:"code"`

		// ReputationSignal is the signal to send when this error code is detected.
		// Values: "minor_error", "major_error", "critical_error", "fatal_error"
		ReputationSignal string `yaml:"reputation_signal"`
	}

	// HealthCheckConfig defines a single configurable health check.
	// This replaces hardcoded QoS checks with YAML-configurable checks.
	HealthCheckConfig struct {
		// Name is a unique identifier for this check within a service.
		Name string `yaml:"name"`

		// Type specifies the RPC protocol type for this check.
		// REQUIRED - must match one of the service's configured rpc_types.
		// Valid values: "json_rpc", "rest", "comet_bft", "websocket", "grpc"
		//
		// BREAKING CHANGE: Old value "jsonrpc" is now "json_rpc" for consistency.
		// No default - explicit specification required to avoid ambiguity.
		Type HealthCheckType `yaml:"type"`

		// Enabled allows disabling individual checks without removing them.
		Enabled *bool `yaml:"enabled,omitempty"`

		// Method is the HTTP method (GET, POST). Required for jsonrpc/rest types.
		// Ignored for websocket and grpc types.
		Method string `yaml:"method,omitempty"`

		// Path is the URL path to send the request to (e.g., "/" or "/ws").
		Path string `yaml:"path"`

		// Headers is a map of HTTP headers to send with the request.
		// If not specified, defaults to {"Content-Type": "application/json"} for POST requests.
		// Can be used to set custom headers like Authorization, X-Api-Key, etc.
		Headers map[string]string `yaml:"headers,omitempty"`

		// Body is the request body (e.g., JSON-RPC payload).
		// For websocket: if provided, sent after connection and response is expected.
		// If empty for websocket: only connection test is performed.
		Body string `yaml:"body,omitempty"`

		// ExpectedStatusCode is the HTTP status code expected for success (default: 200).
		// Only applies to jsonrpc and rest types.
		ExpectedStatusCode int `yaml:"expected_status_code,omitempty"`

		// ExpectedResponseContains is an optional substring to look for in the response body.
		// If specified, the check fails if this substring is not found in the response.
		// For websocket with body: checked against any received message within timeout.
		ExpectedResponseContains string `yaml:"expected_response_contains,omitempty"`

		// ErrorDetection enables error pattern detection in health check responses.
		// If specified, the health check will detect known error patterns (rate limits,
		// bad gateway errors, quota errors, etc.) and send appropriate reputation signals.
		ErrorDetection *ErrorDetection `yaml:"error_detection,omitempty"`

		// Timeout is the request timeout for this check.
		// For websocket: how long to wait for connection + optional response.
		Timeout time.Duration `yaml:"timeout,omitempty"`

		// Archival indicates if this is an archival-specific check.
		Archival bool `yaml:"archival,omitempty"`

		// SyncCheck enables block height validation against the service's sync_allowance.
		// When true, extracts block height from the response and compares against the
		// perceived block number. If the endpoint is more than sync_allowance blocks behind,
		// the health check fails and the configured reputation_signal is recorded.
		// Requires sync_allowance > 0 to be configured on the service.
		SyncCheck bool `yaml:"sync_check,omitempty"`

		// ReputationSignal is the signal type to record on failure.
		// Values: "minor_error", "major_error", "critical_error", "fatal_error"
		ReputationSignal string `yaml:"reputation_signal,omitempty"`
	}

	// ServiceHealthCheckConfig defines health checks for a specific service.
	ServiceHealthCheckConfig struct {
		// ServiceID is the service identifier (e.g., "eth", "base", "poly").
		ServiceID protocol.ServiceID `yaml:"service_id"`
		// CheckInterval is how often to run health checks for this service.
		CheckInterval time.Duration `yaml:"check_interval,omitempty"`
		// Enabled allows disabling all checks for this service.
		Enabled *bool `yaml:"enabled,omitempty"`
		// SyncAllowance is the number of blocks behind the latest block that an endpoint
		// can be before it's considered out of sync. Overrides the global default for this service.
		// 0 means disabled (no sync allowance check). Default: 0 (disabled).
		SyncAllowance *int `yaml:"sync_allowance,omitempty"`
		// Checks is the list of health checks to run for this service.
		Checks []HealthCheckConfig `yaml:"checks"`
	}

	// ExternalConfigSource defines an external URL for health check rules.
	ExternalConfigSource struct {
		// URL is the URL to fetch health check rules from (e.g., GitHub raw file).
		URL string `yaml:"url"`
		// RefreshInterval is how often to re-fetch the external config.
		// 0 means only fetch at startup.
		RefreshInterval time.Duration `yaml:"refresh_interval,omitempty"`
		// Timeout is the HTTP timeout for fetching the external config.
		Timeout time.Duration `yaml:"timeout,omitempty"`
	}

	// LeaderElectionConfig configures leader election for health checks.
	// Only the leader runs health checks to avoid duplicate traffic.
	LeaderElectionConfig struct {
		// Type is the coordination type: "leader_election" or "none".
		// Default: "leader_election" when Redis is configured, "none" otherwise.
		Type string `yaml:"type,omitempty"`
		// LeaseDuration is how long the leader holds the lock.
		LeaseDuration time.Duration `yaml:"lease_duration,omitempty"`
		// RenewInterval is how often the leader renews the lock.
		RenewInterval time.Duration `yaml:"renew_interval,omitempty"`
		// Key is the Redis key for the leader lock.
		Key string `yaml:"key,omitempty"`
	}

	// ActiveHealthChecksConfig is the top-level configuration for active (proactive) health checks.
	// Active health checks send periodic test requests to endpoints to detect issues before user traffic.
	// This replaces hardcoded QoS checks with YAML-configurable checks.
	ActiveHealthChecksConfig struct {
		// Enabled enables/disables the active health check system.
		Enabled bool `yaml:"enabled,omitempty"`
		// SyncAllowance is the default number of blocks behind the latest block that an endpoint
		// can be before it's considered out of sync. Per-service overrides can set different values.
		// 0 means disabled (no sync allowance check). Default: 0 (disabled).
		SyncAllowance int `yaml:"sync_allowance,omitempty"`
		// MaxWorkers is the maximum number of concurrent health check workers.
		// Higher values allow faster health check cycles but increase load on endpoints.
		// Default: 10 workers
		MaxWorkers int `yaml:"max_workers,omitempty"`
		// Coordination configures leader election for distributed deployments.
		Coordination LeaderElectionConfig `yaml:"coordination,omitempty"`
		// External is an optional external URL for health check rules.
		// Local rules override external rules on conflict.
		External *ExternalConfigSource `yaml:"external,omitempty"`
		// Local defines health checks in the local config.
		// These override any checks from External with the same service_id + name.
		Local []ServiceHealthCheckConfig `yaml:"local,omitempty"`
	}

	// RetryConfig configures automatic retry behavior for failed requests.
	RetryConfig struct {
		// Enabled enables/disables automatic retries.
		Enabled bool `yaml:"enabled,omitempty"`
		// MaxRetries is the maximum number of retry attempts.
		MaxRetries int `yaml:"max_retries,omitempty"`
		// MaxRetryLatency is the maximum latency threshold for retries.
		// Only retry if failed request took less than this duration.
		MaxRetryLatency *time.Duration `yaml:"max_retry_latency,omitempty"`
		// RetryOn5xx enables retrying on 5xx errors.
		RetryOn5xx bool `yaml:"retry_on_5xx,omitempty"`
		// RetryOnTimeout enables retrying on timeout errors.
		RetryOnTimeout bool `yaml:"retry_on_timeout,omitempty"`
		// RetryOnConnection enables retrying on connection errors.
		RetryOnConnection bool `yaml:"retry_on_connection,omitempty"`
		// ConnectTimeout is the maximum time to establish a TCP connection.
		// Used for hedge racing to detect slow connections quickly.
		ConnectTimeout *time.Duration `yaml:"connect_timeout,omitempty"`
		// HedgeDelay is the time to wait before starting a hedge (parallel) request.
		// If the primary request hasn't completed within this duration, a second request
		// is started to a different endpoint and the first response wins.
		HedgeDelay *time.Duration `yaml:"hedge_delay,omitempty"`
	}

	// ObservationPipelineConfig configures the observation processing pipeline.
	// Controls how observations from user requests are processed and fed into the reputation system.
	//
	// Architecture:
	//   - Client response: Raw bytes passed through immediately (no parsing) when enabled
	//   - Reputation signals: Always recorded (status code + latency from protocol layer)
	//   - Deep parsing: Sampled requests queued to worker pool for async processing
	//   - endpointStore: Updated by active health checks (100%) + sampled user requests
	ObservationPipelineConfig struct {
		// Enabled enables async observation processing mode.
		// When true, responses are returned as raw bytes without blocking on parsing.
		// When false (default), legacy behavior is used (parse then re-encode).
		Enabled bool `yaml:"enabled,omitempty"`

		// SampleRate is the fraction of requests to deep-parse (0.0 to 1.0).
		// Only applies when Enabled is true.
		// Default: 0.1 (10% of requests get queued for deep parsing)
		// Set to 0.0 to disable sampling (only active health checks update endpointStore)
		// Set to 1.0 to parse all requests async (not recommended for high traffic)
		SampleRate float64 `yaml:"sample_rate,omitempty"`

		// WorkerCount is the number of worker goroutines for async parsing.
		// Default: 4
		WorkerCount int `yaml:"worker_count,omitempty"`

		// QueueSize is the max number of pending observations.
		// If queue is full, new observations are dropped (non-blocking).
		// Default: 1000
		QueueSize int `yaml:"queue_size,omitempty"`
	}
)

// HydrateDefaults applies default values to ActiveHealthChecksConfig.
func (hc *ActiveHealthChecksConfig) HydrateDefaults(hasRedis bool) {
	// Set default coordination type based on Redis availability
	if hc.Coordination.Type == "" {
		if hasRedis {
			hc.Coordination.Type = "leader_election"
		} else {
			hc.Coordination.Type = "none"
		}
	}

	// Set default sync allowance
	if hc.SyncAllowance == 0 {
		hc.SyncAllowance = DefaultSyncAllowance
	}

	// Hydrate coordination defaults
	hc.Coordination.HydrateDefaults()

	// Hydrate external config defaults
	if hc.External != nil {
		hc.External.HydrateDefaults()
	}

	// Hydrate local service check defaults
	for i := range hc.Local {
		hc.Local[i].HydrateDefaults()
	}
}

// HydrateDefaults applies default values to LeaderElectionConfig.
func (lec *LeaderElectionConfig) HydrateDefaults() {
	if lec.LeaseDuration == 0 {
		lec.LeaseDuration = DefaultLeaderLeaseDuration
	}
	if lec.RenewInterval == 0 {
		lec.RenewInterval = DefaultLeaderRenewInterval
	}
	if lec.Key == "" {
		lec.Key = DefaultLeaderKey
	}
}

// HydrateDefaults applies default values to ExternalConfigSource.
func (ecs *ExternalConfigSource) HydrateDefaults() {
	if ecs.Timeout == 0 {
		ecs.Timeout = DefaultExternalConfigTimeout
	}
}

// HydrateDefaults applies default values to ServiceHealthCheckConfig.
func (shc *ServiceHealthCheckConfig) HydrateDefaults() {
	if shc.CheckInterval == 0 {
		shc.CheckInterval = DefaultHealthCheckInterval
	}
	// Enabled defaults to true if not set
	if shc.Enabled == nil {
		enabled := true
		shc.Enabled = &enabled
	}
	// Hydrate individual check defaults
	for i := range shc.Checks {
		shc.Checks[i].HydrateDefaults()
	}
}

// HydrateDefaults applies default values to HealthCheckConfig.
func (hcc *HealthCheckConfig) HydrateDefaults() {
	if hcc.ExpectedStatusCode == 0 {
		hcc.ExpectedStatusCode = DefaultExpectedStatusCode
	}
	if hcc.Timeout == 0 {
		hcc.Timeout = DefaultHealthCheckTimeout
	}
	if hcc.ReputationSignal == "" {
		hcc.ReputationSignal = DefaultReputationSignal
	}
	// Enabled defaults to true if not set
	if hcc.Enabled == nil {
		enabled := true
		hcc.Enabled = &enabled
	}
}

// Validate validates the ActiveHealthChecksConfig.
// Returns an error if validation fails.
func (hc *ActiveHealthChecksConfig) Validate() error {
	// Validate coordination config
	if hc.Coordination.Type != "" && hc.Coordination.Type != "leader_election" && hc.Coordination.Type != "none" {
		return fmt.Errorf("invalid active_health_checks.coordination.type: %s (must be 'leader_election' or 'none')", hc.Coordination.Type)
	}

	// Validate external config if present
	if hc.External != nil {
		if err := hc.External.Validate(); err != nil {
			return err
		}
	}

	// Validate local service configs
	for i, svc := range hc.Local {
		if err := svc.Validate(); err != nil {
			return fmt.Errorf("active_health_checks.local[%d]: %w", i, err)
		}
	}

	// Validate uniqueness: service_id + check.name must be unique
	if err := hc.ValidateUniqueness(); err != nil {
		return err
	}

	return nil
}

// ValidateUniqueness ensures that service_id + check.name combinations are unique.
func (hc *ActiveHealthChecksConfig) ValidateUniqueness() error {
	seen := make(map[string]struct{})

	for _, svc := range hc.Local {
		for _, check := range svc.Checks {
			key := fmt.Sprintf("%s:%s", svc.ServiceID, check.Name)
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate health check: service_id=%s, name=%s", svc.ServiceID, check.Name)
			}
			seen[key] = struct{}{}
		}
	}

	return nil
}

// Validate validates the ExternalConfigSource.
func (ecs *ExternalConfigSource) Validate() error {
	if ecs.URL == "" {
		return fmt.Errorf("health_checks.external.url is required when external config is specified")
	}
	return nil
}

// Validate validates the ServiceHealthCheckConfig.
func (shc *ServiceHealthCheckConfig) Validate() error {
	if shc.ServiceID == "" {
		return fmt.Errorf("service_id is required")
	}
	if len(shc.Checks) == 0 {
		return fmt.Errorf("at least one health check is required for service %s", shc.ServiceID)
	}
	for i, check := range shc.Checks {
		if err := check.Validate(); err != nil {
			return fmt.Errorf("checks[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate validates the HealthCheckConfig.
func (hcc *HealthCheckConfig) Validate() error {
	if hcc.Name == "" {
		return fmt.Errorf("name is required")
	}

	// Type is REQUIRED - no default, must be explicit
	if hcc.Type == "" {
		return fmt.Errorf("type is required for check %s (must be json_rpc, rest, comet_bft, websocket, or grpc)", hcc.Name)
	}

	// Validate type is one of the allowed values
	validTypes := map[HealthCheckType]bool{
		HealthCheckTypeJSONRPC:   true,
		HealthCheckTypeREST:      true,
		HealthCheckTypeCometBFT:  true,
		HealthCheckTypeWebSocket: true,
		HealthCheckTypeGRPC:      true,
	}
	if !validTypes[hcc.Type] {
		return fmt.Errorf("invalid type '%s' for check %s (must be json_rpc, rest, comet_bft, websocket, or grpc)", hcc.Type, hcc.Name)
	}

	// gRPC is not yet implemented
	if hcc.Type == HealthCheckTypeGRPC {
		return fmt.Errorf("grpc health checks are not yet implemented for check %s", hcc.Name)
	}

	// Method is required for HTTP-based types (json_rpc, rest, comet_bft)
	if hcc.Type == HealthCheckTypeJSONRPC || hcc.Type == HealthCheckTypeREST || hcc.Type == HealthCheckTypeCometBFT {
		if hcc.Method == "" {
			return fmt.Errorf("method is required for %s check %s", hcc.Type, hcc.Name)
		}
		if hcc.Method != "GET" && hcc.Method != "POST" {
			return fmt.Errorf("method must be GET or POST for check %s, got %s", hcc.Name, hcc.Method)
		}
	}

	// Path is required for all types
	if hcc.Path == "" {
		return fmt.Errorf("path is required for check %s", hcc.Name)
	}

	// Validate reputation signal if provided
	validSignals := map[string]bool{
		"minor_error": true, "major_error": true, "critical_error": true, "fatal_error": true, "": true,
	}
	if !validSignals[hcc.ReputationSignal] {
		return fmt.Errorf("invalid reputation_signal '%s' for check %s (must be minor_error, major_error, critical_error, or fatal_error)", hcc.ReputationSignal, hcc.Name)
	}

	// Validate error detection if provided
	if hcc.ErrorDetection != nil {
		if err := hcc.ErrorDetection.Validate(hcc.Name); err != nil {
			return err
		}
	}

	return nil
}

// HydrateDefaults applies default values to ObservationPipelineConfig.
func (pc *ObservationPipelineConfig) HydrateDefaults() {
	// SampleRate defaults to 10% (0.1)
	// Note: 0.0 is a valid value (disable sampling), so we don't set default if already set
	if pc.SampleRate == 0 && pc.Enabled {
		pc.SampleRate = DefaultObservationPipelineSampleRate
	}
	if pc.WorkerCount == 0 {
		pc.WorkerCount = DefaultObservationPipelineWorkerCount
	}
	if pc.QueueSize == 0 {
		pc.QueueSize = DefaultObservationPipelineQueueSize
	}
}

// Validate validates the ObservationPipelineConfig.
func (pc *ObservationPipelineConfig) Validate() error {
	if pc.SampleRate < 0 || pc.SampleRate > 1 {
		return fmt.Errorf("observation_pipeline.sample_rate must be between 0.0 and 1.0, got %f", pc.SampleRate)
	}
	if pc.WorkerCount < 0 {
		return fmt.Errorf("observation_pipeline.worker_count must be non-negative, got %d", pc.WorkerCount)
	}
	if pc.QueueSize < 0 {
		return fmt.Errorf("observation_pipeline.queue_size must be non-negative, got %d", pc.QueueSize)
	}
	return nil
}

// Validate validates the RetryConfig for reasonable values and logs warnings.
func (rc *RetryConfig) Validate(logger polylog.Logger) error {
	// Hard limit: max 10 retries (prevents DoS from misconfiguration)
	if rc.MaxRetries > 10 {
		return fmt.Errorf("retry_config.max_retries cannot exceed 10 (got %d) - excessive retries cause high latency and token burn", rc.MaxRetries)
	}

	// Warning: recommend ≤3 retries
	if rc.MaxRetries > 3 && logger != nil {
		logger.Warn().
			Int("max_retries", rc.MaxRetries).
			Msg("⚠️ retry_config.max_retries exceeds recommended threshold of 3 - may cause excessive latency and token burn")
	}

	// Validate max_retry_latency if set
	if rc.MaxRetryLatency != nil && *rc.MaxRetryLatency < 0 {
		return fmt.Errorf("retry_config.max_retry_latency cannot be negative (got %v)", *rc.MaxRetryLatency)
	}

	return nil
}

// Validate validates the ErrorDetection configuration.
func (ed *ErrorDetection) Validate(checkName string) error {
	// At least one detection type must be configured
	if len(ed.StatusCodes) == 0 && len(ed.ResponsePatterns) == 0 && len(ed.JSONRPCErrorCodes) == 0 {
		return fmt.Errorf("error_detection must specify at least one of: status_codes, response_patterns, or jsonrpc_error_codes for check %s", checkName)
	}

	// Validate status codes
	validSignals := map[string]bool{
		"minor_error": true, "major_error": true, "critical_error": true, "fatal_error": true,
	}
	for i, statusCode := range ed.StatusCodes {
		if statusCode.Code < 100 || statusCode.Code > 599 {
			return fmt.Errorf("error_detection.status_codes[%d].code must be a valid HTTP status code (100-599) for check %s, got %d", i, checkName, statusCode.Code)
		}
		if !validSignals[statusCode.ReputationSignal] {
			return fmt.Errorf("error_detection.status_codes[%d].reputation_signal must be minor_error, major_error, critical_error, or fatal_error for check %s, got %s", i, checkName, statusCode.ReputationSignal)
		}
	}

	// Validate response patterns
	for i, pattern := range ed.ResponsePatterns {
		if pattern.Pattern == "" {
			return fmt.Errorf("error_detection.response_patterns[%d].pattern cannot be empty for check %s", i, checkName)
		}
		if !validSignals[pattern.ReputationSignal] {
			return fmt.Errorf("error_detection.response_patterns[%d].reputation_signal must be minor_error, major_error, critical_error, or fatal_error for check %s, got %s", i, checkName, pattern.ReputationSignal)
		}
	}

	// Validate JSON-RPC error codes
	for i, errorCode := range ed.JSONRPCErrorCodes {
		if !validSignals[errorCode.ReputationSignal] {
			return fmt.Errorf("error_detection.jsonrpc_error_codes[%d].reputation_signal must be minor_error, major_error, critical_error, or fatal_error for check %s, got %s", i, checkName, errorCode.ReputationSignal)
		}
	}

	return nil
}

// Type aliases for backwards compatibility
type (
	// HealthChecksConfig is an alias for ActiveHealthChecksConfig (deprecated name)
	HealthChecksConfig = ActiveHealthChecksConfig
	// PassthroughConfig is an alias for ObservationPipelineConfig (deprecated name)
	PassthroughConfig = ObservationPipelineConfig
)
