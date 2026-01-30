package cosmos

import (
	"encoding/json"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/tidwall/gjson"

	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// rawEndpointResponse stores raw bytes from endpoint responses.
// This is used on the hot path to avoid parsing overhead.
// Heavy parsing for observations is done asynchronously via the ObservationQueue.
type rawEndpointResponse struct {
	EndpointAddr   protocol.EndpointAddr
	ResponseBytes  []byte
	HTTPStatusCode int
}

// requestContext provides specialized context for both JSONRPC and REST requests.
// Uses raw byte passthrough on the hot path to minimize latency.
// Implements gateway.RequestQoSContext interface.
type requestContext struct {
	logger polylog.Logger

	// TODO_TECHDEBT(@commoddity): refactor to handle all JSON-RPC specific logic internally
	// in the JSON-RPC package to remove the need to consider JSON-RPC specific logic in the
	// in the Cosmos QoS package.
	//
	// servicePayloads is a map of request IDs to service payloads.
	// Its structure differs depending on the request type:
	//   - JSONRPC: map of request IDs to service payloads.
	//   - REST: single service payload.
	//
	// DEV_NOTE: jsonrpc.ID is used as the map key for consistency with the batch request logic in
	// the JSON-RPC package. Once the TODO_TECHDEBT is addressed, this should no longer be necessary.
	servicePayloads map[jsonrpc.ID]protocol.Payload

	// Whether the request is a batch request.
	// Necessary to distinguish between a batch request of length 1 and a single request.
	// In the case of a batch request of length 1, the response must be returned as an array.
	isBatch bool

	// QoS observations for this request
	observations *qosobservations.CosmosRequestObservations

	// Protocol-level error handlers
	//
	// Builds a response to return to the user.
	// Used only if no endpoint responses are received.
	protocolErrorResponseBuilder func(polylog.Logger) pathhttp.HTTPResponse

	// Builds a request error observation indicating protocol-level error.
	// Used only if no endpoint responses are received.
	protocolErrorObservationBuilder func() *qosobservations.RequestError

	// Service state for endpoint selection
	serviceState protocol.EndpointSelector

	// rawResponses stores raw endpoint responses.
	// Raw bytes are stored without parsing for low-latency client response.
	// Heavy parsing (for observations) is done asynchronously via ObservationQueue.
	rawResponses []rawEndpointResponse

	// protocolError stores a protocol-level error that occurred before any endpoint could respond.
	// Used to provide more specific error messages to clients.
	protocolError error
}

// TODO_NEXT(@commoddity): handle batch requests for Cosmos SDK
// GetServicePayload builds the payload to send to blockchain endpoints
func (rc requestContext) GetServicePayloads() []protocol.Payload {
	var payloads []protocol.Payload
	for _, payload := range rc.servicePayloads {
		payloads = append(payloads, payload)
	}
	return payloads
}

// UpdateWithResponse is NOT safe for concurrent use
//
// Raw bytes are stored without parsing for low-latency client response.
// Heavy parsing (for observations) is done asynchronously via ObservationQueue.
//
// The requestID parameter is unused for raw byte passthrough but required by the interface.
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
// a Cosmos blockchain service request.
//
// Returns raw bytes as-is (no parsing/re-encoding) for low-latency client response.
// Heavy parsing for observations is done asynchronously via ObservationQueue.
//
// Implements the gateway.RequestQoSContext interface.
func (rc requestContext) GetHTTPResponse() pathhttp.HTTPResponse {
	// No responses received - return error response with protocol error context if available
	if len(rc.rawResponses) == 0 {
		// If a specific protocol error is available, use it for a more informative response
		if rc.protocolError != nil {
			return rc.buildProtocolErrorResponse()
		}
		return rc.protocolErrorResponseBuilder(rc.logger)
	}

	// Handle batch requests according to JSON-RPC 2.0 specification
	// https://www.jsonrpc.org/specification#batch
	if rc.isBatch {
		return rc.getBatchHTTPResponse()
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

// getBatchHTTPResponse handles batch requests by combining individual JSON-RPC responses
// into an array according to the JSON-RPC 2.0 specification.
// https://www.jsonrpc.org/specification#batch
func (rc requestContext) getBatchHTTPResponse() pathhttp.HTTPResponse {
	// Collect individual response payloads from raw responses
	var individualResponses []json.RawMessage
	for _, rawResp := range rc.rawResponses {
		// Use raw bytes directly - no parsing needed
		if len(rawResp.ResponseBytes) > 0 {
			individualResponses = append(individualResponses, json.RawMessage(rawResp.ResponseBytes))
		}
	}

	// According to JSON-RPC spec: "If there are no Response objects contained within the Response array
	// as it is to be sent to the client, the server MUST NOT return an empty Array and should return nothing at all."
	// This can happen when all requests in the batch are notifications (which don't get responses)
	// or when all individual responses are empty/invalid.
	if len(individualResponses) == 0 {
		// Create a responseGeneric for empty batch response and return its HTTP response
		errorResponse := getGenericResponseBatchEmpty(rc.logger)
		return errorResponse.GetHTTPResponse()
	}

	// Validate and construct batch response using jsonrpc package
	// This does minimal parsing for ID validation only
	batchResponse, err := jsonrpc.ValidateAndBuildBatchResponse(
		rc.logger,
		individualResponses,
		rc.servicePayloads,
	)
	if err != nil {
		// Create a responseGeneric for batch validation failure and return its HTTP response
		errorResponse := getGenericJSONRPCErrResponseBatchMarshalFailure(rc.logger, err)
		return errorResponse.GetHTTPResponse()
	}

	// Use original HTTP status from backend if available, otherwise default to 200 OK
	httpStatusCode := http.StatusOK
	if len(rc.rawResponses) > 0 && rc.rawResponses[0].HTTPStatusCode != 0 {
		httpStatusCode = rc.rawResponses[0].HTTPStatusCode
	}
	return jsonrpc.HTTPResponse{
		ResponsePayload: batchResponse,
		HTTPStatusCode:  httpStatusCode,
	}
}

// GetObservations returns QoS observations for requests.
// Uses gjson to parse raw bytes for observation creation (async path).
func (rc *requestContext) GetObservations() qosobservations.Observations {
	// Handle case where no endpoint responses were received
	if len(rc.rawResponses) == 0 {
		rc.observations.RequestLevelError = rc.protocolErrorObservationBuilder()

		return qosobservations.Observations{
			ServiceObservations: &qosobservations.Observations_Cosmos{
				Cosmos: rc.observations,
			},
		}
	}

	// Build endpoint observations from raw bytes using gjson
	endpointObservations := make([]*qosobservations.CosmosEndpointObservation, 0, len(rc.rawResponses))
	for _, rawResp := range rc.rawResponses {
		endpointObservations = append(endpointObservations, rc.createEndpointObservationFromRawBytes(rawResp))
	}

	rc.observations.EndpointObservations = endpointObservations

	return qosobservations.Observations{
		ServiceObservations: &qosobservations.Observations_Cosmos{
			Cosmos: rc.observations,
		},
	}
}

// createEndpointObservationFromRawBytes creates an endpoint observation from raw response bytes.
// Uses gjson for efficient parsing of only the fields needed for observations.
// Returns a pointer to avoid copying the protobuf struct which contains a mutex.
func (rc requestContext) createEndpointObservationFromRawBytes(rawResp rawEndpointResponse) *qosobservations.CosmosEndpointObservation {
	// Determine validation error based on response structure
	var validationErr *qosobservations.CosmosResponseValidationError
	if len(rawResp.ResponseBytes) == 0 {
		err := qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_EMPTY
		validationErr = &err
	} else if !gjson.ValidBytes(rawResp.ResponseBytes) {
		err := qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_UNMARSHAL
		validationErr = &err
	}

	// Build parsed JSONRPC response observation using gjson
	parsedResp := rc.parseJSONRPCResponseForObservation(rawResp.ResponseBytes)

	// Build endpoint response validation result
	return &qosobservations.CosmosEndpointObservation{
		EndpointAddr: string(rawResp.EndpointAddr),
		EndpointResponseValidationResult: &qosobservations.CosmosEndpointResponseValidationResult{
			ResponseValidationType: qosobservations.CosmosResponseValidationType_COSMOS_RESPONSE_VALIDATION_TYPE_JSONRPC,
			HttpStatusCode:         int32(rawResp.HTTPStatusCode),
			ValidationError:        validationErr,
			UserJsonrpcResponse:    parsedResp,
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

// GetEndpointSelector returns the endpoint selector for the request context.
// Implements the gateway.RequestQoSContext interface.
func (rc *requestContext) GetEndpointSelector() protocol.EndpointSelector {
	return rc.serviceState
}

// Select returns the address of an endpoint using the request context's service state.
// Implements the protocol.EndpointSelector interface.
func (rc *requestContext) Select(allEndpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	return rc.serviceState.Select(allEndpoints)
}

// SelectMultiple returns multiple endpoint addresses using the request context's service state.
// Implements the protocol.EndpointSelector interface.
func (rc *requestContext) SelectMultiple(allEndpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	// Select multiple endpoints from the available endpoints using the service state.
	return rc.serviceState.SelectMultiple(allEndpoints, numEndpoints)
}

// buildProtocolErrorResponse builds an HTTP response using the stored protocol error.
// This provides more specific error messages to clients than the generic error builders.
func (rc requestContext) buildProtocolErrorResponse() pathhttp.HTTPResponse {
	// Get an appropriate request ID for the error response
	var requestID jsonrpc.ID
	for id := range rc.servicePayloads {
		requestID = id
		break
	}

	errorResp := jsonrpc.NewErrResponseInternalErr(requestID, rc.protocolError)
	bz, err := json.Marshal(errorResp)
	if err != nil {
		rc.logger.Warn().Err(err).Msg("buildProtocolErrorResponse: Marshaling JSONRPC response failed.")
	}

	return jsonrpc.HTTPResponse{
		ResponsePayload: bz,
		HTTPStatusCode:  http.StatusInternalServerError,
	}
}
