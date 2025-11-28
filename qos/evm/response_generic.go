package evm

import (
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

const (
	// errCodeUnmarshaling is set as the JSONRPC response's error code if the endpoint returns a malformed response.
	// `jsonrpc.ResponseCodeBackendServerErr`, i.e. code -31002, will result in returning a 500 HTTP Status Code to the client.
	errCodeUnmarshaling = jsonrpc.ResponseCodeBackendServerErr

	// errMsgUnmarshaling is the generic message returned to the user if the endpoint returns a malformed response.
	errMsgUnmarshaling = "the response returned by the endpoint is not a valid JSONRPC response"

	// errDataFieldRawBytes is the key of the entry in the JSONRPC error response's "data" map which holds the endpoint's original response.
	errDataFieldRawBytes = "endpoint_response"

	// errDataFieldUnmarshalingErr is the key of the entry in the JSONRPC error response's "data" map which holds the unmarshaling error.
	errDataFieldUnmarshalingErr = "unmarshaling_error"
)

// responseGeneric represents the standard response structure for EVM-based blockchain requests.
// Used as a fallback when:
// - No validation/observation is needed for the JSON-RPC method
// - No specific unmarshallers/structs exist for the request method
// responseGeneric captures the fields expected in response to any request on an
// EVM-based blockchain. It is intended to be used when no validation/observation
// is applicable to the corresponding request's JSONRPC method.
// i.e. when there are no unmarshallers/structs matching the method specified by the request.
type responseGeneric struct {
	logger polylog.Logger

	// jsonrpcResponse stores the JSONRPC response parsed from an endpoint's response bytes.
	jsonrpcResponse jsonrpc.Response

	// Why the response has failed validation.
	// Only set if the response is invalid.
	// As of PR #152, a response is deemed valid if it can be unmarshaled as a JSONRPC struct
	// regardless of the contents of the response.
	// Used when generating observations.
	validationError *qosobservations.EVMResponseValidationError
}

// GetObservation returns an observation that is used in validating endpoints.
// This generates observations for unrecognized responses that can trigger endpoint
// disqualification when validation errors are present.
// Implements the response interface.
func (r responseGeneric) GetObservation() qosobservations.EVMEndpointObservation {
	var parsedJSONRPCResponseObservation *qosobservations.JsonRpcResponse
	// If validation error was nil, i.e. if the endpoint response was parsed successfully, update the observation.
	if r.validationError == nil {
		parsedJSONRPCResponseObservation = r.jsonrpcResponse.GetObservation()
	}

	return qosobservations.EVMEndpointObservation{
		ParsedJsonrpcResponse: parsedJSONRPCResponseObservation,
		ResponseObservation: &qosobservations.EVMEndpointObservation_UnrecognizedResponse{
			UnrecognizedResponse: &qosobservations.EVMUnrecognizedResponse{
				// Include JSONRPC response's details in the observation.
				JsonrpcResponse:         r.jsonrpcResponse.GetObservation(),
				ResponseValidationError: r.validationError,
				HttpStatusCode:          int32(r.getHTTPStatusCode()),
			},
		},
	}
}

// GetHTTPResponse builds and returns the httpResponse matching the responseGeneric instance.
// Implements the response interface.
func (r responseGeneric) GetHTTPResponse() jsonrpc.HTTPResponse {
	return jsonrpc.HTTPResponse{
		ResponsePayload: r.getResponsePayload(),
		// Use the HTTP status code recommended by for the underlying JSONRPC response by the jsonrpc package.
		HTTPStatusCode: r.getHTTPStatusCode(),
	}
}

// TODO_MVP(@adshmh): handle any unmarshaling errors
// TODO_INCOMPLETE: build a method-specific payload generator.
func (r responseGeneric) getResponsePayload() []byte {
	// Special case for empty batch responses - return empty payload per JSON-RPC spec
	if (r.jsonrpcResponse == jsonrpc.Response{}) {
		return []byte{} // "nothing at all" per JSON-RPC batch specification
	}

	bz, err := marshalJSONPooled(r.jsonrpcResponse)
	if err != nil {
		// This should never happen: log an entry but return the response anyway.
		r.logger.Warn().Err(err).Msg("responseGeneric: Marshaling JSONRPC response failed.")
	}
	return bz
}

// getHTTPStatusCode returns an HTTP status code corresponding to the underlying JSON-RPC response code.
// DEV_NOTE: This is an opinionated mapping following best practice but not enforced by any specifications or standards.
func (r responseGeneric) getHTTPStatusCode() int {
	// Special case for empty batch responses - return 200 OK per JSON-RPC over HTTP best practices
	if (r.jsonrpcResponse == jsonrpc.Response{}) {
		return http.StatusOK
	}

	return r.jsonrpcResponse.GetRecommendedHTTPStatusCode()
}

// responseUnmarshallerGenericFromResponse processes an already unmarshaled JSON-RPC response into a responseGeneric struct.
// This avoids double unmarshaling when the response has already been parsed.
func responseUnmarshallerGenericFromResponse(logger polylog.Logger, jsonrpcReq jsonrpc.Request, jsonrpcResponse jsonrpc.Response) (response, error) {
	httpStatus := jsonrpcResponse.GetRecommendedHTTPStatusCode()
	logger.With(
		"jsonrpc_response", jsonrpcResponse,
		"jsonrpc_request", jsonrpcReq,
		"http_status", httpStatus,
	).Debug().Msg("Processing EVM generic response")

	// Response successfully parsed into JSONRPC format.
	return responseGeneric{
		logger:          logger,
		jsonrpcResponse: jsonrpcResponse,
		validationError: nil, // Intentionally set to nil to indicate a valid JSONRPC response.
	}, nil
}

// getGenericJSONRPCErrResponse creates a generic response containing:
// - JSON-RPC error with supplied ID
// - Error details
// - Invalid payload in the "data" field
// Sets validation error to UNMARSHAL to trigger endpoint disqualification.
func getGenericJSONRPCErrResponse(_ polylog.Logger, id jsonrpc.ID, malformedResponsePayload []byte, err error) responseGeneric {
	errData := map[string]string{
		errDataFieldRawBytes:        string(malformedResponsePayload),
		errDataFieldUnmarshalingErr: err.Error(),
	}

	validationError := qosobservations.EVMResponseValidationError_EVM_RESPONSE_VALIDATION_ERROR_UNMARSHAL
	return responseGeneric{
		jsonrpcResponse: jsonrpc.GetErrorResponse(id, errCodeUnmarshaling, errMsgUnmarshaling, errData),
		validationError: &validationError, // Set validation error to trigger endpoint disqualification
	}
}

// getGenericJSONRPCErrResponseBatchMarshalFailure creates a generic response for batch marshaling failures.
// This occurs when individual responses are valid but combining them into a JSON array fails.
// Uses null ID per JSON-RPC spec for batch-level errors that cannot be correlated to specific requests.
func getGenericJSONRPCErrResponseBatchMarshalFailure(logger polylog.Logger, err error) responseGeneric {
	logger.Error().Err(err).Msg("Failed to marshal batch response")

	// Create the batch marshal failure response using the error function
	jsonrpcResponse := jsonrpc.NewErrResponseBatchMarshalFailure(err)

	// No validation error since this is an internal processing issue, not an endpoint issue
	return responseGeneric{
		logger:          logger,
		jsonrpcResponse: jsonrpcResponse,
		validationError: nil, // No validation error - this is an internal marshaling issue
	}
}

// getGenericResponseBatchEmpty creates a responseGeneric instance for handling empty batch responses.
// This follows JSON-RPC 2.0 specification requirement to return "nothing at all" when
// no Response objects are contained in the batch response array.
// This occurs when all requests in the batch are notifications or all responses are filtered out.
func getGenericResponseBatchEmpty(logger polylog.Logger) responseGeneric {
	logger.Debug().Msg("Batch request resulted in no response objects - returning empty response per JSON-RPC spec")

	// Create a responseGeneric with empty payload to represent "nothing at all"
	return responseGeneric{
		logger:          logger,
		jsonrpcResponse: jsonrpc.Response{}, // Empty response - will marshal to empty JSON object
		validationError: nil,                // No validation error - this is valid JSON-RPC behavior
	}
}

// getGenericResponseNoEndpointResponse creates a responseGeneric instance for the error case
// where no endpoint responses are available. This should never happen in normal operation
// and indicates an internal error in the request processing pipeline.
func getGenericResponseNoEndpointResponse(logger polylog.Logger) responseGeneric {
	logger.Error().Msg("No endpoint responses available - this indicates an internal error")

	return responseGeneric{
		logger: logger,
		jsonrpcResponse: jsonrpc.Response{
			Error: &jsonrpc.ResponseError{
				Code:    -32603, // JSON-RPC internal error code
				Message: "internal error: no endpoint response available",
			},
		},
		validationError: nil,
	}
}

// TODO_INCOMPLETE: Handle the string `null`, as it could be returned
// when an object is expected.
// See the following link for more details:
// https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_gettransactionbyhash
