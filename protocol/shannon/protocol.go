package shannon

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alitto/pond/v2"
	"github.com/pokt-network/poktroll/pkg/polylog"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/health"
	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/metrics/devtools"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	pathhttp "github.com/pokt-network/path/network/http"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
	"github.com/pokt-network/path/request"
)

// parseAllowedSuppliersHeader extracts and parses the Target-Suppliers header from the HTTP request.
// Returns a slice of supplier addresses, or nil if the header is not present or empty.
func parseAllowedSuppliersHeader(httpReq *http.Request) []string {
	if httpReq == nil {
		return nil
	}

	headerValue := httpReq.Header.Get(request.HTTPHeaderTargetSuppliers)
	if headerValue == "" {
		return nil
	}

	// Split by comma and trim whitespace
	suppliers := strings.Split(headerValue, ",")
	result := make([]string, 0, len(suppliers))
	for _, supplier := range suppliers {
		trimmed := strings.TrimSpace(supplier)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

// gateway package's Protocol interface is fulfilled by the Protocol struct
// below using methods that are specific to Shannon.
var _ gateway.Protocol = &Protocol{}

// Shannon protocol implements the health.Check and health.ServiceIDReporter interfaces.
// This allows the protocol to report its health status and the list of service IDs it is configured for.
var (
	_ health.Check             = &Protocol{}
	_ health.ServiceIDReporter = &Protocol{}
)

// devtools.ProtocolDisqualifiedEndpointsReporter is fulfilled by the Protocol struct below.
// This allows the protocol to report its disqualified endpoints data to the devtools.DisqualifiedEndpointReporter.
var _ devtools.ProtocolDisqualifiedEndpointsReporter = &Protocol{}

// Protocol provides the functionality needed by the gateway package for sending a relay to a specific endpoint.
type Protocol struct {
	logger polylog.Logger
	FullNode

	// gatewayMode is the gateway mode in which the current instance of the Shannon protocol integration operates.
	// See protocol/shannon/gateway_mode.go for more details.
	gatewayMode protocol.GatewayMode

	// gatewayAddr is used by the SDK for selecting onchain applications which have delegated to the gateway.
	// The gateway can only sign relays on behalf of an application if the application has an active delegation to it.
	gatewayAddr string

	// gatewayPrivateKeyHex stores the private key of the gateway running this Shannon Gateway instance.
	// It is used for signing relay request in both Centralized and Delegated Gateway Modes.
	gatewayPrivateKeyHex string

	// ownedApps is the list of apps owned by the gateway operator
	ownedApps map[protocol.ServiceID][]string

	// HTTP client used for sending relay requests to endpoints while also capturing & publishing various debug metrics.
	httpClient *pathhttp.HTTPClientWithDebugMetrics

	// relayPool is a shared worker pool for parallel relay processing.
	// Initialized once and reused across all requests to bound global concurrency.
	relayPool pond.Pool

	// serviceFallbackMap contains the service fallback config per service.
	//
	// The fallback endpoints are used when no endpoints are available for the
	// requested service from the onchain protocol.
	//
	// For example, if all protocol endpoints are filtered out (low reputation), the fallback
	// endpoints will be used to populate the list of endpoints.
	//
	// Each service can have a SendAllTraffic flag to send all traffic to
	// fallback endpoints, regardless of the health of the protocol endpoints.
	serviceFallbackMap map[protocol.ServiceID]serviceFallback

	// Optional.
	// Puts the Gateway in LoadTesting mode if specified.
	// All relays will be sent to a fixed URL.
	// Allows measuring the performance of PATH and full node(s) in isolation.
	loadTestingConfig *LoadTestingConfig

	// concurrencyConfig controls concurrency limits for request processing.
	// These limits protect against resource exhaustion from batch requests and parallel relays.
	concurrencyConfig gateway.ConcurrencyConfig

	// reputationService tracks endpoint reputation scores.
	// If enabled, endpoints are filtered by their reputation score.
	// When nil, no reputation-based filtering is applied.
	reputationService reputation.ReputationService

	// tieredSelector selects endpoints using cascade-down tier logic.
	// Created when a reputation service is enabled with tiered selection enabled.
	tieredSelector *reputation.TieredSelector

	// serviceTieredSelectors stores per-service TieredSelectors for services with custom thresholds.
	// When a service has a per-service tiered selection config, its selector is stored here.
	// Falls back to the global tieredSelector if not present.
	serviceTieredSelectors map[protocol.ServiceID]*reputation.TieredSelector

	// unifiedServicesConfig is the unified YAML-driven service configuration.
	// This consolidates all per-service settings and enables per-service overrides.
	unifiedServicesConfig *gateway.UnifiedServicesConfig

	// supplierBlacklist tracks suppliers with validation/signature errors.
	// These suppliers are temporarily excluded from selection to prevent
	// penalizing domain reputation for individual supplier issues.
	supplierBlacklist *supplierBlacklist

	// loggedMisconfigErrors tracks which misconfiguration errors have been logged
	// to avoid spamming logs with the same error on every request.
	loggedMisconfigErrors sync.Map

	// relaySigner is the pre-initialized signer for signing relay requests.
	// Created once during Protocol initialization and reused across all requests.
	// Uses SignerContext caching internally for optimal ring signature performance.
	relaySigner *signer

	// sessionEndpointsCache caches endpoint maps by session ID.
	// Session endpoints don't change within a session's lifetime, so caching avoids
	// redundant AllEndpoints() calls and endpoint map construction on every request.
	// Key: sessionId (string), Value: map[protocol.EndpointAddr]endpoint
	sessionEndpointsCache sync.Map
}

// serviceFallback holds the fallback information for a service,
// including the endpoints and whether to send all traffic to fallback.
type serviceFallback struct {
	SendAllTraffic bool
	Endpoints      map[protocol.EndpointAddr]endpoint
}

// NewProtocol instantiates an instance of the Shannon protocol integration.
func NewProtocol(
	ctx context.Context,
	logger polylog.Logger,
	config GatewayConfig,
	fullNode FullNode,
) (*Protocol, error) {
	shannonLogger := logger.With("protocol", "shannon")

	// Retrieve the list of apps owned by the gateway.
	ownedApps, err := getOwnedApps(shannonLogger, config.OwnedAppsPrivateKeysHex, fullNode)
	if err != nil {
		return nil, fmt.Errorf("failed to get app addresses from config: %w", err)
	}

	// Wire up defaults from parent config to unified services' config.
	// This allows gateway_config top-level settings to serve as defaults for all services,
	// eliminating the need for a separate "defaults" section in YAML.
	config.UnifiedServices.SetDefaultsFromParent(gateway.ParentConfigDefaults{
		TieredSelectionEnabled:      config.ReputationConfig.TieredSelection.Enabled,
		Tier1Threshold:              config.ReputationConfig.TieredSelection.Tier1Threshold,
		Tier2Threshold:              config.ReputationConfig.TieredSelection.Tier2Threshold,
		ProbationEnabled:            config.ReputationConfig.TieredSelection.Probation.Enabled,
		ProbationThreshold:          config.ReputationConfig.TieredSelection.Probation.Threshold,
		ProbationTrafficPercent:     config.ReputationConfig.TieredSelection.Probation.TrafficPercent,
		ProbationRecoveryMultiplier: config.ReputationConfig.TieredSelection.Probation.RecoveryMultiplier,
		RetryEnabled:                config.RetryConfig.Enabled,
		MaxRetries:                  config.RetryConfig.MaxRetries,
		RetryOn5xx:                  config.RetryConfig.RetryOn5xx,
		RetryOnTimeout:              config.RetryConfig.RetryOnTimeout,
		RetryOnConnection:           config.RetryConfig.RetryOnConnection,
		MaxRetryLatency: func() time.Duration {
			if config.RetryConfig.MaxRetryLatency != nil {
				return *config.RetryConfig.MaxRetryLatency
			}
			return 0
		}(),
		ConnectTimeout:             config.RetryConfig.ConnectTimeout,
		HedgeDelay:                 config.RetryConfig.HedgeDelay,
		ObservationPipelineEnabled: config.ObservationPipelineConfig.Enabled,
		SampleRate:                 config.ObservationPipelineConfig.SampleRate,
		HealthChecksEnabled:        config.ActiveHealthChecksConfig.Enabled,
		HealthCheckInterval:        config.ActiveHealthChecksConfig.Coordination.RenewInterval,
		SyncAllowance:              config.ActiveHealthChecksConfig.SyncAllowance,
		LocalHealthChecks:          config.ActiveHealthChecksConfig.Local,
	})

	// Apply defaults to concurrency config if not set.
	// These defaults match the previous hardcoded behavior for backward compatibility.
	if config.ConcurrencyConfig.MaxParallelEndpoints == 0 {
		config.ConcurrencyConfig.MaxParallelEndpoints = 1
	}
	if config.ConcurrencyConfig.MaxConcurrentRelays == 0 {
		config.ConcurrencyConfig.MaxConcurrentRelays = 5500
	}
	if config.ConcurrencyConfig.MaxBatchPayloads == 0 {
		config.ConcurrencyConfig.MaxBatchPayloads = 5500
	}

	// 🚨 BIG WARNING: Parallel endpoints multiply token burn
	if config.ConcurrencyConfig.MaxParallelEndpoints > 1 {
		shannonLogger.Warn().
			Int("max_parallel_endpoints", config.ConcurrencyConfig.MaxParallelEndpoints).
			Msg("🚨 WARNING: max_parallel_endpoints > 1 is EXPERIMENTAL and will multiply token burn by the number of parallel endpoints! " +
				"Each request will be sent to multiple endpoints simultaneously. " +
				"Monitor your token usage and endpoint metrics closely. " +
				"Recommended: Start with max_parallel_endpoints=1 and test thoroughly before increasing.")
	}

	protocolInstance := &Protocol{
		logger: shannonLogger,

		FullNode: fullNode,

		// TODO_MVP(@adshmh): verify the gateway address and private key are valid, by completing the following:
		// 1. Query onchain data for a gateway with the supplied address.
		// 2. Query onchain data for app(s) matching the derived addresses.
		gatewayAddr:          config.GatewayAddress,
		gatewayPrivateKeyHex: config.GatewayPrivateKeyHex,
		gatewayMode:          config.GatewayMode,

		// ownedApps is the list of apps owned by the gateway operator
		ownedApps: ownedApps,

		// HTTP client with embedded tracking of debug metrics.
		httpClient: pathhttp.NewDefaultHTTPClientWithDebugMetrics(),

		// relayPool is a shared worker pool for parallel relay processing.
		// Uses MaxConcurrentRelays to bound global concurrency and prevent resource exhaustion.
		relayPool: pond.NewPool(config.ConcurrencyConfig.MaxConcurrentRelays),

		// serviceFallbacks contains the fallback information for each service.
		serviceFallbackMap: config.getServiceFallbackMap(),

		// load testing config, if specified.
		loadTestingConfig: config.LoadTestingConfig,

		// concurrency config controls parallel endpoint queries and batch request limits
		concurrencyConfig: config.ConcurrencyConfig,

		// unifiedServicesConfig for per-service configuration overrides
		unifiedServicesConfig: &config.UnifiedServices,

		// supplierBlacklist tracks suppliers with validation/signature errors
		supplierBlacklist: newSupplierBlacklist(),
	}

	// Initialize the relay signer with SignerContext caching for optimal ring signature performance.
	// The signer is created once and reused across all requests.
	relaySigner, err := newSigner(*fullNode.GetAccountClient(), config.GatewayPrivateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to create relay signer: %w", err)
	}
	protocolInstance.relaySigner = relaySigner

	// Initialize reputation service if enabled.
	// Reputation is the primary endpoint quality system - it tracks endpoint scores
	// based on both user requests and health check probes (hydrator).
	// When disabled, requests are relayed to any endpoint in the session without quality filtering.
	if config.ReputationConfig.Enabled {
		config.ReputationConfig.HydrateDefaults()
		reputationLogger := shannonLogger.With("component", "reputation")

		// Create storage based on configuration.
		var store reputation.Storage
		switch config.ReputationConfig.StorageType {
		case "memory", "":
			// Use recovery timeout as TTL for entries - expired entries get auto-cleaned
			store = reputationstorage.NewMemoryStorage(config.ReputationConfig.RecoveryTimeout)
		case "redis":
			if config.RedisConfig == nil {
				return nil, fmt.Errorf("redis storage requires global redis_config to be set")
			}
			redisStore, err := reputationstorage.NewRedisStorage(ctx, *config.RedisConfig, config.ReputationConfig.RecoveryTimeout)
			if err != nil {
				return nil, fmt.Errorf("failed to create redis storage: %w", err)
			}
			store = redisStore
		default:
			return nil, fmt.Errorf("unsupported reputation storage type: %s", config.ReputationConfig.StorageType)
		}

		reputationSvc := reputation.NewService(config.ReputationConfig, store)
		reputationSvc.SetLogger(reputationLogger)
		if err := reputationSvc.Start(ctx); err != nil {
			return nil, fmt.Errorf("failed to start reputation service: %w", err)
		}

		protocolInstance.reputationService = reputationSvc

		// Configure per-service reputation settings from unified config
		if protocolInstance.unifiedServicesConfig != nil {
			for _, svc := range protocolInstance.unifiedServicesConfig.Services {
				merged := protocolInstance.unifiedServicesConfig.GetMergedServiceConfig(svc.ID)
				if merged != nil && merged.ReputationConfig != nil {
					svcConfig := reputation.ServiceConfig{}
					if merged.ReputationConfig.KeyGranularity != "" {
						svcConfig.KeyGranularity = merged.ReputationConfig.KeyGranularity
					}
					if merged.ReputationConfig.InitialScore != nil {
						svcConfig.InitialScore = *merged.ReputationConfig.InitialScore
					}
					if merged.ReputationConfig.MinThreshold != nil {
						svcConfig.MinThreshold = *merged.ReputationConfig.MinThreshold
					}
					if merged.ReputationConfig.RecoveryTimeout != nil {
						svcConfig.RecoveryTimeout = *merged.ReputationConfig.RecoveryTimeout
					}
					// Set health checks and probation enabled flags.
					// These are used by shouldRecover to determine if time-based recovery applies.
					if merged.HealthChecks != nil && merged.HealthChecks.Enabled != nil {
						svcConfig.HealthChecksEnabled = *merged.HealthChecks.Enabled
					}
					if merged.Probation != nil && merged.Probation.Enabled != nil {
						svcConfig.ProbationEnabled = *merged.Probation.Enabled
					}
					reputationSvc.SetServiceConfig(svc.ID, svcConfig)
					reputationLogger.Debug().
						Str("service_id", string(svc.ID)).
						Float64("initial_score", svcConfig.InitialScore).
						Float64("min_threshold", svcConfig.MinThreshold).
						Str("key_granularity", svcConfig.KeyGranularity).
						Bool("health_checks_enabled", svcConfig.HealthChecksEnabled).
						Bool("probation_enabled", svcConfig.ProbationEnabled).
						Msg("Configured per-service reputation settings")
				}

				// Configure per-service latency: choose between simple config and profile-based config
				if merged != nil {
					latencyConfig := buildLatencyConfigForService(merged, config.ReputationConfig.Latency, protocolInstance.unifiedServicesConfig)
					if latencyConfig != nil {
						reputationSvc.SetLatencyProfile(svc.ID, *latencyConfig)
						reputationLogger.Debug().
							Str("service_id", string(svc.ID)).
							Bool("latency_enabled", latencyConfig.Enabled).
							Dur("fast_threshold", latencyConfig.FastThreshold).
							Dur("penalty_threshold", latencyConfig.PenaltyThreshold).
							Msg("Configured per-service latency")
					}
				}
			}
		}

		// Create tiered selector if tiered selection is enabled
		if config.ReputationConfig.TieredSelection.Enabled {
			protocolInstance.tieredSelector = reputation.NewTieredSelectorWithLogger(
				reputationLogger,
				config.ReputationConfig.TieredSelection,
				config.ReputationConfig.MinThreshold,
			)
			reputationLogger.Info().
				Float64("tier1_threshold", config.ReputationConfig.TieredSelection.Tier1Threshold).
				Float64("tier2_threshold", config.ReputationConfig.TieredSelection.Tier2Threshold).
				Float64("min_threshold", config.ReputationConfig.MinThreshold).
				Msg("Tiered endpoint selection enabled")

			// Initialize per-service tiered selectors from unified config
			protocolInstance.serviceTieredSelectors = make(map[protocol.ServiceID]*reputation.TieredSelector)
			if protocolInstance.unifiedServicesConfig != nil {
				for _, svc := range protocolInstance.unifiedServicesConfig.Services {
					merged := protocolInstance.unifiedServicesConfig.GetMergedServiceConfig(svc.ID)
					if merged != nil && merged.TieredSelection != nil {
						// Only create per-service selector if thresholds differ from global defaults
						hasCustomConfig := false
						tier1 := config.ReputationConfig.TieredSelection.Tier1Threshold
						tier2 := config.ReputationConfig.TieredSelection.Tier2Threshold
						minThreshold := config.ReputationConfig.MinThreshold

						if merged.TieredSelection.Tier1Threshold != nil &&
							*merged.TieredSelection.Tier1Threshold != tier1 {
							tier1 = *merged.TieredSelection.Tier1Threshold
							hasCustomConfig = true
						}
						if merged.TieredSelection.Tier2Threshold != nil &&
							*merged.TieredSelection.Tier2Threshold != tier2 {
							tier2 = *merged.TieredSelection.Tier2Threshold
							hasCustomConfig = true
						}
						// Use per-service min threshold if available
						if merged.ReputationConfig != nil && merged.ReputationConfig.MinThreshold != nil {
							minThreshold = *merged.ReputationConfig.MinThreshold
							hasCustomConfig = true
						}

						if hasCustomConfig {
							// Build tiered selection config with probation
							// Use global default for Enabled if nil (defensive nil check)
							tieredEnabled := config.ReputationConfig.TieredSelection.Enabled
							if merged.TieredSelection.Enabled != nil {
								tieredEnabled = *merged.TieredSelection.Enabled
							}
							svcTierConfig := reputation.TieredSelectionConfig{
								Enabled:        tieredEnabled,
								Tier1Threshold: tier1,
								Tier2Threshold: tier2,
							}

							// Include per-service probation config if present
							// Add defensive nil checks for each pointer field
							if merged.Probation != nil {
								// Use global defaults as fallbacks for nil pointer fields
								probEnabled := config.ReputationConfig.TieredSelection.Probation.Enabled
								probThreshold := config.ReputationConfig.TieredSelection.Probation.Threshold
								probTrafficPct := config.ReputationConfig.TieredSelection.Probation.TrafficPercent
								probRecoveryMult := config.ReputationConfig.TieredSelection.Probation.RecoveryMultiplier

								if merged.Probation.Enabled != nil {
									probEnabled = *merged.Probation.Enabled
								}
								if merged.Probation.Threshold != nil {
									probThreshold = *merged.Probation.Threshold
								}
								if merged.Probation.TrafficPercent != nil {
									probTrafficPct = *merged.Probation.TrafficPercent
								}
								if merged.Probation.RecoveryMultiplier != nil {
									probRecoveryMult = *merged.Probation.RecoveryMultiplier
								}

								svcTierConfig.Probation = reputation.ProbationConfig{
									Enabled:            probEnabled,
									Threshold:          probThreshold,
									TrafficPercent:     probTrafficPct,
									RecoveryMultiplier: probRecoveryMult,
								}
							} else {
								// Use global defaults
								svcTierConfig.Probation = config.ReputationConfig.TieredSelection.Probation
							}

							protocolInstance.serviceTieredSelectors[svc.ID] = reputation.NewTieredSelectorWithLogger(
								reputationLogger.With("service_id", string(svc.ID)),
								svcTierConfig,
								minThreshold,
							)
							reputationLogger.Debug().
								Str("service_id", string(svc.ID)).
								Float64("tier1_threshold", tier1).
								Float64("tier2_threshold", tier2).
								Float64("min_threshold", minThreshold).
								Bool("probation_enabled", svcTierConfig.Probation.Enabled).
								Float64("probation_threshold", svcTierConfig.Probation.Threshold).
								Msg("Configured per-service tiered selection")
						}
					}
				}
			}
		}

		reputationLogger.Info().Msg("Reputation service enabled and started")
	}

	return protocolInstance, nil
}

// AvailableHTTPEndpoints returns the available endpoints for a given service ID.
//
// - Provides the list of endpoints that can serve the specified service ID.
// - Returns a list of valid endpoint addresses, protocol observations, and any error encountered.
//
// Usage:
//   - In Delegated mode, httpReq must contain the appropriate headers for app selection.
//   - In Centralized mode, httpReq may be nil.
//
// Returns:
//   - protocol.EndpointAddrList: the discovered endpoints for the service.
//   - protocolobservations.Observations: contextual observations (e.g., error context).
//   - error: if any error occurs during endpoint discovery or validation.
func (p *Protocol) AvailableHTTPEndpoints(
	ctx context.Context,
	serviceID protocol.ServiceID,
	rpcType sharedtypes.RPCType,
	httpReq *http.Request,
) (protocol.EndpointAddrList, protocolobservations.Observations, error) {
	// hydrate the logger.
	logger := p.logger.With(
		"service", serviceID,
		"method", "AvailableEndpoints",
		"gateway_mode", p.gatewayMode,
		"rpc_type", rpcType.String(),
	)

	// TODO_TECHDEBT(@adshmh): validate "serviceID" is a valid onchain Shannon service.
	activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, httpReq)
	if err != nil {
		logger.Error().Err(err).Msg("Relay request will fail: error building the active sessions for service.")
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	logger = logger.With("number_of_valid_sessions", len(activeSessions))
	logger.Debug().Msg("fetched the set of active sessions.")

	// Parse allowed suppliers from header (if present)
	allowedSuppliers := parseAllowedSuppliersHeader(httpReq)

	// Retrieve a list of all unique endpoints for the given service ID filtered by
	// the list of apps this gateway/application owns and can send relays on behalf of.
	//
	// This includes fallback logic: if all session endpoints are filtered out (low reputation) and the
	// requested service is configured with at least one fallback URL, the fallback
	// endpoints will be used to populate the list of endpoints.
	//
	// The final boolean parameter sets whether to filter by reputation.
	// The final RPC type parameter filters endpoints to only those supporting the requested RPC type.
	// The final slice parameter optionally restricts endpoints to specific allowed suppliers.
	endpoints, actualRPCType, err := p.getUniqueEndpoints(ctx, serviceID, activeSessions, true, rpcType, allowedSuppliers, "")
	if err != nil {
		logger.Error().Err(err).Msg(err.Error())
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	// Log if RPC type fallback occurred
	if actualRPCType != rpcType {
		logger.Info().
			Str("requested_rpc_type", rpcType.String()).
			Str("actual_rpc_type", actualRPCType.String()).
			Msg("RPC type fallback was applied during endpoint selection")
	}

	logger = logger.With("number_of_unique_endpoints", len(endpoints))
	logger.Debug().Msg("Successfully fetched the set of available endpoints for the selected apps.")

	// Convert the list of endpoints to a list of endpoint addresses
	endpointAddrs := make(protocol.EndpointAddrList, 0, len(endpoints))
	for endpointAddr := range endpoints {
		endpointAddrs = append(endpointAddrs, endpointAddr)
	}

	return endpointAddrs, buildSuccessfulEndpointLookupObservation(serviceID), nil
}

// AvailableWebsocketEndpoints returns the available endpoints for a given service ID.
//
// - Provides the list of endpoints that can serve the specified service ID.
// - Returns a list of valid endpoint addresses, protocol observations, and any error encountered.
//
// Usage:
//   - In Delegated mode, httpReq must contain the appropriate headers for app selection.
//   - In Centralized mode, httpReq may be nil.
//
// Returns:
//   - protocol.EndpointAddrList: the discovered endpoints for the service.
//   - protocolobservations.Observations: contextual observations (e.g., error context).
//   - error: if any error occurs during endpoint discovery or validation.
func (p *Protocol) AvailableWebsocketEndpoints(
	ctx context.Context,
	serviceID protocol.ServiceID,
	httpReq *http.Request,
) (protocol.EndpointAddrList, protocolobservations.Observations, error) {
	// hydrate the logger.
	logger := p.logger.With(
		"service", serviceID,
		"method", "AvailableEndpoints",
		"gateway_mode", p.gatewayMode,
	)

	// TODO_TECHDEBT(@adshmh): validate "serviceID" is a valid onchain Shannon service.
	activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, httpReq)
	if err != nil {
		logger.Error().Err(err).Msg("Relay request will fail: error building the active sessions for service.")
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	logger = logger.With("number_of_valid_sessions", len(activeSessions))
	logger.Debug().Msg("fetched the set of active sessions.")

	// Parse allowed suppliers from header (if present)
	allowedSuppliers := parseAllowedSuppliersHeader(httpReq)

	// Retrieve a list of all unique endpoints for the given service ID filtered by
	// the list of apps this gateway/application owns and can send relays on behalf of.
	//
	// This includes fallback logic: if all session endpoints are filtered out (low reputation) and the
	// requested service is configured with at least one fallback URL, the fallback
	// endpoints will be used to populate the list of endpoints.
	//
	// The final boolean parameter sets whether to filter by reputation.
	// The final slice parameter optionally restricts endpoints to specific allowed suppliers.
	//
	// NOTE: WebSocket endpoints currently don't have dedicated health checks, so they may have
	// low initial scores. We use filterByReputation=false to allow connections to all WebSocket
	// endpoints until WebSocket health checks are implemented.
	// TODO_IMPROVE: Add WebSocket health checks and re-enable reputation filtering.
	endpoints, actualRPCType, err := p.getUniqueEndpoints(ctx, serviceID, activeSessions, false, sharedtypes.RPCType_WEBSOCKET, allowedSuppliers, "")
	if err != nil {
		logger.Error().Err(err).Msg(err.Error())
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	// Log if RPC type fallback occurred
	if actualRPCType != sharedtypes.RPCType_WEBSOCKET {
		logger.Info().
			Str("requested_rpc_type", sharedtypes.RPCType_WEBSOCKET.String()).
			Str("actual_rpc_type", actualRPCType.String()).
			Msg("RPC type fallback was applied for websocket endpoint selection")
	}

	logger = logger.With("number_of_unique_endpoints", len(endpoints))
	logger.Debug().Msg("Successfully fetched the set of available endpoints for the selected apps.")

	// Convert the list of endpoints to a list of endpoint addresses
	endpointAddrs := make(protocol.EndpointAddrList, 0, len(endpoints))
	for endpointAddr := range endpoints {
		endpointAddrs = append(endpointAddrs, endpointAddr)
	}

	return endpointAddrs, buildSuccessfulEndpointLookupObservation(serviceID), nil
}

// BuildHTTPRequestContextForEndpoint creates a new protocol request context for a specified service and endpoint.
//
// Parameters:
//   - ctx: Context for cancellation, deadlines, and logging.
//   - serviceID: The unique identifier of the target service.
//   - selectedEndpointAddr: The address of the endpoint to use for the request.
//   - httpReq: ONLY used in Delegated mode to extract the selected app from headers.
//   - TODO_TECHDEBT: Decouple context building for different gateway modes.
//
// Behavior:
//   - Retrieves active sessions for the given service ID from the full node.
//   - Retrieves unique endpoints available across all active sessions
//   - Filters endpoints by reputation (if enabled).
//   - Obtains the relay request signer appropriate for the current gateway mode.
//   - Returns a fully initialized request context for use in downstream protocol operations.
//   - On failure, logs the error, returns a context setup observation, and a non-nil error.
//
// Implements the gateway.Protocol interface.
func (p *Protocol) BuildHTTPRequestContextForEndpoint(
	ctx context.Context,
	serviceID protocol.ServiceID,
	selectedEndpointAddr protocol.EndpointAddr,
	rpcType sharedtypes.RPCType,
	httpReq *http.Request,
	filterByReputation bool,
) (gateway.ProtocolRequestContext, protocolobservations.Observations, error) {
	logger := p.logger.With(
		"method", "BuildHTTPRequestContextForEndpoint",
		"service_id", serviceID,
		"endpoint_addr", selectedEndpointAddr,
		"rpc_type", rpcType.String(),
		"filter_by_reputation", filterByReputation,
	)

	activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, httpReq)
	if err != nil {
		logger.Error().Err(err).Msgf("Relay request will fail due to error retrieving active sessions for service %s", serviceID)
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	// Parse allowed suppliers from header (if present)
	allowedSuppliers := parseAllowedSuppliersHeader(httpReq)

	// Retrieve the list of endpoints (i.e. backend service URLs by external operators)
	// that can service RPC requests for the given service ID for the given apps.
	// This includes fallback logic if session endpoints are unavailable.
	// The filterByReputation parameter controls whether to filter by reputation score.
	// The RPC type parameter filters endpoints to only those supporting the requested RPC type.
	// The final slice parameter optionally restricts endpoints to specific allowed suppliers.
	endpoints, actualRPCType, err := p.getUniqueEndpoints(ctx, serviceID, activeSessions, filterByReputation, rpcType, allowedSuppliers, selectedEndpointAddr)
	if err != nil {
		// Log with fresh context (without endpoint_addr which doesn't apply here)
		logEvent := p.logger.Error().Err(err).
			Str("service_id", string(serviceID)).
			Str("requested_rpc_type", rpcType.String()).
			Int("session_count", len(activeSessions))
		if len(activeSessions) > 0 {
			// Add first session's details for context
			firstSession := activeSessions[0]
			logEvent = logEvent.
				Str("session_id", firstSession.SessionId).
				Int64("session_start_height", firstSession.Header.SessionStartBlockHeight).
				Int64("session_end_height", firstSession.Header.SessionEndBlockHeight).
				Str("app_address", firstSession.Header.ApplicationAddress).
				Int("supplier_count", len(firstSession.Suppliers))

			// Extract unique domains from all session suppliers for debugging
			domainSet := make(map[string]struct{})
			for _, session := range activeSessions {
				for _, supplier := range session.Suppliers {
					for _, service := range supplier.Services {
						for _, endpoint := range service.Endpoints {
							if domain, err := shannonmetrics.ExtractDomainOrHost(endpoint.Url); err == nil {
								domainSet[domain] = struct{}{}
							}
						}
					}
				}
			}
			domains := make([]string, 0, len(domainSet))
			for domain := range domainSet {
				domains = append(domains, domain)
			}
			if len(domains) > 0 {
				logEvent = logEvent.Str("session_domains", strings.Join(domains, ", "))
			}
		}
		logEvent.Msg("No valid endpoints available - check RPC type support, reputation scores, or blacklist")
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	// Log if RPC type fallback occurred
	if actualRPCType != rpcType {
		logger.Info().
			Str("requested_rpc_type", rpcType.String()).
			Str("actual_rpc_type", actualRPCType.String()).
			Msg("RPC type fallback was applied during endpoint selection")
	}

	// Select the endpoint that matches the pre-selected address.
	// This ensures QoS checks are performed on the selected endpoint.
	selectedEndpoint, ok := endpoints[selectedEndpointAddr]
	if !ok {
		// Wrap the context setup error.
		// Used to generate the observation.
		err := fmt.Errorf("%w: service %s endpoint %s", errRequestContextSetupInvalidEndpointSelected, serviceID, selectedEndpointAddr)
		// Log at DEBUG level - this is expected during session rollover when:
		// - Health check was scheduled with an endpoint from an old session
		// - Session has since rolled over and the endpoint is no longer in the new session
		logger.Debug().Err(err).Msg("Selected endpoint is not available - likely session rollover")
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	// Retrieve the relay request signer for the current gateway mode.
	permittedSigner, err := p.getGatewayModePermittedRelaySigner(p.gatewayMode)
	if err != nil {
		// Wrap the context setup error.
		// Used to generate the observation.
		err = fmt.Errorf("%w: gateway mode %s: %w", errRequestContextSetupErrSignerSetup, p.gatewayMode, err)
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	// TODO_TECHDEBT: Need to propagate the SendAllTraffic bool to the requestContext.
	// Example use-case:
	// Gateway uses PATH in the opposite way as Grove w/ the goal of:
	// 	1. Primary source: their own infra
	// 	2. Secondary source: fallback to network
	// This would require the requestContext to be aware of _SendAllTraffic in this context.
	fallbackEndpoints, _ := p.getServiceFallbackEndpoints(serviceID)

	// Get tiered selector for probation status checking when recording success signals
	tieredSelector := p.getTieredSelectorForService(serviceID)

	// Create a fully-hydrated logger ONCE at request initialization to avoid repeated allocations.
	// This logger includes all context fields needed throughout the request lifecycle.
	// PERF: Prevents 80% of logger allocations (was creating 7-10 new loggers per request).
	fullyHydratedLogger := p.logger.With(
		"request_type", "http",
		"service_id", serviceID,
		"selected_endpoint_supplier", selectedEndpoint.Supplier(),
		"selected_endpoint_url", selectedEndpoint.PublicURL(),
		"rpc_type", actualRPCType.String(),
	)

	// Add session header details if available
	if session := selectedEndpoint.Session(); session != nil && session.Header != nil {
		fullyHydratedLogger = fullyHydratedLogger.With(
			"selected_endpoint_app", session.Header.ApplicationAddress,
		)
	}

	// Return new request context for the pre-selected endpoint
	return &requestContext{
		logger:                fullyHydratedLogger, // Use fully-hydrated logger
		context:               ctx,
		fullNode:              p.FullNode,
		selectedEndpoint:      selectedEndpoint,
		serviceID:             serviceID,
		relayRequestSigner:    permittedSigner,
		httpClient:            p.httpClient,
		fallbackEndpoints:     fallbackEndpoints,
		loadTestingConfig:     p.loadTestingConfig,
		relayPool:             p.relayPool,
		concurrencyConfig:     p.concurrencyConfig,
		unifiedServicesConfig: p.unifiedServicesConfig,
		reputationService:     p.reputationService,
		tieredSelector:        tieredSelector,
		supplierBlacklist:     p.supplierBlacklist,
		currentRPCType:        actualRPCType, // Use actual RPC type after fallback (may differ from requested)
	}, protocolobservations.Observations{}, nil
}

// ApplyHTTPObservations updates protocol instance state based on endpoint observations.
// Records reputation signals from hydrator (synthetic) health check observations,
// allowing endpoints to recover from failures via successful health checks.
//
// Implements gateway.Protocol interface.
func (p *Protocol) ApplyHTTPObservations(observations *protocolobservations.Observations) error {
	// Sanity check the input
	if observations == nil || observations.GetShannon() == nil {
		p.logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: ApplyHTTPObservations called with nil input or nil Shannon observation list.")
		return nil
	}

	shannonObservations := observations.GetShannon().GetObservations()
	if len(shannonObservations) == 0 {
		p.logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: ApplyHTTPObservations called with nil set of Shannon request observations.")
		return nil
	}

	// Record reputation signals from observations.
	// This allows health check results (from hydrator) to update endpoint reputation scores.
	if p.reputationService != nil {
		p.recordReputationSignalsFromObservations(shannonObservations)
	}

	return nil
}

// ConfiguredServiceIDs returns the list of all all service IDs that are configured
// to be supported by the Gateway.
func (p *Protocol) ConfiguredServiceIDs() map[protocol.ServiceID]struct{} {
	configuredServiceIDs := make(map[protocol.ServiceID]struct{})
	for serviceID := range p.ownedApps {
		configuredServiceIDs[serviceID] = struct{}{}
	}

	return configuredServiceIDs
}

// Name satisfies the HealthCheck#Name interface function
func (p *Protocol) Name() string {
	return "pokt-shannon"
}

// IsAlive satisfies the HealthCheck#IsAlive interface function
func (p *Protocol) IsAlive() bool {
	return p.IsHealthy()
}

// TODO_TECHDEBT(@adshmh): Refactor to split the fallback logic from Shannon endpoints handling.
// Example:
// - Make a `fallback` component to handle all aspects of fallback: when to use a fallback, distribution among multiple fallback URLs, etc.
//
// TODO_FUTURE(@adshmh): If multiple apps (across different sessions) are delegating
// to this gateway, optimize how the endpoints are managed/organized/cached.
//
// getUniqueEndpoints returns a map of all endpoints for a service ID with fallback logic.
// This function coordinates between session endpoints and fallback endpoints:
//   - If configured to send all traffic to fallback, returns fallback endpoints only
//   - Otherwise, attempts to get session endpoints and falls back to fallback endpoints if needed
//
// If allowedSuppliers is not empty, only endpoints from suppliers in the list will be returned,
// bypassing reputation filtering and other selection logic.
func (p *Protocol) getUniqueEndpoints(
	ctx context.Context,
	serviceID protocol.ServiceID,
	activeSessions []sessiontypes.Session,
	filterByReputation bool,
	rpcType sharedtypes.RPCType,
	allowedSuppliers []string,
	requestedEndpointAddr protocol.EndpointAddr,
) (map[protocol.EndpointAddr]endpoint, sharedtypes.RPCType, error) {
	logger := p.logger.With(
		"method", "getUniqueEndpoints",
		"service", serviceID,
		"num_valid_sessions", len(activeSessions),
	)

	// Get fallback configuration for the service ID.
	fallbackEndpoints, shouldSendAllTrafficToFallback := p.getServiceFallbackEndpoints(serviceID)

	// If the service is configured to send all traffic to fallback endpoints,
	// return only the fallback endpoints and skip session endpoint logic.
	if shouldSendAllTrafficToFallback && len(fallbackEndpoints) > 0 {
		logger.Debug().Msgf("Sending all traffic to fallback endpoints for service %s.", serviceID)
		return fallbackEndpoints, rpcType, nil
	}

	// Try to get session endpoints first.
	// Don't log errors here - will be logged at top-level caller with session context
	sessionEndpoints, actualRPCType, _ := p.getSessionsUniqueEndpoints(ctx, serviceID, activeSessions, filterByReputation, rpcType, allowedSuppliers, requestedEndpointAddr)

	// Session endpoints are available, use them.
	// This is the happy path where we have session endpoints available (after reputation filtering).
	if len(sessionEndpoints) > 0 {
		return sessionEndpoints, actualRPCType, nil
	}

	// Handle the case where no session endpoints are available.
	// If fallback endpoints are available for the service ID, use them.
	if len(fallbackEndpoints) > 0 {
		return fallbackEndpoints, rpcType, nil
	}

	// If no session endpoints are available (after reputation filtering) and no fallback
	// endpoints are available for the service ID, return an error.
	// Don't log here - error will be logged at the top-level caller to avoid duplicate logs
	err := fmt.Errorf("%w: service %s", errProtocolContextSetupNoEndpoints, serviceID)
	return nil, rpcType, err
}

// getSessionsUniqueEndpoints returns a map of all endpoints matching service ID from active sessions.
// This function focuses solely on retrieving and filtering session endpoints.
//
// If an endpoint matches a serviceID across multiple apps/sessions, only a single
// entry matching one of the apps/sessions is returned.
//
// If allowedSuppliers is not empty, only endpoints from suppliers in the list will be returned.
// This bypasses reputation filtering and other selection logic.
func (p *Protocol) getSessionsUniqueEndpoints(
	ctx context.Context,
	serviceID protocol.ServiceID,
	activeSessions []sessiontypes.Session,
	filterByReputation bool,
	filterByRPCType sharedtypes.RPCType,
	allowedSuppliers []string,
	requestedEndpointAddr protocol.EndpointAddr,
) (map[protocol.EndpointAddr]endpoint, sharedtypes.RPCType, error) {
	logger := p.logger.With(
		"method", "getSessionsUniqueEndpoints",
		"service", serviceID,
		"num_valid_sessions", len(activeSessions),
	)
	logger.Info().Msgf(
		"About to fetch all unique session endpoints for service %s given %d active sessions.",
		serviceID, len(activeSessions),
	)

	endpoints := make(map[protocol.EndpointAddr]endpoint)

	// Track the actual RPC type used (may differ from requested if fallback occurs)
	actualRPCType := filterByRPCType

	// Build the effective allowed suppliers list
	// Priority: Target-Suppliers header > Load testing config
	effectiveAllowedSuppliers := allowedSuppliers
	if len(effectiveAllowedSuppliers) == 0 {
		// TODO_TECHDEBT(@adshmh): Refactor load testing related code to make the filtering more visible.
		//
		// In Load Testing using RelayMiner mode: drop any endpoints not matching the single supplier specified in the config.
		if ltc := p.loadTestingConfig; ltc != nil {
			if ltc.RelayMinerConfig != nil && ltc.RelayMinerConfig.SupplierAddr != "" {
				effectiveAllowedSuppliers = []string{ltc.RelayMinerConfig.SupplierAddr}
			}
		}
	}

	// Log if supplier filtering is active
	if len(effectiveAllowedSuppliers) > 0 {
		logger.Debug().Msgf("Filtering endpoints to allowed suppliers only: %v", effectiveAllowedSuppliers)
	}

	// Iterate over all active sessions for the service ID.
	for _, session := range activeSessions {
		app := session.Application

		// Using a single iteration scope for this logger.
		// Avoids adding all apps in the loop to the logger's fields.
		// Hydrate the logger with session details.
		logger := logger.With("valid_app_address", app.Address).With("method", "getSessionsUniqueEndpoints")
		logger = hydrateLoggerWithSession(logger, &session)
		logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msgf("Finding unique endpoints for session %s for app %s for service %s.", session.SessionId, app.Address, serviceID)

		// Retrieve all endpoints for the session (cached).
		// Filtering happens after endpoint retrieval.
		sessionEndpoints, err := p.getOrCreateSessionEndpoints(session)
		if err != nil {
			logger.Error().Err(err).Msgf("Internal error: error getting all endpoints for service %s app %s and session: skipping the app.", serviceID, app.Address)
			continue
		}

		// STRICT RPC TYPE FILTERING
		// Filter endpoints to only those supporting the requested RPC type.
		// If a supplier doesn't have the exact RPC type URL, it's excluded (no defaulting).
		qualifiedEndpoints := sessionEndpoints
		if filterByRPCType != sharedtypes.RPCType_UNKNOWN_RPC {
			filteredEndpoints := make(map[protocol.EndpointAddr]endpoint)
			skippedSuppliers := make([]protocol.EndpointAddr, 0) // Track skipped suppliers for metrics

			for addr, ep := range sessionEndpoints {
				url := ep.GetURL(filterByRPCType)
				if url != "" || addr == requestedEndpointAddr {
					// Supplier supports this RPC type (or is the requested endpoint)
					filteredEndpoints[addr] = ep
				} else {
					// Supplier doesn't support requested RPC type - skip it
					skippedSuppliers = append(skippedSuppliers, addr)
					logger.Debug().
						Str("supplier", string(addr)).
						Str("rpc_type", filterByRPCType.String()).
						Msg("Skipping supplier - does not support requested RPC type")
				}
			}

			// RPC TYPE FALLBACK
			// If no endpoints found for the requested RPC type, check for a configured fallback.
			// This is a temporary workaround for suppliers that stake with incorrect RPC types.
			if len(filteredEndpoints) == 0 {
				if fallbackRPCType, hasFallback := p.getRPCTypeFallback(serviceID, filterByRPCType); hasFallback {
					logger.Warn().
						Str("service", string(serviceID)).
						Str("app", app.Address).
						Str("requested_rpc_type", filterByRPCType.String()).
						Str("fallback_rpc_type", fallbackRPCType.String()).
						Int("skipped_suppliers", len(skippedSuppliers)).
						Msg("No endpoints found for requested RPC type, falling back to alternate RPC type")

					// Record RPC type fallback metric for each skipped supplier
					for _, addr := range skippedSuppliers {
						domain, _ := shannonmetrics.ExtractDomainOrHost(string(addr))
						supplier := extractSupplierFromEndpointAddr(string(addr))
						metrics.RecordRPCTypeFallback(
							domain,
							supplier,
							string(serviceID),
							filterByRPCType.String(),
							fallbackRPCType.String(),
						)
					}

					// Retry filtering with fallback RPC type
					fallbackEndpoints := make(map[protocol.EndpointAddr]endpoint)
					fallbackSkipped := 0
					for addr, ep := range sessionEndpoints {
						url := ep.GetURL(fallbackRPCType)
						if url != "" {
							fallbackEndpoints[addr] = ep
						} else {
							fallbackSkipped++
						}
					}

					if len(fallbackEndpoints) > 0 {
						logger.Info().
							Str("fallback_rpc_type", fallbackRPCType.String()).
							Int("endpoints_found", len(fallbackEndpoints)).
							Int("endpoints_skipped", fallbackSkipped).
							Msg("Successfully fell back to alternate RPC type")
						filteredEndpoints = fallbackEndpoints
						actualRPCType = fallbackRPCType // Update the RPC type we're using
					} else {
						logger.Warn().Msgf(
							"No endpoints support fallback RPC type %s either for service %s, app %s (skipped %d suppliers). SKIPPING the app.",
							fallbackRPCType, serviceID, app.Address, fallbackSkipped,
						)
						continue
					}
				} else {
					logger.Warn().Msgf(
						"No endpoints support RPC type %s for service %s, app %s (skipped %d suppliers). SKIPPING the app.",
						filterByRPCType, serviceID, app.Address, len(skippedSuppliers),
					)
					continue
				}
			}

			if len(skippedSuppliers) > 0 && actualRPCType == filterByRPCType {
				logger.Info().Msgf(
					"Filtered endpoints by RPC type %s for app %s: %d remain, %d skipped",
					filterByRPCType, app.Address, len(filteredEndpoints), len(skippedSuppliers),
				)
			}

			qualifiedEndpoints = filteredEndpoints
		}

		// SUPPLIER ALLOWLIST FILTERING
		// If allowed suppliers are specified (via header or load testing config),
		// filter to only those suppliers, bypassing reputation and other logic.
		if len(effectiveAllowedSuppliers) > 0 {
			supplierFilteredEndpoints := make(map[protocol.EndpointAddr]endpoint)
			skippedCount := 0

			for addr, ep := range qualifiedEndpoints {
				// Extract supplier address from endpoint address (format: "supplierAddr-url")
				supplierAddr := string(addr)
				if dashIndex := strings.Index(supplierAddr, "-"); dashIndex > 0 {
					supplierAddr = supplierAddr[:dashIndex]
				}

				// Check if supplier is in the allowed list
				allowed := false
				for _, allowedSupplier := range effectiveAllowedSuppliers {
					if supplierAddr == allowedSupplier {
						allowed = true
						break
					}
				}

				if allowed || addr == requestedEndpointAddr {
					supplierFilteredEndpoints[addr] = ep
				} else {
					skippedCount++
					logger.Debug().
						Str("supplier", supplierAddr).
						Str("endpoint", string(addr)).
						Msg("Skipping endpoint - supplier not in allowed list")
				}
			}

			if len(supplierFilteredEndpoints) == 0 {
				logger.Warn().Msgf(
					"No endpoints match allowed suppliers %v for service %s, app %s (skipped %d endpoints). SKIPPING the app.",
					effectiveAllowedSuppliers, serviceID, app.Address, skippedCount,
				)
				continue
			}

			logger.Info().Msgf(
				"Filtered endpoints by allowed suppliers %v for app %s: %d remain, %d skipped",
				effectiveAllowedSuppliers, app.Address, len(supplierFilteredEndpoints), skippedCount,
			)

			qualifiedEndpoints = supplierFilteredEndpoints

			// IMPORTANT: When supplier filtering is active, skip reputation filtering
			// to allow the user to explicitly target specific suppliers regardless of reputation.
		}

		// SUPPLIER BLACKLIST FILTERING
		// Filter out suppliers that are blacklisted due to validation/signature errors.
		// These suppliers have issues that are not domain-related and should be excluded
		// without penalizing other suppliers at the same domain.
		//
		// SKIP this step if filterByReputation=false (health check case):
		// Health checks need to reach blacklisted suppliers so they can recover.
		if p.supplierBlacklist != nil && filterByReputation {
			blacklistFilteredEndpoints := make(map[protocol.EndpointAddr]endpoint)
			blacklistSkipped := 0

			for addr, ep := range qualifiedEndpoints {
				supplierAddr := ep.Supplier()
				if p.supplierBlacklist.IsBlacklisted(serviceID, supplierAddr) && addr != requestedEndpointAddr {
					blacklistSkipped++
					logger.Debug().
						Str("supplier", supplierAddr).
						Str("endpoint", string(addr)).
						Msg("Skipping blacklisted supplier")
				} else {
					blacklistFilteredEndpoints[addr] = ep
				}
			}

			if blacklistSkipped > 0 {
				logger.Info().
					Int("blacklisted", blacklistSkipped).
					Int("remaining", len(blacklistFilteredEndpoints)).
					Msg("Filtered out blacklisted suppliers")
			}

			qualifiedEndpoints = blacklistFilteredEndpoints
		}

		// Filter out low-reputation endpoints if reputation service is enabled and filtering is requested.
		// Reputation is the primary endpoint quality system - it provides gradual
		// exclusion based on score and allows recovery via health checks.
		// SKIP this step if:
		// - filterByReputation is false (e.g., for leaderboard metrics gathering)
		// - supplier filtering is active (user wants specific suppliers)
		if filterByReputation && p.reputationService != nil && len(effectiveAllowedSuppliers) == 0 {
			beforeCount := len(qualifiedEndpoints)
			qualifiedEndpoints = p.filterByReputation(ctx, serviceID, qualifiedEndpoints, filterByRPCType, logger, requestedEndpointAddr)

			if len(qualifiedEndpoints) == 0 {
				logger.Warn().Msgf(
					"All %d endpoints below reputation threshold for service %s, app %s. SKIPPING the app.",
					beforeCount, serviceID, app.Address,
				)
				continue
			}

			if beforeCount != len(qualifiedEndpoints) {
				logger.Debug().Msgf("app %s has %d endpoints after filtering by reputation (was %d).",
					app.Address, len(qualifiedEndpoints), beforeCount)
			}
		}

		// Log the number of endpoints before and after filtering
		logger.Debug().Msgf("Filtered session endpoints for app %s from %d to %d.", app.Address, len(sessionEndpoints), len(qualifiedEndpoints))

		maps.Copy(endpoints, qualifiedEndpoints)

		logger.Info().Msgf(
			"Successfully fetched %d endpoints for session %s for application %s for service %s.",
			len(qualifiedEndpoints), session.SessionId, app.Address, serviceID,
		)
	}

	// Return session endpoints if available.
	if len(endpoints) > 0 {
		// Apply tiered selection if enabled - only return endpoints from the highest available tier
		// SKIP tiered filtering when filterByReputation is false (e.g., for leaderboard metrics gathering)
		// because tiered selection is based on reputation scores.
		if filterByReputation && p.tieredSelector != nil && p.tieredSelector.Config().Enabled {
			endpoints = p.filterToHighestTier(ctx, serviceID, endpoints, filterByRPCType, logger, requestedEndpointAddr)
		}

		logger.Debug().Msgf("Successfully fetched %d session endpoints for active sessions.", len(endpoints))
		return endpoints, actualRPCType, nil
	}

	// No session endpoints are available.
	// Don't log here - error will be logged at the top-level caller to avoid duplicate logs
	err := fmt.Errorf("%w: service %s", errProtocolContextSetupNoEndpoints, serviceID)
	return nil, filterByRPCType, err
}

// ** Fallback Endpoint Handling **

// getServiceFallbackEndpoints returns the fallback endpoints and SendAllTraffic flag for a given service ID.
// Returns (endpoints, sendAllTraffic) where endpoints is empty if no fallback is configured.
func (p *Protocol) getServiceFallbackEndpoints(serviceID protocol.ServiceID) (map[protocol.EndpointAddr]endpoint, bool) {
	fallbackConfig, exists := p.serviceFallbackMap[serviceID]
	if !exists {
		return make(map[protocol.EndpointAddr]endpoint), false
	}

	return fallbackConfig.Endpoints, fallbackConfig.SendAllTraffic
}

// ** Disqualified Endpoint Reporting **

// GetTotalServiceEndpointsCount returns the count of all unique endpoints for a service ID
// without filtering by reputation.
func (p *Protocol) GetTotalServiceEndpointsCount(serviceID protocol.ServiceID, httpReq *http.Request) (int, error) {
	ctx := context.Background()

	// Get the list of active sessions for the service ID.
	activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, httpReq)
	if err != nil {
		return 0, err
	}

	// Get all endpoints for the service ID without filtering by reputation.
	// No supplier filtering since we don't have access to httpReq here.
	endpoints, _, err := p.getSessionsUniqueEndpoints(ctx, serviceID, activeSessions, false, sharedtypes.RPCType_UNKNOWN_RPC, nil, "")
	if err != nil {
		return 0, err
	}

	return len(endpoints), nil
}

// HydrateDisqualifiedEndpointsResponse hydrates the disqualified endpoint response with the protocol-specific data.
//   - takes a pointer to the DisqualifiedEndpointResponse
//   - called by the devtools.DisqualifiedEndpointReporter to fill it with the protocol-specific data.
func (p *Protocol) HydrateDisqualifiedEndpointsResponse(serviceID protocol.ServiceID, details *devtools.DisqualifiedEndpointResponse) {
	p.logger.Debug().Msgf("hydrating disqualified endpoints response for service ID: %s", serviceID)

	// Protocol-level disqualified endpoints are now managed by the reputation system.
	// Low-reputation endpoints are filtered out during selection, not permanently banned.
	details.ProtocolLevelDisqualifiedEndpoints = make(map[string]devtools.ProtocolLevelDataResponse)

	// TODO_FUTURE: Add reputation-based endpoint status reporting here
	// This could show endpoints below threshold and their current scores
}

// recordReputationSignalsFromObservations maps protocol observations to reputation signals.
// This is called by ApplyHTTPObservations to update endpoint reputation scores based on
// health check results from the hydrator or any other observation source.
func (p *Protocol) recordReputationSignalsFromObservations(shannonObservations []*protocolobservations.ShannonRequestObservations) {
	for _, observationSet := range shannonObservations {
		httpObservations := observationSet.GetHttpObservations()
		if httpObservations == nil {
			continue
		}

		serviceID := protocol.ServiceID(observationSet.GetServiceId())

		for _, endpointObs := range httpObservations.GetEndpointObservations() {
			p.recordSignalFromObservation(serviceID, endpointObs)
		}
	}
}

// recordSignalFromObservation records a reputation signal for a single endpoint observation.
// It maps the observation's error type directly to a reputation signal and records it.
// Also records probation traffic metrics if the endpoint is in probation.
func (p *Protocol) recordSignalFromObservation(serviceID protocol.ServiceID, obs *protocolobservations.ShannonEndpointObservation) {
	endpointAddr := protocol.EndpointAddr(obs.GetEndpointUrl())

	// TODO_FUTURE: Add RPC type to ShannonEndpointObservation proto to support RPC-type-aware reputation.
	// For now, default to JSON_RPC for HTTP observations since the proto doesn't include RPC type.
	// This is acceptable for hydrator health checks which primarily test JSON-RPC endpoints.
	rpcType := sharedtypes.RPCType_JSON_RPC

	// Build endpoint key using key builder to respect key_granularity setting
	keyBuilder := p.reputationService.KeyBuilderForService(serviceID)
	key := keyBuilder.BuildKey(serviceID, endpointAddr, rpcType)

	// Map observation error type to signal
	// See ERROR_CLASSIFICATION.md for error category documentation
	errorType := obs.GetErrorType()

	var signal reputation.Signal
	var isSuccess bool

	// No error = success
	if errorType == protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED {
		signal = reputation.NewSuccessSignal(0)
		isSuccess = true
	} else {
		// Map error type directly to reputation signal
		signal = errorTypeToSignal(errorType, 0)
		isSuccess = false
	}

	// Check if this endpoint is in probation and apply recovery multiplier
	selector := p.getTieredSelectorForService(serviceID)
	if selector != nil && selector.Config().Probation.Enabled && selector.IsInProbation(key) {
		// If probation traffic succeeds, apply recovery multiplier to the signal
		if isSuccess && selector.Config().Probation.RecoveryMultiplier > 0 {
			// Apply recovery multiplier to boost recovery
			signal = signal.WithMultiplier(selector.Config().Probation.RecoveryMultiplier)
		}
	}

	// Record signal (fire-and-forget, non-blocking)
	ctx := context.Background()
	if err := p.reputationService.RecordSignal(ctx, key, signal); err != nil {
		p.logger.Warn().Err(err).
			Str("endpoint", string(endpointAddr)).
			Str("service", string(serviceID)).
			Str("error_type", errorType.String()).
			Msg("Failed to record reputation signal from observation")
	}
}

// ** Health Check Integration **

// GetEndpointsForHealthCheck returns a function that provides endpoint information
// for health checks. This is used by the HealthCheckExecutor.RunAllChecks method.
//
// The returned function:
//   - Gets sessions for the service from all owned apps
//   - Filters by RPC type: Only endpoints supporting health check RPC types
//   - Filters by session validity: Only endpoints from sessions within grace period
//   - Does NOT filter by reputation: Health checks help recover low-scoring endpoints
//   - Extracts endpoints from sessions with HTTP and WebSocket URLs
//   - Returns []gateway.EndpointInfo suitable for health checks
func (p *Protocol) GetEndpointsForHealthCheck() func(protocol.ServiceID) ([]gateway.EndpointInfo, error) {
	return func(serviceID protocol.ServiceID) ([]gateway.EndpointInfo, error) {
		ctx := context.Background()
		logger := p.logger.With("method", "GetEndpointsForHealthCheck", "service_id", string(serviceID))

		// Get active sessions for this service (without filtering by reputation)
		activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get sessions for service %s: %w", serviceID, err)
		}

		if len(activeSessions) == 0 {
			logger.Debug().Msg("No active sessions for service")
			return nil, nil
		}

		// Get current block height for session validity filtering
		currentHeight, err := p.GetCurrentBlockHeight(ctx)
		if err != nil {
			logger.Warn().Err(err).Msg("Failed to get current block height, skipping session validity filter")
			currentHeight = 0 // If we can't get height, include all sessions
		}

		// Get grace period from shared params
		var gracePeriod int64 = 0
		if currentHeight > 0 {
			sharedParams, err := p.GetSharedParams(ctx)
			if err != nil {
				logger.Warn().Err(err).Msg("Failed to get shared params for grace period, using 0")
			} else {
				gracePeriod = int64(sharedParams.GracePeriodEndOffsetBlocks)
			}
		}

		// Determine which RPC types are used in health checks for this service
		healthCheckRPCTypes := p.getHealthCheckRPCTypes(serviceID)
		if len(healthCheckRPCTypes) == 0 {
			logger.Debug().Msg("No health checks configured for service")
			return nil, nil
		}

		logger.Debug().
			Int64("current_height", currentHeight).
			Int64("grace_period", gracePeriod).
			Int("health_check_rpc_types", len(healthCheckRPCTypes)).
			Msg("Filtering endpoints for health checks")

		// Collect endpoints from valid sessions, filtered by RPC type
		allEndpoints := make(map[protocol.EndpointAddr]endpoint)

		for _, session := range activeSessions {
			sessionEndHeight := session.Header.SessionEndBlockHeight
			sessionEndWithGrace := sessionEndHeight + gracePeriod

			// Skip sessions that have expired (beyond grace period)
			if currentHeight > 0 && currentHeight > sessionEndWithGrace {
				logger.Debug().
					Str("session_id", session.SessionId).
					Int64("session_end", sessionEndHeight).
					Int64("session_end_with_grace", sessionEndWithGrace).
					Msg("Skipping expired session (beyond grace period)")
				continue
			}

			sessionEndpoints, err := p.getOrCreateSessionEndpoints(session)
			if err != nil {
				logger.Warn().
					Err(err).
					Str("session_id", session.SessionId).
					Msg("Failed to get endpoints from session")
				continue
			}

			// Filter endpoints by RPC type support
			for addr, ep := range sessionEndpoints {
				supportsAnyType := false
				for rpcType := range healthCheckRPCTypes {
					url := ep.GetURL(rpcType)
					if url != "" {
						supportsAnyType = true
						break
					}
				}

				if supportsAnyType {
					allEndpoints[addr] = ep
				} else {
					logger.Debug().
						Str("endpoint", string(addr)).
						Msg("Skipping endpoint - does not support any health check RPC types")
				}
			}
		}

		// Also include fallback endpoints if configured, filtered by RPC type
		fallbackEndpoints, _ := p.getServiceFallbackEndpoints(serviceID)
		for addr, ep := range fallbackEndpoints {
			supportsAnyType := false
			for rpcType := range healthCheckRPCTypes {
				url := ep.GetURL(rpcType)
				if url != "" {
					supportsAnyType = true
					break
				}
			}

			if supportsAnyType {
				allEndpoints[addr] = ep
			}
		}

		if len(allEndpoints) == 0 {
			logger.Debug().Msg("No endpoints available after filtering")
			return nil, nil
		}

		// Convert to gateway.EndpointInfo
		result := make([]gateway.EndpointInfo, 0, len(allEndpoints))
		for _, ep := range allEndpoints {
			info := gateway.EndpointInfo{
				Addr:    ep.Addr(),
				HTTPURL: ep.PublicURL(),
			}

			// Get WebSocket URL if available
			if wsURL, err := ep.WebsocketURL(); err == nil {
				info.WebSocketURL = wsURL
			}

			// Include session ID for tracking session rollover
			if session := ep.Session(); session != nil {
				info.SessionID = session.SessionId
			}

			result = append(result, info)
		}

		logger.Info().
			Int("endpoint_count", len(result)).
			Int("session_count", len(activeSessions)).
			Msg("Retrieved filtered endpoints for health checks")

		return result, nil
	}
}

// getHealthCheckRPCTypes extracts the RPC types used in health checks for a service.
// Returns a map of RPC types (as keys) that are configured in health checks.
// If no local health checks are configured, returns default RPC types (JSON_RPC)
// to allow external health checks to run.
func (p *Protocol) getHealthCheckRPCTypes(serviceID protocol.ServiceID) map[sharedtypes.RPCType]struct{} {
	rpcTypes := make(map[sharedtypes.RPCType]struct{})

	// If no unified services config, return default JSON_RPC to allow external health checks
	if p.unifiedServicesConfig == nil {
		rpcTypes[sharedtypes.RPCType_JSON_RPC] = struct{}{}
		return rpcTypes
	}

	// Find the service configuration
	var svcConfig *gateway.ServiceConfig
	for i := range p.unifiedServicesConfig.Services {
		if p.unifiedServicesConfig.Services[i].ID == serviceID {
			svcConfig = &p.unifiedServicesConfig.Services[i]
			break
		}
	}

	// If service not found in unified config, return default JSON_RPC
	// This allows external health checks to run for services defined only in external config
	if svcConfig == nil {
		rpcTypes[sharedtypes.RPCType_JSON_RPC] = struct{}{}
		return rpcTypes
	}

	// Extract RPC types from local health check configurations
	if svcConfig.HealthChecks != nil && len(svcConfig.HealthChecks.Local) > 0 {
		mapper := gateway.NewRPCTypeMapper()
		for _, check := range svcConfig.HealthChecks.Local {
			// Skip disabled checks
			if check.Enabled != nil && !*check.Enabled {
				continue
			}

			// Convert health check type to RPC type
			rpcType, err := mapper.ParseRPCType(string(check.Type))
			if err != nil {
				p.logger.Warn().
					Str("service_id", string(serviceID)).
					Str("check_name", check.Name).
					Str("check_type", string(check.Type)).
					Err(err).
					Msg("Failed to parse RPC type from health check config")
				continue
			}

			rpcTypes[rpcType] = struct{}{}
		}
	}

	// If no local health checks configured, return default JSON_RPC to allow external health checks
	if len(rpcTypes) == 0 {
		rpcTypes[sharedtypes.RPCType_JSON_RPC] = struct{}{}
	}

	return rpcTypes
}

// GetReputationService returns the reputation service instance used by the protocol.
// This is used by the health check executor to record health check results.
func (p *Protocol) GetReputationService() reputation.ReputationService {
	return p.reputationService
}

// GetUnifiedServicesConfig returns the unified services configuration.
// This is used by components that need access to per-service configuration overrides.
func (p *Protocol) GetUnifiedServicesConfig() *gateway.UnifiedServicesConfig {
	return p.unifiedServicesConfig
}

// getRPCTypeFallback checks if a fallback RPC type is configured for the given service and RPC type.
// Returns the fallback RPC type and true if configured, or zero value and false otherwise.
//
// This is a temporary workaround for suppliers that stake with incorrect RPC types.
// Example: If cosmoshub is configured with {comet_bft: json_rpc}, requests for comet_bft
// will fall back to json_rpc endpoints if no comet_bft endpoints are found.
func (p *Protocol) getRPCTypeFallback(serviceID protocol.ServiceID, requestedRPCType sharedtypes.RPCType) (sharedtypes.RPCType, bool) {
	if p.unifiedServicesConfig == nil {
		return sharedtypes.RPCType_UNKNOWN_RPC, false
	}

	// Find the service configuration
	var svcConfig *gateway.ServiceConfig
	for i := range p.unifiedServicesConfig.Services {
		if p.unifiedServicesConfig.Services[i].ID == serviceID {
			svcConfig = &p.unifiedServicesConfig.Services[i]
			break
		}
	}

	if svcConfig == nil || svcConfig.RPCTypeFallbacks == nil {
		return sharedtypes.RPCType_UNKNOWN_RPC, false
	}

	// Look up fallback for the requested RPC type
	// Try exact string match first, then lowercase version for flexibility
	rpcTypeStr := requestedRPCType.String()
	fallbackStr, exists := svcConfig.RPCTypeFallbacks[rpcTypeStr]
	if !exists {
		// Try lowercase version (config might use lowercase like "comet_bft")
		fallbackStr, exists = svcConfig.RPCTypeFallbacks[strings.ToLower(rpcTypeStr)]
		if !exists {
			return sharedtypes.RPCType_UNKNOWN_RPC, false
		}
	}

	// Parse the fallback RPC type string
	fallbackRPCType := sharedtypes.RPCType(sharedtypes.RPCType_value[strings.ToUpper(fallbackStr)])
	if fallbackRPCType == sharedtypes.RPCType_UNKNOWN_RPC {
		return sharedtypes.RPCType_UNKNOWN_RPC, false
	}

	return fallbackRPCType, true
}

// GetConcurrencyConfig returns the concurrency configuration.
// This is used by components that need to respect concurrency limits.
func (p *Protocol) GetConcurrencyConfig() gateway.ConcurrencyConfig {
	return p.concurrencyConfig
}

// UnblacklistSupplier removes a supplier from the blacklist.
// Called when a health check succeeds for a previously blacklisted supplier.
// Returns true if the supplier was blacklisted and has been removed.
func (p *Protocol) UnblacklistSupplier(serviceID protocol.ServiceID, supplierAddr string) bool {
	if p.supplierBlacklist == nil {
		return false
	}
	return p.supplierBlacklist.Unblacklist(serviceID, supplierAddr)
}

// IsSupplierBlacklisted checks if a supplier is currently blacklisted.
func (p *Protocol) IsSupplierBlacklisted(serviceID protocol.ServiceID, supplierAddr string) bool {
	if p.supplierBlacklist == nil {
		return false
	}
	return p.supplierBlacklist.IsBlacklisted(serviceID, supplierAddr)
}

// IsSessionActive checks if a session is currently active for a service.
// Returns true if the session is still in the current active sessions list.
// This is used by health check executor to detect session rollover.
func (p *Protocol) IsSessionActive(ctx context.Context, serviceID protocol.ServiceID, sessionID string) bool {
	// Get current active sessions for this service
	sessions, err := p.getActiveGatewaySessions(ctx, serviceID, nil)
	if err != nil {
		// If we can't get sessions, assume it's active to avoid false negatives
		return true
	}

	// Check if the sessionID is in the current active sessions
	for _, session := range sessions {
		if session.SessionId == sessionID {
			return true
		}
	}

	return false
}

// extractSupplierFromEndpointAddr extracts the supplier address from an endpoint address.
// Endpoint addresses are in the format "supplier-url" (e.g., "pokt1abc...-https://example.com").
// Returns the supplier portion, or the full address if no separator is found.
func extractSupplierFromEndpointAddr(endpointAddr string) string {
	// Find the first occurrence of "-http" which separates supplier from URL
	if idx := strings.Index(endpointAddr, "-http"); idx != -1 {
		return endpointAddr[:idx]
	}
	// Fallback: return the full address
	return endpointAddr
}
