package cosmos

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/qos/jsonrpc"
)

var (
	// CosmosSDK response IDs for different request types:
	// * JSON-RPC id for REST responses: -1
	// TODO_NEXT(@adshmh): Use proper JSON-RPC ID response validation that works for all Cosmos SDK chains.
	jsonrpcIdForRESTResponses = jsonrpc.IDFromInt(-1)
)

// unmarshalRESTRequestEndpointResponse routes REST endpoint responses to appropriate validators.
// Uses the request path to match the response validator.
// It serves as the main entry point for processing REST responses.
// Always returns a valid response interface, never returns an error.
func unmarshalRESTRequestEndpointResponse(
	logger polylog.Logger,
	requestPath string,
	endpointResponseBz []byte,
) response {
	// Route to specific unmarshalers based on endpoint path
	switch requestPath {

	// CometBFT /status endpoint returns a JSONRPC response.
	// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#status
	// TODO_TECHDEBT(@adshmh): Refactor to properly separate response validation functionality shared between REST and JSONRPC.
	case "/status":
		// Build a JSONRPC request with the correct method and ID to use JSONRPC validator.
		// `/status` returns a JSONRPC response.
		jsonrpcReq := jsonrpc.Request{
			ID:     jsonrpcIdForRESTResponses,
			Method: "status",
		}
		jsonRpcReqs := map[jsonrpc.ID]jsonrpc.Request{
			jsonrpcIdForRESTResponses: jsonrpcReq,
		}
		return unmarshalJSONRPCRequestEndpointResponse(logger, jsonRpcReqs, endpointResponseBz, "")

	// CometBFT /health endpoint returns a JSONRPC response.
	// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#health
	case "/health":
		// TODO_TECHDEBT(@adshmh): Refactor to properly separate response validation functionality shared between REST and JSONRPC.
		jsonrpcReq := jsonrpc.Request{
			ID:     jsonrpcIdForRESTResponses,
			Method: "health",
		}
		jsonRpcReqs := map[jsonrpc.ID]jsonrpc.Request{
			jsonrpcIdForRESTResponses: jsonrpcReq,
		}
		return unmarshalJSONRPCRequestEndpointResponse(logger, jsonRpcReqs, endpointResponseBz, "")

	// Cosmos SDK /cosmos/base/node/v1beta1/status endpoint returns a REST JSON response.
	// Reference: https://docs.cosmos.network/api#tag/Service/operation/Status
	case apiPathCosmosStatus:
		response, err := responseValidatorCosmosStatus(logger, endpointResponseBz)
		// TODO_TECHDEBT(@adshmh): use an "error response" type here to mark a response validation error.
		// This is needed to:
		// - Support sanctions on endpoints based on RESTful API requests.
		// - Export metrics on invalid responses to RESTful API requests.
		//
		if err != nil {
			// For Cosmos SDK status endpoint, return a generic response if the response is not valid.
			return responseRESTUnrecognized{
				logger:             logger,
				endpointResponseBz: endpointResponseBz,
			}
		}
		return response

	default:
		// For unrecognized endpoints, return a generic response.
		return responseRESTUnrecognized{
			logger:             logger,
			endpointResponseBz: endpointResponseBz,
		}
	}
}
