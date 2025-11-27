package cosmos

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	pathhttp "github.com/buildwithgrove/path/network/http"
	qosobservations "github.com/buildwithgrove/path/observation/qos"
	"github.com/buildwithgrove/path/qos"
	"github.com/buildwithgrove/path/qos/jsonrpc"
)

// jsonrpcUnrecognizedResponse handles unrecognized JSONRPC responses
// Implements the response interface for cases where the response cannot be properly parsed
// but we still have a valid JSONRPC response structure
type jsonrpcUnrecognizedResponse struct {
	logger          polylog.Logger
	jsonrpcResponse jsonrpc.Response
	validationErr   qosobservations.CosmosResponseValidationError
}

// GetHTTPResponse builds the HTTP response to return to the client
// Uses the existing QoS helper to build response from JSONRPC response
func (r *jsonrpcUnrecognizedResponse) GetHTTPResponse() pathhttp.HTTPResponse {
	return qos.BuildHTTPResponseFromJSONRPCResponse(r.logger, r.jsonrpcResponse)
}

// GetObservation returns the QoS observation for this unrecognized response
func (r *jsonrpcUnrecognizedResponse) GetObservation() qosobservations.CosmosEndpointObservation {
	return qosobservations.CosmosEndpointObservation{
		EndpointResponseValidationResult: &qosobservations.CosmosEndpointResponseValidationResult{
			ResponseValidationType: qosobservations.CosmosResponseValidationType_COSMOS_RESPONSE_VALIDATION_TYPE_JSONRPC,
			HttpStatusCode:         int32(500),
			ValidationError:        &r.validationErr,
			UserJsonrpcResponse:    r.jsonrpcResponse.GetObservation(),
			ParsedResponse: &qosobservations.CosmosEndpointResponseValidationResult_ResponseJsonrpc{
				ResponseJsonrpc: r.jsonrpcResponse.GetObservation(),
			},
		},
	}
}

// This follows JSON-RPC 2.0 specification requirement to return "nothing at all" when
// no Response objects are contained in the batch response array.
// This occurs when all requests in the batch are notifications or all responses are filtered out.
func getGenericResponseBatchEmpty(logger polylog.Logger) jsonrpcUnrecognizedResponse {
	logger.Debug().Msg("Batch request resulted in no response objects - returning empty response per JSON-RPC spec")

	// Create a responseGeneric with empty payload to represent "nothing at all"
	return jsonrpcUnrecognizedResponse{
		logger:          logger,
		jsonrpcResponse: jsonrpc.Response{},                                                                         // Empty response - will marshal to empty JSON object
		validationErr:   qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_UNSPECIFIED, // No validation error - this is valid JSON-RPC behavior
	}
}

// getGenericJSONRPCErrResponseBatchMarshalFailure creates a generic response for batch marshaling failures.
// This occurs when individual responses are valid but combining them into a JSON array fails.
// Uses null ID per JSON-RPC spec for batch-level errors that cannot be correlated to specific requests.
func getGenericJSONRPCErrResponseBatchMarshalFailure(logger polylog.Logger, err error) jsonrpcUnrecognizedResponse {
	logger.Error().Err(err).Msg("Failed to marshal batch response")

	// Create the batch marshal failure response using the error function
	jsonrpcResponse := jsonrpc.NewErrResponseBatchMarshalFailure(err)

	// No validation error since this is an internal processing issue, not an endpoint issue
	return jsonrpcUnrecognizedResponse{
		logger:          logger,
		jsonrpcResponse: jsonrpcResponse,
		validationErr:   qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_UNSPECIFIED, // No validation error - this is an internal marshaling issue
	}
}
