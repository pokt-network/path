// Package gateway provides the health check executor for pluggable health checks.
//
// The HealthCheckExecutor executes YAML-configurable health checks against endpoints
// through the protocol layer (sending synthetic relay requests) and records results
// to the reputation system.
//
// Unlike direct HTTP calls, health checks are sent through the protocol just like
// regular user requests, ensuring the full path (including relay miners) is tested.
//
// Supported health check types:
//   - jsonrpc: JSON-RPC endpoints (HTTP POST with JSON body)
//   - rest: REST endpoints (HTTP GET/POST)
//   - websocket: WebSocket endpoints (connect, optionally send/receive message)
//   - grpc: gRPC endpoints (future implementation)
package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pokt-network/poktroll/pkg/polylog"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/yaml.v3"

	healthcheckmetrics "github.com/pokt-network/path/metrics/healthcheck"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/observation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// HealthCheckExecutor executes configurable health checks against endpoints
// and records results to the reputation system.
//
// Health checks are sent through the protocol layer (via synthetic relay requests)
// to ensure the full path including relay miners is tested.
type HealthCheckExecutor struct {
	config        *ActiveHealthChecksConfig
	reputationSvc reputation.ReputationService
	logger        polylog.Logger

	// Protocol is used to send health check requests through the relay path.
	// This ensures health checks test the full path including relay miners.
	protocol Protocol

	// MetricsReporter is used to export metrics based on health check observations.
	metricsReporter RequestResponseReporter

	// DataReporter is used to export data pipeline observations from health checks.
	dataReporter RequestResponseReporter

	// httpClient is used for external config fetching only (not for health checks).
	httpClient *http.Client

	// leaderElector is optional - if nil, all instances run health checks
	leaderElector *LeaderElector

	// observationQueue handles async extraction of quality data from health check responses.
	// If set, health check responses are submitted for deep parsing (block height, chain ID, etc.)
	// This enables the same async processing pipeline for both user requests and health checks.
	observationQueue *ObservationQueue

	// maxWorkers is the maximum number of concurrent health check workers.
	maxWorkers int

	// External config caching
	externalConfigMu    sync.RWMutex
	externalConfigs     []ServiceHealthCheckConfig
	externalConfigError error
	stopRefresh         chan struct{}
}

// HealthCheckExecutorConfig contains configuration for creating a HealthCheckExecutor.
type HealthCheckExecutorConfig struct {
	Config           *ActiveHealthChecksConfig
	ReputationSvc    reputation.ReputationService
	Logger           polylog.Logger
	Protocol         Protocol
	MetricsReporter  RequestResponseReporter
	DataReporter     RequestResponseReporter
	LeaderElector    *LeaderElector
	ObservationQueue *ObservationQueue
	MaxWorkers       int
}

// NewHealthCheckExecutor creates a new HealthCheckExecutor.
func NewHealthCheckExecutor(cfg HealthCheckExecutorConfig) *HealthCheckExecutor {
	maxWorkers := cfg.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 10 // Default number of concurrent workers
	}

	return &HealthCheckExecutor{
		config:          cfg.Config,
		reputationSvc:   cfg.ReputationSvc,
		logger:          cfg.Logger,
		protocol:        cfg.Protocol,
		metricsReporter: cfg.MetricsReporter,
		dataReporter:    cfg.DataReporter,
		maxWorkers:      maxWorkers,
		// HTTP client for external config fetching only
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		leaderElector:    cfg.LeaderElector,
		observationQueue: cfg.ObservationQueue,
	}
}

// IsEnabled returns true if the health check executor is enabled.
func (e *HealthCheckExecutor) IsEnabled() bool {
	return e.config != nil && e.config.Enabled
}

// ShouldRunChecks returns true if this instance should run health checks.
// If leader election is configured and we're not the leader, returns false.
func (e *HealthCheckExecutor) ShouldRunChecks() bool {
	if !e.IsEnabled() {
		return false
	}

	// If leader election is configured, only run if we're the leader
	if e.leaderElector != nil && !e.leaderElector.IsLeader() {
		e.logger.Debug().Msg("Not leader, skipping health checks")
		return false
	}

	return true
}

// GetServiceConfigs returns the merged health check configurations for all services.
// This merges external (if configured) and local configs, with local taking precedence.
func (e *HealthCheckExecutor) GetServiceConfigs() []ServiceHealthCheckConfig {
	if e.config == nil {
		return nil
	}

	// If no external config, just return local
	if e.config.External == nil || e.config.External.URL == "" {
		return e.config.Local
	}

	// Get cached external configs
	e.externalConfigMu.RLock()
	externalConfigs := e.externalConfigs
	e.externalConfigMu.RUnlock()

	// If no external configs loaded yet or failed, return local only
	if len(externalConfigs) == 0 {
		return e.config.Local
	}

	// Merge external and local configs (local takes precedence)
	return e.mergeConfigs(externalConfigs, e.config.Local)
}

// GetConfigForService returns the health check configuration for a specific service.
// Returns nil if no config exists for the service.
func (e *HealthCheckExecutor) GetConfigForService(serviceID protocol.ServiceID) *ServiceHealthCheckConfig {
	// Get merged configs and search
	configs := e.GetServiceConfigs()
	for i := range configs {
		if configs[i].ServiceID == serviceID {
			return &configs[i]
		}
	}
	return nil
}

// InitExternalConfig initializes external config fetching.
// Should be called after NewHealthCheckExecutor to start loading external configs.
func (e *HealthCheckExecutor) InitExternalConfig(ctx context.Context) {
	if e.config == nil || e.config.External == nil || e.config.External.URL == "" {
		return
	}

	// Fetch initial config
	e.refreshExternalConfig(ctx)

	// Start periodic refresh if configured
	if e.config.External.RefreshInterval > 0 {
		e.stopRefresh = make(chan struct{})
		go e.startExternalConfigRefresh(ctx)
	}
}

// Stop stops the external config refresh goroutine if running.
func (e *HealthCheckExecutor) Stop() {
	if e.stopRefresh != nil {
		close(e.stopRefresh)
	}
}

// refreshExternalConfig fetches and parses the external config from the configured URL.
func (e *HealthCheckExecutor) refreshExternalConfig(ctx context.Context) {
	if e.config == nil || e.config.External == nil || e.config.External.URL == "" {
		return
	}

	externalURL := e.config.External.URL
	timeout := e.config.External.Timeout
	if timeout == 0 {
		timeout = DefaultExternalConfigTimeout
	}

	// Create request with timeout
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", externalURL, nil)
	if err != nil {
		e.logger.Error().
			Err(err).
			Str("url", externalURL).
			Msg("Failed to create external config request")
		e.setExternalConfigError(err)
		return
	}

	// Execute request
	resp, err := e.httpClient.Do(req)
	if err != nil {
		e.logger.Error().
			Err(err).
			Str("url", externalURL).
			Msg("Failed to fetch external health check config")
		e.setExternalConfigError(err)
		return
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("unexpected status code %d", resp.StatusCode)
		e.logger.Error().
			Err(err).
			Str("url", externalURL).
			Int("status_code", resp.StatusCode).
			Msg("Failed to fetch external health check config")
		e.setExternalConfigError(err)
		return
	}

	// Read body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		e.logger.Error().
			Err(err).
			Str("url", externalURL).
			Msg("Failed to read external health check config body")
		e.setExternalConfigError(err)
		return
	}

	// Parse YAML - expecting []ServiceHealthCheckConfig (same format as local:)
	var configs []ServiceHealthCheckConfig
	if err := yaml.Unmarshal(bodyBytes, &configs); err != nil {
		e.logger.Error().
			Err(err).
			Str("url", externalURL).
			Msg("Failed to parse external health check config YAML")
		e.setExternalConfigError(err)
		return
	}

	// Hydrate defaults and validate each config
	for i := range configs {
		configs[i].HydrateDefaults()
		if err := configs[i].Validate(); err != nil {
			e.logger.Warn().
				Err(err).
				Str("url", externalURL).
				Str("service_id", string(configs[i].ServiceID)).
				Msg("External config validation warning, skipping invalid service config")
			// Don't fail completely, just log the warning
			// Could also remove this service from configs if strict validation is needed
		}
	}

	// Store the configs
	e.externalConfigMu.Lock()
	e.externalConfigs = configs
	e.externalConfigError = nil
	e.externalConfigMu.Unlock()

	e.logger.Info().
		Str("url", externalURL).
		Int("service_count", len(configs)).
		Msg("Successfully loaded external health check config")
}

// setExternalConfigError stores an error from external config loading.
func (e *HealthCheckExecutor) setExternalConfigError(err error) {
	e.externalConfigMu.Lock()
	e.externalConfigError = err
	e.externalConfigMu.Unlock()
}

// startExternalConfigRefresh runs periodic refresh of external config.
func (e *HealthCheckExecutor) startExternalConfigRefresh(ctx context.Context) {
	if e.config == nil || e.config.External == nil || e.config.External.RefreshInterval <= 0 {
		return
	}

	ticker := time.NewTicker(e.config.External.RefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopRefresh:
			return
		case <-ticker.C:
			e.refreshExternalConfig(ctx)
		}
	}
}

// mergeConfigs merges external and local configs.
// Local configs take precedence over external configs.
// Merging is done at both the service level (entire service config) and
// the check level (individual checks within a service).
func (e *HealthCheckExecutor) mergeConfigs(
	external []ServiceHealthCheckConfig,
	local []ServiceHealthCheckConfig,
) []ServiceHealthCheckConfig {
	// Build a map of local configs by service ID for quick lookup
	localByService := make(map[protocol.ServiceID]*ServiceHealthCheckConfig)
	for i := range local {
		localByService[local[i].ServiceID] = &local[i]
	}

	// Start with all external configs as base
	result := make([]ServiceHealthCheckConfig, 0, len(external)+len(local))

	// Process external configs, merging with local where overlapping
	processedServices := make(map[protocol.ServiceID]struct{})

	for _, extSvc := range external {
		if localSvc, exists := localByService[extSvc.ServiceID]; exists {
			// Service exists in both - merge checks (local takes precedence)
			merged := e.mergeServiceConfigs(extSvc, *localSvc)
			result = append(result, merged)
			processedServices[extSvc.ServiceID] = struct{}{}
		} else {
			// Service only in external - use as-is
			result = append(result, extSvc)
		}
	}

	// Add any local-only services (not in external)
	for _, localSvc := range local {
		if _, processed := processedServices[localSvc.ServiceID]; !processed {
			result = append(result, localSvc)
		}
	}

	return result
}

// mergeServiceConfigs merges two service configs for the same service.
// Local config values take precedence over external config values.
func (e *HealthCheckExecutor) mergeServiceConfigs(
	external ServiceHealthCheckConfig,
	local ServiceHealthCheckConfig,
) ServiceHealthCheckConfig {
	merged := ServiceHealthCheckConfig{
		ServiceID: local.ServiceID,
	}

	// Use local values if set, otherwise use external
	if local.CheckInterval > 0 {
		merged.CheckInterval = local.CheckInterval
	} else {
		merged.CheckInterval = external.CheckInterval
	}

	if local.Enabled != nil {
		merged.Enabled = local.Enabled
	} else {
		merged.Enabled = external.Enabled
	}

	// Build a map of local checks by name
	localChecksByName := make(map[string]*HealthCheckConfig)
	for i := range local.Checks {
		localChecksByName[local.Checks[i].Name] = &local.Checks[i]
	}

	// Merge checks: local checks override external checks with same name
	mergedChecks := make([]HealthCheckConfig, 0, len(external.Checks)+len(local.Checks))
	processedCheckNames := make(map[string]struct{})

	// First, add all external checks (potentially overridden by local)
	for _, extCheck := range external.Checks {
		if localCheck, exists := localChecksByName[extCheck.Name]; exists {
			// Local check overrides external check
			mergedChecks = append(mergedChecks, *localCheck)
			processedCheckNames[extCheck.Name] = struct{}{}
		} else {
			// External check only
			mergedChecks = append(mergedChecks, extCheck)
		}
	}

	// Add any local-only checks (not in external)
	for _, localCheck := range local.Checks {
		if _, processed := processedCheckNames[localCheck.Name]; !processed {
			mergedChecks = append(mergedChecks, localCheck)
		}
	}

	merged.Checks = mergedChecks
	return merged
}

// ExecuteCheck runs a single health check against an endpoint and returns the result.
// Returns nil error if the check passes, or an error describing the failure.
//
// The check type determines the execution method:
//   - jsonrpc, rest: HTTP request with optional body and response validation
//   - websocket: WebSocket connection test with optional message exchange
//   - grpc: Not yet implemented (returns error)
func (e *HealthCheckExecutor) ExecuteCheck(
	ctx context.Context,
	endpointURL string,
	check HealthCheckConfig,
) error {
	// Skip disabled checks
	if check.Enabled != nil && !*check.Enabled {
		return nil
	}

	// Use check-specific timeout or context deadline
	checkCtx := ctx
	if check.Timeout > 0 {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(ctx, check.Timeout)
		defer cancel()
	}

	// Dispatch to appropriate handler based on check type
	switch check.Type {
	case HealthCheckTypeJSONRPC, HealthCheckTypeREST:
		result := e.executeHTTPCheck(checkCtx, endpointURL, check)
		return result.Error
	case HealthCheckTypeWebSocket:
		return e.executeWebSocketCheck(checkCtx, endpointURL, check)
	case HealthCheckTypeGRPC:
		return fmt.Errorf("grpc health checks are not yet implemented")
	default:
		return fmt.Errorf("unknown health check type: %s", check.Type)
	}
}

// executeHTTPCheckWithObservation runs an HTTP health check and submits the result to the observation queue.
// This is called by RunChecksForEndpoint to handle both validation and async observation processing.
func (e *HealthCheckExecutor) executeHTTPCheckWithObservation(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	endpointURL string,
	check HealthCheckConfig,
) error {
	// Skip disabled checks
	if check.Enabled != nil && !*check.Enabled {
		return nil
	}

	// Use check-specific timeout or context deadline
	checkCtx := ctx
	if check.Timeout > 0 {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(ctx, check.Timeout)
		defer cancel()
	}

	// Execute the HTTP check
	result := e.executeHTTPCheck(checkCtx, endpointURL, check)

	// Submit to observation queue (always, not sampled) for async parsing
	e.submitHealthCheckObservation(serviceID, endpointAddr, check, result)

	return result.Error
}

// submitHealthCheckObservation submits a health check response to the observation queue for async processing.
// Health checks are always submitted (not sampled) since they are already rate-limited by the check interval.
func (e *HealthCheckExecutor) submitHealthCheckObservation(
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	check HealthCheckConfig,
	result httpCheckResult,
) {
	// Skip if observation queue is not configured or not enabled
	if e.observationQueue == nil || !e.observationQueue.IsEnabled() {
		return
	}

	// Skip if we didn't get a response (connection error, etc.)
	if result.ResponseBody == nil && result.StatusCode == 0 {
		return
	}

	// Create the observation with health check context
	obs := &QueuedObservation{
		ServiceID:          serviceID,
		EndpointAddr:       endpointAddr,
		Source:             SourceHealthCheck, // Indicates this is from a health check
		Timestamp:          time.Now(),
		Latency:            result.Latency,
		RequestPath:        check.Path,
		RequestHTTPMethod:  check.Method,
		RequestBody:        []byte(check.Body),
		ResponseStatusCode: result.StatusCode,
		ResponseBody:       result.ResponseBody,
	}

	// Submit (not TryQueue) - health checks should always be processed
	e.observationQueue.Submit(obs)
}

// httpCheckResult contains the result of an HTTP health check, including
// the response data needed for async observation processing.
type httpCheckResult struct {
	StatusCode   int
	ResponseBody []byte
	Latency      time.Duration
	Error        error
}

// executeHTTPCheck performs an HTTP-based health check (jsonrpc or rest).
// It sends an HTTP request and validates the response status code and optionally the body.
// Returns the full result including response data for observation queue processing.
func (e *HealthCheckExecutor) executeHTTPCheck(
	ctx context.Context,
	endpointURL string,
	check HealthCheckConfig,
) httpCheckResult {
	result := httpCheckResult{}

	// Build the full URL
	fullURL := strings.TrimSuffix(endpointURL, "/") + check.Path

	// Create the request body
	var body io.Reader
	if check.Body != "" {
		body = bytes.NewBufferString(check.Body)
	}

	req, err := http.NewRequestWithContext(ctx, check.Method, fullURL, body)
	if err != nil {
		result.Error = fmt.Errorf("failed to create request: %w", err)
		return result
	}

	// Apply configured headers if provided
	if len(check.Headers) > 0 {
		for key, value := range check.Headers {
			req.Header.Set(key, value)
		}
	}

	// Set default Content-Type for POST requests with body if not explicitly configured
	if check.Method == "POST" && check.Body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Execute the request
	startTime := time.Now()
	resp, err := e.httpClient.Do(req)
	result.Latency = time.Since(startTime)

	if err != nil {
		result.Error = fmt.Errorf("request failed (latency=%v): %w", result.Latency, err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode

	// Always read the response body (needed for observation queue)
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = fmt.Errorf("failed to read response body: %w", err)
		return result
	}
	result.ResponseBody = bodyBytes

	// Check status code
	if resp.StatusCode != check.ExpectedStatusCode {
		result.Error = fmt.Errorf("unexpected status code: got %d, expected %d (latency=%v)",
			resp.StatusCode, check.ExpectedStatusCode, result.Latency)
		return result
	}

	// If ExpectedResponseContains is specified, validate the response body
	if check.ExpectedResponseContains != "" {
		if !strings.Contains(string(bodyBytes), check.ExpectedResponseContains) {
			result.Error = fmt.Errorf("response body does not contain expected string %q (latency=%v)",
				check.ExpectedResponseContains, result.Latency)
			return result
		}
	}

	return result
}

// executeWebSocketCheck performs a WebSocket health check.
//
// Behavior:
//   - If Body is empty: Connection-only test. Success if connection is established.
//   - If Body is provided: Send message and wait for response containing ExpectedResponseContains.
//     If ExpectedResponseContains is empty, any response is considered success.
//
// The check respects the configured Timeout for the entire operation (connect + send/receive).
func (e *HealthCheckExecutor) executeWebSocketCheck(
	ctx context.Context,
	endpointURL string,
	check HealthCheckConfig,
) error {
	// Build WebSocket URL
	wsURL, err := e.buildWebSocketURL(endpointURL, check.Path)
	if err != nil {
		return fmt.Errorf("failed to build websocket URL: %w", err)
	}

	// Create dialer with context
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	// Connect to WebSocket endpoint
	startTime := time.Now()
	conn, resp, err := dialer.DialContext(ctx, wsURL, nil)
	connectLatency := time.Since(startTime)

	if err != nil {
		if resp != nil {
			return fmt.Errorf("websocket connection failed with status %d (latency=%v): %w",
				resp.StatusCode, connectLatency, err)
		}
		return fmt.Errorf("websocket connection failed (latency=%v): %w", connectLatency, err)
	}
	defer conn.Close()

	e.logger.Debug().
		Str("url", wsURL).
		Dur("connect_latency", connectLatency).
		Msg("WebSocket connection established")

	// If no body provided, connection-only test succeeds here
	if check.Body == "" {
		return nil
	}

	// Send message
	if err := conn.WriteMessage(websocket.TextMessage, []byte(check.Body)); err != nil {
		return fmt.Errorf("failed to send websocket message: %w", err)
	}

	// Wait for response
	// If ExpectedResponseContains is specified, we keep reading messages until we find a match or timeout
	// If not specified, any message is considered success
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("websocket response timeout: %w", ctx.Err())
		default:
			// Set read deadline based on remaining context time
			if deadline, ok := ctx.Deadline(); ok {
				if err := conn.SetReadDeadline(deadline); err != nil {
					return fmt.Errorf("failed to set read deadline: %w", err)
				}
			}

			_, msgBytes, err := conn.ReadMessage()
			if err != nil {
				// Check if it's a timeout
				if ctx.Err() != nil {
					return fmt.Errorf("websocket response timeout: %w", ctx.Err())
				}
				return fmt.Errorf("failed to read websocket message: %w", err)
			}

			// If no specific response expected, any message is success
			if check.ExpectedResponseContains == "" {
				return nil
			}

			// Check if this message contains the expected string
			if strings.Contains(string(msgBytes), check.ExpectedResponseContains) {
				return nil
			}

			// Message didn't match, continue reading for more messages
			e.logger.Debug().
				Str("url", wsURL).
				Str("expected", check.ExpectedResponseContains).
				Int("msg_len", len(msgBytes)).
				Msg("WebSocket message received but didn't match expected, waiting for more")
		}
	}
}

// buildWebSocketURL converts an HTTP URL to a WebSocket URL (ws:// or wss://).
func (e *HealthCheckExecutor) buildWebSocketURL(endpointURL, path string) (string, error) {
	parsedURL, err := url.Parse(endpointURL)
	if err != nil {
		return "", err
	}

	// Convert http(s) to ws(s)
	switch parsedURL.Scheme {
	case "http":
		parsedURL.Scheme = "ws"
	case "https":
		parsedURL.Scheme = "wss"
	case "ws", "wss":
		// Already a WebSocket URL, keep as-is
	default:
		return "", fmt.Errorf("unsupported URL scheme: %s", parsedURL.Scheme)
	}

	// Append path
	parsedURL.Path = strings.TrimSuffix(parsedURL.Path, "/") + path

	return parsedURL.String(), nil
}

// RunChecksForEndpoint runs all configured checks for a service against a single endpoint.
// Returns a map of check name to error (nil if check passed).
//
// Each check uses the appropriate URL based on its type:
//   - jsonrpc, rest: Uses EndpointInfo.HTTPURL
//   - websocket: Uses EndpointInfo.WebSocketURL
func (e *HealthCheckExecutor) RunChecksForEndpoint(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpoint EndpointInfo,
) map[string]error {
	svcConfig := e.GetConfigForService(serviceID)
	if svcConfig == nil {
		return nil
	}

	// Skip disabled services
	if svcConfig.Enabled != nil && !*svcConfig.Enabled {
		return nil
	}

	results := make(map[string]error)
	for _, check := range svcConfig.Checks {
		// Get the appropriate URL for this check type
		endpointURL, err := endpoint.GetURLForCheckType(check.Type)
		if err != nil {
			// URL not available for this check type (e.g., no WebSocket URL)
			// Skip this check but don't record as failure
			e.logger.Debug().
				Str("service_id", string(serviceID)).
				Str("endpoint", string(endpoint.Addr)).
				Str("check", check.Name).
				Str("check_type", string(check.Type)).
				Err(err).
				Msg("Skipping health check - URL not available for check type")
			results[check.Name] = err
			continue
		}

		// Use executeHTTPCheckWithObservation for HTTP checks to capture response for async processing
		switch check.Type {
		case HealthCheckTypeJSONRPC, HealthCheckTypeREST:
			err = e.executeHTTPCheckWithObservation(ctx, serviceID, endpoint.Addr, endpointURL, check)
		default:
			// WebSocket, gRPC, etc. use standard ExecuteCheck
			err = e.ExecuteCheck(ctx, endpointURL, check)
		}
		results[check.Name] = err

		// Record the result to reputation
		e.recordCheckResult(ctx, serviceID, endpoint.Addr, check, err)
	}

	return results
}

// recordCheckResult records the health check result to the reputation system and metrics.
func (e *HealthCheckExecutor) recordCheckResult(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	check HealthCheckConfig,
	checkErr error,
) {
	key := reputation.NewEndpointKey(serviceID, endpointAddr)

	// Extract domain from endpoint address for metrics
	endpointDomain, err := shannonmetrics.ExtractDomainOrHost(string(endpointAddr))
	if err != nil {
		endpointDomain = shannonmetrics.ErrDomain
	}

	if checkErr == nil {
		// Check passed - record success
		signal := reputation.NewSuccessSignal(0)
		if err := e.reputationSvc.RecordSignal(ctx, key, signal); err != nil {
			e.logger.Warn().
				Err(err).
				Str("service_id", string(serviceID)).
				Str("endpoint", string(endpointAddr)).
				Str("check", check.Name).
				Msg("Failed to record success signal")
		}

		// Record successful health check metric (duration recorded separately via RecordHealthCheckWithDuration)
		healthcheckmetrics.RecordHealthCheckResult(
			string(serviceID),
			endpointDomain,
			check.Name,
			string(check.Type),
			true, // success
			"",   // no error
			0,    // duration will be recorded separately
		)
		return
	}

	// Check failed - record error signal based on configured severity
	signal := e.mapSignalType(check.ReputationSignal, checkErr.Error())
	if err := e.reputationSvc.RecordSignal(ctx, key, signal); err != nil {
		e.logger.Warn().
			Err(err).
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Msg("Failed to record error signal")
	}

	// Determine error type for metrics
	errorType := categorizeHealthCheckError(checkErr)

	// Record health check metric for failures
	healthcheckmetrics.RecordHealthCheckResult(
		string(serviceID),
		endpointDomain,
		check.Name,
		string(check.Type),
		false, // not success
		errorType,
		0, // duration will be recorded separately in ExecuteCheckViaProtocol
	)

	e.logger.Debug().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Str("signal", check.ReputationSignal).
		Str("error", checkErr.Error()).
		Msg("Health check failed, recorded signal")
}

// categorizeHealthCheckError categorizes a health check error for metrics.
func categorizeHealthCheckError(err error) string {
	if err == nil {
		return ""
	}
	errStr := err.Error()
	switch {
	case strings.Contains(errStr, "timeout"):
		return "timeout"
	case strings.Contains(errStr, "connection"):
		return "connection_error"
	case strings.Contains(errStr, "status code"):
		return "unexpected_status"
	case strings.Contains(errStr, "does not contain"):
		return "response_validation"
	case strings.Contains(errStr, "protocol"):
		return "protocol_error"
	default:
		return "unknown"
	}
}

// mapSignalType converts a configured signal type string to a reputation.Signal.
func (e *HealthCheckExecutor) mapSignalType(signalType string, reason string) reputation.Signal {
	switch signalType {
	case "minor_error":
		return reputation.NewMinorErrorSignal(reason)
	case "major_error":
		return reputation.NewMajorErrorSignal(reason, 0)
	case "critical_error":
		return reputation.NewCriticalErrorSignal(reason, 0)
	case "fatal_error":
		return reputation.NewFatalErrorSignal(reason)
	case "recovery_success":
		// This is only used internally for recovery, not configurable
		return reputation.NewRecoverySuccessSignal(0)
	default:
		// Default to minor_error for unknown types
		return reputation.NewMinorErrorSignal(reason)
	}
}

// RunAllChecks runs health checks for all configured services against provided endpoints.
// This is the main entry point called by the health check loop.
func (e *HealthCheckExecutor) RunAllChecks(
	ctx context.Context,
	getEndpoints func(protocol.ServiceID) ([]EndpointInfo, error),
) error {
	if !e.ShouldRunChecks() {
		return nil
	}

	serviceConfigs := e.GetServiceConfigs()
	if len(serviceConfigs) == 0 {
		e.logger.Debug().Msg("No health check configurations found")
		return nil
	}

	// Track statistics for summary logging
	startTime := time.Now()
	totalEndpoints := 0
	totalChecks := 0
	totalPassed := 0
	totalFailed := 0

	e.logger.Info().
		Int("service_count", len(serviceConfigs)).
		Msg("Starting health check cycle")

	for _, svcConfig := range serviceConfigs {
		if svcConfig.Enabled != nil && !*svcConfig.Enabled {
			continue
		}

		endpoints, err := getEndpoints(svcConfig.ServiceID)
		if err != nil {
			e.logger.Warn().
				Err(err).
				Str("service_id", string(svcConfig.ServiceID)).
				Msg("Failed to get endpoints for health checks")
			continue
		}

		if len(endpoints) == 0 {
			e.logger.Debug().
				Str("service_id", string(svcConfig.ServiceID)).
				Msg("No endpoints available for health checks")
			continue
		}

		e.logger.Info().
			Str("service_id", string(svcConfig.ServiceID)).
			Int("endpoint_count", len(endpoints)).
			Int("check_count", len(svcConfig.Checks)).
			Msg("Running health checks for service")

		for _, endpoint := range endpoints {
			totalEndpoints++
			results := e.RunChecksForEndpoint(ctx, svcConfig.ServiceID, endpoint)
			for checkName, checkErr := range results {
				totalChecks++
				if checkErr == nil {
					totalPassed++
				} else {
					totalFailed++
					e.logger.Info().
						Str("service_id", string(svcConfig.ServiceID)).
						Str("endpoint", string(endpoint.Addr)).
						Str("check", checkName).
						Str("error", checkErr.Error()).
						Msg("Health check failed")
				}
			}
		}
	}

	// Log summary
	duration := time.Since(startTime)
	e.logger.Info().
		Int("total_endpoints", totalEndpoints).
		Int("total_checks", totalChecks).
		Int("passed", totalPassed).
		Int("failed", totalFailed).
		Dur("duration", duration).
		Msg("Health check cycle completed")

	return nil
}

// EndpointInfo contains endpoint information needed for health checks.
// Each endpoint may have different URLs for different RPC types.
type EndpointInfo struct {
	// Addr is the unique identifier for the endpoint (supplier-url format).
	Addr protocol.EndpointAddr

	// HTTPURL is the URL for HTTP-based health checks (jsonrpc, rest).
	// This is the endpoint's public URL for JSON-RPC requests.
	HTTPURL string

	// WebSocketURL is the URL for WebSocket health checks.
	// May be empty if the endpoint doesn't support WebSocket.
	WebSocketURL string
}

// GetURLForCheckType returns the appropriate URL for the given health check type.
// Returns the HTTP URL for jsonrpc/rest checks, WebSocket URL for websocket checks.
// Returns an error if the required URL is not available.
func (e EndpointInfo) GetURLForCheckType(checkType HealthCheckType) (string, error) {
	switch checkType {
	case HealthCheckTypeJSONRPC, HealthCheckTypeREST:
		if e.HTTPURL == "" {
			return "", fmt.Errorf("HTTP URL not available for endpoint %s", e.Addr)
		}
		return e.HTTPURL, nil
	case HealthCheckTypeWebSocket:
		if e.WebSocketURL == "" {
			return "", fmt.Errorf("WebSocket URL not available for endpoint %s", e.Addr)
		}
		return e.WebSocketURL, nil
	case HealthCheckTypeGRPC:
		return "", fmt.Errorf("gRPC health checks are not yet implemented")
	default:
		return "", fmt.Errorf("unknown health check type: %s", checkType)
	}
}

// ExecuteCheckViaProtocol executes a health check through the protocol layer.
// This sends the health check as a synthetic relay request, testing the full path
// including relay miners, just like regular user requests.
//
// This is the preferred method for health checks as it validates the entire request path.
func (e *HealthCheckExecutor) ExecuteCheckViaProtocol(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	check HealthCheckConfig,
	serviceQoS QoSService,
) error {
	if e.protocol == nil {
		return fmt.Errorf("protocol not configured for health check executor")
	}

	// Skip disabled checks
	if check.Enabled != nil && !*check.Enabled {
		return nil
	}

	startTime := time.Now()

	// Create timeout context for the health check
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if check.Timeout > 0 {
		cancel()
		checkCtx, cancel = context.WithTimeout(ctx, check.Timeout)
	}
	defer cancel()

	// Build the service payload from the health check config
	servicePayload := e.buildServicePayload(check)

	e.logger.Info().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Str("method", check.Method).
		Str("path", check.Path).
		Msg("🔍 Sending health check relay to supplier")

	// Create the health check QoS context
	hcQoSCtx := NewHealthCheckQoSContext(HealthCheckQoSContextConfig{
		Logger:         e.logger,
		ServiceID:      serviceID,
		CheckConfig:    check,
		ServicePayload: servicePayload,
	})

	// Get a protocol request context for this endpoint
	// Passing nil for HTTP request since this is a synthetic request
	protocolCtx, protocolObs, err := e.protocol.BuildHTTPRequestContextForEndpoint(checkCtx, serviceID, endpointAddr, nil)
	if err != nil {
		e.logger.Warn().
			Err(err).
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Msg("Failed to build protocol context for health check")
		return fmt.Errorf("failed to build protocol context: %w", err)
	}

	// Execute the relay request through the protocol - this sends the actual relay to the supplier
	responses, relayErr := protocolCtx.HandleServiceRequest(hcQoSCtx.GetServicePayloads())
	latency := time.Since(startTime)

	// Process the response
	if relayErr != nil {
		e.logger.Warn().
			Err(relayErr).
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Dur("latency", latency).
			Msg("❌ Health check relay request failed")

		// Still publish observations for failed requests
		e.publishHealthCheckObservations(serviceID, endpointAddr, startTime, protocolCtx, &protocolObs)
		return relayErr
	}

	// Process responses through QoS context
	for _, response := range responses {
		hcQoSCtx.UpdateWithResponse(response.EndpointAddr, response.Bytes, response.HTTPStatusCode)

		e.logger.Info().
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Int("status_code", response.HTTPStatusCode).
			Int("response_size", len(response.Bytes)).
			Dur("latency", latency).
			Msg("✅ Health check relay response received from supplier")
	}

	// Publish observations for metrics (without calling ApplyObservations on QoS)
	e.publishHealthCheckObservations(serviceID, endpointAddr, startTime, protocolCtx, &protocolObs)

	// Check if the health check QoS context reports success
	if !hcQoSCtx.IsSuccess() {
		checkErr := fmt.Errorf("health check validation failed: %s", hcQoSCtx.GetError())
		e.logger.Warn().
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Str("error", hcQoSCtx.GetError()).
			Dur("latency", latency).
			Msg("⚠️ Health check response validation failed")
		return checkErr
	}

	e.logger.Info().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Dur("latency", latency).
		Msg("✅ Health check passed via protocol relay")

	return nil
}

// publishHealthCheckObservations publishes observations for a health check without
// calling ApplyObservations on QoS (which expects chain-specific observations).
func (e *HealthCheckExecutor) publishHealthCheckObservations(
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	startTime time.Time,
	protocolCtx ProtocolRequestContext,
	protocolObs *protocolobservations.Observations,
) {
	completedTime := time.Now()

	// Get protocol observations from context if not already provided
	var observations *protocolobservations.Observations
	if protocolObs != nil {
		observations = protocolObs
	} else if protocolCtx != nil {
		obs := protocolCtx.GetObservations()
		observations = &obs
	}

	// Apply protocol observations (for sanctioning, etc.)
	if observations != nil {
		if err := e.protocol.ApplyHTTPObservations(observations); err != nil {
			e.logger.Debug().Err(err).Msg("Failed to apply protocol observations for health check")
		}
	}

	// Build and publish request/response observations for metrics
	reqRespObs := &observation.RequestResponseObservations{
		ServiceId: string(serviceID),
		Gateway: &observation.GatewayObservations{
			RequestType:   observation.RequestType_REQUEST_TYPE_SYNTHETIC,
			ServiceId:     string(serviceID),
			ReceivedTime:  timestamppb.New(startTime),
			CompletedTime: timestamppb.New(completedTime),
		},
		Protocol: observations,
		// NOTE: We intentionally don't include QoS observations here because
		// HealthCheckQoSContext returns empty observations that cause "nil EVM observation" errors.
		// Health checks record results directly to the reputation system instead.
	}

	if e.metricsReporter != nil {
		e.metricsReporter.Publish(reqRespObs)
	}
	if e.dataReporter != nil {
		e.dataReporter.Publish(reqRespObs)
	}
}

// buildServicePayload creates a protocol.Payload from the health check configuration.
func (e *HealthCheckExecutor) buildServicePayload(check HealthCheckConfig) protocol.Payload {
	// Use configured headers if provided, otherwise default to Content-Type: application/json for POST
	headers := check.Headers
	if headers == nil {
		headers = make(map[string]string)
	}
	// Set default Content-Type for POST requests if not explicitly configured
	if check.Method == "POST" && headers["Content-Type"] == "" {
		headers["Content-Type"] = "application/json"
	}

	return protocol.Payload{
		Method:  check.Method,
		Path:    check.Path,
		Data:    check.Body,
		Headers: headers,
	}
}

// ExecuteWebSocketCheckViaProtocol executes a WebSocket health check through the protocol layer.
// This uses protocol.CheckWebsocketConnection() to test WebSocket connectivity.
//
// Enhancement over old hydrator: We now wrap protocol observations in RequestResponseObservations
// and publish to metrics/data reporters for full visibility into WebSocket health check results.
func (e *HealthCheckExecutor) ExecuteWebSocketCheckViaProtocol(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	check HealthCheckConfig,
) error {
	if e.protocol == nil {
		return fmt.Errorf("protocol not configured for health check executor")
	}

	// Skip disabled checks
	if check.Enabled != nil && !*check.Enabled {
		return nil
	}

	startTime := time.Now()

	e.logger.Debug().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Msg("Executing WebSocket health check via protocol")

	// Create timeout context for the health check
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if check.Timeout > 0 {
		cancel()
		checkCtx, cancel = context.WithTimeout(ctx, check.Timeout)
	}
	defer cancel()

	// Use protocol's CheckWebsocketConnection method
	protocolObs := e.protocol.CheckWebsocketConnection(checkCtx, serviceID, endpointAddr)

	// Apply observations to protocol (this updates reputation via observations)
	if protocolObs != nil {
		if err := e.protocol.ApplyWebSocketObservations(protocolObs); err != nil {
			e.logger.Warn().
				Err(err).
				Str("service_id", string(serviceID)).
				Str("endpoint", string(endpointAddr)).
				Str("check", check.Name).
				Msg("Failed to apply WebSocket observations")
		}

		// ENHANCEMENT: Wrap protocol observations in RequestResponseObservations
		// and publish to reporters for full visibility (old hydrator didn't do this)
		completedTime := time.Now()
		reqRespObs := &observation.RequestResponseObservations{
			ServiceId: string(serviceID),
			Gateway: &observation.GatewayObservations{
				RequestType:   observation.RequestType_REQUEST_TYPE_SYNTHETIC,
				ServiceId:     string(serviceID),
				ReceivedTime:  timestamppb.New(startTime),
				CompletedTime: timestamppb.New(completedTime),
			},
			Protocol: protocolObs,
		}

		// Publish to reporters for metrics and data pipeline visibility
		if e.metricsReporter != nil {
			e.metricsReporter.Publish(reqRespObs)
		}
		if e.dataReporter != nil {
			e.dataReporter.Publish(reqRespObs)
		}

		e.logger.Debug().
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Dur("latency", completedTime.Sub(startTime)).
			Msg("WebSocket health check observations published")
	}

	e.logger.Debug().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Msg("WebSocket health check completed via protocol")

	return nil
}

// RunChecksForEndpointViaProtocol runs all configured checks for a service through the protocol.
// This sends synthetic relay requests for each health check, testing the full relay path.
func (e *HealthCheckExecutor) RunChecksForEndpointViaProtocol(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	serviceQoS QoSService,
) map[string]error {
	svcConfig := e.GetConfigForService(serviceID)
	if svcConfig == nil {
		return nil
	}

	// Skip disabled services
	if svcConfig.Enabled != nil && !*svcConfig.Enabled {
		return nil
	}

	results := make(map[string]error)
	for _, check := range svcConfig.Checks {
		var err error

		switch check.Type {
		case HealthCheckTypeWebSocket:
			// WebSocket checks use the protocol's CheckWebsocketConnection
			err = e.ExecuteWebSocketCheckViaProtocol(ctx, serviceID, endpointAddr, check)
		case HealthCheckTypeGRPC:
			// gRPC checks not yet implemented
			e.logger.Debug().
				Str("service_id", string(serviceID)).
				Str("endpoint", string(endpointAddr)).
				Str("check", check.Name).
				Msg("Skipping gRPC check - not yet implemented")
			continue
		default:
			// HTTP-based checks (jsonrpc, rest)
			err = e.ExecuteCheckViaProtocol(ctx, serviceID, endpointAddr, check, serviceQoS)
		}

		results[check.Name] = err

		// Record the result to reputation
		e.recordCheckResult(ctx, serviceID, endpointAddr, check, err)
	}

	return results
}

// RunAllChecksViaProtocol runs health checks through the protocol layer for all configured services.
// This is the main entry point for protocol-based health checks.
func (e *HealthCheckExecutor) RunAllChecksViaProtocol(
	ctx context.Context,
	getEndpointAddrs func(protocol.ServiceID) ([]protocol.EndpointAddr, error),
	getServiceQoS func(protocol.ServiceID) QoSService,
) error {
	if !e.ShouldRunChecks() {
		return nil
	}

	if e.protocol == nil {
		e.logger.Warn().Msg("Protocol not configured, cannot run health checks via protocol")
		return fmt.Errorf("protocol not configured")
	}

	serviceConfigs := e.GetServiceConfigs()
	if len(serviceConfigs) == 0 {
		e.logger.Debug().Msg("No health check configurations found")
		return nil
	}

	e.logger.Info().
		Int("service_count", len(serviceConfigs)).
		Msg("Starting health checks via protocol")

	for _, svcConfig := range serviceConfigs {
		if svcConfig.Enabled != nil && !*svcConfig.Enabled {
			continue
		}

		endpoints, err := getEndpointAddrs(svcConfig.ServiceID)
		if err != nil {
			e.logger.Warn().
				Err(err).
				Str("service_id", string(svcConfig.ServiceID)).
				Msg("Failed to get endpoints for health checks")
			continue
		}

		if len(endpoints) == 0 {
			e.logger.Debug().
				Str("service_id", string(svcConfig.ServiceID)).
				Msg("No endpoints available for health checks")
			continue
		}

		serviceQoS := getServiceQoS(svcConfig.ServiceID)
		if serviceQoS == nil {
			e.logger.Warn().
				Str("service_id", string(svcConfig.ServiceID)).
				Msg("No QoS service available for health checks")
			continue
		}

		e.logger.Info().
			Str("service_id", string(svcConfig.ServiceID)).
			Int("endpoint_count", len(endpoints)).
			Int("check_count", len(svcConfig.Checks)).
			Msg("Running health checks for service via protocol")

		for _, endpointAddr := range endpoints {
			e.RunChecksForEndpointViaProtocol(ctx, svcConfig.ServiceID, endpointAddr, serviceQoS)
		}
	}

	return nil
}
