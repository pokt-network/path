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

	// TODO_TECHDEBT(@adshmh,@commoddity,@olshansk): JSON_RPC RPC type should more correctly be called HTTP
	// when used in this context. Add an HTTP RPC-type to the enum in poktroll and update this map when it is done.
	//
	// sanctionedEndpointsStores tracks sanctioned endpoints per RPC type
	// currently only JSON_RPC (stand-in for HTTP) and WEBSOCKET are supported
	sanctionedEndpointsStores map[sharedtypes.RPCType]*sanctionedEndpointsStore

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
		// tracks sanctioned endpoints per RPC type
		// currently only JSON_RPC and WEBSOCKET are supported
		sanctionedEndpointsStores: map[sharedtypes.RPCType]*sanctionedEndpointsStore{
			sharedtypes.RPCType_JSON_RPC:  newSanctionedEndpointsStore(logger, config.SanctionConfig),
			sharedtypes.RPCType_WEBSOCKET: newSanctionedEndpointsStore(logger, config.SanctionConfig),
		},

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
	}

	// Initialize reputation service if enabled.
	// When enabled, endpoints are filtered by reputation score in addition to binary sanctions.
	if config.ReputationConfig.Enabled {
		reputationLogger := shannonLogger.With("component", "reputation")

		// Create storage based on configuration.
		var store reputation.Storage
		switch config.ReputationConfig.StorageType {
		case "memory", "":
			// Use recovery timeout as TTL for entries - expired entries get auto-cleaned
			store = reputationstorage.NewMemoryStorage(config.ReputationConfig.RecoveryTimeout)
		case "redis":
			if config.ReputationConfig.Redis == nil {
				return nil, fmt.Errorf("redis storage requires redis configuration")
			}
			redisStore, err := reputationstorage.NewRedisStorage(ctx, *config.ReputationConfig.Redis, config.ReputationConfig.RecoveryTimeout)
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
	}, protocolobservations.Observations{}, nil
}

// ApplyHTTPObservations updates protocol instance state based on endpoint observations.
// Examples:
// - Mark endpoints as invalid based on response quality
// - Disqualify endpoints for a time period
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
	// hand over the observations to the sanctioned endpoints store for adding any applicable sanctions.
	sanctionedEndpointsStore, ok := p.sanctionedEndpointsStores[sharedtypes.RPCType_JSON_RPC]
	if !ok {
		return fmt.Errorf("INVARIANT VIOLATION: sanctioned endpoints store not initialized for RPC type: %s", sharedtypes.RPCType_JSON_RPC)
	}
	sanctionedEndpointsStore.ApplyObservations(shannonObservations)

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
		// Sanctioned endpoints will be filtered out below if a valid RPC type is provided.
		qualifiedEndpoints := sessionEndpoints

		// Filter out sanctioned endpoints if a valid RPC type is provided.
		// If no valid RPC type is provided, don't filter out sanctioned endpoints.
		// As of PR #424 the only supported RPC types are JSON_RPC and WEBSOCKET.
		if sanctionedEndpointsStore, ok := p.sanctionedEndpointsStores[filterByRPCType]; ok {
			logger.Debug().Msgf(
				"app %s has %d endpoints before filtering sanctioned endpoints.",
				app.Address, len(sessionEndpoints),
			)

			// Filter out any sanctioned endpoints
			filteredEndpoints := sanctionedEndpointsStore.FilterSanctionedEndpoints(qualifiedEndpoints)
			// All endpoints are sanctioned: log a warning and skip this app.
			if len(filteredEndpoints) == 0 {
				logger.Error().Msgf(
					"❌ All %d session endpoints are sanctioned for service %s, app %s. SKIPPING the app.",
					len(sessionEndpoints), serviceID, app.Address,
				)
				continue
			}
			qualifiedEndpoints = filteredEndpoints

			logger.Debug().Msgf("app %s has %d endpoints after filtering sanctioned endpoints.", app.Address, len(qualifiedEndpoints))
		}

		// Filter out low-reputation endpoints if reputation service is enabled.
		// This provides gradual exclusion based on score in addition to binary sanctions.
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

	details.ProtocolLevelDisqualifiedEndpoints = make(map[string]devtools.ProtocolLevelDataResponse)

	for rpcType, sanctionedEndpointsStore := range p.sanctionedEndpointsStores {
		details.ProtocolLevelDisqualifiedEndpoints[rpcType.String()] = sanctionedEndpointsStore.getSanctionDetails(serviceID)
	}
}
