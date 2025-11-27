package cosmos

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// TODO_IMPROVE(@commoddity): Replace custom structs with official CometBFT types.
//
// Current issue: The official `coretypes.ResultStatus` expects int64 for `latest_block_height`,
// but CometBFT JSON-RPC returns string values, causing unmarshalling errors.
//
// Workaround: Using custom structs with string fields for compatibility.
// Only includes fields needed for QoS validation (chain_id, catching_up, latest_block_height).
//
// Reference: https://github.com/cometbft/cometbft/blob/4226b0ea6ab4725ef807a16b86d6d24835bb45d4/rpc/core/types/responses.go#L100
type (
	// Node Status
	ResultStatus struct {
		NodeInfo DefaultNodeInfo `json:"node_info"`
		SyncInfo SyncInfo        `json:"sync_info"`
	}

	// Info about the node's syncing state
	SyncInfo struct {
		LatestBlockHeight string `json:"latest_block_height"`
		CatchingUp        bool   `json:"catching_up"`
	}

	// DefaultNodeInfo is the basic node information exchanged
	// between two peers during the CometBFT P2P handshake.
	DefaultNodeInfo struct {
		Network string `json:"network"` // network/chain ID
	}
)

// responseValidatorCometBFTStatus implements jsonrpcResponseValidator for `status` method
// Takes a parsed JSONRPC response and validates it as a status response
func responseValidatorCometBFTStatus(logger polylog.Logger, jsonrpcResponse jsonrpc.Response) response {
	logger = logger.With("response_validator", "status")

	// The endpoint returned an error: no need to do further processing of the response
	if jsonrpcResponse.IsError() {
		logger.Warn().
			Str("jsonrpc_error", jsonrpcResponse.Error.Message).
			Int("jsonrpc_error_code", jsonrpcResponse.Error.Code).
			Msg("Endpoint returned JSON-RPC error for /status request")

		return &responseCometBFTStatus{
			logger:          logger,
			jsonrpcResponse: jsonrpcResponse,
		}
	}

	// Marshal the result to parse it as ResultStatus
	resultBytes, err := json.Marshal(jsonrpcResponse.Result)
	if err != nil {
		logger.Error().
			Err(err).
			Msg("Failed to marshal JSON-RPC result for /status")

		// Return error response but still include the original JSONRPC response
		return &responseCometBFTStatus{
			logger:          logger,
			jsonrpcResponse: jsonrpcResponse,
		}
	}

	// Then unmarshal the JSON bytes into the ResultStatus struct
	var result ResultStatus
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		logger.Error().
			Err(err).
			Str("result_data", string(resultBytes)).
			Msg("Failed to unmarshal JSON-RPC result into ResultStatus structure")

		// Return error response but still include the original JSONRPC response
		return &responseCometBFTStatus{
			logger:          logger,
			jsonrpcResponse: jsonrpcResponse,
		}
	}

	logger.Debug().
		Str("chain_id", result.NodeInfo.Network).
		Bool("catching_up", result.SyncInfo.CatchingUp).
		Str("latest_block_height", result.SyncInfo.LatestBlockHeight).
		Msg("Successfully parsed /status response")

	return &responseCometBFTStatus{
		logger:            logger,
		jsonrpcResponse:   jsonrpcResponse,
		cosmosSDKChainID:  result.NodeInfo.Network,
		catchingUp:        result.SyncInfo.CatchingUp,
		latestBlockHeight: result.SyncInfo.LatestBlockHeight,
	}
}

// responseCometBFTStatus captures the fields expected in a
// response to a /status request (which returns JSON-RPC)
type responseCometBFTStatus struct {
	logger polylog.Logger

	// jsonrpcResponse stores the JSON-RPC response parsed from an endpoint's response bytes
	jsonrpcResponse jsonrpc.Response

	// cosmosSDKChainID stores the chain ID of the endpoint
	// Comes from the `NodeInfo.Network` field in the `/status` response
	// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#status
	cosmosSDKChainID string

	// catchingUp indicates if the endpoint is catching up to the network
	// Comes from the `SyncInfo.CatchingUp` field in the `/status` response
	// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#status
	catchingUp bool

	// latestBlockHeight stores the latest block height of a
	// response to a block height request as a string
	// Comes from the `SyncInfo.LatestBlockHeight` field in the `/status` response
	// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#status
	latestBlockHeight string
}

// GetObservation returns an observation using a /status request's response
// Implements the response interface
func (r *responseCometBFTStatus) GetObservation() qosobservations.CosmosEndpointObservation {
	return qosobservations.CosmosEndpointObservation{
		EndpointResponseValidationResult: &qosobservations.CosmosEndpointResponseValidationResult{
			ResponseValidationType: qosobservations.CosmosResponseValidationType_COSMOS_RESPONSE_VALIDATION_TYPE_JSONRPC,
			HttpStatusCode:         int32(r.jsonrpcResponse.GetRecommendedHTTPStatusCode()),
			ValidationError:        nil, // No validation error for successfully processed responses
			UserJsonrpcResponse:    r.jsonrpcResponse.GetObservation(),
			ParsedResponse: &qosobservations.CosmosEndpointResponseValidationResult_ResponseCometBftStatus{
				ResponseCometBftStatus: &qosobservations.CosmosResponseCometBFTStatus{
					ChainId:           r.cosmosSDKChainID,
					CatchingUp:        r.catchingUp,
					LatestBlockHeight: r.latestBlockHeight,
				},
			},
		},
	}
}

// GetHTTPResponse builds and returns the HTTP response
// Implements the response interface
func (r *responseCometBFTStatus) GetHTTPResponse() pathhttp.HTTPResponse {
	return qos.BuildHTTPResponseFromJSONRPCResponse(r.logger, r.jsonrpcResponse)
}
