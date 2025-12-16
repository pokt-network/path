package cosmos

import (
	"context"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics/devtools"
	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
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

// NewSimpleQoSInstance creates a minimal CosmosSDK QoS instance without chain-specific validation.
// Validation (chain ID, etc.) is now handled by active health checks.
// This constructor only requires the service ID and provides REST/JSON-RPC request parsing.
func NewSimpleQoSInstance(logger polylog.Logger, serviceID protocol.ServiceID) *QoS {
	return NewSimpleQoSInstanceWithSyncAllowance(logger, serviceID, 0)
}

// NewSimpleQoSInstanceWithSyncAllowance creates a minimal CosmosSDK QoS instance with custom sync allowance.
// Validation (chain ID, etc.) is now handled by active health checks.
// If syncAllowance is 0, the default value is used.
func NewSimpleQoSInstanceWithSyncAllowance(logger polylog.Logger, serviceID protocol.ServiceID, syncAllowance uint64) *QoS {
	// Default supported APIs for CosmosSDK chains (REST and CometBFT)
	defaultAPIs := map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {},
		sharedtypes.RPCType_COMET_BFT: {},
	}
	return NewSimpleQoSInstanceWithAPIs(logger, serviceID, syncAllowance, defaultAPIs)
}

// NewSimpleQoSInstanceWithAPIs creates a minimal CosmosSDK QoS instance with custom supported APIs.
// This is useful for hybrid chains (e.g., XRPLEVM) that support both CosmosSDK and EVM APIs.
// Validation (chain ID, etc.) is now handled by active health checks.
// If syncAllowance is 0, the default value is used.
func NewSimpleQoSInstanceWithAPIs(logger polylog.Logger, serviceID protocol.ServiceID, syncAllowance uint64, supportedAPIs map[sharedtypes.RPCType]struct{}) *QoS {
	logger = logger.With(
		"qos_instance", "cosmossdk",
		"service_id", serviceID,
	)

	store := &endpointStore{
		logger:    logger,
		endpoints: make(map[protocol.EndpointAddr]endpoint),
	}

	// Create a minimal config wrapper
	minimalConfig := &simpleCosmosConfig{
		serviceID:     serviceID,
		syncAllowance: syncAllowance,
		supportedAPIs: supportedAPIs,
	}

	serviceState := &serviceState{
		logger:           logger,
		serviceQoSConfig: minimalConfig,
		endpointStore:    store,
	}

	requestValidator := &requestValidator{
		logger:        logger,
		serviceID:     serviceID,
		cosmosChainID: "", // No chain ID validation - handled by health checks
		evmChainID:    "",
		serviceState:  serviceState,
		supportedAPIs: supportedAPIs,
	}

	return &QoS{
		logger:           logger,
		serviceState:     serviceState,
		requestValidator: requestValidator,
	}
}

// simpleCosmosConfig is a minimal config for services without chain-specific params.
type simpleCosmosConfig struct {
	serviceID     protocol.ServiceID
	syncAllowance uint64                           // If 0, uses default
	supportedAPIs map[sharedtypes.RPCType]struct{} // Supported RPC types
}

func (c *simpleCosmosConfig) GetServiceID() protocol.ServiceID { return c.serviceID }
func (c *simpleCosmosConfig) GetServiceQoSType() string        { return QoSType }
func (c *simpleCosmosConfig) getCosmosSDKChainID() string      { return "" }
func (c *simpleCosmosConfig) getEVMChainID() string            { return "" }
func (c *simpleCosmosConfig) getSyncAllowance() uint64 {
	if c.syncAllowance == 0 {
		return defaultCosmosSDKBlockNumberSyncAllowance
	}
	return c.syncAllowance
}
func (c *simpleCosmosConfig) getSupportedAPIs() map[sharedtypes.RPCType]struct{} {
	if c.supportedAPIs != nil {
		return c.supportedAPIs
	}
	// Default to REST and COMET_BFT if not configured
	return map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {},
		sharedtypes.RPCType_COMET_BFT: {},
	}
}

// ParseHTTPRequest builds a request context from an HTTP request.
// Returns (requestContext, true) if the request is valid
// Returns (errorContext, false) if the request is not valid.
//
// Supports both REST endpoints (/health, /status) and JSON-RPC requests.
//
// Implements gateway.QoSService interface.
func (qos *QoS) ParseHTTPRequest(_ context.Context, req *http.Request, detectedRPCType sharedtypes.RPCType) (gateway.RequestQoSContext, bool) {
	return qos.validateHTTPRequest(req, detectedRPCType)
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

// UpdateFromExtractedData updates QoS state from extracted observation data.
// Called by the observation pipeline after async parsing completes.
// This updates the perceived block number without blocking user requests.
//
// Implements gateway.QoSService interface.
func (qos *QoS) UpdateFromExtractedData(endpointAddr protocol.EndpointAddr, data *qostypes.ExtractedData) error {
	if data == nil {
		return nil
	}

	// Only update if we extracted a valid block height
	if data.BlockHeight <= 0 {
		return nil
	}

	qos.serviceStateLock.Lock()
	defer qos.serviceStateLock.Unlock()

	// Update perceived block number to maximum across all endpoints
	blockNumber := uint64(data.BlockHeight)
	if blockNumber > qos.perceivedBlockNumber {
		qos.logger.Debug().
			Str("endpoint", string(endpointAddr)).
			Uint64("old_block", qos.perceivedBlockNumber).
			Uint64("new_block", blockNumber).
			Msg("Updating perceived block number from observation pipeline")
		qos.perceivedBlockNumber = blockNumber
	}

	return nil
}
