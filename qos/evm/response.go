package evm

import (
	"encoding/json"
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/log"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// responseUnmarshaller is the entrypoint function for any
// new supported response types.
// E.g. to handle "eth_getBalance" requests, the following need to be defined:
//  1. A new custom responseUnmarshaller
//  2. A new custom struct to handle the details of the particular response.
type responseUnmarshaller func(
	logger polylog.Logger,
	jsonrpcReq jsonrpc.Request,
	jsonrpcResp jsonrpc.Response,
) (response, error)

var (
	// All response types need to implement the response interface.
	// Any new response struct needs to be added to the following list.
	_ response = &responseToChainID{}
	_ response = &responseToBlockNumber{}
	_ response = &responseToGetBalance{}
	_ response = &responseGeneric{}

	methodResponseMappings = map[jsonrpc.Method]responseUnmarshaller{
		methodChainID:     responseUnmarshallerChainID,
		methodBlockNumber: responseUnmarshallerBlockNumber,
		methodGetBalance:  responseUnmarshallerGetBalance,
	}
)

// unmarshalResponse converts raw endpoint bytes into a JSONRPC response struct.
// As of PR #194, generates endpoint observations for responses to:
//   - eth_chainId
//   - eth_blockNumber
//   - eth_getBalance
//   - any empty response, regardless of method
func unmarshalResponse(
	logger polylog.Logger,
	servicePayloads map[jsonrpc.ID]protocol.Payload,
	data []byte,
	endpointAddr protocol.EndpointAddr,
) (response, error) {
	// Create a specialized response for empty endpoint response.
	if len(data) == 0 {
		return responseEmpty{
			logger:          logger,
			servicePayloads: servicePayloads,
		}, nil
	}

	// Unmarshal the raw response payload into a JSONRPC response.
	var jsonrpcResponse jsonrpc.Response
	err := json.Unmarshal(data, &jsonrpcResponse)
	if err != nil {
		// Get the request payload, for single JSONRPC requests.
		requestPayload := getSingleJSONRPCRequestPayload(servicePayloads)

		// The response raw payload (e.g. as received from an endpoint) could not be unmarshalled as a JSONRC response.
		// Return a generic response to the user.
		payloadStr := string(data)
		logger.With(
			"jsonrpc_request", requestPayload,
			"unmarshal_err", err,
			"raw_payload", log.Preview(payloadStr),
			"endpoint_addr", endpointAddr,
		).Error().Msg("❌ Failed to unmarshal response payload as JSON-RPC")

		return getGenericJSONRPCErrResponse(logger, getJsonRpcIDForErrorResponse(servicePayloads), data, err), err
	}

	// Get the corresponding service payload for the response.
	servicePayload, ok := findServicePayloadByID(servicePayloads, jsonrpcResponse.ID)
	if !ok {
		// TODO_TECHDEBT(@commoddity): Add QoS check for if endpoint fails to return the correct ID in the response.
		logger.Error().Msg("SHOULD NEVER HAPPEN: JSON-RPC ID not found in the response")
		err := fmt.Errorf("JSON-RPC ID not found in the response")
		return getGenericJSONRPCErrResponse(logger, getJsonRpcIDForErrorResponse(servicePayloads), data, err), err
	}

	// Get the JSON-RPC request from the service payload.
	jsonrpcReq, err := jsonrpc.GetJsonRpcReqFromServicePayload(servicePayload)
	if err != nil {
		logger.Error().Err(err).Msg("SHOULD NEVER HAPPEN: Failed to get JSONRPC request from service payload")
		return getGenericJSONRPCErrResponse(logger, getJsonRpcIDForErrorResponse(servicePayloads), data, err), err
	}

	// Validate the JSONRPC response.
	if err := jsonrpcResponse.Validate(jsonrpcReq.ID); err != nil {
		payloadStr := string(data)
		logger.With(
			"jsonrpc_request", jsonrpcReq,
			"validation_err", err,
			"raw_payload", log.Preview(payloadStr),
			"endpoint_addr", endpointAddr,
		).Error().Msg("❌ Failed to unmarshal response payload as valid JSON-RPC")

		return getGenericJSONRPCErrResponse(logger, jsonrpcReq.ID, data, err), err
	}

	// The parsed JSONRPC response contains an error:
	// - Log the request and the response
	// - Used to track potential endpoint quality issues.
	if jsonrpcResponseErr := jsonrpcResponse.Error; jsonrpcResponseErr != nil {
		logger.With(
			"jsonrpc_request", jsonrpcReq,
			"jsonrpc_response_error_code", jsonrpcResponseErr.Code,
			"jsonrpc_response_error_message", jsonrpcResponseErr.Message,
			"endpoint_addr", endpointAddr,
		).Error().Msg("❌ Endpoint returned JSONRPC response with error")
	}

	// Unmarshal the JSONRPC response into a method-specific response.
	unmarshaller, found := methodResponseMappings[jsonrpcReq.Method]
	if found {
		return unmarshaller(logger, jsonrpcReq, jsonrpcResponse)
	}

	// Default to a generic response if no method-specific response is found.
	// Pass the already unmarshaled jsonrpcResponse to avoid double unmarshaling.
	return responseUnmarshallerGenericFromResponse(logger, jsonrpcReq, jsonrpcResponse)
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
func getJsonRpcIDForErrorResponse(servicePayloads map[jsonrpc.ID]protocol.Payload) jsonrpc.ID {
	if len(servicePayloads) == 1 {
		for id := range servicePayloads {
			return id
		}
	}
	return jsonrpc.ID{}
}

// findServicePayloadByID finds a service payload by ID using value-based comparison.
// This handles the case where JSON unmarshaling creates new ID structs with different
// pointer addresses but equivalent values.
func findServicePayloadByID(servicePayloads map[jsonrpc.ID]protocol.Payload, targetID jsonrpc.ID) (protocol.Payload, bool) {
	for id, payload := range servicePayloads {
		if id.Equal(targetID) {
			return payload, true
		}
	}
	return protocol.Payload{}, false
}

// TODO_HACK(@adshmh): Drop this once single and batch JSONRPC request handling logic is fully separated.
// - There should be no need to rely on endpoint's payload to match the request from a batch.
// - Single JSONRPC requests do not need the complexity of batch request logic.
//
// This only targets single JSONRPC requests.
func getSingleJSONRPCRequestPayload(servicePayloads map[jsonrpc.ID]protocol.Payload) string {
	if len(servicePayloads) != 1 {
		return ""
	}

	for _, payload := range servicePayloads {
		if len(payload.Data) > 0 {
			return payload.Data
		}
	}

	return ""
}
