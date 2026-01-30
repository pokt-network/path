package evm

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// emptyResponse provides the functionality required from a response by a requestContext instance.
var _ response = responseEmpty{}

// TODO_MVP(@adshmh): Implement request retry support:
//  1. Add ShouldRetry() method to gateway.RequestQoSContext
//  2. Integrate ShouldRetry() into gateway request handler
//  3. Extend evm.response interface with ShouldRetry()
//  4. Add ShouldRetry() to evm.requestContext to evaluate retry eligibility based on responses
//
// responseEmpty processes empty endpoint responses by:
//  1. Creating an observation to penalize the endpoint and track metrics
//  2. Generating a JSONRPC error to return to the client
type responseEmpty struct {
	logger          polylog.Logger
	servicePayloads map[jsonrpc.ID]protocol.Payload
	// requestID is the JSON-RPC request ID for this specific request.
	// Used for batch requests to ensure error responses have the correct ID.
	// When empty, falls back to extracting ID from servicePayloads (single request case).
	requestID string
}

// GetObservation returns an observation indicating the endpoint returned an empty response.
// Implements the response interface.
func (r responseEmpty) GetObservation() qosobservations.EVMEndpointObservation {

	return qosobservations.EVMEndpointObservation{
		ResponseObservation: &qosobservations.EVMEndpointObservation_EmptyResponse{
			EmptyResponse: &qosobservations.EVMEmptyResponse{
				HttpStatusCode: int32(r.getHTTPStatusCode()), // EmptyResponse always returns a 500 Internal error HTTP status code.
				// An empty response is always invalid.
				ResponseValidationError: qosobservations.EVMResponseValidationError_EVM_RESPONSE_VALIDATION_ERROR_EMPTY,
			},
		},
	}
}

// GetHTTPResponse builds and returns the httpResponse matching the responseEmpty instance.
// Implements the response interface.
func (r responseEmpty) GetHTTPResponse() jsonrpc.HTTPResponse {
	return jsonrpc.HTTPResponse{
		ResponsePayload: r.getResponsePayload(),
		// HTTP Status 500 Internal Server Error for an empty response
		HTTPStatusCode: r.getHTTPStatusCode(),
	}
}

// getResponsePayload constructs a JSONRPC error response indicating endpoint failure.
// Uses request ID in response per JSONRPC spec: https://www.jsonrpc.org/specification#response_object
func (r responseEmpty) getResponsePayload() []byte {
	// Use the specific requestID if provided (batch request case),
	// otherwise fall back to extracting from servicePayloads (single request case).
	var responseID jsonrpc.ID
	if r.requestID != "" {
		responseID = jsonrpc.IDFromString(r.requestID)
	} else {
		responseID = getJsonRpcIDForErrorResponse(r.servicePayloads)
	}

	userResponse := jsonrpc.NewErrResponseEmptyEndpointResponse(responseID)
	bz, err := json.Marshal(userResponse)
	if err != nil {
		// This should never happen: log an entry but return the response anyway.
		r.logger.Warn().Err(err).Msg("responseEmpty: Marshaling JSONRPC response failed.")
	}
	return bz
}

// getHTTPStatusCode returns the HTTP status code to be returned to the client.
// Always returns returns 500 Internal Server Error on responseEmpty struct.
func (r responseEmpty) getHTTPStatusCode() int {
	return jsonrpc.HTTPStatusResponseValidationFailureEmptyResponse
}
