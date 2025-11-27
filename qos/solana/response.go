package solana

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/log"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// responseUnmarshaller is the entrypoint for processing new supported response types.
//
// To add support for a new endpoint (e.g. "getBalance"):
// 1. Define a new custom responseUnmarshaller
// 2. Create a corresponding struct to handle the response details
type responseUnmarshaller func(
	logger polylog.Logger,
	jsonrpcReq jsonrpc.Request,
	jsonrpcResp jsonrpc.Response,
) response

var (
	// All response types must implement the response interface.
	_ response = &responseToGetEpochInfo{}
	_ response = &responseToGetHealth{}
	_ response = &responseGeneric{}

	// Maps JSON-RPC methods to their corresponding response unmarshallers.
	methodResponseMappings = map[jsonrpc.Method]responseUnmarshaller{
		methodGetHealth:    responseUnmarshallerGetHealth,
		methodGetEpochInfo: responseUnmarshallerGetEpochInfo,
	}
)

// unmarshalResponse parses the supplied raw byte slice from an endpoint into a JSON-RPC response.
func unmarshalResponse(
	logger polylog.Logger,
	jsonrpcReq jsonrpc.Request,
	data []byte,
	endpointAddr protocol.EndpointAddr,
) response {
	// Unmarshal the raw response payload into a JSON-RPC response.
	var jsonrpcResponse jsonrpc.Response
	if err := json.Unmarshal(data, &jsonrpcResponse); err != nil {
		// The response raw payload (e.g. as received from an endpoint) could not be unmarshalled as a JSONRC response.
		// Return a generic response to the user.
		payloadStr := string(data)
		logger.With(
			"request", jsonrpcReq,
			"unmarshal_err", err,
			"raw_payload", log.Preview(payloadStr),
			"endpoint_addr", endpointAddr,
		).Debug().Msg("Failed to unmarshal response payload as JSON-RPC")

		// The response raw payload (e.g. as received from an endpoint) could not be unmarshalled as a JSON-RPC response.
		// Return a generic response to the user.
		return getGenericJSONRPCErrResponse(logger, jsonrpcReq.ID, data, err)
	}

	// Validate the JSON-RPC response.
	if err := jsonrpcResponse.Validate(jsonrpcReq.ID); err != nil {
		payloadStr := string(data)
		logger.With(
			"request", jsonrpcReq,
			"validation_err", err,
			"raw_payload", log.Preview(payloadStr),
			"endpoint_addr", endpointAddr,
		).Debug().Msg("JSON-RPC response validation failed")

		return getGenericJSONRPCErrResponse(logger, jsonrpcReq.ID, data, err)
	}

	// NOTE: We intentionally skip checking whether the JSON-RPC response indicates an error.
	// This allows the method-specific handler to determine how to respond to the user.

	// Unmarshal the JSON-RPC response into a method-specific response.
	unmarshaller, found := methodResponseMappings[jsonrpcReq.Method]
	if found {
		return unmarshaller(logger, jsonrpcReq, jsonrpcResponse)
	}

	// Default to a generic response if no method-specific response is found.
	return responseUnmarshallerGeneric(logger, jsonrpcReq, jsonrpcResponse)
}
