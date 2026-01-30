package evm

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/tidwall/gjson"

	"github.com/pokt-network/path/gateway"
	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// requestContext provides the support required by the gateway
// package for handling service requests.
var _ gateway.RequestQoSContext = &requestContext{}

// requestContext provides the endpoint selection capability required
// by the protocol package for handling a service request.
var _ protocol.EndpointSelector = &requestContext{}

// TODO_REFACTOR: Improve naming clarity by distinguishing between interfaces and adapters
// in the metrics/qos/evm and qos/evm packages, and elsewhere names like `response` are used.
// Consider renaming:
//   - metrics/qos/evm: response → EVMMetricsResponse
//   - qos/evm: response → EVMQoSResponse
//   - observation/evm: observation -> EVMObservation
//
// response defines the functionality required from a parsed endpoint response, which all response types must implement.
// It provides methods to:
//  1. Generate observations for endpoint quality tracking
//  2. Format HTTP responses to send back to clients
//
// NOTE: This interface is used by synthetic checks (hydrator) where detailed parsing is needed.
// For organic requests on the hot path, raw bytes are stored without parsing.
type response interface {
	// GetObservation returns an observation of the endpoint's response
	// for quality metrics tracking, including HTTP status code.
	GetObservation() qosobservations.EVMEndpointObservation

	// GetHTTPResponse returns the HTTP response to be sent back to the client.
	GetHTTPResponse() jsonrpc.HTTPResponse
}

// rawEndpointResponse stores raw bytes from endpoint responses.
// This is used on the hot path to avoid parsing overhead.
// Heavy parsing for observations is done asynchronously via the ObservationQueue.
type rawEndpointResponse struct {
	EndpointAddr   protocol.EndpointAddr
	ResponseBytes  []byte
	HTTPStatusCode int
}

// requestContext implements the functionality for EVM-based blockchain services.
// It uses raw byte passthrough on the hot path to minimize latency.
// Heavy parsing for observations is done asynchronously via the ObservationQueue.
type requestContext struct {
	logger polylog.Logger

	// chainID is the chain identifier for EVM QoS implementation.
	// Expected as the `Result` field in eth_chainId responses.
	chainID string

	// service_id is the identifier for the evm QoS implementation.
	// It is the "alias" or human readable interpratation of the chain_id.
	// Used in generating observations.
	serviceID protocol.ServiceID

	// The origin of the request handled by the context.
	// Either:
	// - Organic: user requests
	// - Synthetic: requests built by the QoS service to get additional data points on endpoints.
	requestOrigin qosobservations.RequestOrigin

	// The length of the request payload in bytes.
	requestPayloadLength uint

	serviceState *serviceState

	// JSON-RPC requests - supports both single and batch requests per JSON-RPC 2.0 spec
	servicePayloads map[jsonrpc.ID]protocol.Payload

	// Whether the request is a batch request.
	// Necessary to distinguish between a batch request of length 1 and a single request.
	// In the case of a batch request of length 1, the response must be returned as an array.
	isBatch bool

	// rawResponses stores raw endpoint responses.
	// Raw bytes are stored without parsing for low-latency client response.
	// Heavy parsing (for observations) is done asynchronously via ObservationQueue.
	rawResponses []rawEndpointResponse

	// endpointSelectionMetadata contains metadata about the endpoint selection process
	endpointSelectionMetadata EndpointSelectionMetadata

	// archivalResult stores the result of archival heuristic detection.
	// Used to conditionally filter endpoints based on whether the request needs archival data.
	archivalResult ArchivalHeuristicResult

	// protocolError stores a protocol-level error that occurred before any endpoint could respond.
	// Used to provide more specific error messages to clients (e.g., "no valid endpoints available").
	protocolError error
}

// GetServicePayloads returns the service payloads for the JSON-RPC requests in the request context.
//
// jsonrpcReqs is a map of JSON-RPC request IDs to JSON-RPC requests.
// It is assigned to the request context in the `evmRequestValidator.validateHTTPRequest` method.
//
// TODO_MVP(@adshmh): Ensure the JSONRPC request struct can handle all valid service requests.
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
// The requestID parameter is the JSON-RPC request ID that this response corresponds to.
// For batch requests, this ensures error responses use the correct ID.
// For single requests or when empty, the ID is extracted from the response bytes.
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

// TODO_TECHDEBT(@adshmh): Drop the responseNone struct:
// - Not having received a response from protocol layer is a protocol error
// - Use a RequestError, consistent with qos/cosmos and qos/solana packages.
// - Update the metrics/qos/evm package to use RequestError.
//
// GetHTTPResponse builds the HTTP response that should be returned for
// an EVM blockchain service request.
//
// Returns raw bytes as-is (no parsing/re-encoding) for low-latency client response.
// Heavy parsing for observations is done asynchronously via ObservationQueue.
//
// Implements the gateway.RequestQoSContext interface.
func (rc requestContext) GetHTTPResponse() pathhttp.HTTPResponse {
	// No responses received - return error response with protocol error context if available
	if len(rc.rawResponses) == 0 {
		rc.logger.Warn().Msg("No responses received from any endpoints. Returning generic non-response.")
		responseNoneObj := responseNone{
			logger:          rc.logger,
			servicePayloads: rc.servicePayloads,
			protocolError:   rc.protocolError,
		}
		return responseNoneObj.GetHTTPResponse()
	}

	// Handle batch requests according to JSON-RPC 2.0 specification
	// https://www.jsonrpc.org/specification#batch
	if rc.isBatch {
		return rc.getBatchHTTPResponse()
	}

	// Return the most recent raw response as-is for single requests
	latestResponse := rc.rawResponses[len(rc.rawResponses)-1]

	// Check for empty response bytes - endpoint returned no data
	if len(latestResponse.ResponseBytes) == 0 {
		rc.logger.Warn().
			Str("endpoint_addr", string(latestResponse.EndpointAddr)).
			Msg("Endpoint returned empty response bytes. Returning error response.")
		responseNoneObj := responseNone{
			logger:          rc.logger,
			servicePayloads: rc.servicePayloads,
			protocolError:   rc.protocolError,
		}
		return responseNoneObj.GetHTTPResponse()
	}

	// Strip trailing newline if present (suppliers may use json.Encoder which adds \n)
	payload := latestResponse.ResponseBytes
	if payload[len(payload)-1] == '\n' {
		payload = payload[:len(payload)-1]
	}

	return &rawHTTPResponse{
		httpStatusCode: latestResponse.HTTPStatusCode,
		payload:        payload,
		headers:        rc.buildResponseHeaders(),
	}
}

// rawHTTPResponse implements pathhttp.HTTPResponse for raw byte passthrough.
type rawHTTPResponse struct {
	httpStatusCode int
	payload        []byte
	headers        map[string]string
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
	return r.headers
}

// buildResponseHeaders builds the HTTP response headers including archival detection result.
func (rc requestContext) buildResponseHeaders() map[string]string {
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	// Add archival detection header for visibility
	headers["X-Archival-Request"] = strconv.FormatBool(rc.archivalResult.RequiresArchival)
	return headers
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
			// Strip trailing newline if present (suppliers may use json.Encoder which adds \n)
			respBytes := rawResp.ResponseBytes
			if respBytes[len(respBytes)-1] == '\n' {
				respBytes = respBytes[:len(respBytes)-1]
			}
			individualResponses = append(individualResponses, json.RawMessage(respBytes))
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

// GetObservations returns all endpoint observations from the request context.
// Uses gjson to parse raw bytes for observation creation (async path).
// Implements gateway.RequestQoSContext interface.
func (rc requestContext) GetObservations() qosobservations.Observations {
	// Create observations for each JSON-RPC request in the batch (or single request)
	requestObservations := rc.createRequestObservations()

	// Convert endpoint selection validation results to proto format
	validationResults := rc.convertValidationResults()

	var requestError *qosobservations.RequestError
	// Set request error as protocol-level error if no endpoint responses were received.
	if len(rc.rawResponses) == 0 {
		requestError = qos.GetRequestErrorForProtocolError()
	}

	// Check for any valid JSON-RPC responses using gjson (light parsing).
	var foundValidJSONRPCResponse bool
	for _, rawResp := range rc.rawResponses {
		// Use gjson to quickly check if response has valid structure
		if rc.isValidJSONRPCResponse(rawResp.ResponseBytes) {
			foundValidJSONRPCResponse = true
			break
		}
	}

	// No valid JSONRPC responses received from any endpoints.
	// Set request error as backend service malformed payload error.
	if !foundValidJSONRPCResponse && len(rc.rawResponses) > 0 {
		requestError = qos.GetRequestErrorForJSONRPCBackendServiceUnmarshalError()
	}

	return qosobservations.Observations{
		ServiceObservations: &qosobservations.Observations_Evm{
			Evm: &qosobservations.EVMRequestObservations{
				ChainId:              rc.chainID,
				ServiceId:            string(rc.serviceID),
				RequestPayloadLength: uint32(rc.requestPayloadLength),
				RequestOrigin:        rc.requestOrigin,
				RequestError:         requestError,
				RequestObservations:  requestObservations,
				EndpointSelectionMetadata: &qosobservations.EndpointSelectionMetadata{
					RandomEndpointFallback: rc.endpointSelectionMetadata.RandomEndpointFallback,
					ValidationResults:      validationResults,
				},
			},
		},
	}
}

// isValidJSONRPCResponse checks if bytes represent a valid JSON-RPC response using gjson.
// Returns true if the response has either a result or error field.
func (rc requestContext) isValidJSONRPCResponse(responseBz []byte) bool {
	if len(responseBz) == 0 {
		return false
	}
	// Check for jsonrpc version field
	version := gjson.GetBytes(responseBz, "jsonrpc")
	if !version.Exists() || version.String() != "2.0" {
		return false
	}
	// Valid JSON-RPC response must have either result or error
	hasResult := gjson.GetBytes(responseBz, "result").Exists()
	hasError := gjson.GetBytes(responseBz, "error").Exists()
	return hasResult || hasError
}

// createRequestObservations creates observations for all JSON-RPC requests in the batch.
// For batch requests, jsonrpcReqs contains multiple requests keyed by their ID strings.
// For single requests, jsonrpcReqs contains one request.
// Each observation correlates a JSON-RPC request with its corresponding endpoint response(s).
// Uses gjson to parse raw bytes for observation creation (async path).
func (rc requestContext) createRequestObservations() []*qosobservations.EVMRequestObservation {
	// Handle the special case where no endpoint responses were received
	if len(rc.rawResponses) == 0 {
		return rc.createNoResponseObservations()
	}

	// Create observations by correlating endpoint responses with their corresponding JSON-RPC requests
	return rc.createResponseObservations()
}

// createNoResponseObservations creates a single observation when no endpoint responses were received.
// This can happen when all endpoints are unreachable or fail to respond.
// The observation includes all JSON-RPC requests from the batch but no endpoint observations.
func (rc requestContext) createNoResponseObservations() []*qosobservations.EVMRequestObservation {
	responseNoneObj := responseNone{
		logger:          rc.logger,
		servicePayloads: rc.servicePayloads,
	}
	responseNoneObs := responseNoneObj.GetObservation()

	return []*qosobservations.EVMRequestObservation{
		{
			EndpointObservations: []*qosobservations.EVMEndpointObservation{
				&responseNoneObs,
			},
		},
	}
}

// createResponseObservations creates observations by correlating endpoint responses with their
// corresponding JSON-RPC requests from the batch. Uses gjson for efficient parsing.
//
// For batch requests: multiple responses are correlated with multiple requests
// For single requests: one response is correlated with one request
//
// Per JSON-RPC 2.0 spec, responses with null IDs are valid for error cases when the server
// couldn't parse the request ID. These are skipped during observation creation.
func (rc requestContext) createResponseObservations() []*qosobservations.EVMRequestObservation {
	var observations []*qosobservations.EVMRequestObservation

	for _, rawResp := range rc.rawResponses {
		// Use gjson to extract only the fields needed for observation
		idResult := gjson.GetBytes(rawResp.ResponseBytes, "id")

		// Skip responses with null/missing IDs - per JSON-RPC 2.0 spec, these indicate error responses
		// where the server couldn't parse the request ID.
		if !idResult.Exists() || idResult.Type == gjson.Null {
			rc.logger.Debug().Msg("Skipping observation creation for response with null ID (JSON-RPC error response)")
			continue
		}

		// Build the JSON-RPC ID from gjson result
		responseID := rc.gjsonResultToJSONRPCID(idResult)

		// Look up the original JSON-RPC request using the response ID
		servicePayload, ok := rc.findServicePayload(responseID)
		if !ok {
			rc.logger.Warn().Msgf("Could not find JSONRPC request for response ID: %s (endpoint may have modified the ID)", responseID.String())
			continue
		}

		jsonrpcReq, err := jsonrpc.GetJsonRpcReqFromServicePayload(servicePayload)
		if err != nil {
			rc.logger.Error().Err(err).Msg("SHOULD RARELY HAPPEN: requestContext.createResponseObservations() should never fail to get the JSONRPC request from the service payload.")
			continue
		}

		// Create endpoint observation from raw bytes using gjson
		endpointObs := rc.createEndpointObservationFromRawBytes(rawResp)

		observations = append(observations, &qosobservations.EVMRequestObservation{
			JsonrpcRequest: jsonrpcReq.GetObservation(),
			EndpointObservations: []*qosobservations.EVMEndpointObservation{
				endpointObs,
			},
		})
	}

	return observations
}

// gjsonResultToJSONRPCID converts a gjson result to a jsonrpc.ID.
func (rc requestContext) gjsonResultToJSONRPCID(result gjson.Result) jsonrpc.ID {
	switch result.Type {
	case gjson.String:
		return jsonrpc.IDFromString(result.String())
	case gjson.Number:
		return jsonrpc.IDFromInt(int(result.Int()))
	default:
		return jsonrpc.ID{}
	}
}

// createEndpointObservationFromRawBytes creates an endpoint observation from raw response bytes.
// Uses gjson for efficient parsing of only the fields needed for observations.
// Returns a pointer to avoid copying the protobuf struct which contains a mutex.
func (rc requestContext) createEndpointObservationFromRawBytes(rawResp rawEndpointResponse) *qosobservations.EVMEndpointObservation {
	// Build parsed JSONRPC response observation using gjson
	parsedResp := rc.parseJSONRPCResponseForObservation(rawResp.ResponseBytes)

	// Create an unrecognized response observation (since we're not doing method-specific parsing on hot path)
	return &qosobservations.EVMEndpointObservation{
		EndpointAddr:          string(rawResp.EndpointAddr),
		ParsedJsonrpcResponse: parsedResp,
		ResponseObservation: &qosobservations.EVMEndpointObservation_UnrecognizedResponse{
			UnrecognizedResponse: &qosobservations.EVMUnrecognizedResponse{
				JsonrpcResponse:         parsedResp,
				HttpStatusCode:          int32(rawResp.HTTPStatusCode),
				ResponseValidationError: nil, // No validation error if we could parse it
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

// convertValidationResults converts endpoint selection validation results to proto format.
// These results contain information about which endpoints were considered during selection
// and why they were accepted or rejected.
func (rc requestContext) convertValidationResults() []*qosobservations.EndpointValidationResult {
	var validationResults []*qosobservations.EndpointValidationResult
	validationResults = append(validationResults, rc.endpointSelectionMetadata.ValidationResults...)
	return validationResults
}

func (rc *requestContext) GetEndpointSelector() protocol.EndpointSelector {
	return rc
}

// Select returns endpoint address using request context's endpoint store.
// Implements protocol.EndpointSelector interface.
// Tracks random selection when all endpoints fail validation.
// Passes archival requirement to endpoint selection for conditional filtering.
func (rc *requestContext) Select(allEndpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	// TODO_FUTURE(@adshmh): Enhance the endpoint selection meta data to track, e.g.:
	// * Endpoint Selection Latency
	// * Number of available endpoints
	selectionResult, err := rc.serviceState.SelectWithMetadata(allEndpoints, rc.archivalResult.RequiresArchival)
	if err != nil {
		return protocol.EndpointAddr(""), err
	}

	// Store selection metadata for observation tracking
	rc.endpointSelectionMetadata = selectionResult.Metadata

	return selectionResult.SelectedEndpoint, nil
}

// SelectMultiple returns multiple endpoint addresses using the request context's endpoint store.
// Implements the protocol.EndpointSelector interface.
func (rc *requestContext) SelectMultiple(allEndpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	return rc.serviceState.SelectMultiple(allEndpoints, numEndpoints)
}

// findServicePayload finds a service payload by ID using value-based comparison.
// This handles the case where JSON unmarshaling creates new ID structs with different
// pointer addresses but equivalent values.
func (rc *requestContext) findServicePayload(targetID jsonrpc.ID) (protocol.Payload, bool) {
	for id, payload := range rc.servicePayloads {
		if id.Equal(targetID) {
			return payload, true
		}
	}
	return protocol.Payload{}, false
}
