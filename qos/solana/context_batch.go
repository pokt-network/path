package solana

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// batchJSONRPCRequestContext provides the support required by the gateway
// package for handling service requests.
var _ gateway.RequestQoSContext = &batchJSONRPCRequestContext{}

type endpointJSONRPCResponse struct {
	protocol.EndpointAddr
	jsonrpc.Response
}

// batchJSONRPCRequestContext provides the functionality required
// to support QoS for a Solana blockchain service.
type batchJSONRPCRequestContext struct {
	logger polylog.Logger

	// chainID is the chain identifier for the Solana QoS implementation.
	chainID string

	// service_id is the identifier for the Solana QoS implementation.
	// It is the "alias" or human readable interpretation of the chain_id.
	// Used in generating observations.
	serviceID protocol.ServiceID

	// The length of the request payload in bytes.
	requestPayloadLength uint

	endpointStore *EndpointStore

	JSONRPCBatchRequest jsonrpc.BatchRequest

	// The origin of the request handled by the context.
	// Either:
	// - User: user requests
	// - QoS: requests built by the QoS service to get additional data points on endpoints.
	requestOrigin qosobservations.RequestOrigin

	// endpointResponses is the set of responses received from one or
	// more endpoints as part of handling this service request.
	endpointJSONRPCResponses []endpointJSONRPCResponse

	// protocolError stores a protocol-level error that occurred before any endpoint could respond.
	// Used to provide more specific error messages to clients.
	protocolError error
}

// TODO_NEXT(@commoddity): handle batch requests for Solana
// TODO_MVP(@adshmh): Ensure the JSONRPC request struct
// can handle all valid service requests.
func (brc batchJSONRPCRequestContext) GetServicePayloads() []protocol.Payload {
	protocolPayloads := make([]protocol.Payload, len(brc.JSONRPCBatchRequest.Requests))

	for i, jsonrpcRequestPayload := range brc.JSONRPCBatchRequest.GetRequestsPayloads() {
		// TODO_TECHDEBT(@adshmh): Set method-specific timeouts on protocol payload entry.
		protocolPayloads[i] = protocol.Payload{
			Data:    string(jsonrpcRequestPayload),
			Method:  http.MethodPost, // Method is alway POST for Solana.
			Path:    "",              // Path field is not used for Solana.
			RPCType: sharedtypes.RPCType_JSON_RPC,
		}
	}

	return protocolPayloads
}

// TODO_TECHDEBT(@adshmh): Refactor once the QoS context interface is updated to receive an array of responses.
// UpdateWithResponse is NOT safe for concurrent use
// The requestID parameter is used for batch requests to ensure error responses have the correct ID.
func (brc *batchJSONRPCRequestContext) UpdateWithResponse(endpointAddr protocol.EndpointAddr, responseBz []byte, httpStatusCode int, requestID string) {
	// TODO_TECHDEBT(@adshmh): Refactor this once the QoS context interface is updated to accept all endpoint responses at once.
	// This would make it possible to map each JSONRPC request of a batch to its corresponding endpoint response.
	// This is required to enable request method-specific esponse validation: e.g. format of result field in response to a `getHealth` request.
	//
	// Parse and track the endpoint payload as a JSONRPC response.
	var jsonrpcResponse jsonrpc.Response
	if err := json.Unmarshal(responseBz, &jsonrpcResponse); err != nil {
		// TODO_UPNEXT(@adshmh): Include a preview of malformed payload in the response.
		//
		// Parsing failed, store a generic error JSONRPC response with the correct request ID
		responseID := jsonrpc.IDFromString(requestID)
		jsonrpcResponse = jsonrpc.GetErrorResponse(responseID, errCodeUnmarshaling, errMsgUnmarshaling, nil)
	}

	// Store the response: will be processed later by the JSONRPC batch request struct.
	brc.endpointJSONRPCResponses = append(brc.endpointJSONRPCResponses, endpointJSONRPCResponse{
		EndpointAddr: endpointAddr,
		Response:     jsonrpcResponse,
	})
}

// SetProtocolError stores a protocol-level error for more specific client error messages.
// Implements the gateway.RequestQoSContext interface.
func (brc *batchJSONRPCRequestContext) SetProtocolError(err error) {
	brc.protocolError = err
}

// TODO_MVP(@adshmh): add `Content-Type: application/json` header.
// GetHTTPResponse builds the HTTP response that should be returned for
// a Solana blockchain service request.
func (brc batchJSONRPCRequestContext) GetHTTPResponse() pathhttp.HTTPResponse {
	// TODO_UPNEXT(@adshmh): Return an error response matching the batch of JSONRPC requests.
	//
	// No responses received: this is an internal error:
	// e.g. protocol-level errors like endpoint timing out.
	if len(brc.endpointJSONRPCResponses) == 0 {
		// Use the specific protocol error if available, otherwise use a generic message.
		var errToReport error
		if brc.protocolError != nil {
			errToReport = brc.protocolError
		} else {
			errToReport = errors.New("protocol-level error: no endpoint responses received")
		}
		jsonrpcErrorResponse := jsonrpc.NewErrResponseInternalErr(jsonrpc.ID{}, errToReport)
		return qos.BuildHTTPResponseFromJSONRPCResponse(brc.logger, jsonrpcErrorResponse)
	}

	// assemble the array of JSONRPC responses
	jsonrpcResponses := make([]jsonrpc.Response, len(brc.endpointJSONRPCResponses))
	for i, jsonrpcResponse := range brc.endpointJSONRPCResponses {
		jsonrpcResponses[i] = jsonrpcResponse.Response
	}

	// Use the Batch JSONRPC request to assemble the JSONRPC batch response.
	batchResponseBz := brc.JSONRPCBatchRequest.BuildResponseBytes(jsonrpcResponses)

	// TODO_UPNEXT(@adshmh): Adjust HTTP status code according to responses in the batch.
	return jsonrpc.HTTPResponse{
		ResponsePayload: batchResponseBz,
		// According to the JSON-RPC 2.0 specification, even if individual responses
		// in a batch contain errors, the entire batch should still return HTTP 200 OK.
		HTTPStatusCode: http.StatusOK,
	}
}

// GetObservations returns all the observations contained in the request context.
// Implements the gateway.RequestQoSContext interface.
func (rc batchJSONRPCRequestContext) GetObservations() qosobservations.Observations {
	// Set the observation fields common for all requests: successful or failed.
	observations := &qosobservations.SolanaRequestObservations{
		ChainId:              rc.chainID,
		ServiceId:            string(rc.serviceID),
		RequestPayloadLength: uint32(rc.requestPayloadLength),
		RequestOrigin:        rc.requestOrigin,
		// TODO_UPNEXT(@adshmh): Add a Batch JSONRPC request observation.
	}

	// No endpoint responses received.
	// Set request error.
	if len(rc.endpointJSONRPCResponses) == 0 {
		observations.RequestError = qos.GetRequestErrorForProtocolError()

		return qosobservations.Observations{
			ServiceObservations: &qosobservations.Observations_Solana{
				Solana: observations,
			},
		}
	}

	// TODO_UPNEXT(@adshmh): Report batch JSONRPC requests endpoint observations via metrics.
	//
	return qosobservations.Observations{
		ServiceObservations: &qosobservations.Observations_Solana{
			Solana: observations,
		},
	}
}

// GetEndpointSelector is required to satisfy the gateway package's RequestQoSContext interface.
// The request context is queried for the correct endpoint selector.
// This allows different endpoint selectors based on the request's context.
// e.g. the request context for a particular request method can potentially rank endpoints based on their latency when responding to requests with matching method.
func (rc *batchJSONRPCRequestContext) GetEndpointSelector() protocol.EndpointSelector {
	return rc
}

// TODO_TECHDEBT(@adshmh): Enhance endpoint selection to consider endpoint quality specific to batch requests.
//
// Select chooses an endpoint from the list of supplied endpoints.
// It uses the perceived state of the Solana chain using other endpoints' responses.
// It is required to satisfy the protocol package's EndpointSelector interface.
func (rc *batchJSONRPCRequestContext) Select(allEndpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	return rc.endpointStore.Select(allEndpoints)
}

// SelectMultiple chooses multiple endpoints from the list of supplied endpoints.
// It uses the perceived state of the Solana chain using other endpoints' responses.
// It is required to satisfy the protocol package's EndpointSelector interface.
func (rc *batchJSONRPCRequestContext) SelectMultiple(allEndpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	return rc.endpointStore.SelectMultiple(allEndpoints, numEndpoints)
}
