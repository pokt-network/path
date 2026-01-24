package evm

import (
	"encoding/json"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"

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
// TODO_TECHDEBT: Need to add a Validate() method here to allow the caller (e.g. gateway)
// determine whether the endpoint's response was valid, and whether a retry makes sense.
//
// response defines the functionality required from a parsed endpoint response, which all response types must implement.
// It provides methods to:
//  1. Generate observations for endpoint quality tracking
//  2. Format HTTP responses to send back to clients
type response interface {
	// GetObservation returns an observation of the endpoint's response
	// for quality metrics tracking, including HTTP status code.
	GetObservation() qosobservations.EVMEndpointObservation

	// GetHTTPResponse returns the HTTP response to be sent back to the client.
	GetHTTPResponse() jsonrpc.HTTPResponse
}

var _ response = &endpointResponse{}

type endpointResponse struct {
	protocol.EndpointAddr
	response
	unmarshalErr error
	// httpStatusCode is the original HTTP status code from the backend endpoint.
	httpStatusCode int
}

// rawEndpointResponse stores raw bytes for passthrough mode.
// Used when parsing is skipped for low-latency client responses.
type rawEndpointResponse struct {
	EndpointAddr   protocol.EndpointAddr
	ResponseBytes  []byte
	HTTPStatusCode int
}

// requestContext implements the functionality for EVM-based blockchain services.
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

	// endpointResponses is the set of responses received from one or
	// more endpoints as part of handling this service request.
	// Supports both single and batch JSON-RPC requests.
	endpointResponses []endpointResponse

	// endpointSelectionMetadata contains metadata about the endpoint selection process
	endpointSelectionMetadata EndpointSelectionMetadata

	// --- Passthrough mode fields ---
	// When passthroughMode is true:
	//   - UpdateWithResponse stores raw bytes without parsing
	//   - GetHTTPResponse returns raw bytes as-is
	//   - Heavy parsing is done asynchronously via sampling (see ObservationQueue)
	// This reduces latency on the hot path for client responses.

	// passthroughMode enables raw byte passthrough for client responses.
	// When true, responses are not parsed - they're returned as-is to reduce latency.
	passthroughMode bool

	// rawResponses stores raw endpoint responses in passthrough mode.
	// Used by GetHTTPResponse to return raw bytes without parsing.
	rawResponses []rawEndpointResponse

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
// In passthrough mode (rc.passthroughMode == true):
//   - Raw bytes are stored without parsing for fast client response
//   - Heavy parsing is done asynchronously via ObservationQueue sampling
//
// In legacy mode (rc.passthroughMode == false):
//   - Full JSON parsing is done synchronously (existing behavior)
func (rc *requestContext) UpdateWithResponse(endpointAddr protocol.EndpointAddr, responseBz []byte, httpStatusCode int) {
	rc.logger = rc.logger.With(
		"endpoint_addr", endpointAddr,
		"endpoint_response_len", len(responseBz),
	)

	// PASSTHROUGH MODE: Store raw bytes without parsing for low-latency client response.
	// Heavy parsing (if needed) is done async via ObservationQueue sampling.
	if rc.passthroughMode {
		rc.rawResponses = append(rc.rawResponses, rawEndpointResponse{
			EndpointAddr:   endpointAddr,
			ResponseBytes:  responseBz,
			HTTPStatusCode: httpStatusCode,
		})
		return
	}

	// LEGACY MODE: Full synchronous parsing (existing behavior)
	// TODO_IMPROVE: check whether the request was valid, and return an error if it was not.
	// This would be an extra safety measure, as the caller should have checked the returned value
	// indicating the validity of the request when calling on QoS instance's ParseHTTPRequest

	response, err := unmarshalResponse(
		rc.logger, rc.servicePayloads, responseBz, endpointAddr,
	)

	rc.endpointResponses = append(rc.endpointResponses, endpointResponse{
		EndpointAddr:   endpointAddr,
		response:       response,
		unmarshalErr:   err,
		httpStatusCode: httpStatusCode,
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
// In passthrough mode: Returns raw bytes as-is (no parsing/re-encoding).
// In legacy mode: Returns parsed and potentially re-formatted JSON-RPC response.
//
// Implements the gateway.RequestQoSContext interface.
func (rc requestContext) GetHTTPResponse() pathhttp.HTTPResponse {
	// PASSTHROUGH MODE: Return raw bytes as-is (like NoOp)
	if rc.passthroughMode {
		return rc.getPassthroughHTTPResponse()
	}

	// LEGACY MODE: Return parsed response (existing behavior)
	return rc.getLegacyHTTPResponse()
}

// getPassthroughHTTPResponse returns raw bytes as-is without parsing.
// Used in passthrough mode for low-latency client responses.
func (rc requestContext) getPassthroughHTTPResponse() pathhttp.HTTPResponse {
	// No raw responses received - return error response with protocol error context if available
	if len(rc.rawResponses) == 0 {
		rc.logger.Warn().Msg("No responses received from any endpoints in passthrough mode. Returning generic non-response.")
		responseNoneObj := responseNone{
			logger:          rc.logger,
			servicePayloads: rc.servicePayloads,
			protocolError:   rc.protocolError,
		}
		return responseNoneObj.GetHTTPResponse()
	}

	// Return the most recent raw response as-is
	latestResponse := rc.rawResponses[len(rc.rawResponses)-1]

	// Use original HTTP status from backend if available, otherwise default to 200 OK
	statusCode := http.StatusOK
	if latestResponse.HTTPStatusCode != 0 {
		statusCode = latestResponse.HTTPStatusCode
	}

	return &passthroughHTTPResponse{
		httpStatusCode: statusCode,
		payload:        latestResponse.ResponseBytes,
	}
}

// getLegacyHTTPResponse returns parsed JSON-RPC response (existing behavior).
func (rc requestContext) getLegacyHTTPResponse() pathhttp.HTTPResponse {
	// Use a noResponses struct if no responses were reported by the protocol from any endpoints.
	if len(rc.endpointResponses) == 0 {
		rc.logger.Warn().Msg("No responses received from any endpoints. Returning generic non-response.")
		responseNoneObj := responseNone{
			logger:          rc.logger,
			servicePayloads: rc.servicePayloads,
			protocolError:   rc.protocolError,
		}
		return responseNoneObj.GetHTTPResponse()
	}

	numJSONRPCRequests := len(rc.servicePayloads)
	numEndpointResponses := len(rc.endpointResponses)

	// Handle batch requests according to JSON-RPC 2.0 specification
	// https://www.jsonrpc.org/specification#batch
	if rc.isBatch {
		if numJSONRPCRequests != numEndpointResponses {
			rc.logger.Warn().Msgf("TODO_INVESTIGATE: The number of JSON-RPC requests (%d) does not match the number of endpoint responses (%d). This should not happen.", numJSONRPCRequests, numEndpointResponses)
		}

		return rc.getBatchHTTPResponse()
	}

	// Guard against empty endpoint responses to prevent index out of bounds panic
	if numEndpointResponses == 0 {
		rc.logger.Error().Msg("SHOULD NEVER HAPPEN: No endpoint responses available for single JSON-RPC request")
		return getGenericResponseNoEndpointResponse(rc.logger).GetHTTPResponse()
	}

	if numEndpointResponses != 1 {
		rc.logger.Warn().Msgf("TODO_INVESTIGATE: Expected exactly one endpoint response for single JSON-RPC request, but received %d. Only using the first response for now.", numEndpointResponses)
	}

	// Non-batch requests.
	// Return the only endpoint response reported to the context for single requests.
	resp := rc.endpointResponses[0].GetHTTPResponse()
	// Use the original HTTP status code from the backend if available
	if rc.endpointResponses[0].httpStatusCode != 0 {
		resp.HTTPStatusCode = rc.endpointResponses[0].httpStatusCode
	}
	return resp
}

// passthroughHTTPResponse implements pathhttp.HTTPResponse for raw byte passthrough.
type passthroughHTTPResponse struct {
	httpStatusCode int
	payload        []byte
}

func (r *passthroughHTTPResponse) GetPayload() []byte {
	return r.payload
}

func (r *passthroughHTTPResponse) GetHTTPStatusCode() int {
	return r.httpStatusCode
}

func (r *passthroughHTTPResponse) GetHTTPHeaders() map[string]string {
	return map[string]string{
		"Content-Type": "application/json",
	}
}

// getBatchHTTPResponse handles batch requests by combining individual JSON-RPC responses
// into an array according to the JSON-RPC 2.0 specification.
// https://www.jsonrpc.org/specification#batch
func (rc requestContext) getBatchHTTPResponse() pathhttp.HTTPResponse {
	// Collect individual response payloads
	var individualResponses []json.RawMessage
	for _, endpointResp := range rc.endpointResponses {
		// Extract the JSON payload from each response
		payload := endpointResp.GetHTTPResponse().GetPayload()
		if len(payload) > 0 {
			individualResponses = append(individualResponses, json.RawMessage(payload))
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
	if len(rc.endpointResponses) > 0 && rc.endpointResponses[0].httpStatusCode != 0 {
		httpStatusCode = rc.endpointResponses[0].httpStatusCode
	}
	return jsonrpc.HTTPResponse{
		ResponsePayload: batchResponse,
		HTTPStatusCode:  httpStatusCode,
	}
}

// GetObservations returns all endpoint observations from the request context.
// Implements gateway.RequestQoSContext interface.
func (rc requestContext) GetObservations() qosobservations.Observations {
	// Create observations for each JSON-RPC request in the batch (or single request)
	requestObservations := rc.createRequestObservations()

	// Convert endpoint selection validation results to proto format
	validationResults := rc.convertValidationResults()

	var requestError *qosobservations.RequestError
	// Set request error as protocol-level error if no endpoint responses were received.
	if len(rc.endpointResponses) == 0 {
		requestError = qos.GetRequestErrorForProtocolError()
	}

	// Check for any successfully parsedresponses.
	var foundParsedJSONRPCResponse bool
	for _, endpointResponse := range rc.endpointResponses {
		// Endpoint payload was successfully parsed as JSONRPC response.
		// Mark the request as having no errors.
		if endpointResponse.unmarshalErr == nil {
			foundParsedJSONRPCResponse = true
			break
		}
	}

	// No valid JSONRPC responses received from any endpoints.
	// Set request error as backend service malformed payload error.
	// i.e. the payload from the the endpoint/backend service failed to parse as a valid JSONRPC response.
	if !foundParsedJSONRPCResponse {
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

// createRequestObservations creates observations for all JSON-RPC requests in the batch.
// For batch requests, jsonrpcReqs contains multiple requests keyed by their ID strings.
// For single requests, jsonrpcReqs contains one request.
// Each observation correlates a JSON-RPC request with its corresponding endpoint response(s).
func (rc requestContext) createRequestObservations() []*qosobservations.EVMRequestObservation {
	// Handle the special case where no endpoint responses were received
	if len(rc.endpointResponses) == 0 {
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
// corresponding JSON-RPC requests from the batch. Each endpoint response contains a JSON-RPC ID
// that is used to look up the original request in the jsonrpcReqs map.
//
// For batch requests: multiple responses are correlated with multiple requests
// For single requests: one response is correlated with one request
//
// Per JSON-RPC 2.0 spec, responses with null IDs are valid for error cases when the server
// couldn't parse the request ID. These are skipped during observation creation.
func (rc requestContext) createResponseObservations() []*qosobservations.EVMRequestObservation {
	var observations []*qosobservations.EVMRequestObservation

	for _, endpointResp := range rc.endpointResponses {
		var jsonrpcResponse jsonrpc.Response
		err := json.Unmarshal(endpointResp.GetHTTPResponse().GetPayload(), &jsonrpcResponse)
		if err != nil {
			rc.logger.Error().Err(err).Msg("SHOULD RARELY HAPPEN: requestContext.createResponseObservations() should never fail to unmarshal the JSONRPC response.")
			continue
		}

		// Skip responses with null IDs - per JSON-RPC 2.0 spec, these indicate error responses
		// where the server couldn't parse the request ID. We can't correlate these with specific
		// requests, so we skip observation creation for them.
		if jsonrpcResponse.ID.IsEmpty() {
			rc.logger.Debug().Msg("Skipping observation creation for response with null ID (JSON-RPC error response)")
			continue
		}

		// Look up the original JSON-RPC request using the response ID
		// This correlation is critical for batch requests where multiple requests/responses
		// need to be properly matched
		servicePayload, ok := rc.findServicePayload(jsonrpcResponse.ID)
		if !ok {
			rc.logger.Warn().Msgf("Could not find JSONRPC request for response ID: %s (endpoint may have modified the ID)", jsonrpcResponse.ID.String())
			continue
		}

		jsonrpcReq, err := jsonrpc.GetJsonRpcReqFromServicePayload(servicePayload)
		if err != nil {
			rc.logger.Error().Err(err).Msg("SHOULD RARELY HAPPEN: requestContext.createResponseObservations() should never fail to get the JSONRPC request from the service payload.")
			continue
		}

		// Create observations for both the request and its corresponding endpoint response
		endpointObs := endpointResp.GetObservation()

		// Ensure the endpoint address is always set in the observation
		endpointObs.EndpointAddr = string(endpointResp.EndpointAddr)

		observations = append(observations, &qosobservations.EVMRequestObservation{
			JsonrpcRequest: jsonrpcReq.GetObservation(),
			EndpointObservations: []*qosobservations.EVMEndpointObservation{
				&endpointObs,
			},
		})
	}

	return observations
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
func (rc *requestContext) Select(allEndpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	// TODO_FUTURE(@adshmh): Enhance the endpoint selection meta data to track, e.g.:
	// * Endpoint Selection Latency
	// * Number of available endpoints
	selectionResult, err := rc.serviceState.SelectWithMetadata(allEndpoints)
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
