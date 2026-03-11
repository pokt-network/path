package evm

import (
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

var (
	// All response types need to implement the response interface.
	_ response = &responseGeneric{}
)

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
func getJsonRpcIDForErrorResponse(servicePayloads map[jsonrpc.ID]protocol.Payload) jsonrpc.ID {
	if len(servicePayloads) == 1 {
		for id := range servicePayloads {
			return id
		}
	}
	return jsonrpc.ID{}
}
