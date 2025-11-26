package evm

import (
	"fmt"

	"github.com/pokt-network/path/qos/jsonrpc"
)

// TODO_CONSIDERATION(@adshmh): Is there any value in further breaking down error codes here to indicate different failures?
//
// newErrResponseInternalErr creates a JSON-RPC error response when an internal error has occurred (e.g. reading HTTP request's body)
// Marks the error as retryable to allow clients to safely retry their request.
func newErrResponseInternalErr(requestID jsonrpc.ID, err error) jsonrpc.Response {
	return jsonrpc.GetErrorResponse(
		requestID,
		jsonrpc.ResponseCodeDefaultInternalErr,         // Used to indicate an internal error on PATH/protocol (e.g. failed to read the HTTP request's body)
		fmt.Sprintf("internal error: %s", err.Error()), // Error Message
		map[string]string{
			"error": err.Error(),
			// Custom extension - not part of the official JSON-RPC spec
			// Marks the error as retryable to allow clients to safely retry their request
			"retryable": "true",
		},
	)
}

// newErrResponseInvalidRequest returns a JSON-RPC error response for malformed or invalid requests.
// The error indicates the request cannot be processed due to issues like:
//   - Failed JSON-RPC deserialization
//   - Missing required JSON-RPC fields (e.g. `method`)
//   - Unsupported JSON-RPC method
//
// If the request contains a valid JSON-RPC ID, it is included in the error response.
// The error is marked as permanent since retrying without correcting the request will fail.
func newErrResponseInvalidRequest(err error, requestID jsonrpc.ID) jsonrpc.Response {
	return jsonrpc.GetErrorResponse(
		requestID,                             // Use request's original ID if present
		jsonrpc.ResponseCodeDefaultBadRequest, // JSON-RPC error code indicating bad user request
		fmt.Sprintf("invalid request: %s", err.Error()), // Error Message
		map[string]string{
			"error": err.Error(),
			// Custom extension - not part of the official JSON-RPC spec
			// Indicates this error is permanent - the request must be corrected as retrying will not succeed
			"retryable": "false",
		},
	)
}
