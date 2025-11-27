package cosmos

import (
	"context"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics/devtools"
	"github.com/pokt-network/path/protocol"
)

// QoS implements gateway.QoSService by providing:
//  1. QoSRequestParser - Builds CosmosSDK-specific RequestQoSContext objects from HTTP requests
//  2. EndpointSelector - Selects endpoints for service requests
var _ gateway.QoSService = &QoS{}

// devtools.QoSDisqualifiedEndpointsReporter is fulfilled by the QoS struct below.
// This allows the QoS service to report its disqualified endpoints data to the devtools.DisqualifiedEndpointReporter.
var _ devtools.QoSDisqualifiedEndpointsReporter = &QoS{}

// QoS implements ServiceQoS for CosmosSDK-based chains.
// It handles chain-specific:
//   - Request parsing (both REST and JSON-RPC)
//   - Response building
//   - Endpoint validation and selection
type QoS struct {
	logger polylog.Logger
	*serviceState
	*requestValidator
}

// NewQoSInstance builds and returns an instance of the CosmosSDK QoS service.
func NewQoSInstance(logger polylog.Logger, config CosmosSDKServiceQoSConfig) *QoS {
	serviceId := config.GetServiceID()

	cosmosChainID := config.getCosmosSDKChainID()
	// Some CosmosSDK services may have an EVM chain ID. For example, XRPLEVM.
	evmChainID := config.getEVMChainID()

	logger = logger.With(
		"qos_instance", "cosmossdk",
		"service_id", serviceId,
		"cosmos_chain_id", cosmosChainID,
		"evm_chain_id", evmChainID,
	)

	store := &endpointStore{
		logger: logger,
		// Initialize the endpoint store with an empty map.
		endpoints: make(map[protocol.EndpointAddr]endpoint),
	}

	serviceState := &serviceState{
		logger:           logger,
		serviceQoSConfig: config,
		endpointStore:    store,
	}

	requestValidator := &requestValidator{
		logger:        logger,
		serviceID:     serviceId,
		cosmosChainID: cosmosChainID,
		evmChainID:    evmChainID,
		serviceState:  serviceState,
		supportedAPIs: config.getSupportedAPIs(),
	}

	return &QoS{
		logger:           logger,
		serviceState:     serviceState,
		requestValidator: requestValidator,
	}
}

// ParseHTTPRequest builds a request context from an HTTP request.
// Returns (requestContext, true) if the request is valid
// Returns (errorContext, false) if the request is not valid.
//
// Supports both REST endpoints (/health, /status) and JSON-RPC requests.
//
// Implements gateway.QoSService interface.
func (qos *QoS) ParseHTTPRequest(_ context.Context, req *http.Request) (gateway.RequestQoSContext, bool) {
	return qos.validateHTTPRequest(req)
}

// ParseWebsocketRequest builds a request context from the provided Websocket request.
// Websocket connection requests do not have a body, so we don't need to parse it.
//
// Implements gateway.QoSService interface.
func (qos *QoS) ParseWebsocketRequest(_ context.Context) (gateway.RequestQoSContext, bool) {
	return qos.validateWebsocketRequest()
}

// HydrateDisqualifiedEndpointsResponse hydrates the disqualified endpoint response with the QoS-specific data.
//   - takes a pointer to the DisqualifiedEndpointResponse
//   - called by the devtools.DisqualifiedEndpointReporter to fill it with the QoS-specific data.
func (qos *QoS) HydrateDisqualifiedEndpointsResponse(serviceID protocol.ServiceID, details *devtools.DisqualifiedEndpointResponse) {
	qos.logger.Info().Msgf("hydrating disqualified endpoints response for service ID: %s", serviceID)
	details.QoSLevelDisqualifiedEndpoints = qos.getDisqualifiedEndpointsResponse(serviceID)
}
