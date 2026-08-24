package noop

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// requestContext implements all the functionality required by gateway.RequestQoSContext interface.
var _ gateway.RequestQoSContext = &requestContext{}

// requestContext provides the functionality required to fulfill the role of a Noop QoS service,
// i.e. no validation of requests or responses, and no data is kept on endpoints to guide
// the endpoint selection process.
type requestContext struct {
	// httpRequestBody contains the body of the HTTP request for which this instance of
	// requestContext was constructed.
	httpRequestBody []byte

	// httpRequestMethod contains the HTTP method (GET, POST, PUT, etc.) of the request for
	// which this instance of requestContext was constructed.
	// For more details, see https://pkg.go.dev/net/http#Request
	httpRequestMethod string

	// httpRequestPath contains the path of the HTTP request for which this instance of
	// requestContext was constructed.
	httpRequestPath string

	// receivedResponses maintains response(s) received from one or more endpoints, for the
	// request represented by this instance of requestContext.
	receivedResponses []endpointResponse

	// presetFailureResponse, if set, is used to return a preconstructed response to the user.
	// This is used by the conductor of the requestContext instance, e.g. if reading the HTTP request's body fails.
	presetFailureResponse pathhttp.HTTPResponse

	// protocolError stores a protocol-level error that occurred before any endpoint could respond.
	// Used to provide more specific error messages to clients.
	protocolError error

	// detectedRPCType is the RPC type detected by the gateway for this request.
	// Used to set the correct RPCType on the service payload instead of UNKNOWN_RPC.
	detectedRPCType sharedtypes.RPCType

	// isBatch is true when the request was a JSON-RPC batch (array of requests).
	// Set by GetServicePayloads when batch splitting occurs.
	isBatch bool

	// logger is the QoS service's logger; used only to report batch items that got no response.
	logger polylog.Logger

	// batchRequestIDs holds each batch item's id, in item order, so an item that gets no
	// response can still be answered with an error object carrying its own id. Set alongside
	// isBatch. An item whose id cannot be read is recorded as empty and treated as a
	// notification (never filled) — it is still passed through untouched.
	batchRequestIDs []jsonrpc.ID

	// endpointSelector is the selector used for choosing endpoints.
	// When block height tracking is active, this performs sync-allowance filtering.
	endpointSelector protocol.EndpointSelector
}

// GetServicePayloads returns the payload(s) to be sent to service endpoint(s).
// For JSON-RPC batch requests (body starts with '['), each item in the array
// is returned as a separate payload so the gateway can distribute them across
// different suppliers — consistent with how EVM/Cosmos QoS handles batches.
// For all other requests, the full body is returned as a single payload.
// Implements the gateway.RequestQoSContext interface.
func (rc *requestContext) GetServicePayloads() []protocol.Payload {
	path := rc.httpRequestPath

	// Only attempt batch splitting for JSON-RPC requests (POST with array body)
	if rc.httpRequestMethod == http.MethodPost && rc.detectedRPCType == sharedtypes.RPCType_JSON_RPC {
		trimmed := bytes.TrimSpace(rc.httpRequestBody)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			var items []json.RawMessage
			if err := json.Unmarshal(trimmed, &items); err == nil && len(items) > 1 {
				rc.isBatch = true
				rc.batchRequestIDs = make([]jsonrpc.ID, 0, len(items))
				payloads := make([]protocol.Payload, 0, len(items))
				for _, item := range items {
					var probe struct {
						ID jsonrpc.ID `json:"id"`
					}
					_ = json.Unmarshal(item, &probe) // unreadable id → empty → notification
					rc.batchRequestIDs = append(rc.batchRequestIDs, probe.ID)
					payloads = append(payloads, protocol.Payload{
						Data:    string(item),
						Method:  rc.httpRequestMethod,
						Path:    path,
						Headers: map[string]string{},
						RPCType: rc.detectedRPCType,
					})
				}
				return payloads
			}
		}
	}

	// Non-batch: return full body as single payload
	payload := protocol.Payload{
		Data:    string(rc.httpRequestBody),
		Method:  rc.httpRequestMethod,
		Path:    path,
		Headers: map[string]string{},
		RPCType: rc.detectedRPCType,
	}
	return []protocol.Payload{payload}
}

// UpdateWithResponse is used to inform the requestContext of the response to its underlying service request, returned from an endpoint.
// UpdateWithResponse is NOT safe for concurrent use
// Implements the gateway.RequestQoSContext interface.
// The requestID parameter is unused for NoOp QoS but required by the interface.
func (rc *requestContext) UpdateWithResponse(endpointAddr protocol.EndpointAddr, endpointSerializedResponse []byte, httpStatusCode int, requestID string) {
	rc.receivedResponses = append(rc.receivedResponses, endpointResponse{
		EndpointAddr:   endpointAddr,
		ResponseBytes:  endpointSerializedResponse,
		HTTPStatusCode: httpStatusCode,
	})
}

// SetProtocolError stores a protocol-level error for more specific client error messages.
// Implements the gateway.RequestQoSContext interface.
func (rc *requestContext) SetProtocolError(err error) {
	rc.protocolError = err
}

// GetHTTPResponse returns a user-facing response that fulfills the pathhttp.HTTPResponse interface.
// Any preset failure responses, e.g. set during the construction of the requestContext instance, take priority.
// For batch requests, individual responses are reassembled into a JSON array.
// For single requests, returns the most recently reported endpoint response.
// Implements the gateway.RequestQoSContext interface.
func (rc *requestContext) GetHTTPResponse() pathhttp.HTTPResponse {
	if rc.presetFailureResponse != nil {
		return rc.presetFailureResponse
	}

	if len(rc.receivedResponses) == 0 {
		// Use the specific protocol error if available, otherwise use a generic message.
		if rc.protocolError != nil {
			return getNoEndpointResponseWithError(rc.protocolError)
		}
		return getNoEndpointResponse()
	}

	// Reassemble batch responses into a JSON array
	if rc.isBatch {
		var items []json.RawMessage
		for _, resp := range rc.receivedResponses {
			if len(resp.ResponseBytes) > 0 {
				// Strip trailing newline if present
				payload := resp.ResponseBytes
				if payload[len(payload)-1] == '\n' {
					payload = payload[:len(payload)-1]
				}
				items = append(items, json.RawMessage(payload))
			}
		}

		if len(items) == 0 {
			return getNoEndpointResponse()
		}

		// An item that failed every attempt arrived with no body and was skipped above. It
		// still gets a response object — an error carrying its own id — rather than silently
		// vanishing from the array the client correlates on.
		items = jsonrpc.FillMissingBatchResponses(rc.logger, items, rc.batchRequestIDs)

		batchPayload, err := json.Marshal(items)
		if err != nil {
			return getNoEndpointResponse()
		}

		return &HTTPResponse{
			httpStatusCode: http.StatusOK,
			payload:        batchPayload,
		}
	}

	latestResponse := rc.receivedResponses[len(rc.receivedResponses)-1]
	// Use original HTTP status from backend if available, otherwise default to 200 OK
	statusCode := http.StatusOK
	if latestResponse.HTTPStatusCode != 0 {
		statusCode = latestResponse.HTTPStatusCode
	}
	return &HTTPResponse{
		httpStatusCode: statusCode,
		payload:        latestResponse.ResponseBytes,
	}
}

// GetObservations returns an empty struct that fulfill the required interface, since the noop QoS does not make or use
// any endpoint observations to improve endpoint selection.
// Implements the gateway.RequestQoSContext interface.
func (rc *requestContext) GetObservations() qosobservations.Observations {
	return qosobservations.Observations{}
}

// GetEndpointSelector returns the endpoint selector for this request context.
// When block height tracking is enabled, this returns a filtering selector.
// Implements the gateway.RequestQoSContext interface.
func (rc *requestContext) GetEndpointSelector() protocol.EndpointSelector {
	if rc.endpointSelector != nil {
		return rc.endpointSelector
	}
	return RandomEndpointSelector{}
}
