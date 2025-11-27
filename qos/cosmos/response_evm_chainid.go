package cosmos

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	pathhttp "github.com/buildwithgrove/path/network/http"
	qosobservations "github.com/buildwithgrove/path/observation/qos"
	"github.com/buildwithgrove/path/qos"
	"github.com/buildwithgrove/path/qos/jsonrpc"
)

// responseEVMChainID provides the functionality required from a response by a requestContext instance.
var _ response = responseEVMChainID{}

// responseValidatorEVMChainID implements jsonrpcResponseValidator for eth_chainId method
// Takes a parsed JSONRPC response and validates it as a chain ID response
func responseValidatorEVMChainID(logger polylog.Logger, jsonrpcResponse jsonrpc.Response) response {
	logger = logger.With("response_validator", "eth_chainId")

	// The endpoint returned an error: no need to do further processing of the response
	if jsonrpcResponse.IsError() {
		logger.Warn().
			Str("jsonrpc_error", jsonrpcResponse.Error.Message).
			Int("jsonrpc_error_code", jsonrpcResponse.Error.Code).
			Msg("Endpoint returned JSON-RPC error for eth_chainId request")

		return &responseEVMChainID{
			logger:          logger,
			jsonrpcResponse: jsonrpcResponse,
		}
	}

	// Marshal the result to parse it as ResultStatus
	resultBytes, err := json.Marshal(jsonrpcResponse.Result)
	if err != nil {
		logger.Error().
			Err(err).
			Msg("Failed to marshal JSON-RPC result for eth_chainId")

		// Return error response but still include the original JSONRPC response
		return &responseEVMChainID{
			logger:          logger,
			jsonrpcResponse: jsonrpcResponse,
		}
	}

	// Then unmarshal the JSON bytes into the ResultStatus struct
	var evmChainID string
	if err := json.Unmarshal(resultBytes, &evmChainID); err != nil {
		logger.Error().
			Err(err).
			Str("result_data", string(resultBytes)).
			Msg("Failed to unmarshal JSON-RPC result into string")

		// Return error response but still include the original JSONRPC response
		return &responseEVMChainID{
			logger:          logger,
			jsonrpcResponse: jsonrpcResponse,
		}
	}

	logger.Debug().
		Str("evm_chain_id", evmChainID).
		Msg("Successfully parsed eth_chainId response")

	return &responseEVMChainID{
		logger:          logger,
		jsonrpcResponse: jsonrpcResponse,
		evmChainID:      evmChainID,
	}
}

// responseEVMChainID captures the fields expected in a
// response to an `eth_chainId` request.
type responseEVMChainID struct {
	logger polylog.Logger

	// jsonrpcResponse stores the JSONRPC response parsed from an endpoint's response bytes.
	jsonrpcResponse jsonrpc.Response

	// evmChainID captures the `result` field of a JSONRPC response to an `eth_chainId` request.
	evmChainID string
}

// GetObservation returns an observation of the endpoint's response to an `eth_chainId` request.
// Implements the response interface.
// Reference: https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_chainid
func (r responseEVMChainID) GetObservation() qosobservations.CosmosEndpointObservation {
	return qosobservations.CosmosEndpointObservation{
		EndpointResponseValidationResult: &qosobservations.CosmosEndpointResponseValidationResult{
			ResponseValidationType: qosobservations.CosmosResponseValidationType_COSMOS_RESPONSE_VALIDATION_TYPE_JSONRPC,
			HttpStatusCode:         int32(200),
			ValidationError:        nil, // No validation error for successfully processed responses
			UserJsonrpcResponse:    r.jsonrpcResponse.GetObservation(),
			ParsedResponse: &qosobservations.CosmosEndpointResponseValidationResult_ResponseEvmJsonrpcChainId{
				ResponseEvmJsonrpcChainId: &qosobservations.CosmosResponseEVMJSONRPCChainID{
					HttpStatusCode: int32(200),
					EvmChainId:     r.evmChainID,
				},
			},
		},
	}
}

// GetHTTPResponse builds and returns the HTTP response
// Implements the response interface
func (r responseEVMChainID) GetHTTPResponse() pathhttp.HTTPResponse {
	return qos.BuildHTTPResponseFromJSONRPCResponse(r.logger, r.jsonrpcResponse)
}
