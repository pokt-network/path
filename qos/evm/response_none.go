package evm

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// responseNone satisfies the response interface and handles the case
// where no response has been received from any endpoint.
// This is not the same as empty responses (where an endpoint responded with empty data).
var _ response = responseNone{}

// responseNone represents the absence of any endpoint response.
// This can occur due to protocol-level failures or when no endpoint was selected.
type responseNone struct {
	logger          polylog.Logger
	servicePayloads map[jsonrpc.ID]protocol.Payload
}

// GetObservation returns an observation indicating no endpoint provided a response.
// This allows tracking metrics for scenarios where endpoint selection or communication failed.
// Implements the response interface.
func (r responseNone) GetObservation() qosobservations.EVMEndpointObservation {
	return qosobservations.EVMEndpointObservation{
		ResponseObservation: &qosobservations.EVMEndpointObservation_NoResponse{
			NoResponse: &qosobservations.EVMNoResponse{
				// NoResponse's underlying getHTTPStatusCode always returns a 500 Internal error.
				HttpStatusCode: int32(r.getHTTPStatusCode()),
				// NoResponse is always an invalid response.
				ResponseValidationError: qosobservations.EVMResponseValidationError_EVM_RESPONSE_VALIDATION_ERROR_NO_RESPONSE,
			},
		},
	}
}

// GetHTTPResponse creates and returns a predefined httpResponse for cases when QoS has received no responses from the protocol.
// Implements the response interface.
func (r responseNone) GetHTTPResponse() jsonrpc.HTTPResponse {
	return jsonrpc.HTTPResponse{
		ResponsePayload: r.getResponsePayload(),
		HTTPStatusCode:  r.getHTTPStatusCode(),
	}
}

// getResponsePayload constructs a JSONRPC error response indicating no endpoint response was received.
// Uses request ID in response per JSONRPC spec: https://www.jsonrpc.org/specification#response_object
func (r responseNone) getResponsePayload() []byte {
	userResponse := jsonrpc.NewErrResponseNoEndpointResponse(getJsonRpcIDForErrorResponse(r.servicePayloads))
	bz, err := json.Marshal(userResponse)
	if err != nil {
		// This should never happen: log an entry but return the response anyway.
		r.logger.Warn().Err(err).Msg("responseNone: Marshaling JSONRPC response failed.")
	}
	return bz
}

// getHTTPStatusCode returns the HTTP status code to be returned to the client.
// Always a 500 Internal Server Error for the responseNone struct.
func (r responseNone) getHTTPStatusCode() int {
	return jsonrpc.HTTPStatusResponseValidationFailureNoResponse
}
