package cosmos

import (
	"encoding/json"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"

	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// requestContext provides specialized context for both JSONRPC and REST requests
// Implements gateway.RequestQoSContext interface
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

	// Validator to use to build user response/endpoint observations from the endpoint response.
	// The string parameter is the requestID for batch request error handling.
	endpointResponseValidator func(polylog.Logger, []byte, string) response

	// Service state for endpoint selection
	serviceState protocol.EndpointSelector

	// Endpoint response tracking
	endpointResponses []endpointResponse

	// protocolError stores a protocol-level error that occurred before any endpoint could respond.
	// Used to provide more specific error messages to clients.
	protocolError error
}

// endpointResponse tracks a response from a specific endpoint
type endpointResponse struct {
	endpointAddr protocol.EndpointAddr
	response     response
	// httpStatusCode is the original HTTP status code from the backend endpoint.
	httpStatusCode int
}

// response interface defines what endpoint response validators must return
type response interface {
	GetHTTPResponse() pathhttp.HTTPResponse
	GetObservation() qosobservations.CosmosEndpointObservation
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

// UpdateWithResponse processes a response from an endpoint
// Uses the existing response unmarshaling system
// NOT safe for concurrent use
// The requestID parameter is used to ensure error responses have the correct JSON-RPC ID
// when processing batch requests with independent flows per item.
func (rc *requestContext) UpdateWithResponse(endpointAddr protocol.EndpointAddr, responseBz []byte, httpStatusCode int, requestID string) {
	logger := rc.logger.With(
		"method", "UpdateWithResponse",
		"endpoint_addr", endpointAddr,
		"request_id", requestID,
	)

	// Debug: Log incoming response
	logger.Debug().
		Int("response_size", len(responseBz)).
		Int("http_status", httpStatusCode).
		Bool("is_batch", rc.isBatch).
		Int("current_response_count", len(rc.endpointResponses)).
		Msg("Processing endpoint response")

	// Parse and validate the endpoint response.
	// Pass requestID for batch request error handling.
	parsedEndpointResponse := rc.endpointResponseValidator(logger, responseBz, requestID)

	rc.endpointResponses = append(rc.endpointResponses, endpointResponse{
		endpointAddr:   endpointAddr,
		response:       parsedEndpointResponse,
		httpStatusCode: httpStatusCode,
	})

	logger.Debug().
		Int("new_response_count", len(rc.endpointResponses)).
		Msg("Added endpoint response")
}

// SetProtocolError stores a protocol-level error for more specific client error messages.
// Implements the gateway.RequestQoSContext interface.
func (rc *requestContext) SetProtocolError(err error) {
	rc.protocolError = err
}

// GetHTTPResponse builds the HTTP response that should be returned for
// an EVM blockchain service request.
// Implements the gateway.RequestQoSContext interface.
func (rc requestContext) GetHTTPResponse() pathhttp.HTTPResponse {
	// Use a noResponses struct if no responses were reported by the protocol from any endpoints.
	if len(rc.endpointResponses) == 0 {
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

	// Handle single requests
	resp := rc.endpointResponses[0].response.GetHTTPResponse()
	// Use the original HTTP status code from the backend if available
	if rc.endpointResponses[0].httpStatusCode != 0 {
		return &httpResponseWithStatus{
			wrapped:    resp,
			statusCode: rc.endpointResponses[0].httpStatusCode,
		}
	}
	return resp
}

// httpResponseWithStatus wraps an HTTPResponse and overrides its status code
type httpResponseWithStatus struct {
	wrapped    pathhttp.HTTPResponse
	statusCode int
}

func (r *httpResponseWithStatus) GetPayload() []byte {
	return r.wrapped.GetPayload()
}

func (r *httpResponseWithStatus) GetHTTPStatusCode() int {
	return r.statusCode
}

func (r *httpResponseWithStatus) GetHTTPHeaders() map[string]string {
	return r.wrapped.GetHTTPHeaders()
}

// getBatchHTTPResponse handles batch requests by combining individual JSON-RPC responses
// into an array according to the JSON-RPC 2.0 specification.
// https://www.jsonrpc.org/specification#batch
func (rc requestContext) getBatchHTTPResponse() pathhttp.HTTPResponse {
	logger := rc.logger.With("method", "getBatchHTTPResponse")

	// Log request IDs for debugging
	var reqIDsStr string
	for id := range rc.servicePayloads {
		if reqIDsStr != "" {
			reqIDsStr += ","
		}
		reqIDsStr += id.String()
	}
	logger.Debug().
		Str("request_ids", reqIDsStr).
		Int("num_endpoint_responses", len(rc.endpointResponses)).
		Msg("Building batch response")

	// Collect individual response payloads
	var individualResponses []json.RawMessage
	var responseIDsStr string
	for i, endpointResp := range rc.endpointResponses {
		// Extract the JSON payload from each response
		payload := endpointResp.response.GetHTTPResponse().GetPayload()
		if len(payload) > 0 {
			individualResponses = append(individualResponses, json.RawMessage(payload))

			// Debug: log each response ID
			var respObj struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(payload, &respObj); err == nil {
				if responseIDsStr != "" {
					responseIDsStr += ","
				}
				responseIDsStr += string(respObj.ID)
				logger.Debug().
					Int("response_index", i).
					Str("response_id_raw", string(respObj.ID)).
					Msg("Collected response payload")
			}
		} else {
			logger.Warn().
				Int("response_index", i).
				Msg("Empty payload in endpoint response - skipping")
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

	logger.Debug().
		Int("num_individual_responses", len(individualResponses)).
		Int("num_service_payloads", len(rc.servicePayloads)).
		Str("response_ids", responseIDsStr).
		Msg("Validating batch response")

	// Validate and construct batch response using jsonrpc package
	batchResponse, err := jsonrpc.ValidateAndBuildBatchResponse(
		rc.logger,
		individualResponses,
		rc.servicePayloads,
	)
	if err != nil {
		logger.Error().
			Err(err).
			Int("num_responses", len(individualResponses)).
			Int("num_requests", len(rc.servicePayloads)).
			Str("request_ids", reqIDsStr).
			Str("response_ids", responseIDsStr).
			Msg("Batch response validation failed")

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

// GetObservations returns QoS observations for requests
func (rc *requestContext) GetObservations() qosobservations.Observations {
	// Handle case where no endpoint responses were received
	if len(rc.endpointResponses) == 0 {
		rc.observations.RequestLevelError = rc.protocolErrorObservationBuilder()

		return qosobservations.Observations{
			ServiceObservations: &qosobservations.Observations_Cosmos{
				Cosmos: rc.observations,
			},
		}
	}

	// Build endpoint observations using the existing response system
	endpointObservations := make([]*qosobservations.CosmosEndpointObservation, 0, len(rc.endpointResponses))
	for _, endpointResp := range rc.endpointResponses {
		endpointObs := endpointResp.response.GetObservation()
		endpointObs.EndpointAddr = string(endpointResp.endpointAddr)
		endpointObservations = append(endpointObservations, &endpointObs)
	}

	rc.observations.EndpointObservations = endpointObservations

	return qosobservations.Observations{
		ServiceObservations: &qosobservations.Observations_Cosmos{
			Cosmos: rc.observations,
		},
	}
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
