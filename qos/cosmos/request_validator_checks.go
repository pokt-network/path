package cosmos

import (
	"io"
	"net/http"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

/* -------------------- QoS Endpoint Check Generator -------------------- */

// requestValidator provides the required synthetic QoS checks to the hydrator.
// It generates requests for both JSONRPC and REST endpoints.
var _ gateway.QoSEndpointCheckGenerator = &requestValidator{}

// CheckWebsocketConnection returns true if the endpoint supports Websocket connections.
func (rv *requestValidator) CheckWebsocketConnection() bool {
	_, supportsWebsockets := rv.serviceState.serviceQoSConfig.getSupportedAPIs()[sharedtypes.RPCType_WEBSOCKET]
	return supportsWebsockets
}

// GetRequiredQualityChecks returns the list of quality checks required for an endpoint.
// It is called in the `gateway/hydrator.go` file on each run of the hydrator.
func (rv *requestValidator) GetRequiredQualityChecks(endpointAddr protocol.EndpointAddr) []gateway.RequestQoSContext {
	// Get the endpoint from the endpoint store.
	endpoint := rv.serviceState.endpointStore.getEndpoint(endpointAddr)

	// Get the RPC types supported by the CosmosSDK service.
	supportedAPIs := rv.serviceState.serviceQoSConfig.getSupportedAPIs()

	// List of all synthetic QoS checks required for the endpoint.
	var checks []gateway.RequestQoSContext

	// Add CometBFT JSONRPC checks if supported
	if _, ok := supportedAPIs[sharedtypes.RPCType_COMET_BFT]; ok {
		checks = append(checks, rv.getCometBFTEndpointChecks(endpoint)...)
	}

	// Add CosmosSDK REST checks if supported
	if _, ok := supportedAPIs[sharedtypes.RPCType_REST]; ok {
		checks = append(checks, rv.getCosmosSDKEndpointChecks(endpoint)...)
	}

	// Add EVM JSON-RPC checks if supported
	if _, ok := supportedAPIs[sharedtypes.RPCType_JSON_RPC]; ok {
		checks = append(checks, rv.getEVMEndpointChecks(endpoint)...)
	}

	return checks
}

// getCometBFTEndpointChecks generates the endpoint checks for the CometBFT RPC type.
// API reference: https://docs.cometbft.com/v1.0/rpc/
func (rv *requestValidator) getCometBFTEndpointChecks(endpoint endpoint) []gateway.RequestQoSContext {
	checks := []gateway.RequestQoSContext{}

	// CometBFT 'health' method check
	if rv.shouldCometBFTHealthCheckRun(endpoint.checkCometBFTHealth) {
		checks = append(checks, rv.getJSONRPCRequestContextFromRequest(
			sharedtypes.RPCType_COMET_BFT,
			endpoint.checkCometBFTHealth.getRequest(),
		))
	}

	// CometBFT 'status' method check
	if rv.shouldCometBFTStatusCheckRun(endpoint.checkCometBFTStatus) {
		checks = append(checks, rv.getJSONRPCRequestContextFromRequest(
			sharedtypes.RPCType_COMET_BFT,
			endpoint.checkCometBFTStatus.getRequest(),
		))
	}

	return checks
}

// getCosmosSDKEndpointChecks generates the endpoint checks for the CosmosSDK RPC type.
// API reference: https://docs.cosmos.network/api
func (rv *requestValidator) getCosmosSDKEndpointChecks(endpoint endpoint) []gateway.RequestQoSContext {
	// Cosmos SDK status check should always be run.
	checks := []gateway.RequestQoSContext{
		rv.getRESTRequestContextFromRequest(
			sharedtypes.RPCType_REST,
			endpoint.checkCosmosStatus.getRequest(),
		),
	}

	return checks
}

// getEVMEndpointChecks generates the endpoint checks for the EVM JSON-RPC type.
// API reference: https://ethereum.org/en/developers/docs/apis/json-rpc/
func (rv *requestValidator) getEVMEndpointChecks(endpoint endpoint) []gateway.RequestQoSContext {
	checks := []gateway.RequestQoSContext{}

	// EVM chain ID check
	if rv.shouldEVMChainIDCheckRun(endpoint.checkEVMChainID) {
		checks = append(checks, rv.getJSONRPCRequestContextFromRequest(
			sharedtypes.RPCType_JSON_RPC,
			endpoint.checkEVMChainID.getRequest(),
		))
	}

	return checks
}

// shouldCometBFTHealthCheckRun returns true if the health check is not yet initialized or has expired.
func (rv *requestValidator) shouldCometBFTHealthCheckRun(check endpointCheckCometBFTHealth) bool {
	return check.expiresAt.IsZero() || check.IsExpired()
}

// shouldCometBFTStatusCheckRun returns true if the status check is not yet initialized or has expired.
func (rv *requestValidator) shouldCometBFTStatusCheckRun(check endpointCheckCometBFTStatus) bool {
	return check.expiresAt.IsZero() || check.IsExpired()
}

// shouldEVMChainIDCheckRun returns true if the chain ID check is not yet initialized or has expired.
func (rv *requestValidator) shouldEVMChainIDCheckRun(check endpointCheckEVMChainID) bool {
	return check.expiresAt.IsZero() || check.IsExpired()
}

// getJSONRPCRequestContextFromRequest prepares a gateway request context for a JSONRPC QoS endpoint check.
func (rv *requestValidator) getJSONRPCRequestContextFromRequest(
	rpcType sharedtypes.RPCType,
	jsonrpcReq jsonrpc.Request,
) gateway.RequestQoSContext {
	// Create a map with single request for consistency with batch handling
	jsonrpcReqs := map[jsonrpc.ID]jsonrpc.Request{
		jsonrpcReq.ID: jsonrpcReq,
	}

	context, ok := rv.buildJSONRPCRequestContext(
		jsonrpcReqs,
		false, // isBatch = false for single request
		qosobservations.RequestOrigin_REQUEST_ORIGIN_SYNTHETIC,
	)
	if !ok {
		rv.logger.Error().Msg("SHOULD NEVER HAPPEN: failed to build JSONRPC request context")
	}
	return context
}

// getRESTRequestContextFromRequest prepares a gateway request context for a REST QoS endpoint check.
func (rv *requestValidator) getRESTRequestContextFromRequest(
	rpcType sharedtypes.RPCType,
	restReq *http.Request,
) gateway.RequestQoSContext {
	var httpRequestBody []byte
	if restReq.Body != nil {
		bodyBytes, err := io.ReadAll(restReq.Body)
		if err != nil {
			rv.logger.Error().Err(err).Msg("failed to read request body")
			return nil
		}
		httpRequestBody = bodyBytes
	}

	context, ok := rv.buildRESTRequestContext(
		rpcType,
		restReq.URL,
		restReq.Method,
		httpRequestBody,
		qosobservations.RequestOrigin_REQUEST_ORIGIN_SYNTHETIC,
	)
	if !ok {
		rv.logger.Error().Msg("SHOULD NEVER HAPPEN: failed to build REST request context")
	}

	return context
}
