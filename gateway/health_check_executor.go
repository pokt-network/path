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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alitto/pond/v2"
	"github.com/pokt-network/poktroll/pkg/polylog"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/yaml.v3"

	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/observation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/heuristic"
	qostypes "github.com/pokt-network/path/qos/types"
	"github.com/pokt-network/path/reputation"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
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

	// httpClient is used for external config fetching only (not for health checks).
	httpClient *http.Client

	// leaderElector is optional - if nil, all instances run health checks
	leaderElector *LeaderElector

	// observationQueue handles async extraction of quality data from health check responses.
	// If set, health check responses are submitted for deep parsing (block height, chain ID, etc.)
	// This enables the same async processing pipeline for both user requests and health checks.
	observationQueue *ObservationQueue

	// qosInstances holds references to QoS instances for sync check validation.
	qosInstances map[protocol.ServiceID]QoSService

	// pool is the worker pool for concurrent health check execution.
	pool pond.Pool

	// External config caching (global)
	externalConfigMu    sync.RWMutex
	externalConfigs     []ServiceHealthCheckConfig
	externalConfigError error
	stopRefresh         chan struct{}
	stopOnce            sync.Once // Prevents double-close panic on stopRefresh channel

	// Per-service external config caching
	// Maps service ID to the list of health check configs fetched from that service's external URL
	perServiceExternalMu      sync.RWMutex
	perServiceExternalConfigs map[protocol.ServiceID][]HealthCheckConfig

	// unifiedServicesConfig provides per-service health check overrides from unified config.
	// Health checks defined here are merged with local/external configs.
	unifiedServicesConfig *UnifiedServicesConfig
}

// HealthCheckExecutorConfig contains configuration for creating a HealthCheckExecutor.
type HealthCheckExecutorConfig struct {
	Config                *ActiveHealthChecksConfig
	ReputationSvc         reputation.ReputationService
	Logger                polylog.Logger
	Protocol              Protocol
	MetricsReporter       RequestResponseReporter
	LeaderElector         *LeaderElector
	ObservationQueue      *ObservationQueue
	MaxWorkers            int
	UnifiedServicesConfig *UnifiedServicesConfig
}

// NewHealthCheckExecutor creates a new HealthCheckExecutor.
func NewHealthCheckExecutor(cfg HealthCheckExecutorConfig) *HealthCheckExecutor {
	maxWorkers := cfg.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 10 // Default number of concurrent workers
	}

	// Create worker pool for concurrent health check execution
	pool := pond.NewPool(maxWorkers)

	return &HealthCheckExecutor{
		config:          cfg.Config,
		reputationSvc:   cfg.ReputationSvc,
		logger:          cfg.Logger,
		protocol:        cfg.Protocol,
		metricsReporter: cfg.MetricsReporter,
		pool:            pool,
		// HTTP client for external config fetching only
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		leaderElector:             cfg.LeaderElector,
		observationQueue:          cfg.ObservationQueue,
		qosInstances:              make(map[protocol.ServiceID]QoSService),
		unifiedServicesConfig:     cfg.UnifiedServicesConfig,
		perServiceExternalConfigs: make(map[protocol.ServiceID][]HealthCheckConfig),
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

// SetQoSInstances sets the QoS instances for sync check validation.
// This should be called after QoS instances are created in the main application.
func (e *HealthCheckExecutor) SetQoSInstances(instances map[protocol.ServiceID]QoSService) {
	e.qosInstances = instances
}

// GetServiceConfigs returns the merged health check configurations for all services.
// This merges external (if configured), local configs, and unified services config,
// with the following precedence: unified services > local > external.
func (e *HealthCheckExecutor) GetServiceConfigs() []ServiceHealthCheckConfig {
	if e.config == nil {
		return e.getUnifiedServicesHealthChecks()
	}

	// Start with external configs if available
	var baseConfigs []ServiceHealthCheckConfig
	if e.config.External != nil && e.config.External.URL != "" {
		e.externalConfigMu.RLock()
		externalConfigs := e.externalConfigs
		e.externalConfigMu.RUnlock()
		if len(externalConfigs) > 0 {
			baseConfigs = externalConfigs
		}
	}

	// Merge with local configs (local takes precedence over external)
	if len(e.config.Local) > 0 {
		if len(baseConfigs) > 0 {
			baseConfigs = e.mergeConfigs(baseConfigs, e.config.Local)
		} else {
			baseConfigs = e.config.Local
		}
	}

	// Merge with unified services config (highest precedence)
	unifiedConfigs := e.getUnifiedServicesHealthChecks()
	if len(unifiedConfigs) > 0 {
		if len(baseConfigs) > 0 {
			return e.mergeConfigs(baseConfigs, unifiedConfigs)
		}
		return unifiedConfigs
	}

	return baseConfigs
}

// getUnifiedServicesHealthChecks extracts health check configs from unified services config.
// Converts ServiceHealthCheckOverride to ServiceHealthCheckConfig for each service.
func (e *HealthCheckExecutor) getUnifiedServicesHealthChecks() []ServiceHealthCheckConfig {
	if e.unifiedServicesConfig == nil {
		return nil
	}

	// Build a map of local configs for quick lookup of per-service check_interval
	localConfigMap := make(map[protocol.ServiceID]*ServiceHealthCheckConfig)
	if e.config != nil {
		for i := range e.config.Local {
			localConfigMap[e.config.Local[i].ServiceID] = &e.config.Local[i]
		}
	}

	var configs []ServiceHealthCheckConfig
	for _, svc := range e.unifiedServicesConfig.Services {
		// Get merged config (with defaults applied)
		merged := e.unifiedServicesConfig.GetMergedServiceConfig(svc.ID)
		if merged == nil || merged.HealthChecks == nil {
			continue
		}

		// Only include if health checks are enabled for this service
		if merged.HealthChecks.Enabled != nil && !*merged.HealthChecks.Enabled {
			continue
		}

		// Get per-service external checks if any
		e.perServiceExternalMu.RLock()
		externalChecks := e.perServiceExternalConfigs[svc.ID]
		e.perServiceExternalMu.RUnlock()

		// Get local checks
		localChecks := merged.HealthChecks.Local

		// Merge external + local (local takes precedence)
		var finalChecks []HealthCheckConfig
		if len(externalChecks) > 0 && len(localChecks) > 0 {
			// Merge: local overrides external with same name
			finalChecks = e.mergeHealthCheckConfigs(externalChecks, localChecks)
		} else if len(localChecks) > 0 {
			finalChecks = localChecks
		} else if len(externalChecks) > 0 {
			finalChecks = externalChecks
		}

		// Only add if we have checks
		if len(finalChecks) > 0 {
			// Determine check interval: local config takes precedence over unified config default
			checkInterval := merged.HealthChecks.Interval
			if localCfg, exists := localConfigMap[svc.ID]; exists && localCfg.CheckInterval > 0 {
				checkInterval = localCfg.CheckInterval
			}

			cfg := ServiceHealthCheckConfig{
				ServiceID:     svc.ID,
				CheckInterval: checkInterval,
				SyncAllowance: merged.HealthChecks.SyncAllowance,
				Checks:        finalChecks,
			}
			configs = append(configs, cfg)
		}
	}
	return configs
}

// mergeHealthCheckConfigs merges external and local health check configs.
// Local checks override external checks with the same name.
func (e *HealthCheckExecutor) mergeHealthCheckConfigs(external, local []HealthCheckConfig) []HealthCheckConfig {
	// Build a map of local checks by name
	localByName := make(map[string]*HealthCheckConfig, len(local))
	for i := range local {
		localByName[local[i].Name] = &local[i]
	}

	// Merge: external checks unless overridden by local
	merged := make([]HealthCheckConfig, 0, len(external)+len(local))
	processedNames := make(map[string]struct{})

	for _, extCheck := range external {
		if localCheck, exists := localByName[extCheck.Name]; exists {
			// Local overrides external
			merged = append(merged, *localCheck)
			processedNames[extCheck.Name] = struct{}{}
		} else {
			merged = append(merged, extCheck)
		}
	}

	// Add local-only checks (not in external)
	for _, localCheck := range local {
		if _, processed := processedNames[localCheck.Name]; !processed {
			merged = append(merged, localCheck)
		}
	}

	return merged
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
	// Fetch global external config if configured
	if e.config != nil && e.config.External != nil && e.config.External.URL != "" {
		// Fetch initial config
		e.refreshExternalConfig(ctx)

		// Start periodic refresh if configured
		if e.config.External.RefreshInterval > 0 {
			e.stopRefresh = make(chan struct{})
			go e.startExternalConfigRefresh(ctx)
		}
	}

	// Fetch per-service external configs
	e.refreshPerServiceExternalConfigs(ctx)
}

// Stop stops the external config refresh goroutine and worker pool.
// Safe to call multiple times - uses sync.Once to prevent double-close panic.
func (e *HealthCheckExecutor) Stop() {
	e.stopOnce.Do(func() {
		if e.stopRefresh != nil {
			close(e.stopRefresh)
		}
		if e.pool != nil {
			e.pool.StopAndWait()
		}
	})
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

	// Propagate sync_allowance from external configs to unified services config
	// This ensures the QoS layer uses the correct sync_allowance for endpoint selection
	syncAllowanceCount := 0
	if e.unifiedServicesConfig != nil {
		for _, cfg := range configs {
			if cfg.SyncAllowance != nil && *cfg.SyncAllowance > 0 {
				e.unifiedServicesConfig.SetServiceSyncAllowance(cfg.ServiceID, *cfg.SyncAllowance)
				syncAllowanceCount++
				e.logger.Info().
					Str("service_id", string(cfg.ServiceID)).
					Int("sync_allowance", *cfg.SyncAllowance).
					Msg("📏 Loaded sync_allowance from external health check rules")
			}
		}
	}

	e.logger.Info().
		Str("url", externalURL).
		Int("service_count", len(configs)).
		Int("sync_allowance_count", syncAllowanceCount).
		Msg("✅ Loaded external health check config")
}

// setExternalConfigError stores an error from external config loading.
func (e *HealthCheckExecutor) setExternalConfigError(err error) {
	e.externalConfigMu.Lock()
	e.externalConfigError = err
	e.externalConfigMu.Unlock()
}

// refreshPerServiceExternalConfigs fetches external configs for each service that has one configured.
// Per-service external configs are merged with their local checks in getUnifiedServicesHealthChecks.
func (e *HealthCheckExecutor) refreshPerServiceExternalConfigs(ctx context.Context) {
	if e.unifiedServicesConfig == nil {
		return
	}

	for _, svc := range e.unifiedServicesConfig.Services {
		merged := e.unifiedServicesConfig.GetMergedServiceConfig(svc.ID)
		if merged == nil || merged.HealthChecks == nil || merged.HealthChecks.External == nil {
			continue
		}

		ext := merged.HealthChecks.External
		if ext.URL == "" {
			continue
		}

		// Fetch the external config for this service
		checks, err := e.fetchExternalChecksForService(ctx, svc.ID, ext)
		if err != nil {
			e.logger.Warn().
				Err(err).
				Str("service_id", string(svc.ID)).
				Str("url", ext.URL).
				Msg("Failed to fetch per-service external health checks")
			continue
		}

		// Store in the per-service map
		e.perServiceExternalMu.Lock()
		e.perServiceExternalConfigs[svc.ID] = checks
		e.perServiceExternalMu.Unlock()

		e.logger.Info().
			Str("service_id", string(svc.ID)).
			Str("url", ext.URL).
			Int("check_count", len(checks)).
			Msg("Loaded per-service external health checks")
	}
}

// fetchExternalChecksForService fetches health check configs from a per-service external URL.
// Returns a list of HealthCheckConfig for that specific service.
func (e *HealthCheckExecutor) fetchExternalChecksForService(
	ctx context.Context,
	serviceID protocol.ServiceID,
	ext *ExternalConfigSource,
) ([]HealthCheckConfig, error) {
	timeout := ext.Timeout
	if timeout == 0 {
		timeout = DefaultExternalConfigTimeout
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", ext.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	// Parse YAML - expecting []HealthCheckConfig (list of checks for this service)
	var checks []HealthCheckConfig
	if err := yaml.Unmarshal(bodyBytes, &checks); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Hydrate defaults for each check
	for i := range checks {
		checks[i].HydrateDefaults()
	}

	return checks, nil
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

	if local.SyncAllowance != nil {
		merged.SyncAllowance = local.SyncAllowance
	} else {
		merged.SyncAllowance = external.SyncAllowance
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

// recordCheckResult records the health check result to the reputation system and metrics.
func (e *HealthCheckExecutor) recordCheckResult(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	check HealthCheckConfig,
	checkErr error,
	latency time.Duration,
) {
	// Convert health check type to RPC type for reputation tracking
	// HealthCheckType string values are lowercase (e.g., "json_rpc", "rest")
	// RPCType_value map keys are uppercase (e.g., "JSON_RPC", "REST")
	rpcType := sharedtypes.RPCType(sharedtypes.RPCType_value[strings.ToUpper(string(check.Type))])

	// Use the key builder to respect key_granularity setting (per-endpoint, per-domain, per-supplier)
	// This ensures health check signals are recorded to the same keys used by tiered selection
	keyBuilder := e.reputationSvc.KeyBuilderForService(serviceID)
	key := keyBuilder.BuildKey(serviceID, endpointAddr, rpcType)

	// Extract domain and supplier from endpoint address for metrics
	domain, err := shannonmetrics.ExtractDomainOrHost(string(endpointAddr))
	if err != nil {
		domain = shannonmetrics.ErrDomain
	}
	supplier := extractSupplierFromEndpoint(endpointAddr)
	rpcTypeStr := metrics.NormalizeRPCType(rpcType.String())

	if checkErr == nil {
		// Check passed - record recovery success signal with latency
		// Health checks use RecoverySuccessSignal (+5) because their purpose is to help
		// low-scoring endpoints recover. This provides moderate positive reinforcement
		// than regular SuccessSignal (+1) used by client requests, but requires
		// sustained good behavior to fully recover from critical errors.
		signal := reputation.NewRecoverySuccessSignal(latency)
		if err := e.reputationSvc.RecordSignal(ctx, key, signal); err != nil {
			e.logger.Warn().
				Err(err).
				Str("service_id", string(serviceID)).
				Str("endpoint", string(endpointAddr)).
				Str("check", check.Name).
				Msg("Failed to record recovery success signal")
		}

		// Record health check metric for success (per supplier)
		metrics.RecordHealthCheck(domain, supplier, rpcTypeStr, string(serviceID), check.Name, metrics.SignalOK)
		return
	}

	// Check failed - record error signal based on configured severity
	signal := e.mapSignalType(check.ReputationSignal, checkErr.Error(), latency)
	if err := e.reputationSvc.RecordSignal(ctx, key, signal); err != nil {
		e.logger.Warn().
			Err(err).
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Msg("Failed to record error signal")
	}

	// Record health check metric for failure (per supplier)
	metricSignal := mapReputationSignalToMetricSignal(check.ReputationSignal)
	metrics.RecordHealthCheck(domain, supplier, rpcTypeStr, string(serviceID), check.Name, metricSignal)

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

// mapReputationSignalToMetricSignal converts a configured signal string to a metrics signal constant.
func mapReputationSignalToMetricSignal(signalType string) string {
	switch signalType {
	case "minor_error":
		return metrics.SignalMinorError
	case "major_error":
		return metrics.SignalMajorError
	case "critical_error":
		return metrics.SignalCriticalError
	case "fatal_error":
		return metrics.SignalFatalError
	case "slow":
		return metrics.SignalSlow
	case "slow_asf", "very_slow":
		return metrics.SignalSlowASF
	default:
		return metrics.SignalOK
	}
}

// mapSignalType converts a configured signal type string to a reputation.Signal.
func (e *HealthCheckExecutor) mapSignalType(signalType string, reason string, latency time.Duration) reputation.Signal {
	switch signalType {
	case "minor_error":
		return reputation.NewMinorErrorSignal(reason)
	case "major_error":
		return reputation.NewMajorErrorSignal(reason, latency)
	case "critical_error":
		return reputation.NewCriticalErrorSignal(reason, latency)
	case "fatal_error":
		return reputation.NewFatalErrorSignal(reason)
	case "recovery_success":
		// This is only used internally for recovery, not configurable
		return reputation.NewRecoverySuccessSignal(latency)
	default:
		// Default to minor_error for unknown types
		return reputation.NewMinorErrorSignal(reason)
	}
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

	// SessionID is the session this endpoint belongs to.
	// Used to detect session rollover - if session is no longer active,
	// health checks for this endpoint should be skipped.
	SessionID string
}

// ExecuteCheckViaProtocol executes a health check through the protocol layer.
// This sends the health check as a synthetic relay request, testing the full path
// including relay miners, just like regular user requests.
//
// This is the preferred method for health checks as it validates the entire request path.
// syncAllowance is the service-level sync_allowance from health check config (0 = disabled).
func (e *HealthCheckExecutor) ExecuteCheckViaProtocol(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	check HealthCheckConfig,
	syncAllowance uint64,
) (time.Duration, error) {
	if e.protocol == nil {
		return 0, fmt.Errorf("protocol not configured for health check executor")
	}

	// Skip disabled checks
	if check.Enabled != nil && !*check.Enabled {
		return 0, nil
	}

	startTime := time.Now()

	// Create timeout context for the health check
	// Use configured timeout or default (5s for faster detection of unhealthy endpoints)
	timeout := DefaultHealthCheckTimeout
	if check.Timeout > 0 {
		timeout = check.Timeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build the service payload from the health check config
	servicePayload := e.buildServicePayload(check)

	e.logger.Debug().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Str("method", check.Method).
		Str("path", check.Path).
		Msg("Sending health check relay to supplier")

	// Create the health check QoS context
	hcQoSCtx := NewHealthCheckQoSContext(HealthCheckQoSContextConfig{
		Logger:         e.logger,
		ServiceID:      serviceID,
		CheckConfig:    check,
		ServicePayload: servicePayload,
	})

	// Get a protocol request context for this endpoint
	// Passing nil for HTTP request since this is a synthetic request
	// Use the RPC type from the health check payload for correct endpoint selection
	// filterByReputation=false: Health checks must reach ALL endpoints including low-scoring ones
	// This prevents death spiral where low-scoring endpoints can never recover
	protocolCtx, protocolObs, err := e.protocol.BuildHTTPRequestContextForEndpoint(checkCtx, serviceID, endpointAddr, servicePayload.RPCType, nil, false)
	if err != nil {
		// Log at DEBUG - this is expected during session rollover when the endpoint
		// was collected from an old session that has since expired
		e.logger.Debug().
			Err(err).
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Msg("Failed to build protocol context for health check - endpoint may have left session")
		return time.Since(startTime), fmt.Errorf("failed to build protocol context: %w", err)
	}

	// Execute the relay request through the protocol - this sends the actual relay to the supplier
	responses, relayErr := protocolCtx.HandleServiceRequest(hcQoSCtx.GetServicePayloads())
	latency := time.Since(startTime)

	// Extract domain for metrics
	domain, domainErr := shannonmetrics.ExtractDomainOrHost(string(endpointAddr))
	if domainErr != nil {
		domain = shannonmetrics.ErrDomain
	}
	rpcTypeStr := metrics.NormalizeRPCType(servicePayload.RPCType.String())

	// Process the response
	if relayErr != nil {
		// Debug level - health check failures are expected and handled by reputation system
		e.logger.Debug().
			Err(relayErr).
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Dur("latency", latency).
			Msg("Health check relay request failed")

		// Record relay metric for failed request
		metrics.RecordRelay(domain, rpcTypeStr, string(serviceID), "error", metrics.SignalMajorError, metrics.RelayTypeHealthCheck, latency.Seconds())

		// Still publish observations for failed requests
		e.publishHealthCheckObservations(serviceID, endpointAddr, startTime, protocolCtx, &protocolObs)
		return latency, relayErr
	}

	// Process responses through QoS context
	var httpStatusCode int
	var responseBody []byte
	for _, response := range responses {
		hcQoSCtx.UpdateWithResponse(response.EndpointAddr, response.Bytes, response.HTTPStatusCode, response.RequestID)
		httpStatusCode = response.HTTPStatusCode
		responseBody = response.Bytes

		e.logger.Debug().
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Int("status_code", response.HTTPStatusCode).
			Int("response_size", len(response.Bytes)).
			Dur("latency", latency).
			Msg("Health check relay response received from supplier")
	}

	// Heuristic analysis - detect bad gateway, empty responses, and other error patterns
	// This runs BEFORE QoS validation to catch issues the basic validation might miss
	heuristicResult := heuristic.Analyze(responseBody, httpStatusCode, servicePayload.RPCType)
	if heuristicResult.ShouldRetry {
		heuristicErr := fmt.Errorf("heuristic detected error: %s - %s", heuristicResult.Reason, heuristicResult.Details)
		e.logger.Debug().
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Str("heuristic_reason", heuristicResult.Reason).
			Float64("heuristic_confidence", heuristicResult.Confidence).
			Str("heuristic_details", heuristicResult.Details).
			Int("response_size", len(responseBody)).
			Dur("latency", latency).
			Msg("Health check failed heuristic analysis - bad gateway, empty response, or error pattern detected")

		// Record relay metric with appropriate signal based on heuristic confidence
		statusCodeStr := metrics.GetStatusCodeCategory(httpStatusCode)
		signalType := metrics.SignalMinorError
		if heuristicResult.Confidence >= 0.95 {
			signalType = metrics.SignalMajorError
		}
		metrics.RecordRelay(domain, rpcTypeStr, string(serviceID), statusCodeStr, signalType, metrics.RelayTypeHealthCheck, latency.Seconds())

		// Publish observations even for heuristic failures
		e.publishHealthCheckObservations(serviceID, endpointAddr, startTime, protocolCtx, &protocolObs)
		return latency, heuristicErr
	}

	// Process observation SYNCHRONOUSLY for block height extraction
	// Health checks run in background, so no need for async queue - process immediately
	e.processObservationSync(serviceID, endpointAddr, check, servicePayload, startTime, latency, httpStatusCode, responseBody)

	// Publish observations for metrics (without calling ApplyObservations on QoS)
	e.publishHealthCheckObservations(serviceID, endpointAddr, startTime, protocolCtx, &protocolObs)

	// Check if the health check QoS context reports success
	if !hcQoSCtx.IsSuccess() {
		checkErr := fmt.Errorf("health check validation failed: %s", hcQoSCtx.GetError())
		// Debug level - health check failures are expected and handled by reputation system
		e.logger.Debug().
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Str("error", hcQoSCtx.GetError()).
			Dur("latency", latency).
			Msg("Health check response validation failed")

		// Record relay metric for validation failure (relay succeeded but validation failed)
		statusCodeStr := metrics.GetStatusCodeCategory(httpStatusCode)
		metrics.RecordRelay(domain, rpcTypeStr, string(serviceID), statusCodeStr, metrics.SignalMinorError, metrics.RelayTypeHealthCheck, latency.Seconds())

		return latency, checkErr
	}

	// Perform sync check validation if enabled
	// Uses the service's sync_allowance (passed as parameter) to validate block height against perceived block number
	if check.SyncCheck && syncAllowance > 0 {
		if err := e.validateSyncCheck(serviceID, responseBody, syncAllowance); err != nil {
			// Sync check validation failed
			e.logger.Debug().
				Err(err).
				Str("service_id", string(serviceID)).
				Str("endpoint", string(endpointAddr)).
				Str("check", check.Name).
				Uint64("sync_allowance", syncAllowance).
				Dur("latency", latency).
				Msg("Sync check failed - endpoint block height outside sync allowance")

			// Record relay metric for sync check failure
			statusCodeStr := metrics.GetStatusCodeCategory(httpStatusCode)
			signalType := metrics.SignalMajorError
			if check.ReputationSignal != "" {
				signalType = check.ReputationSignal
			}
			metrics.RecordRelay(domain, rpcTypeStr, string(serviceID), statusCodeStr, signalType, metrics.RelayTypeHealthCheck, latency.Seconds())

			return latency, err
		}
	}

	// Perform error detection if configured
	if check.ErrorDetection != nil {
		if detectedSignal, detectedError := e.detectKnownErrors(check.ErrorDetection, httpStatusCode, responseBody); detectedError != nil {
			// Known error pattern detected
			e.logger.Debug().
				Err(detectedError).
				Str("service_id", string(serviceID)).
				Str("endpoint", string(endpointAddr)).
				Str("check", check.Name).
				Int("status_code", httpStatusCode).
				Str("detected_signal", detectedSignal).
				Dur("latency", latency).
				Msg("Known error pattern detected")

			// Record relay metric for error detection
			statusCodeStr := metrics.GetStatusCodeCategory(httpStatusCode)
			metrics.RecordRelay(domain, rpcTypeStr, string(serviceID), statusCodeStr, detectedSignal, metrics.RelayTypeHealthCheck, latency.Seconds())

			return latency, detectedError
		}
	}

	// Record relay metric for successful health check
	statusCodeStr := metrics.GetStatusCodeCategory(httpStatusCode)
	metrics.RecordRelay(domain, rpcTypeStr, string(serviceID), statusCodeStr, metrics.SignalOK, metrics.RelayTypeHealthCheck, latency.Seconds())

	// Try to unblacklist the supplier if it was previously blacklisted
	// Extract supplier address from endpoint address (format: "supplierAddr-url")
	supplierAddr := string(endpointAddr)
	if dashIndex := strings.Index(supplierAddr, "-"); dashIndex > 0 {
		supplierAddr = supplierAddr[:dashIndex]
	}
	if e.protocol.UnblacklistSupplier(serviceID, supplierAddr) {
		e.logger.Info().
			Str("service_id", string(serviceID)).
			Str("supplier", supplierAddr).
			Str("domain", domain).
			Msg("Supplier unblacklisted after successful health check")
	}

	// If this was an archival health check that passed all validations (including error_detection),
	// mark the endpoint as archival-capable. This is done explicitly AFTER all checks pass to avoid
	// marking endpoints that returned "false success" responses (like "0x0" for archival queries).
	if check.Archival {
		e.markEndpointArchival(serviceID, endpointAddr)
	}

	e.logger.Debug().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Bool("archival_check", check.Archival).
		Dur("latency", latency).
		Msg("Health check passed via protocol relay")

	return latency, nil
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
}

// processObservationSync processes a health check response SYNCHRONOUSLY for block height extraction.
// Unlike user requests that use the async observation queue, health checks already run in background
// workers, so there's no need to add another async hop. Process immediately for faster updates.
func (e *HealthCheckExecutor) processObservationSync(
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	check HealthCheckConfig,
	payload protocol.Payload,
	startTime time.Time,
	latency time.Duration,
	httpStatusCode int,
	responseBody []byte,
) {
	// Skip if observation queue is not configured (we use it for access to registry + handler)
	if e.observationQueue == nil {
		return
	}

	// Skip if the queue is not enabled
	if !e.observationQueue.IsEnabled() {
		return
	}

	obs := &QueuedObservation{
		ServiceID:          serviceID,
		EndpointAddr:       endpointAddr,
		Source:             SourceHealthCheck,
		IsArchivalCheck:    check.Archival,
		Timestamp:          startTime,
		Latency:            latency,
		RequestPath:        payload.Path,
		RequestHTTPMethod:  payload.Method,
		RequestBody:        []byte(payload.Data),
		ResponseStatusCode: httpStatusCode,
		ResponseBody:       responseBody,
	}

	// Process SYNCHRONOUSLY - health checks are already in background workers
	e.observationQueue.ProcessSync(obs)

	e.logger.Debug().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Msg("Health check observation processed synchronously for block height extraction")
}

// markEndpointArchival marks an endpoint as archival-capable via the QoS instance.
// This is called ONLY after an archival health check passes ALL validations including error_detection.
// This ensures we don't mark endpoints that returned "false success" responses (e.g., "0x0").
func (e *HealthCheckExecutor) markEndpointArchival(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr) {
	qosInstance, exists := e.qosInstances[serviceID]
	if !exists || qosInstance == nil {
		e.logger.Debug().
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Msg("No QoS instance found for archival marking")
		return
	}

	// Create extracted data with only IsArchival set
	data := &qostypes.ExtractedData{
		IsArchival: true,
	}

	if err := qosInstance.UpdateFromExtractedData(endpointAddr, data); err != nil {
		e.logger.Warn().
			Err(err).
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Msg("Failed to mark endpoint as archival-capable")
		return
	}

	e.logger.Info().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Msg("📜 Endpoint marked as archival-capable from health check")
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

	// Convert health check Type (now aligned with rpc_types) to RPCType enum
	mapper := NewRPCTypeMapper()
	rpcType, err := mapper.ParseRPCType(string(check.Type))
	if err != nil {
		// Should never happen if config validation passed
		e.logger.Error().
			Str("check_name", check.Name).
			Str("check_type", string(check.Type)).
			Err(err).
			Msg("Invalid health check type - using UNKNOWN_RPC")
		rpcType = sharedtypes.RPCType_UNKNOWN_RPC
	}

	return protocol.Payload{
		Method:  check.Method,
		Path:    check.Path,
		Data:    check.Body,
		Headers: headers,
		RPCType: rpcType, // Set from aligned health check type
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
) (time.Duration, error) {
	if e.protocol == nil {
		return 0, fmt.Errorf("protocol not configured for health check executor")
	}

	// Skip disabled checks
	if check.Enabled != nil && !*check.Enabled {
		return 0, nil
	}

	startTime := time.Now()

	e.logger.Debug().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Msg("Executing WebSocket health check via protocol")

	// Create timeout context for the health check
	// Use configured timeout or default (5s for faster detection of unhealthy endpoints)
	timeout := DefaultHealthCheckTimeout
	if check.Timeout > 0 {
		timeout = check.Timeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
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

		e.logger.Debug().
			Str("service_id", string(serviceID)).
			Str("endpoint", string(endpointAddr)).
			Str("check", check.Name).
			Dur("latency", completedTime.Sub(startTime)).
			Msg("WebSocket health check observations published")
	}

	latency := time.Since(startTime)
	e.logger.Debug().
		Str("service_id", string(serviceID)).
		Str("endpoint", string(endpointAddr)).
		Str("check", check.Name).
		Dur("latency", latency).
		Msg("WebSocket health check completed via protocol")

	return latency, nil
}

// RunChecksForEndpointViaProtocol runs all configured checks for a service through the protocol.
// This sends synthetic relay requests for each health check, testing the full relay path.
func (e *HealthCheckExecutor) RunChecksForEndpointViaProtocol(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
) map[string]error {
	svcConfig := e.GetConfigForService(serviceID)
	if svcConfig == nil {
		return nil
	}

	// Skip disabled services
	if svcConfig.Enabled != nil && !*svcConfig.Enabled {
		return nil
	}

	// Get sync_allowance from service config (0 = disabled)
	var syncAllowance uint64
	if svcConfig.SyncAllowance != nil && *svcConfig.SyncAllowance > 0 {
		syncAllowance = uint64(*svcConfig.SyncAllowance)
	}

	results := make(map[string]error)
	for _, check := range svcConfig.Checks {
		var err error
		var latency time.Duration

		switch check.Type {
		case HealthCheckTypeWebSocket:
			// WebSocket checks use the protocol's CheckWebsocketConnection
			latency, err = e.ExecuteWebSocketCheckViaProtocol(ctx, serviceID, endpointAddr, check)
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
			latency, err = e.ExecuteCheckViaProtocol(ctx, serviceID, endpointAddr, check, syncAllowance)
		}

		results[check.Name] = err

		// Record the result to reputation with latency
		e.recordCheckResult(ctx, serviceID, endpointAddr, check, err, latency)
	}

	return results
}

// RunAllChecksViaProtocol runs health checks through the protocol layer for all configured services.
// This is the main entry point for protocol-based health checks.
// Health checks are executed in parallel using a pond worker pool.
func (e *HealthCheckExecutor) RunAllChecksViaProtocol(
	ctx context.Context,
	getEndpointInfos func(protocol.ServiceID) ([]EndpointInfo, error),
) error {
	if !e.ShouldRunChecks() {
		return nil
	}

	if e.protocol == nil {
		e.logger.Warn().Msg("Protocol not configured, cannot run health checks via protocol")
		return fmt.Errorf("protocol not configured")
	}

	if e.pool == nil {
		e.logger.Warn().Msg("Worker pool not initialized, cannot run health checks")
		return fmt.Errorf("worker pool not initialized")
	}

	serviceConfigs := e.GetServiceConfigs()
	if len(serviceConfigs) == 0 {
		e.logger.Debug().Msg("No health check configurations found")
		return nil
	}

	// Create a task group to track all submitted jobs
	group := e.pool.NewGroup()
	totalJobs := 0

	// Submit health check jobs to the worker pool
	for _, svcConfig := range serviceConfigs {
		if svcConfig.Enabled != nil && !*svcConfig.Enabled {
			continue
		}

		endpointInfos, err := getEndpointInfos(svcConfig.ServiceID)
		if err != nil {
			e.logger.Warn().
				Err(err).
				Str("service_id", string(svcConfig.ServiceID)).
				Msg("Failed to get endpoints for health checks")
			continue
		}

		if len(endpointInfos) == 0 {
			e.logger.Debug().
				Str("service_id", string(svcConfig.ServiceID)).
				Msg("No endpoints available for health checks")
			continue
		}

		// Submit a job for each endpoint
		for _, endpointInfo := range endpointInfos {
			// Capture loop variables for closure
			serviceID := svcConfig.ServiceID
			endpoint := endpointInfo.Addr
			sessionID := endpointInfo.SessionID

			group.Submit(func() {
				select {
				case <-ctx.Done():
					return
				default:
					// Check if session is still active before executing health check
					// This prevents errors when session has rolled over between collection and execution
					if sessionID != "" && e.protocol != nil {
						if !e.protocol.IsSessionActive(ctx, serviceID, sessionID) {
							// Session no longer active - skip silently
							// This is expected during session rollover
							return
						}
					}
					e.RunChecksForEndpointViaProtocol(ctx, serviceID, endpoint)
				}
			})
			totalJobs++
		}
	}

	if totalJobs == 0 {
		e.logger.Debug().Msg("No health check jobs to execute")
		return nil
	}

	e.logger.Info().
		Int("service_count", len(serviceConfigs)).
		Int("total_jobs", totalJobs).
		Int("pool_running", int(e.pool.RunningWorkers())).
		Msg("Starting health checks via protocol with pond pool")

	// Wait for all jobs to complete
	// group.Wait() returns an error only if context is canceled
	if err := group.Wait(); err != nil {
		e.logger.Warn().Err(err).
			Int("total_jobs", totalJobs).
			Msg("Health check cycle interrupted")
		// Don't return error - allow health check loop to continue on next cycle
	}

	e.logger.Info().
		Int("total_jobs", totalJobs).
		Msg("Health check cycle completed")

	return nil
}

// detectKnownErrors checks for known error patterns in health check responses.
// Returns (reputationSignal, error) if a known error is detected, or ("", nil) if no errors detected.
func (e *HealthCheckExecutor) detectKnownErrors(
	errorDetection *ErrorDetection,
	httpStatusCode int,
	responseBody []byte,
) (string, error) {
	if errorDetection == nil {
		return "", nil
	}

	// Check HTTP status codes
	for _, statusCodeCheck := range errorDetection.StatusCodes {
		if httpStatusCode == statusCodeCheck.Code {
			return statusCodeCheck.ReputationSignal, fmt.Errorf("detected error status code: %d", statusCodeCheck.Code)
		}
	}

	// Convert response body to lowercase string for case-insensitive matching
	responseStr := strings.ToLower(string(responseBody))

	// Check response body patterns
	for _, patternCheck := range errorDetection.ResponsePatterns {
		if strings.Contains(responseStr, strings.ToLower(patternCheck.Pattern)) {
			return patternCheck.ReputationSignal, fmt.Errorf("detected error pattern in response: %s", patternCheck.Pattern)
		}
	}

	// Check JSON-RPC error codes if the response looks like JSON-RPC
	if len(errorDetection.JSONRPCErrorCodes) > 0 && len(responseBody) > 0 {
		// Try to parse as JSON-RPC response
		var jsonRPCResp struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(responseBody, &jsonRPCResp); err == nil && jsonRPCResp.Error != nil {
			// Found a JSON-RPC error - check if it matches any configured error codes
			for _, errorCodeCheck := range errorDetection.JSONRPCErrorCodes {
				if jsonRPCResp.Error.Code == errorCodeCheck.Code {
					return errorCodeCheck.ReputationSignal, fmt.Errorf("detected JSON-RPC error code: %d", errorCodeCheck.Code)
				}
			}
		}
	}

	return "", nil
}

// validateSyncCheck validates that the endpoint's block height is within sync_allowance
// of the perceived block number from the QoS instance.
func (e *HealthCheckExecutor) validateSyncCheck(
	serviceID protocol.ServiceID,
	responseBody []byte,
	syncAllowance uint64,
) error {
	// Extract block height from response
	endpointHeight, err := extractBlockHeight(responseBody)
	if err != nil {
		return fmt.Errorf("failed to extract block height: %w", err)
	}

	// Block height 0 is always invalid
	if endpointHeight == 0 {
		return fmt.Errorf("endpoint returned block height 0 - invalid or unsynced node")
	}

	// Get perceived block number from QoS instance
	qos, exists := e.qosInstances[serviceID]
	if !exists {
		// No QoS instance - skip sync check
		e.logger.Debug().
			Str("service_id", string(serviceID)).
			Msg("No QoS instance for sync check, skipping")
		return nil
	}

	perceivedGetter, ok := qos.(interface{ GetPerceivedBlockNumber() uint64 })
	if !ok {
		// QoS doesn't support perceived block number - skip sync check
		return nil
	}

	perceivedHeight := perceivedGetter.GetPerceivedBlockNumber()
	if perceivedHeight == 0 {
		// Perceived not yet available - skip sync check
		e.logger.Debug().
			Str("service_id", string(serviceID)).
			Msg("Perceived block number not available, skipping sync check")
		return nil
	}

	// Check if endpoint is within sync allowance
	minAllowedHeight := int64(perceivedHeight) - int64(syncAllowance)
	if minAllowedHeight < 0 {
		minAllowedHeight = 0
	}

	if endpointHeight < minAllowedHeight {
		blocksBehind := int64(perceivedHeight) - endpointHeight
		return fmt.Errorf(
			"sync check failed: endpoint height %d is %d blocks behind perceived %d (allowance: %d)",
			endpointHeight,
			blocksBehind,
			perceivedHeight,
			syncAllowance,
		)
	}

	return nil
}

// extractBlockHeight extracts the block height from a JSON-RPC response.
// Supports various response formats:
// - {"result": "0x1940c6f5"} (EVM eth_blockNumber)
// - {"result": {"sync_info": {"latest_block_height": "12345"}}} (Cosmos status)
// - {"result": 12345} (numeric result)
func extractBlockHeight(responseBody []byte) (int64, error) {
	var response map[string]interface{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return 0, fmt.Errorf("invalid JSON: %w", err)
	}

	// Check for JSON-RPC error
	if errField, exists := response["error"]; exists && errField != nil {
		return 0, fmt.Errorf("JSON-RPC error in response")
	}

	result, exists := response["result"]
	if !exists {
		return 0, fmt.Errorf("no result field in response")
	}

	// Try different formats
	switch v := result.(type) {
	case string:
		// EVM format: "0x1940c6f5"
		return parseHexBlockNumber(v)

	case float64:
		// Already a number
		return int64(v), nil

	case map[string]interface{}:
		// Cosmos format: {"sync_info": {"latest_block_height": "12345"}}
		if syncInfo, ok := v["sync_info"].(map[string]interface{}); ok {
			if heightStr, ok := syncInfo["latest_block_height"].(string); ok {
				height, err := strconv.ParseInt(heightStr, 10, 64)
				if err != nil {
					return 0, fmt.Errorf("invalid block height in sync_info: %w", err)
				}
				return height, nil
			}
		}
		return 0, fmt.Errorf("unsupported response format: object without sync_info")

	default:
		return 0, fmt.Errorf("unsupported result type: %T", v)
	}
}

// parseHexBlockNumber parses a hex string block number (e.g., "0x1940c6f5").
func parseHexBlockNumber(hexStr string) (int64, error) {
	// Remove 0x prefix if present
	if len(hexStr) > 2 && hexStr[:2] == "0x" {
		hexStr = hexStr[2:]
	}
	height, err := strconv.ParseInt(hexStr, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid hex block number: %w", err)
	}
	return height, nil
}
