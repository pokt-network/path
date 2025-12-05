package shannon

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"runtime"

	"github.com/alitto/pond/v2"
	"github.com/pokt-network/poktroll/pkg/polylog"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/health"
	"github.com/pokt-network/path/metrics/devtools"
	reputationmetrics "github.com/pokt-network/path/metrics/reputation"
	pathhttp "github.com/pokt-network/path/network/http"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
)

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
// This allows the protocol to report its sanctioned endpoints data to the devtools.DisqualifiedEndpointReporter.
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
	// For example, if all protocol endpoints are sanctioned, the fallback
	// endpoints will be used to populate the list of endpoints.
	//
	// Each service can have a SendAllTraffic flag to send all traffic to
	// fallback endpoints, regardless of the health of the protocol endpoints.
	serviceFallbackMap map[protocol.ServiceID]serviceFallback

	// Optional.
	// Puts the Gateway in LoadTesting mode if specified.
	// All relays will be sent to a fixed URL.
	// Allows measuring performance of PATH and full node(s) in isolation.
	loadTestingConfig *LoadTestingConfig

	// reputationService tracks endpoint reputation scores.
	// If enabled, endpoints are filtered by score in addition to binary sanctions.
	// When nil, only binary sanctions are used for endpoint filtering.
	reputationService reputation.ReputationService

	// tieredSelector selects endpoints using cascade-down tier logic.
	// Created when reputation service is enabled with tiered selection enabled.
	tieredSelector *reputation.TieredSelector

	// serviceTieredSelectors stores per-service TieredSelectors for services with custom thresholds.
	// When a service has a per-service tiered selection config, its selector is stored here.
	// Falls back to the global tieredSelector if not present.
	serviceTieredSelectors map[protocol.ServiceID]*reputation.TieredSelector

	// unifiedServicesConfig is the unified YAML-driven service configuration.
	// This consolidates all per-service settings and enables per-service overrides.
	unifiedServicesConfig *gateway.UnifiedServicesConfig
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
		// Uses MaxConcurrentRelaysPerRequest as the max workers to bound global concurrency.
		relayPool: pond.NewPool(runtime.NumCPU() * 2),

		// serviceFallbacks contains the fallback information for each service.
		serviceFallbackMap: config.getServiceFallbackMap(),

		// load testing config, if specified.
		loadTestingConfig: config.LoadTestingConfig,

		// unifiedServicesConfig for per-service configuration overrides
		unifiedServicesConfig: &config.UnifiedServices,
	}

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
			protocolInstance.tieredSelector = reputation.NewTieredSelector(
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

							protocolInstance.serviceTieredSelectors[svc.ID] = reputation.NewTieredSelector(
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

	// Retrieve a list of all unique endpoints for the given service ID filtered by
	// the list of apps this gateway/application owns and can send relays on behalf of.
	//
	// This includes fallback logic: if all session endpoints are sanctioned and the
	// requested service is configured with at least one fallback URL, the fallback
	// endpoints will be used to populate the list of endpoints.
	//
	// The final boolean parameter sets whether to filter out sanctioned endpoints.
	endpoints, err := p.getUniqueEndpoints(ctx, serviceID, activeSessions, true, sharedtypes.RPCType_JSON_RPC)
	if err != nil {
		logger.Error().Err(err).Msg(err.Error())
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
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

	// Retrieve a list of all unique endpoints for the given service ID filtered by
	// the list of apps this gateway/application owns and can send relays on behalf of.
	//
	// This includes fallback logic: if all session endpoints are sanctioned and the
	// requested service is configured with at least one fallback URL, the fallback
	// endpoints will be used to populate the list of endpoints.
	//
	// The final boolean parameter sets whether to filter out sanctioned endpoints.
	endpoints, err := p.getUniqueEndpoints(ctx, serviceID, activeSessions, true, sharedtypes.RPCType_WEBSOCKET)
	if err != nil {
		logger.Error().Err(err).Msg(err.Error())
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
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
//   - Filtering out sanctioned endpoints from list of unique endpoints.
//   - Obtains the relay request signer appropriate for the current gateway mode.
//   - Returns a fully initialized request context for use in downstream protocol operations.
//   - On failure, logs the error, returns a context setup observation, and a non-nil error.
//
// Implements the gateway.Protocol interface.
func (p *Protocol) BuildHTTPRequestContextForEndpoint(
	ctx context.Context,
	serviceID protocol.ServiceID,
	selectedEndpointAddr protocol.EndpointAddr,
	httpReq *http.Request,
) (gateway.ProtocolRequestContext, protocolobservations.Observations, error) {
	logger := p.logger.With(
		"method", "BuildHTTPRequestContextForEndpoint",
		"service_id", serviceID,
		"endpoint_addr", selectedEndpointAddr,
	)

	activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, httpReq)
	if err != nil {
		logger.Error().Err(err).Msgf("Relay request will fail due to error retrieving active sessions for service %s", serviceID)
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	// Retrieve the list of endpoints (i.e. backend service URLs by external operators)
	// that can service RPC requests for the given service ID for the given apps.
	// This includes fallback logic if session endpoints are unavailable.
	// The final boolean parameter sets whether to filter out sanctioned endpoints.
	endpoints, err := p.getUniqueEndpoints(ctx, serviceID, activeSessions, true, sharedtypes.RPCType_JSON_RPC)
	if err != nil {
		logger.Error().Err(err).Msg(err.Error())
		return nil, buildProtocolContextSetupErrorObservation(serviceID, err), err
	}

	// Select the endpoint that matches the pre-selected address.
	// This ensures QoS checks are performed on the selected endpoint.
	selectedEndpoint, ok := endpoints[selectedEndpointAddr]
	if !ok {
		// Wrap the context setup error.
		// Used to generate the observation.
		err := fmt.Errorf("%w: service %s endpoint %s", errRequestContextSetupInvalidEndpointSelected, serviceID, selectedEndpointAddr)
		logger.Error().Err(err).Msg("Selected endpoint is not available.")
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

	// Return new request context for the pre-selected endpoint
	return &requestContext{
		logger:             p.logger,
		context:            ctx,
		fullNode:           p.FullNode,
		selectedEndpoint:   selectedEndpoint,
		serviceID:          serviceID,
		relayRequestSigner: permittedSigner,
		httpClient:         p.httpClient,
		fallbackEndpoints:  fallbackEndpoints,
		loadTestingConfig:  p.loadTestingConfig,
		relayPool:          p.relayPool,
		reputationService:  p.reputationService,
		currentRPCType:     sharedtypes.RPCType_JSON_RPC, // Health checks use JSON-RPC by default
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
func (p *Protocol) getUniqueEndpoints(
	ctx context.Context,
	serviceID protocol.ServiceID,
	activeSessions []sessiontypes.Session,
	filterSanctioned bool,
	rpcType sharedtypes.RPCType,
) (map[protocol.EndpointAddr]endpoint, error) {
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
		logger.Info().Msgf("🔀 Sending all traffic to fallback endpoints for service %s.", serviceID)
		return fallbackEndpoints, nil
	}

	// Try to get session endpoints first.
	sessionEndpoints, err := p.getSessionsUniqueEndpoints(ctx, serviceID, activeSessions, rpcType)
	if err != nil {
		logger.Error().Err(err).Msgf("Error getting session endpoints for service %s: %v", serviceID, err)
	}

	// Session endpoints are available, use them.
	// This is the happy path where we have unsanctioned session endpoints available.
	if len(sessionEndpoints) > 0 {
		return sessionEndpoints, nil
	}

	// Handle the case where no session endpoints are available.
	// If fallback endpoints are available for the service ID, use them.
	if len(fallbackEndpoints) > 0 {
		return fallbackEndpoints, nil
	}

	// If no unsanctioned session endpoints are available and no fallback
	// endpoints are available for the service ID, return an error.
	// Wrap the context setup error. Used for generating observations.
	err = fmt.Errorf("%w: service %s", errProtocolContextSetupNoEndpoints, serviceID)
	logger.Warn().Err(err).Msg("No endpoints or fallback available after filtering sanctioned endpoints: relay request will fail.")
	return nil, err
}

// getSessionsUniqueEndpoints returns a map of all endpoints matching service ID from active sessions.
// This function focuses solely on retrieving and filtering session endpoints.
//
// If an endpoint matches a serviceID across multiple apps/sessions, only a single
// entry matching one of the apps/sessions is returned.
func (p *Protocol) getSessionsUniqueEndpoints(
	ctx context.Context,
	serviceID protocol.ServiceID,
	activeSessions []sessiontypes.Session,
	filterByRPCType sharedtypes.RPCType,
) (map[protocol.EndpointAddr]endpoint, error) {
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

	// TODO_TECHDEBT(@adshmh): Refactor load testing related code to make the filtering more visible.
	//
	// In Load Testing using RelayMiner mode: drop any endpoints ot matching the single supplier specified in the config.
	//
	var allowedSupplierAddr string
	if ltc := p.loadTestingConfig; ltc != nil {
		if ltc.RelayMinerConfig != nil {
			allowedSupplierAddr = ltc.RelayMinerConfig.SupplierAddr
		}
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

		// Retrieve all endpoints for the session.
		sessionEndpoints, err := endpointsFromSession(session, allowedSupplierAddr)
		if err != nil {
			logger.Error().Err(err).Msgf("Internal error: error getting all endpoints for service %s app %s and session: skipping the app.", serviceID, app.Address)
			continue
		}

		// Initialize the qualified endpoints as the full set of session endpoints.
		// Low-reputation endpoints will be filtered out below if reputation service is enabled.
		qualifiedEndpoints := sessionEndpoints

		// Filter out low-reputation endpoints if reputation service is enabled.
		// Reputation is the primary endpoint quality system - it provides gradual
		// exclusion based on score and allows recovery via health checks.
		if p.reputationService != nil {
			beforeCount := len(qualifiedEndpoints)
			qualifiedEndpoints = p.filterByReputation(ctx, serviceID, qualifiedEndpoints, logger)

			if len(qualifiedEndpoints) == 0 {
				logger.Warn().Msgf(
					"⚠️ All %d endpoints below reputation threshold for service %s, app %s. SKIPPING the app.",
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
		logger.Info().Msgf("Filtered session endpoints for app %s from %d to %d.", app.Address, len(sessionEndpoints), len(qualifiedEndpoints))

		maps.Copy(endpoints, qualifiedEndpoints)

		logger.Info().Msgf(
			"Successfully fetched %d endpoints for session %s for application %s for service %s.",
			len(qualifiedEndpoints), session.SessionId, app.Address, serviceID,
		)
	}

	// Return session endpoints if available.
	if len(endpoints) > 0 {
		// Apply tiered selection if enabled - only return endpoints from the highest available tier
		if p.tieredSelector != nil && p.tieredSelector.Config().Enabled {
			endpoints = p.filterToHighestTier(ctx, serviceID, endpoints, logger)
		}

		logger.Info().Msgf("Successfully fetched %d session endpoints for active sessions.", len(endpoints))
		return endpoints, nil
	}

	// No session endpoints are available.
	err := fmt.Errorf("%w: service %s", errProtocolContextSetupNoEndpoints, serviceID)
	logger.Warn().Err(err).Msg("No session endpoints available after filtering.")
	return nil, err
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
// without filtering sanctioned endpoints.
func (p *Protocol) GetTotalServiceEndpointsCount(serviceID protocol.ServiceID, httpReq *http.Request) (int, error) {
	ctx := context.Background()

	// Get the list of active sessions for the service ID.
	activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, httpReq)
	if err != nil {
		return 0, err
	}

	// Get all endpoints for the service ID without filtering sanctioned endpoints.
	// Since we don't want to filter sanctioned endpoints, we use an unsupported RPC type.
	endpoints, err := p.getSessionsUniqueEndpoints(ctx, serviceID, activeSessions, sharedtypes.RPCType_UNKNOWN_RPC)
	if err != nil {
		return 0, err
	}

	return len(endpoints), nil
}

// HydrateDisqualifiedEndpointsResponse hydrates the disqualified endpoint response with the protocol-specific data.
//   - takes a pointer to the DisqualifiedEndpointResponse
//   - called by the devtools.DisqualifiedEndpointReporter to fill it with the protocol-specific data.
func (p *Protocol) HydrateDisqualifiedEndpointsResponse(serviceID protocol.ServiceID, details *devtools.DisqualifiedEndpointResponse) {
	p.logger.Info().Msgf("hydrating disqualified endpoints response for service ID: %s", serviceID)

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
// It maps the observation's error type and sanction type to a reputation signal and records it.
// Also records probation traffic metrics if the endpoint is in probation.
func (p *Protocol) recordSignalFromObservation(serviceID protocol.ServiceID, obs *protocolobservations.ShannonEndpointObservation) {
	endpointAddr := protocol.EndpointAddr(obs.GetEndpointUrl())

	// Build endpoint key for reputation service
	key := reputation.NewEndpointKey(serviceID, endpointAddr)

	// Map observation to signal using the existing mapping function
	errorType := obs.GetErrorType()
	sanctionType := obs.GetRecommendedSanction()

	var signal reputation.Signal
	var isSuccess bool

	// No error = success
	if errorType == protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED {
		signal = reputation.NewSuccessSignal(0)
		isSuccess = true
	} else {
		// Map error type and sanction type to a reputation signal
		signal = mapErrorToSignal(errorType, sanctionType, 0)
		isSuccess = false
	}

	// Check if this endpoint is in probation and record probation traffic metric
	selector := p.getTieredSelectorForService(serviceID)
	if selector != nil && selector.Config().Probation.Enabled && selector.IsInProbation(key) {
		domain := extractEndpointDomain(string(endpointAddr), p.logger)
		reputationmetrics.RecordProbationTraffic(string(serviceID), domain, isSuccess)

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
//   - Extracts endpoints from sessions with HTTP and WebSocket URLs
//   - Returns []gateway.EndpointInfo suitable for health checks
//
// Note: This does NOT filter by reputation - health checks should run against
// all endpoints to allow recovery of low-scoring endpoints.
func (p *Protocol) GetEndpointsForHealthCheck() func(protocol.ServiceID) ([]gateway.EndpointInfo, error) {
	return func(serviceID protocol.ServiceID) ([]gateway.EndpointInfo, error) {
		ctx := context.Background()

		// Get active sessions for this service (without filtering by reputation)
		activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get sessions for service %s: %w", serviceID, err)
		}

		if len(activeSessions) == 0 {
			p.logger.Debug().
				Str("service_id", string(serviceID)).
				Msg("No active sessions for service")
			return nil, nil
		}

		// Collect all unique endpoints from all sessions
		allEndpoints := make(map[protocol.EndpointAddr]endpoint)

		for _, session := range activeSessions {
			sessionEndpoints, err := endpointsFromSession(session, "")
			if err != nil {
				p.logger.Warn().
					Err(err).
					Str("service_id", string(serviceID)).
					Str("session_id", session.SessionId).
					Msg("Failed to get endpoints from session")
				continue
			}
			maps.Copy(allEndpoints, sessionEndpoints)
		}

		// Also include fallback endpoints if configured
		fallbackEndpoints, _ := p.getServiceFallbackEndpoints(serviceID)
		maps.Copy(allEndpoints, fallbackEndpoints)

		if len(allEndpoints) == 0 {
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

			result = append(result, info)
		}

		p.logger.Debug().
			Str("service_id", string(serviceID)).
			Int("endpoint_count", len(result)).
			Msg("Retrieved endpoints for health checks")

		return result, nil
	}
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
