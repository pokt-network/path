package evm

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
//  1. QoSRequestParser - Builds EVM-specific RequestQoSContext objects from HTTP requests
//  2. EndpointSelector - Selects endpoints for service requests
var _ gateway.QoSService = &QoS{}

// devtools.QoSDisqualifiedEndpointsReporter is fulfilled by the QoS struct below.
// This allows the QoS service to report its disqualified endpoints data to the devtools.DisqualifiedEndpointReporter.
var _ devtools.QoSDisqualifiedEndpointsReporter = &QoS{}

// QoS implements ServiceQoS for EVM-based chains.
// It handles chain-specific:
//   - Request parsing
//   - Response building
//   - Endpoint validation and selection
type QoS struct {
	logger polylog.Logger
	*serviceState
	*evmRequestValidator
}

// NewSimpleQoSInstance creates a minimal EVM QoS instance without chain-specific validation.
// Validation (chain ID, archival checks) is now handled by active health checks.
// This constructor only requires the service ID and provides JSON-RPC request parsing.
func NewSimpleQoSInstance(logger polylog.Logger, serviceID protocol.ServiceID) *QoS {
	return NewSimpleQoSInstanceWithSyncAllowance(logger, serviceID, 0)
}

// NewSimpleQoSInstanceWithSyncAllowance creates a minimal EVM QoS instance with custom sync allowance.
// Validation (chain ID, archival checks) is now handled by active health checks.
// If syncAllowance is 0, the default value is used.
func NewSimpleQoSInstanceWithSyncAllowance(logger polylog.Logger, serviceID protocol.ServiceID, syncAllowance uint64) *QoS {
	logger = logger.With(
		"qos_instance", "evm",
		"service_id", serviceID,
	)

	store := &endpointStore{
		logger:    logger,
		endpoints: make(map[protocol.EndpointAddr]endpoint),
	}

	// Create a minimal config wrapper for backward compatibility with serviceState
	minimalConfig := &simpleServiceConfig{
		serviceID:     serviceID,
		syncAllowance: syncAllowance,
	}

	serviceState := &serviceState{
		logger:           logger,
		serviceQoSConfig: minimalConfig,
		endpointStore:    store,
	}

	evmRequestValidator := &evmRequestValidator{
		logger:       logger,
		serviceID:    serviceID,
		chainID:      "", // No chain ID validation - handled by health checks
		serviceState: serviceState,
	}

	return &QoS{
		logger:              logger,
		serviceState:        serviceState,
		evmRequestValidator: evmRequestValidator,
	}
}

// simpleServiceConfig is a minimal config for services without chain-specific params.
type simpleServiceConfig struct {
	serviceID     protocol.ServiceID
	syncAllowance uint64 // If 0, uses default
}

func (c *simpleServiceConfig) GetServiceID() protocol.ServiceID { return c.serviceID }
func (c *simpleServiceConfig) GetServiceQoSType() string        { return QoSType }
func (c *simpleServiceConfig) getEVMChainID() string            { return "" }
func (c *simpleServiceConfig) getSyncAllowance() uint64 {
	if c.syncAllowance == 0 {
		return defaultEVMBlockNumberSyncAllowance
	}
	return c.syncAllowance
}
func (c *simpleServiceConfig) getEVMArchivalCheckConfig() evmArchivalCheckConfig {
	return evmArchivalCheckConfig{}
}
func (c *simpleServiceConfig) archivalCheckEnabled() bool { return false }
func (c *simpleServiceConfig) getSupportedAPIs() map[sharedtypes.RPCType]struct{} {
	return map[sharedtypes.RPCType]struct{}{sharedtypes.RPCType_JSON_RPC: {}}
}

// ParseHTTPRequest builds a request context from an HTTP request.
// Returns (requestContext, true) if the request is valid JSONRPC
// Returns (errorContext, false) if the request is not valid JSONRPC.
//
// Implements gateway.QoSService interface.
// Fallback logic for EVM: header → jsonrpc (EVM only supports JSON-RPC)
func (qos *QoS) ParseHTTPRequest(_ context.Context, req *http.Request, detectedRPCType sharedtypes.RPCType) (gateway.RequestQoSContext, bool) {
	return qos.validateHTTPRequest(req, detectedRPCType)
}

// ParseWebsocketRequest builds a request context from the provided Websocket request.
// Websocket connection requests do not have a body, so we don't need to parse it.
//
// Implements gateway.QoSService interface.
func (qos *QoS) ParseWebsocketRequest(_ context.Context) (gateway.RequestQoSContext, bool) {
	return &requestContext{
		logger:       qos.logger,
		serviceState: qos.serviceState,
	}, true
}

// HydrateDisqualifiedEndpointsResponse hydrates the disqualified endpoint response with the QoS-specific data.
//   - takes a pointer to the DisqualifiedEndpointResponse
//   - called by the devtools.DisqualifiedEndpointReporter to fill it with the QoS-specific data.
func (qos *QoS) HydrateDisqualifiedEndpointsResponse(serviceID protocol.ServiceID, details *devtools.DisqualifiedEndpointResponse) {
	qos.logger.Debug().Msgf("hydrating disqualified endpoints response for service ID: %s", serviceID)
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

// GetPerceivedBlockNumber returns the perceived current block number.
// Used by health checks for block height validation.
// Returns 0 if no block number has been observed yet.
//
// Implements gateway.QoSService interface.
func (qos *QoS) GetPerceivedBlockNumber() uint64 {
	qos.serviceStateLock.RLock()
	defer qos.serviceStateLock.RUnlock()
	return qos.perceivedBlockNumber
}
