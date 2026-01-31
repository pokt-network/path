package solana

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/tidwall/gjson"

	"github.com/pokt-network/path/gateway"
	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

const (
	// errCodeUnmarshaling is set as the JSON-RPC response's error code if the endpoint returns a malformed response.
	// `jsonrpc.ResponseCodeBackendServerErr`, i.e. code -31002, will result in returning a 500 HTTP Status Code to the client.
	errCodeUnmarshaling = jsonrpc.ResponseCodeBackendServerErr

	// errMsgUnmarshaling is the generic message returned to the user if the endpoint returns a malformed response.
	errMsgUnmarshaling = "the response returned by the endpoint is not a valid JSON-RPC response"
)

// requestContext provides the support required by the gateway
// package for handling service requests.
var _ gateway.RequestQoSContext = &requestContext{}

// rawEndpointResponse stores raw bytes from endpoint responses.
// This is used on the hot path to avoid parsing overhead.
// Heavy parsing for observations is done asynchronously via the ObservationQueue.
type rawEndpointResponse struct {
	EndpointAddr   protocol.EndpointAddr
	ResponseBytes  []byte
	HTTPStatusCode int
}

// requestContext provides the functionality required
// to support QoS for a Solana blockchain service.
// Uses raw byte passthrough on the hot path to minimize latency.
type requestContext struct {
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

	JSONRPCReq jsonrpc.Request

	// The origin of the request handled by the context.
	// Either:
	// - User: user requests
	// - QoS: requests built by the QoS service to get additional data points on endpoints.
	requestOrigin qosobservations.RequestOrigin

	// rawResponses stores raw endpoint responses.
	// Raw bytes are stored without parsing for low-latency client response.
	// Heavy parsing (for observations) is done asynchronously via ObservationQueue.
	rawResponses []rawEndpointResponse

	// protocolError stores a protocol-level error that occurred before any endpoint could respond.
	// Used to provide more specific error messages to clients.
	protocolError error
}

// TODO_NEXT(@commoddity): handle batch requests for Solana
// TODO_MVP(@adshmh): Ensure the JSONRPC request struct
// can handle all valid service requests.
func (rc requestContext) GetServicePayloads() []protocol.Payload {
	reqBz, err := json.Marshal(rc.JSONRPCReq)
	if err != nil {
		rc.logger.Error().Err(err).Msg("SHOULD RARELY HAPPEN: requestContext.GetServicePayload() should never fail marshaling the JSONRPC request.")
		return []protocol.Payload{protocol.EmptyErrorPayload()}
	}

	payload := protocol.Payload{
		Data:    string(reqBz),
		Method:  http.MethodPost, // Method is alway POST for Solana.
		Path:    "",              // Path field is not used for Solana.
		Headers: map[string]string{},
		RPCType: sharedtypes.RPCType_JSON_RPC,
	}

	return []protocol.Payload{payload}
}

// UpdateWithResponse is NOT safe for concurrent use
//
// Raw bytes are stored without parsing for low-latency client response.
// Heavy parsing (for observations) is done asynchronously via ObservationQueue.
//
// The requestID parameter is unused for Solana QoS (single request only) but required by the interface.
func (rc *requestContext) UpdateWithResponse(endpointAddr protocol.EndpointAddr, responseBz []byte, httpStatusCode int, _ string) {
	rc.logger = rc.logger.With(
		"endpoint_addr", endpointAddr,
		"endpoint_response_len", len(responseBz),
	)

	// Store raw bytes without parsing for low-latency client response.
	// Heavy parsing (for observations) is done async via ObservationQueue.
	rc.rawResponses = append(rc.rawResponses, rawEndpointResponse{
		EndpointAddr:   endpointAddr,
		ResponseBytes:  responseBz,
		HTTPStatusCode: httpStatusCode,
	})
}

// SetProtocolError stores a protocol-level error for more specific client error messages.
// Implements the gateway.RequestQoSContext interface.
func (rc *requestContext) SetProtocolError(err error) {
	rc.protocolError = err
}

// GetHTTPResponse builds the HTTP response that should be returned for
// a Solana blockchain service request.
//
// Returns raw bytes as-is (no parsing/re-encoding) for low-latency client response.
// Heavy parsing for observations is done asynchronously via ObservationQueue.
func (rc requestContext) GetHTTPResponse() pathhttp.HTTPResponse {
	// No responses received: this is an internal error:
	// e.g. protocol-level errors like endpoint timing out.
	if len(rc.rawResponses) == 0 {
		// Use the specific protocol error if available, otherwise use a generic message.
		var errToReport error
		if rc.protocolError != nil {
			errToReport = rc.protocolError
		} else {
			errToReport = errors.New("protocol-level error: no endpoint responses received")
		}
		jsonrpcErrorResponse := jsonrpc.NewErrResponseInternalErr(rc.JSONRPCReq.ID, errToReport)
		return qos.BuildHTTPResponseFromJSONRPCResponse(rc.logger, jsonrpcErrorResponse)
	}

	// Return the most recent raw response as-is for single requests
	latestResponse := rc.rawResponses[len(rc.rawResponses)-1]

	return &rawHTTPResponse{
		httpStatusCode: latestResponse.HTTPStatusCode,
		payload:        latestResponse.ResponseBytes,
	}
}

// rawHTTPResponse implements pathhttp.HTTPResponse for raw byte passthrough.
type rawHTTPResponse struct {
	httpStatusCode int
	payload        []byte
}

func (r *rawHTTPResponse) GetPayload() []byte {
	return r.payload
}

func (r *rawHTTPResponse) GetHTTPStatusCode() int {
	if r.httpStatusCode == 0 {
		return http.StatusOK
	}
	return r.httpStatusCode
}

func (r *rawHTTPResponse) GetHTTPHeaders() map[string]string {
	return map[string]string{
		"Content-Type": "application/json",
	}
}

// GetObservations returns all the observations contained in the request context.
// Uses gjson to parse raw bytes for observation creation (async path).
// Implements the gateway.RequestQoSContext interface.
func (rc requestContext) GetObservations() qosobservations.Observations {
	// Set the observation fields common for all requests: successful or failed.
	observations := &qosobservations.SolanaRequestObservations{
		ChainId:              rc.chainID,
		ServiceId:            string(rc.serviceID),
		RequestPayloadLength: uint32(rc.requestPayloadLength),
		RequestOrigin:        rc.requestOrigin,
		JsonrpcRequest:       rc.JSONRPCReq.GetObservation(),
	}

	// No endpoint responses received.
	// Set request error.
	if len(rc.rawResponses) == 0 {
		observations.RequestError = qos.GetRequestErrorForProtocolError()

		return qosobservations.Observations{
			ServiceObservations: &qosobservations.Observations_Solana{
				Solana: observations,
			},
		}
	}

	// Build endpoint observations from raw bytes using gjson
	endpointObservations := make([]*qosobservations.SolanaEndpointObservation, len(rc.rawResponses))
	for idx, rawResp := range rc.rawResponses {
		endpointObservations[idx] = rc.createEndpointObservationFromRawBytes(rawResp)
	}

	// Set the endpoint observations fields.
	observations.EndpointObservations = endpointObservations

	return qosobservations.Observations{
		ServiceObservations: &qosobservations.Observations_Solana{
			Solana: observations,
		},
	}
}

// createEndpointObservationFromRawBytes creates an endpoint observation from raw response bytes.
// Uses gjson for efficient parsing of only the fields needed for observations.
// Returns a pointer to avoid copying the protobuf struct which contains a mutex.
func (rc requestContext) createEndpointObservationFromRawBytes(rawResp rawEndpointResponse) *qosobservations.SolanaEndpointObservation {
	// Build parsed JSONRPC response observation using gjson
	parsedResp := rc.parseJSONRPCResponseForObservation(rawResp.ResponseBytes)

	// Create an unrecognized response observation (since we're not doing method-specific parsing on hot path)
	return &qosobservations.SolanaEndpointObservation{
		EndpointAddr:   string(rawResp.EndpointAddr),
		HttpStatusCode: int32(rawResp.HTTPStatusCode),
		ResponseObservation: &qosobservations.SolanaEndpointObservation_UnrecognizedResponse{
			UnrecognizedResponse: &qosobservations.SolanaUnrecognizedResponse{
				JsonrpcResponse: parsedResp,
				// ValidationError is nil - no validation error if we could parse it
			},
		},
	}
}

// parseJSONRPCResponseForObservation parses raw bytes into a JsonRpcResponse observation using gjson.
func (rc requestContext) parseJSONRPCResponseForObservation(responseBz []byte) *qosobservations.JsonRpcResponse {
	if len(responseBz) == 0 {
		return nil
	}

	resp := &qosobservations.JsonRpcResponse{}

	// Extract ID as string
	idResult := gjson.GetBytes(responseBz, "id")
	if idResult.Exists() {
		resp.Id = idResult.String()
	}

	// Check for error
	errorResult := gjson.GetBytes(responseBz, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		errorCode := gjson.GetBytes(responseBz, "error.code")
		errorMsg := gjson.GetBytes(responseBz, "error.message")
		resp.Error = &qosobservations.JsonRpcResponseError{
			Code:    errorCode.Int(),
			Message: errorMsg.String(),
		}
	}

	// Extract result preview (truncated for size)
	resultResult := gjson.GetBytes(responseBz, "result")
	if resultResult.Exists() && resultResult.Type != gjson.Null {
		resultStr := resultResult.String()
		// Truncate result preview to a reasonable length
		const maxPreviewLen = 200
		if len(resultStr) > maxPreviewLen {
			resultStr = resultStr[:maxPreviewLen] + "..."
		}
		resp.ResultPreview = resultStr
	}

	return resp
}

// GetEndpointSelector is required to satisfy the gateway package's RequestQoSContext interface.
// The request context is queried for the correct endpoint selector.
// This allows different endpoint selectors based on the request's context.
// e.g. the request context for a particular request method can potentially rank endpoints based on their latency when responding to requests with matching method.
func (rc *requestContext) GetEndpointSelector() protocol.EndpointSelector {
	return rc
}

// Select chooses an endpoint from the list of supplied endpoints.
// It uses the perceived state of the Solana chain using other endpoints' responses.
// It is required to satisfy the protocol package's EndpointSelector interface.
func (rc *requestContext) Select(allEndpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	return rc.endpointStore.Select(allEndpoints)
}

// SelectMultiple chooses multiple endpoints from the list of supplied endpoints.
// It uses the perceived state of the Solana chain using other endpoints' responses.
// It is required to satisfy the protocol package's EndpointSelector interface.
func (rc *requestContext) SelectMultiple(allEndpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	return rc.endpointStore.SelectMultiple(allEndpoints, numEndpoints)
}

// SelectMultipleWithArchival chooses multiple endpoints with optional archival filtering.
// Solana does not have an archival concept, so requiresArchival is ignored.
// It is required to satisfy the protocol package's EndpointSelector interface.
func (rc *requestContext) SelectMultipleWithArchival(allEndpoints protocol.EndpointAddrList, numEndpoints uint, requiresArchival bool) (protocol.EndpointAddrList, error) {
	return rc.endpointStore.SelectMultipleWithArchival(allEndpoints, numEndpoints, requiresArchival)
}
