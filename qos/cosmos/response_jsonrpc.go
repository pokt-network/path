package cosmos

import (
	"encoding/json"
	"errors"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/log"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

const (
	// error codes to use as the JSONRPC response's error code if the endpoint returns a malformed response.
	// `jsonrpc.ResponseCodeBackendServerErr`, i.e. code -31002, will result in returning a 500 HTTP Status Code to the client.
	errCodeUnmarshaling  = jsonrpc.ResponseCodeBackendServerErr
	errCodeEmptyResponse = jsonrpc.ResponseCodeBackendServerErr

	// Error messages for JSONRPC response validation failures
	errMsgJSONRPCUnmarshaling  = "the JSONRPC response returned by the endpoint is not valid"
	errMsgJSONRPCEmptyResponse = "the endpoint returned an empty JSON-RPC response"

	// errDataFieldUnmarshalingErr is the key of the entry in the JSON-RPC error response's "data" map which holds the unmarshaling error.
	errDataFieldUnmarshalingErr = "unmarshaling_error"
)

// A jsonrpcResponseValidator takes a parsed JSONRPC response and verifies its contents against the expected result.
// e.g. The response validator for "status" method verifies the result field against the expected status info struct.
type jsonrpcResponseValidator func(polylog.Logger, jsonrpc.Response) response

var (
	// All response types must implement the response validator interface.
	_ jsonrpcResponseValidator = responseValidatorCometBFTHealth
	_ jsonrpcResponseValidator = responseValidatorCometBFTStatus

	// Maps JSONRPC requests to their corresponding response validators, based on the JSONRPC method.
	jsonrpcRequestEndpointResponseValidators = map[string]jsonrpcResponseValidator{
		// CometBFT `health` method observation
		// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#health
		string(methodCometBFTHealth): responseValidatorCometBFTHealth,

		// CometBFT `status` method observation
		// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#status
		string(methodCometBFTStatus): responseValidatorCometBFTStatus,

		// EVM `eth_chainId` method observation
		// Reference: https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_chainid
		string(methodEVMChainID): responseValidatorEVMChainID,
	}
)

// unmarshalJSONRPCRequestEndpointResponse parses the supplied raw byte slice from an endpoint.
// The raw byte is returned by an endpoint in response to a JSONRPC request.
// The requestID parameter is used to ensure error responses have the correct JSON-RPC ID
// when processing batch requests with independent flows per item.
func unmarshalJSONRPCRequestEndpointResponse(
	logger polylog.Logger,
	jsonrpcReqs map[jsonrpc.ID]jsonrpc.Request,
	data []byte,
	requestID string,
) response {
	// Parse and validate the raw payload as a JSONRPC response.
	jsonrpcResponse, jsonrpcReq, responseValidationErr := unmarshalAsJSONRPCResponse(
		logger,
		jsonrpcReqs,
		data,
		requestID,
	)

	// Log JSONRPC responses indicating an error.
	if jsonrpcResponse.Error != nil {
		logger.With(
			"jsonrpc_response_error", jsonrpcResponse.Error,
			"jsonrpc_request", jsonrpcReq,
		).Debug().Msg("JSONRPC error response returned for the request")
	}

	// TODO_TECHDEBT(@adshmh): Separate User-response, which could be a generic response indicating an endpoint error, from the parsed response.
	// Endpoint response failed validation.
	// Return a generic response to the user.
	if responseValidationErr != nil {
		return &jsonrpcUnrecognizedResponse{
			logger: logger,
			// The generic user-facing response indicating an endpoint error.
			jsonrpcResponse: jsonrpcResponse,
			validationErr:   *responseValidationErr,
		}
	}

	// Lookup the JSONRPC method-specific validator for the response.
	jsonrpcRequestMethod := string(jsonrpcReq.Method)
	validator, found := jsonrpcRequestEndpointResponseValidators[jsonrpcRequestMethod]
	if found {
		return validator(logger, jsonrpcResponse)
	}

	// Default to a generic response if no method-specific response is found.
	return &jsonrpcUnrecognizedResponse{
		logger:          logger,
		jsonrpcResponse: jsonrpcResponse,
		validationErr:   qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_UNSPECIFIED,
	}
}

// unmarshalAsJSONRPCResponse converts raw endpoint bytes into a JSONRPC response struct.
// The second return value contains the validation failure, if any.
// The requestID parameter is used to ensure error responses have the correct JSON-RPC ID
// when processing batch requests with independent flows per item.
func unmarshalAsJSONRPCResponse(
	logger polylog.Logger,
	jsonrpcReqs map[jsonrpc.ID]jsonrpc.Request,
	data []byte,
	requestID string,
) (jsonrpc.Response, jsonrpc.Request, *qosobservations.CosmosResponseValidationError) {
	// Empty payload is invalid.
	if len(data) == 0 {
		errEmptyPayload := errors.New("failed to unmarshal endpoint payload as JSONRPC: endpoint returned an empty response")
		logger.With(
			"unmarshal_err", errEmptyPayload,
			"error_type", "empty_response",
		).Debug().Msg(errEmptyPayload.Error())

		// Create a generic JSONRPC response for the user.
		// Use the provided requestID if available (batch request case),
		// otherwise fall back to extracting from jsonrpcReqs (single request case).
		errorID := getJsonRpcIDForErrorResponse(jsonrpcReqs)
		if requestID != "" {
			errorID = jsonrpc.IDFromString(requestID)
		}
		validationErr := qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_EMPTY
		return getGenericJSONRPCErrResponse(
			logger,
			errorID,
			errCodeEmptyResponse,
			errMsgJSONRPCEmptyResponse,
		), jsonrpc.Request{}, &validationErr
	}

	// Unmarshal the raw response payload into a JSONRPC response.
	var jsonrpcResponse jsonrpc.Response
	err := json.Unmarshal(data, &jsonrpcResponse)
	if err != nil {
		// The response raw payload (e.g. as received from an endpoint) could not be unmarshalled as a JSONRPC response.
		// Return a generic response to the user.
		payloadStr := string(data)
		logger.With(
			"unmarshal_err", err,
			"raw_payload", log.Preview(payloadStr),
			"error_type", "parse_error",
		).Debug().Msg("Failed to unmarshal endpoint payload as JSONRPC.")

		// Create a generic JSONRPC response for the user.
		// Use the provided requestID if available (batch request case),
		// otherwise fall back to extracting from jsonrpcReqs (single request case).
		errorID := getJsonRpcIDForErrorResponse(jsonrpcReqs)
		if requestID != "" {
			errorID = jsonrpc.IDFromString(requestID)
		}
		validationErr := qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_UNMARSHAL
		return getGenericJSONRPCErrResponse(
			logger,
			errorID,
			errCodeUnmarshaling,
			errMsgJSONRPCUnmarshaling,
		), jsonrpc.Request{}, &validationErr
	}

	// TODO_TECHDEBT(@adshmh): Remove this once proto messages are separated for single and batch JSONRPC requests.
	//
	jsonrpcReq, ok := findJsonRpcRequestByID(jsonrpcReqs, jsonrpcResponse.ID)
	if !ok {
		// Use the provided requestID if available (batch request case),
		// otherwise fall back to extracting from jsonrpcReqs (single request case).
		errorID := getJsonRpcIDForErrorResponse(jsonrpcReqs)
		if requestID != "" {
			errorID = jsonrpc.IDFromString(requestID)
		}
		validationErr := qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_FORMAT_MISMATCH
		return getGenericJSONRPCErrResponse(
			logger,
			errorID,
			errCodeUnmarshaling,
			errMsgJSONRPCUnmarshaling,
		), jsonrpc.Request{}, &validationErr
	}

	// Validate the JSONRPC response.
	if err := jsonrpcResponse.Validate(jsonrpcReq.ID); err != nil {
		payloadStr := string(data)
		logger.With(
			"unmarshal_err", err,
			"raw_payload", log.Preview(payloadStr),
			"error_type", "validation_error",
		).Debug().Msg("Failed to unmarshal endpoint payload as JSONRPC: JSONRPC response failed validation.")

		validationErr := qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_FORMAT_MISMATCH
		return getGenericJSONRPCErrResponse(
			logger,
			jsonrpcReq.ID,
			errCodeUnmarshaling,
			errMsgJSONRPCUnmarshaling,
		), jsonrpc.Request{}, &validationErr
	}

	// JSONRPC response successfully validated.
	return jsonrpcResponse, jsonrpcReq, nil
}

// getGenericJSONRPCErrResponse returns a generic response wrapped around a JSONRPC error response with the supplied ID, error, and the invalid payload in the "data" field.
func getGenericJSONRPCErrResponse(
	logger polylog.Logger,
	id jsonrpc.ID,
	errCode int,
	errMsg string,
) jsonrpc.Response {
	errData := map[string]string{
		errDataFieldUnmarshalingErr: errMsg,
	}

	return jsonrpc.GetErrorResponse(id, errCode, errMsg, errData)
}

// getJsonRpcIDForErrorResponse determines the appropriate ID to use in error responses when no endpoint response was received.
// Follows JSON-RPC 2.0 specification guidelines for ID handling in error scenarios:
//
// Single request (len == 1):
//   - Returns the original request's ID to maintain proper request-response correlation
//   - Allows client to match the error response back to the specific request that failed
//
// Batch request or no requests (len != 1):
//   - Returns null ID (empty jsonrpc.ID{}) per JSON-RPC spec requirement
//   - Per spec: "If there was an error in detecting the id in the Request object, it MUST be Null"
//   - For batch requests, no single ID represents the entire failed batch
//   - For zero requests, no valid ID exists to return
//
// This approach ensures specification compliance and clear error semantics for clients.
// Reference: https://www.jsonrpc.org/specification#response_object
func getJsonRpcIDForErrorResponse(servicePayloads map[jsonrpc.ID]jsonrpc.Request) jsonrpc.ID {
	if len(servicePayloads) == 1 {
		for id := range servicePayloads {
			return id
		}
	}
	return jsonrpc.ID{}
}

// findJsonRpcRequestByID finds a JSONRPC request by ID using value-based comparison.
// This handles the case where JSON unmarshaling creates new ID structs with different
// pointer addresses but equivalent values.
func findJsonRpcRequestByID(servicePayloads map[jsonrpc.ID]jsonrpc.Request, targetID jsonrpc.ID) (jsonrpc.Request, bool) {
	for id, payload := range servicePayloads {
		if id.Equal(targetID) {
			return payload, true
		}
	}
	return jsonrpc.Request{}, false
}
